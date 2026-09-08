package openova

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func testKeys(t *testing.T) *crypto.Keyring {
	t.Helper()
	keys, err := crypto.NewKeyringFromBytes(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// orgUnstructured builds an Organization CR the way the apiserver would
// hand it to the dynamic informer.
func orgUnstructured(slug string, mutate func(spec map[string]any)) *unstructured.Unstructured {
	spec := map[string]any{
		"slug":         slug,
		"displayName":  "ACME Corp",
		"kind":         "customer",
		"tier":         "org",
		"billingMode":  "real",
		"sovereignRef": "t99.omani.works",
		"owners": []any{
			map[string]any{"email": "ceo@acme.example", "role": "owner"},
		},
	}
	if mutate != nil {
		mutate(spec)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata":   map[string]any{"name": slug},
		"spec":       spec,
	}}
}

type fakeVerifier struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (v *fakeVerifier) VerifyProject(_ context.Context, region, projectID, accessKey, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls = append(v.calls, region+"/"+projectID+"/"+accessKey)
	return v.err
}

func waitFor(t *testing.T, d time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestOrgSyncInformerCreatesAndSuspends drives the real list+watch against
// the fake dynamic client: an existing Organization becomes an active
// customer with its platform source; deleting the CR SUSPENDS the customer
// (never deletes — history is billing data).
func TestOrgSyncInformerCreatesAndSuspends(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{OrganizationGVR: "OrganizationList"},
		orgUnstructured("acme", nil))
	repo := newFakeRepo()
	s := &OrgSync{Dyn: dyn, Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New(), Resync: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	waitFor(t, 5*time.Second, "customer upserted from the Organization CR", func() bool {
		c, ok := repo.customerBySlug("acme")
		return ok && c.Status == "active"
	})
	c, _ := repo.customerBySlug("acme")
	if c.Kind != "organization" || c.BillingMode != "real" || c.AdminEmail != "ceo@acme.example" {
		t.Fatalf("customer = %+v", c)
	}
	if c.OrgSlug == nil || *c.OrgSlug != "acme" {
		t.Fatalf("org_slug = %v, want acme", c.OrgSlug)
	}
	waitFor(t, 5*time.Second, "verified platform source", func() bool {
		for _, src := range repo.sourcesOf(c.ID) {
			if src.Kind == SourceKindOrg && src.Status == "verified" {
				return true
			}
		}
		return false
	})

	if err := dyn.Resource(OrganizationGVR).Delete(ctx, "acme", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "customer suspended on Organization delete", func() bool {
		got, ok := repo.customerBySlug("acme")
		return ok && got.Status == "suspended"
	})
}

// TestSyncOrganizationCostSources: spec.costSources[] become cost_sources
// with the credential resolved read-only from the named Secret in the Org's
// host namespace, verified through the Verifier, and a resync never mints a
// second credential.
func TestSyncOrganizationCostSources(t *testing.T) {
	repo := newFakeRepo()
	keys := testKeys(t)
	core := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "acme", Name: "acme-huawei-aksk"},
		Data:       map[string][]byte{"aksk": []byte("AKTEST:SKSECRET")},
	})
	ver := &fakeVerifier{}
	s := &OrgSync{Core: core, Repo: repo, Keys: keys, Verifier: ver, Metrics: metrics.New()}
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["costSources"] = []any{
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-1",
				"credentialRef": map[string]any{"name": "acme-huawei-aksk", "key": "aksk"}},
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-2"},
		}
	})
	ctx := context.Background()
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, ok := repo.customerBySlug("acme")
	if !ok || c.Status != "active" {
		t.Fatalf("customer = %+v ok=%v", c, ok)
	}
	var declared, bare, platform int
	for _, src := range repo.sourcesOf(c.ID) {
		switch {
		case src.Kind == SourceKindOrg && src.Status == "verified":
			platform++
		case src.Kind == "huawei-project" && src.ProjectID == "proj-1":
			declared++
			if src.Status != "verified" || src.AccessKey != "AKTEST" {
				t.Fatalf("declared source = %+v", src)
			}
			sk, err := keys.Open(repo.credEnc[*src.CredentialID])
			if err != nil || string(sk) != "SKSECRET" {
				t.Fatalf("stored secret = %q err=%v", sk, err)
			}
		case src.Kind == "huawei-project" && src.ProjectID == "proj-2":
			bare++
			if src.Status != "pending" || src.CredentialID != nil {
				t.Fatalf("bare source must stay pending for UI credential entry: %+v", src)
			}
		}
	}
	if platform != 1 || declared != 1 || bare != 1 {
		t.Fatalf("sources platform=%d declared=%d bare=%d, want 1/1/1", platform, declared, bare)
	}
	if len(ver.calls) != 1 || ver.calls[0] != "me-east-215/proj-1/AKTEST" {
		t.Fatalf("verifier calls = %v", ver.calls)
	}

	// Resync: nothing changed → no new credential, no duplicate source.
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if len(repo.creds) != 1 {
		t.Fatalf("resync minted credentials: %d, want 1", len(repo.creds))
	}
	if n := len(repo.sourcesOf(c.ID)); n != 3 {
		t.Fatalf("resync duplicated sources: %d, want 3", n)
	}
}

