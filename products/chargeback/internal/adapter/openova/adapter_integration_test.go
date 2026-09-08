package openova

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// TestIntegrationOrgSyncAgainstStore proves *store.Store satisfies the
// adapter contract on real SQL: sync creates the customer + sources, a
// resync is idempotent, the platform collector writes usage rows, and a
// delete suspends without touching history. Skipped unless
// CHARGEBACK_TEST_DATABASE_URL is set (the module's integration pattern).
func TestIntegrationOrgSyncAgainstStore(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	core := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "agwalk", Name: "aksk"},
		Data:       map[string][]byte{"v": []byte("AKINT:SKINT")},
	})
	ver := &fakeVerifier{}
	s := &OrgSync{Core: core, Repo: st, Keys: testKeys(t), Verifier: ver, Metrics: metrics.New()}
	org := orgUnstructured("agwalk", func(spec map[string]any) {
		spec["billingMode"] = "chargeback"
		spec["costSources"] = []any{
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-int",
				"credentialRef": map[string]any{"name": "aksk", "key": "v"}},
		}
	})
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatalf("resync: %v", err)
	}
	c, err := st.GetCustomerBySlug(ctx, "agwalk")
	if err != nil || c.Status != "active" || c.Kind != "organization" || c.BillingMode != "chargeback" {
		t.Fatalf("customer = %+v err=%v", c, err)
	}
	srcs, err := st.ListSources(ctx, store.OperatorScope, c.ID)
	if err != nil || len(srcs) != 2 {
		t.Fatalf("sources = %+v err=%v (resync must not duplicate)", srcs, err)
	}
	var orgSrc store.CostSource
	for _, src := range srcs {
		switch src.Kind {
		case SourceKindOrg:
			orgSrc = src
			if src.Status != "verified" {
				t.Fatalf("platform source = %+v", src)
			}
		case "huawei-project":
			if src.Status != "verified" || src.AccessKey != "AKINT" {
				t.Fatalf("declared source = %+v", src)
			}
		}
	}

	// Platform usage lands on the real usage_records table.
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	pc := &PlatformCollector{Repo: st, Metrics: metrics.New(), Now: func() time.Time { return now }}
	pc.ObserveNamespace(orgNamespace("agwalk"))
	pc.ObservePod(testPod("agwalk", "web-0", "pod-int-1", now.Add(-30*time.Minute), "500m", "1Gi"))
	if _, err := pc.EmitOrg(ctx, "agwalk"); err != nil {
		t.Fatal(err)
	}
	nRecords, err := st.UsageCount(ctx, orgSrc.ID)
	if err != nil || nRecords != 2 {
		t.Fatalf("usage rows = %d err=%v, want 2 (one 30-minute slice × vcpu + mem)", nRecords, err)
	}

	// Delete suspends; the usage rows stay.
	if err := s.SuspendOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c2, err := st.GetCustomerBySlug(ctx, "agwalk")
	if err != nil || c2.Status != "suspended" {
		t.Fatalf("customer after delete = %+v err=%v", c2, err)
	}
	nAfter, err := st.UsageCount(ctx, orgSrc.ID)
	if err != nil || nAfter != nRecords {
		t.Fatalf("usage rows after suspend = %d err=%v, want %d kept", nAfter, err, nRecords)
	}
}

// TestIntegrationBillingHookOnIssuedStatement: a real issued statement of a
// real-billing Organization posts its total once, keyed by the statement id.
func TestIntegrationBillingHookOnIssuedStatement(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "agwalk", Name: "AG Walk", AdminEmail: "owner@agwalk.example", Kind: "organization", OrgSlug: "agwalk", BillingMode: "real"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCustomerStatus(ctx, c.ID, "active"); err != nil {
		t.Fatal(err)
	}
	draft, err := st.WriteDraftStatement(ctx, store.StatementDraft{
		CustomerID:  c.ID,
		PeriodStart: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Currency:    "OMR",
		Subtotal:    "10",
		TaxRate:     "0.05",
		Tax:         "0.5",
		Total:       "10.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := st.IssueStatement(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}

	var got meteringPayload
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"ledger_entry_id": "led-1", "duplicate": false})
	}))
	defer srv.Close()
	hook := &BillingHook{URL: srv.URL, Token: "t", Metrics: metrics.New()}
	cust, err := st.GetCustomer(ctx, store.OperatorScope, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := hook.StatementIssued(ctx, issued, cust); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || got.Metadata.RequestID != issued.ID || got.AmountMicroOMR != -10500000 || got.CustomerID != "agwalk" {
		t.Fatalf("hook payload = %+v calls=%d", got, calls)
	}
}

// TestIntegrationPlanLineAgainstStore (DESIGN.md §2.8 "Plan revenue"): on
// real SQL the sync stores the plan, creates the "OpenOva plans" book once
// and assigns it, and the collector's plan.m records land on usage_records
// keyed like every other meter.
func TestIntegrationPlanLineAgainstStore(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: st, Keys: testKeys(t), Metrics: metrics.New()}
	org := orgUnstructured("agwalk", func(spec map[string]any) { spec["planSlug"] = "m" })
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatalf("resync: %v", err)
	}
	c, err := st.GetCustomerBySlug(ctx, "agwalk")
	if err != nil || c.PlanSlug != "m" || c.PriceBookID != nil {
		t.Fatalf("customer = %+v err=%v (the book is a property of the source, never the customer)", c, err)
	}
	// The plans book is assigned to the Organization's PLATFORM source.
	srcs, err := st.ListSources(ctx, store.OperatorScope, c.ID)
	if err != nil || len(srcs) != 1 || srcs[0].Kind != SourceKindOrg || srcs[0].Layer != store.LayerPlatform || srcs[0].PriceBookID == nil {
		t.Fatalf("platform source = %+v err=%v", srcs, err)
	}
	book, err := st.GetPriceBook(ctx, *srcs[0].PriceBookID)
	if err != nil || book.Name != store.PlanBookName || book.Scope != store.LayerPlatform || len(book.Items) != 4 {
		t.Fatalf("assigned book = %+v err=%v", book, err)
	}
	books, err := st.ListPriceBooks(ctx)
	if err != nil || len(books) != 1 {
		t.Fatalf("books after two syncs = %d err=%v, want the one plan book", len(books), err)
	}

	// The collector, 90 minutes after the customer was first synced: the
	// plan line is owed from that instant, so its slices sum to 1.5 h.
	now := c.CreatedAt.Add(90 * time.Minute)
	pc := &PlatformCollector{Repo: st, Metrics: metrics.New(), Now: func() time.Time { return now }}
	pc.ObserveNamespace(orgNamespace("agwalk"))
	if _, err := pc.EmitOrg(ctx, "agwalk"); err != nil {
		t.Fatal(err)
	}
	usage, err := st.UsageForRating(ctx, c.ID, now.Add(-2*time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].SKU != "plan.m" || usage[0].Unit != store.PlanUnit || usage[0].ResourceKind != store.PlanKind || usage[0].Quantity != "1.500000" || usage[0].ResourceCount != 1 {
		t.Fatalf("rateable usage = %+v, want one plan.m line of 1.5 plan-hours", usage)
	}
}
