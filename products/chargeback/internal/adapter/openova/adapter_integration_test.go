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
	// Both platform books exist after a sync — the plans book and the
	// pay-per-use book a flexi Organization would be billed by (§2.9a) — and
	// two syncs create each exactly once.
	books, err := st.ListPriceBooks(ctx)
	if err != nil || len(books) != 2 {
		t.Fatalf("books after two syncs = %d err=%v, want the two platform books", len(books), err)
	}
	names := map[string]string{}
	for _, b := range books {
		if b.Scope != store.LayerPlatform {
			t.Fatalf("book %q has scope %q, want platform", b.Name, b.Scope)
		}
		names[b.Name] = b.ID
	}
	if names[store.PlanBookName] == "" || names[store.PAYGBookName] == "" {
		t.Fatalf("books = %+v, want %q and %q", books, store.PlanBookName, store.PAYGBookName)
	}
	if *srcs[0].PriceBookID != names[store.PlanBookName] {
		t.Fatalf("the plan m Organization is on %v, want the plans book %s", srcs[0].PriceBookID, names[store.PlanBookName])
	}
	payg, err := st.GetPriceBook(ctx, names[store.PAYGBookName])
	if err != nil || len(payg.Items) != 3 {
		t.Fatalf("pay-per-use book = %+v err=%v, want the three platform meters priced", payg, err)
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

// TestIntegrationCommerceAddonsAgainstStore (DESIGN.md §22.9): on real SQL
// an Organization created from an order attaches its platform source to the
// add-on the order names through the matrix — the source_addons row is
// written once, a resync writes nothing, and a package that includes the
// feature already is refused by the store without failing the sync.
func TestIntegrationCommerceAddonsAgainstStore(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	plans, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The matrix: backup is an add-on priced in the plans book, optional on
	// M and included in XL.
	if _, err := st.AddPriceItem(ctx, plans.ID, store.PriceItem{SKU: "addon.backup", Unit: store.PlanUnit, UnitPrice: "0.00205479", Description: "Backup add-on"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFeature(ctx, store.FeatureInput{Key: "backup", Name: "Backup", Kind: store.FeatureKindBoolean, AddonSKU: "addon.backup", SortOrder: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutEntitlement(ctx, plans.ID, store.PlanSKU("m"), "backup", store.EntitlementInput{State: store.EntitlementOptional}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutEntitlement(ctx, plans.ID, store.PlanSKU("xl"), "backup", store.EntitlementInput{State: store.EntitlementIncluded}); err != nil {
		t.Fatal(err)
	}

	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: st, Keys: testKeys(t), Metrics: metrics.New()}
	// Created by the storefront: no spec.planSlug, the package and the add-on
	// in spec.commerce.
	org := orgUnstructured("agshop", func(spec map[string]any) {
		delete(spec, "planSlug")
		spec["commerce"] = map[string]any{"packageSKU": "plan.m", "addons": []any{"addon.backup"}, "priceSource": "bss:OpenOva plans@2026-10-01", "orderID": "ord-int-1"}
	})
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetCustomerBySlug(ctx, "agshop")
	if err != nil || c.PlanSlug != "m" || c.Status != "active" {
		t.Fatalf("customer = %+v err=%v, want active on plan m from plan.m", c, err)
	}
	srcs, err := st.ListSources(ctx, store.OperatorScope, c.ID)
	if err != nil || len(srcs) != 1 || srcs[0].PriceBookID == nil || *srcs[0].PriceBookID != plans.ID {
		t.Fatalf("platform source = %+v err=%v, want one on the plans book", srcs, err)
	}
	if len(srcs[0].Addons) != 1 || srcs[0].Addons[0] != "backup" {
		t.Fatalf("addons = %v, want backup from addon.backup", srcs[0].Addons)
	}
	// The row the rating run reads.
	var rows int
	if err := st.DB().QueryRowContext(ctx, `SELECT count(*) FROM source_addons WHERE source_id = $1`, srcs[0].ID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("source_addons rows = %d err=%v, want 1", rows, err)
	}
	var takenAt time.Time
	if err := st.DB().QueryRowContext(ctx, `SELECT taken_at FROM source_addons WHERE source_id = $1`, srcs[0].ID).Scan(&takenAt); err != nil {
		t.Fatal(err)
	}
	// Resync: the same order writes nothing — the row keeps its taken_at.
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatalf("resync: %v", err)
	}
	var takenAgain time.Time
	if err := st.DB().QueryRowContext(ctx, `SELECT taken_at FROM source_addons WHERE source_id = $1`, srcs[0].ID).Scan(&takenAgain); err != nil || !takenAgain.Equal(takenAt) {
		t.Fatalf("resync rewrote the add-on row: taken_at %v → %v err=%v", takenAt, takenAgain, err)
	}

	// An XL order carrying backup: included already, refused by the store,
	// the customer and its source still sync.
	big := orgUnstructured("agbig", func(spec map[string]any) {
		spec["planSlug"] = "xl"
		spec["commerce"] = map[string]any{"packageSKU": "plan.xl", "addons": []any{"addon.backup"}, "orderID": "ord-int-2"}
	})
	if err := s.SyncOrganization(ctx, big); err != nil {
		t.Fatalf("a refused add-on must not fail the sync: %v", err)
	}
	cb, err := st.GetCustomerBySlug(ctx, "agbig")
	if err != nil || cb.PlanSlug != "xl" || cb.Status != "active" {
		t.Fatalf("customer = %+v err=%v", cb, err)
	}
	bigSrcs, err := st.ListSources(ctx, store.OperatorScope, cb.ID)
	if err != nil || len(bigSrcs) != 1 || len(bigSrcs[0].Addons) != 0 || bigSrcs[0].PriceBookID == nil {
		t.Fatalf("XL source = %+v err=%v, want on its book with no add-on", bigSrcs, err)
	}
}