// TestSyncOrganizationFailedVerificationRedactsSecret: a failing declared
// credential flips the source to failed with the secret redacted from
// last_error, and the sync itself still succeeds (per-source isolation).
func TestSyncOrganizationFailedVerificationRedactsSecret(t *testing.T) {
	repo := newFakeRepo()
	core := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "acme", Name: "aksk"},
		Data:       map[string][]byte{"v": []byte(`{"accessKey":"AKX","secretKey":"SKX"}`)},
	})
	ver := &fakeVerifier{err: errors.New("gateway said no to SKX")}
	s := &OrgSync{Core: core, Repo: repo, Keys: testKeys(t), Verifier: ver, Metrics: metrics.New()}
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["costSources"] = []any{
			map[string]any{"kind": "huawei-project", "region": "me-east-215", "projectId": "proj-9",
				"credentialRef": map[string]any{"name": "aksk", "key": "v"}},
		}
	})
	if err := s.SyncOrganization(context.Background(), org); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	for _, src := range repo.sourcesOf(c.ID) {
		if src.Kind != "huawei-project" {
			continue
		}
		if src.Status != "failed" || src.LastError == nil {
			t.Fatalf("source = %+v", src)
		}
		if strings.Contains(*src.LastError, "SKX") {
			t.Fatalf("last_error leaks the secret: %q", *src.LastError)
		}
	}
}

// TestReadOrgOwnerAndBillingDefaults: the owner-role email wins over the
// roster order and an absent billingMode falls back to showback.
func TestReadOrgOwnerAndBillingDefaults(t *testing.T) {
	org := orgUnstructured("bank", func(spec map[string]any) {
		spec["owners"] = []any{
			map[string]any{"email": "admin@bank.example", "role": "admin"},
			map[string]any{"email": "cto@bank.example", "role": "owner"},
		}
		delete(spec, "billingMode")
	})
	f, err := readOrg(org)
	if err != nil {
		t.Fatal(err)
	}
	if f.AdminEmail != "cto@bank.example" {
		t.Fatalf("admin email = %q, want the owner-role entry", f.AdminEmail)
	}
	if f.BillingMode != "showback" {
		t.Fatalf("billing mode = %q, want the showback floor", f.BillingMode)
	}

	// No roster at all → blank-pending.
	f2, err := readOrg(orgUnstructured("solo", func(spec map[string]any) { delete(spec, "owners") }))
	if err != nil {
		t.Fatal(err)
	}
	if f2.AdminEmail != "" {
		t.Fatalf("admin email = %q, want blank-pending", f2.AdminEmail)
	}
}

func TestParseAKSK(t *testing.T) {
	ak, sk, err := parseAKSK([]byte("AK1:SK1\n"))
	if err != nil || ak != "AK1" || string(sk) != "SK1" {
		t.Fatalf("colon form: %q/%q err=%v", ak, sk, err)
	}
	ak, sk, err = parseAKSK([]byte(`{"access_key":"AK2","secret_key":"SK2"}`))
	if err != nil || ak != "AK2" || string(sk) != "SK2" {
		t.Fatalf("snake json: %q/%q err=%v", ak, sk, err)
	}
	if _, _, err := parseAKSK([]byte("just-one-token")); err == nil {
		t.Fatal("want error for a value with no separator")
	}
}

// TestReadOrgPlanSlug: spec.planSlug lower-cased; absent → "s" (the
// org-controller's default); the Sovereign's own Organization has no plan.
func TestReadOrgPlanSlug(t *testing.T) {
	f, err := readOrg(orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = " M " }))
	if err != nil || f.PlanSlug != "m" {
		t.Fatalf("planSlug M → %q err=%v", f.PlanSlug, err)
	}
	f, err = readOrg(orgUnstructured("legacy", nil))
	if err != nil || f.PlanSlug != "s" {
		t.Fatalf("absent planSlug → %q, want s", f.PlanSlug)
	}
	f, err = readOrg(orgUnstructured("platform", func(spec map[string]any) { spec["kind"] = "internal"; spec["planSlug"] = "xl" }))
	if err != nil || !f.Internal || f.PlanSlug != "" {
		t.Fatalf("internal org → plan %q internal=%v, want no plan", f.PlanSlug, f.Internal)
	}
}

// TestSyncOrganizationPlanAndBook: a synced Organization carries its plan
// and its PLATFORM SOURCE is put on the "OpenOva plans" book when it has
// none (DESIGN.md §2 — the book belongs to the source, never to the
// customer); an explicit book on the source is never overwritten; a plan
// change on the CR follows; the book is ensured, never re-created (the fake
// counts calls and returns the same book).
func TestSyncOrganizationPlanAndBook(t *testing.T) {
	repo := newFakeRepo()
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	acme := orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "m" })
	if err := s.SyncOrganization(ctx, acme); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	if c.PlanSlug != "m" || c.PriceBookID != nil {
		t.Fatalf("customer = plan %q book %v, want m and NO customer-level book", c.PlanSlug, c.PriceBookID)
	}
	orgSource := func() store.CostSource {
		t.Helper()
		for _, src := range repo.sourcesOf(c.ID) {
			if src.Kind == SourceKindOrg {
				return src
			}
		}
		t.Fatalf("no platform source for acme: %+v", repo.sourcesOf(c.ID))
		return store.CostSource{}
	}
	src := orgSource()
	if src.Layer != store.LayerPlatform || src.PriceBookID == nil || *src.PriceBookID != repo.planBook.ID {
		t.Fatalf("platform source = layer %q book %v, want the plan book %s", src.Layer, src.PriceBookID, repo.planBook.ID)
	}
	if repo.planBook.Name != store.PlanBookName || len(repo.planBook.Items) != 4 {
		t.Fatalf("plan book = %+v", repo.planBook)
	}

	// Operator moves the SOURCE to a negotiated clone; the CR upgrades to xl.
	clone := "book-negotiated"
	if err := repo.SetSourcePriceBook(ctx, src.ID, clone); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "XL" })); err != nil {
		t.Fatal(err)
	}
	c, _ = repo.customerBySlug("acme")
	if src = orgSource(); c.PlanSlug != "xl" || src.PriceBookID == nil || *src.PriceBookID != clone {
		t.Fatalf("after resync: plan %q source book %v, want xl on the negotiated clone", c.PlanSlug, src.PriceBookID)
	}

	// A source that lost its book gets the plan book back; the book itself
	// was ensured on every sync and created exactly once.
	if err := repo.SetSourcePriceBook(ctx, src.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, acme); err != nil {
		t.Fatal(err)
	}
	if src = orgSource(); src.PriceBookID == nil || *src.PriceBookID != repo.planBook.ID {
		t.Fatalf("bookless source not re-assigned: %v", src.PriceBookID)
	}
	if repo.planCalls != 3 {
		t.Fatalf("EnsurePlanBook calls = %d, want one per Organization sync (3)", repo.planCalls)
	}
}

// TestSyncInternalOrganizationIsNotACustomer (DESIGN.md §2, founder
// direction 2026-09-08): the Sovereign's OWN Organization gets NO customer
// row at all — its footprint lives on the internal openova-platform source
// with no customer — and the plan book is not even consulted for it. On the
// old code this created a customer, which is the mixing the founder rejected.
func TestSyncInternalOrganizationIsNotACustomer(t *testing.T) {
	repo := newFakeRepo()
	sink := &fakeOverheadSink{}
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New(), OverheadSink: sink}
	ctx := context.Background()
	org := orgUnstructured("platform", func(spec map[string]any) { spec["kind"] = "internal"; spec["planSlug"] = "xl" })
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if _, ok := repo.customerBySlug("platform"); ok {
		t.Fatal("the Sovereign's own Organization was synced as a customer — the Sovereign is not a customer")
	}
	if repo.planCalls != 0 {
		t.Fatalf("plan book consulted for the internal Organization: %d calls", repo.planCalls)
	}
	if sink.slug != "platform" {
		t.Fatalf("overhead sink = %q, want the internal Organization's slug", sink.slug)
	}
	src, ok := repo.internalSource("platform")
	if !ok {
		t.Fatal("no internal platform source ensured; the Sovereign's footprint would be dropped")
	}
	if !src.Internal || src.CustomerID != "" || src.Kind != store.SourceKindPlatform || src.Layer != store.LayerPlatform || src.Status != "verified" {
		t.Fatalf("internal source = %+v", src)
	}
	// A second sync is idempotent: no duplicate source, still no customer.
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if n := len(repo.sources); n != 1 {
		t.Fatalf("sources after resync = %d, want the one internal source", n)
	}
	// Deleting the internal Organization suspends nothing (it has no customer).
	if err := s.SuspendOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
}

// TestSyncInternalOrganizationRetiresTheOldCustomer: a database written by
// the previous model, where the Sovereign's own Organization IS a customer
// holding an openova-org source, is migrated on the first sync — the
// customer becomes a plain external one (its cloud sources stay with it) and
// the openova-org source becomes the internal source with no customer.
func TestSyncInternalOrganizationRetiresTheOldCustomer(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	old := repo.addActiveCustomer("hw307-omani-works")
	orgSrc, _, err := repo.UpsertSource(ctx, old.ID, SourceKindOrg, "", "hw307-omani-works")
	if err != nil {
		t.Fatal(err)
	}
	cloudSrc, _, err := repo.UpsertSource(ctx, old.ID, "huawei-project", "me-east-215", "proj-landlord")
	if err != nil {
		t.Fatal(err)
	}
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	if err := s.SyncOrganization(ctx, orgUnstructured("hw307-omani-works", func(spec map[string]any) { spec["kind"] = "internal" })); err != nil {
		t.Fatal(err)
	}
	c, ok := repo.customerBySlug("hw307-omani-works")
	if !ok || c.Kind != "external" || c.OrgSlug != nil || c.PlanSlug != "" {
		t.Fatalf("landlord customer after retirement = %+v ok=%v", c, ok)
	}
	if len(repo.retired) != 1 || repo.retired[0] != old.ID {
		t.Fatalf("RetireOrganizationCustomer calls = %v", repo.retired)
	}
	var cloud, internal, org int
	for _, src := range repo.sourcesOf(c.ID) {
		switch src.Kind {
		case "huawei-project":
			cloud++
			if src.ID != cloudSrc.ID || src.Layer != store.LayerCloud {
				t.Fatalf("the landlord's cloud source must stay with it: %+v", src)
			}
		case SourceKindOrg:
			org++
		}
	}
	if src, ok := repo.internalSource("hw307-omani-works"); ok && src.ID == orgSrc.ID && src.Internal && src.CustomerID == "" {
		internal++
	}
	if cloud != 1 || org != 0 || internal != 1 {
		t.Fatalf("after retirement: cloud=%d org=%d internal=%d, want 1/0/1", cloud, org, internal)
	}
}

// fakeOverheadSink records the slug OrgSync publishes to the collector.
type fakeOverheadSink struct{ slug string }

func (f *fakeOverheadSink) SetOverheadOrg(slug string) { f.slug = slug }

// TestSyncOrganizationResumeStampsPlatformSource: an Organization that was
// deleted (customer suspended) and re-created resumes with its platform
// source's collection stamp at the resume instant, so the collector never
// bills the plan across the gap. A plain resync of an active customer does
// not touch the stamp.
func TestSyncOrganizationResumeStampsPlatformSource(t *testing.T) {
	repo := newFakeRepo()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New(), Now: func() time.Time { return now }}
	ctx := context.Background()
	org := orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "m" })
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	src := repo.sourcesOf(c.ID)[0]
	if src.LastCollectedAt != nil {
		t.Fatalf("a fresh source must keep its backfill window: stamp = %v", src.LastCollectedAt)
	}
	stamp := now.Add(-3 * 24 * time.Hour)
	if err := repo.SetSourceCollected(ctx, src.ID, stamp); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if got := repo.sourcesOf(c.ID)[0].LastCollectedAt; got == nil || !got.Equal(stamp) {
		t.Fatalf("active resync moved the stamp: %v", got)
	}

	if err := s.SuspendOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, _ = repo.customerBySlug("acme")
	if got := repo.sourcesOf(c.ID)[0].LastCollectedAt; c.Status != "active" || got == nil || !got.Equal(now) {
		t.Fatalf("resume: status %s stamp %v, want active at %v", c.Status, got, now)
	}
}
