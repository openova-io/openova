package openova

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

// ---- spec.commerce — the hand-over from the order (#6971 item 8) -----------

// captureLogs routes slog's default logger into a buffer for the test and
// restores it afterwards, so a WARN the adapter promises can be asserted on.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// commerceRepo is a fake with the matrix an M-package Organization meets:
// backup and waf optional on M, backup included on XL, ssl included
// everywhere and carrying no add-on SKU. The plans book is ensured first so
// the cells can hang off it.
func commerceRepo(t *testing.T) *fakeRepo {
	t.Helper()
	repo := newFakeRepo()
	if _, _, err := repo.EnsurePlanBook(context.Background()); err != nil {
		t.Fatal(err)
	}
	repo.addFeature("backup", "addon.backup")
	repo.addFeature("waf", "addon.waf")
	repo.addFeature("ssl", "")
	book := repo.planBook.ID
	repo.entitle(book, "m", "backup", store.EntitlementOptional)
	repo.entitle(book, "m", "waf", store.EntitlementOptional)
	repo.entitle(book, "m", "ssl", store.EntitlementIncluded)
	repo.entitle(book, "xl", "backup", store.EntitlementIncluded)
	repo.entitle(book, "xl", "ssl", store.EntitlementIncluded)
	repo.entitle(book, "s", "ssl", store.EntitlementIncluded)
	return repo
}

func withCommerce(pkg string, addons []any, extra map[string]any) func(spec map[string]any) {
	return func(spec map[string]any) {
		block := map[string]any{"packageSKU": pkg, "priceSource": "bss:OpenOva plans@2026-10-01", "orderID": "ord-6971"}
		if addons != nil {
			block["addons"] = addons
		}
		for k, v := range extra {
			block[k] = v
		}
		spec["commerce"] = block
	}
}

// TestReadOrgCommerce: the block is read tolerantly — absent is "nothing",
// a block that is not a map is "nothing", entries are trimmed and
// deduplicated — and the package stands in for an absent spec.planSlug only
// when it names a billable catalog plan.
func TestReadOrgCommerce(t *testing.T) {
	f, err := readOrg(orgUnstructured("plain", nil))
	if err != nil || f.Commerce.Present || f.Commerce.Addons != nil || f.PlanSlug != "s" || f.PlanFromPackage {
		t.Fatalf("no block → %+v err=%v, want absent and the default plan", f.Commerce, err)
	}
	f, err = readOrg(orgUnstructured("junk", func(spec map[string]any) { spec["commerce"] = "not-a-map" }))
	if err != nil || f.Commerce.Present {
		t.Fatalf("malformed block must read as absent, never fail: %+v err=%v", f.Commerce, err)
	}
	f, err = readOrg(orgUnstructured("shop", func(spec map[string]any) {
		delete(spec, "planSlug")
		withCommerce(" plan.l ", []any{" addon.backup ", "addon.backup", "", 7, "addon.waf"}, nil)(spec)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Commerce.Present || f.Commerce.PackageSKU != "plan.l" || f.Commerce.OrderID != "ord-6971" || f.Commerce.PriceSource != "bss:OpenOva plans@2026-10-01" {
		t.Fatalf("block = %+v", f.Commerce)
	}
	if strings.Join(f.Commerce.Addons, ",") != "addon.backup,addon.waf" {
		t.Fatalf("addons = %v, want trimmed and deduplicated in cart order", f.Commerce.Addons)
	}
	if f.PlanSlug != "l" || !f.PlanFromPackage {
		t.Fatalf("plan = %q from package %v, want l from the package", f.PlanSlug, f.PlanFromPackage)
	}
	// spec.planSlug present: it wins, whatever the package says.
	f, _ = readOrg(orgUnstructured("sized", func(spec map[string]any) { spec["planSlug"] = "s"; withCommerce("plan.l", nil, nil)(spec) }))
	if f.PlanSlug != "s" || f.PlanFromPackage {
		t.Fatalf("plan = %q from package %v, want spec.planSlug s", f.PlanSlug, f.PlanFromPackage)
	}
	// Not a package: flexi has none, an unknown plan is none, a non-plan SKU is none.
	for _, sku := range []string{"plan.flexi", "plan.platinum", "addon.backup", ""} {
		f, _ = readOrg(orgUnstructured("odd", func(spec map[string]any) { withCommerce(sku, nil, nil)(spec) }))
		if f.PlanSlug != "s" || f.PlanFromPackage {
			t.Fatalf("packageSKU %q alone → plan %q from package %v, want the default s", sku, f.PlanSlug, f.PlanFromPackage)
		}
	}
	// The Sovereign's own Organization buys nothing from itself.
	f, _ = readOrg(orgUnstructured("platform", func(spec map[string]any) { spec["kind"] = "internal"; withCommerce("plan.xl", nil, nil)(spec) }))
	if f.PlanSlug != "" || f.PlanFromPackage {
		t.Fatalf("internal org → plan %q, want none", f.PlanSlug)
	}
}

// TestSyncOrganizationAttachesAddonsFromOrder: the order's add-on SKUs become
// the Source's add-ons by feature key; a resync with the same order writes
// nothing; a changed order replaces the set.
func TestSyncOrganizationAttachesAddonsFromOrder(t *testing.T) {
	repo := commerceRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["planSlug"] = "m"
		withCommerce("plan.m", []any{"addon.waf", "addon.backup"}, nil)(spec)
	})
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	src := orgSourceOf(t, repo, "acme")
	if strings.Join(src.Addons, ",") != "backup,waf" {
		t.Fatalf("addons = %v, want backup,waf in matrix order", src.Addons)
	}
	if repo.addonWrites != 1 {
		t.Fatalf("SetSourceAddons writes = %d, want 1", repo.addonWrites)
	}
	if out := logs.String(); !strings.Contains(out, "attached to the Organization's order") || !strings.Contains(out, "ord-6971") || !strings.Contains(out, "bss:OpenOva plans@2026-10-01") {
		t.Fatalf("the first attach must record the order's provenance at Info:\n%s", out)
	}
	if strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("a clean attach must not warn:\n%s", logs.String())
	}

	// Resync with the same order, cart order reversed: nothing is written.
	logs.Reset()
	same := orgUnstructured("acme", func(spec map[string]any) {
		spec["planSlug"] = "m"
		withCommerce("plan.m", []any{"addon.backup", "addon.waf"}, nil)(spec)
	})
	if err := s.SyncOrganization(ctx, same); err != nil {
		t.Fatal(err)
	}
	if repo.addonWrites != 1 {
		t.Fatalf("resync wrote the add-ons again: %d writes, want 1", repo.addonWrites)
	}
	if strings.Contains(logs.String(), "attached to the Organization's order") {
		t.Fatalf("an unchanged set must not be re-attached:\n%s", logs.String())
	}

	// The order now carries backup only: the set is replaced.
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) {
		spec["planSlug"] = "m"
		withCommerce("plan.m", []any{"addon.backup"}, nil)(spec)
	})); err != nil {
		t.Fatal(err)
	}
	if src = orgSourceOf(t, repo, "acme"); strings.Join(src.Addons, ",") != "backup" || repo.addonWrites != 2 {
		t.Fatalf("after the order changed: addons %v writes %d, want backup and 2", src.Addons, repo.addonWrites)
	}
}

// TestSyncOrganizationRefusedAddonIsLoggedNotFatal: backup is INCLUDED in
// XL, so an XL order carrying addon.backup is refused by the store as
// redundant. The sync still succeeds, the Source keeps the add-ons it had
// (none), and the WARN names the org, the SKU and the store's sentence.
func TestSyncOrganizationRefusedAddonIsLoggedNotFatal(t *testing.T) {
	repo := commerceRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	org := orgUnstructured("big", func(spec map[string]any) {
		spec["planSlug"] = "xl"
		withCommerce("plan.xl", []any{"addon.backup"}, nil)(spec)
	})
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatalf("a refused add-on must never fail the sync: %v", err)
	}
	c, ok := repo.customerBySlug("big")
	if !ok || c.Status != "active" || c.PlanSlug != "xl" {
		t.Fatalf("customer = %+v ok=%v; the customer must sync regardless", c, ok)
	}
	src := orgSourceOf(t, repo, "big")
	if len(src.Addons) != 0 || repo.addonWrites != 0 || src.PriceBookID == nil {
		t.Fatalf("source = addons %v writes %d book %v; want nothing attached, source still on its book", src.Addons, repo.addonWrites, src.PriceBookID)
	}
	out := logs.String()
	if !strings.Contains(out, "level=WARN") || !strings.Contains(out, "org=big") || !strings.Contains(out, "addon.backup") || !strings.Contains(out, "included in the XL package") {
		t.Fatalf("WARN must name the org, the SKU and the store's refusal:\n%s", out)
	}

	// Not offered on S either (no cell): refused the same way, sync still fine.
	logs.Reset()
	if err := s.SyncOrganization(ctx, orgUnstructured("small", func(spec map[string]any) {
		spec["planSlug"] = "s"
		withCommerce("plan.s", []any{"addon.waf"}, nil)(spec)
	})); err != nil {
		t.Fatal(err)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "not offered on the S package") || !strings.Contains(out, "addon.waf") {
		t.Fatalf("WARN must carry the store's not-offered sentence:\n%s", out)
	}
	if n := len(orgSourceOf(t, repo, "small").Addons); n != 0 || repo.addonWrites != 0 {
		t.Fatalf("not-offered add-on attached: %d add-ons, %d writes", n, repo.addonWrites)
	}
}

// TestSyncOrganizationUnknownAddonSKUIsSkipped: a SKU no feature carries is
// warned about and skipped; the rest of the order is applied. An order whose
// SKUs are ALL unknown applies nothing and writes nothing.
func TestSyncOrganizationUnknownAddonSKUIsSkipped(t *testing.T) {
	repo := commerceRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	org := orgUnstructured("acme", func(spec map[string]any) {
		spec["planSlug"] = "m"
		withCommerce("plan.m", []any{"addon.nope", "addon.backup"}, nil)(spec)
	})
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	if src := orgSourceOf(t, repo, "acme"); strings.Join(src.Addons, ",") != "backup" || repo.addonWrites != 1 {
		t.Fatalf("addons = %v writes %d, want backup attached and the unknown SKU skipped", src.Addons, repo.addonWrites)
	}
	if out := logs.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "addon_sku=addon.nope") || !strings.Contains(out, "names no feature") {
		t.Fatalf("WARN must name the unknown SKU:\n%s", out)
	}
	if err := s.SyncOrganization(ctx, orgUnstructured("lost", func(spec map[string]any) {
		spec["planSlug"] = "m"
		withCommerce("plan.m", []any{"addon.nope"}, nil)(spec)
	})); err != nil {
		t.Fatal(err)
	}
	if n := len(orgSourceOf(t, repo, "lost").Addons); n != 0 || repo.addonWrites != 1 {
		t.Fatalf("all-unknown order: %d add-ons, %d writes; want nothing applied and nothing written", n, repo.addonWrites)
	}
}

// TestSyncOrganizationAbsentCommerceLeavesHandSetAddons: add-ons a
// sovereign-admin set through the console stay when the CR carries no
// commerce block, a block without addons, or an empty list — only an
// explicit, non-empty spec.commerce.addons drives the set.
func TestSyncOrganizationAbsentCommerceLeavesHandSetAddons(t *testing.T) {
	repo := commerceRepo(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	plain := orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "m" })
	if err := s.SyncOrganization(ctx, plain); err != nil {
		t.Fatal(err)
	}
	// The console: PUT /customers/{id}/sources/{sid}/addons ["waf"].
	src := orgSourceOf(t, repo, "acme")
	if _, err := repo.SetSourceAddons(ctx, src.ID, []string{"waf"}); err != nil {
		t.Fatal(err)
	}
	writes := repo.addonWrites
	for name, mutate := range map[string]func(spec map[string]any){
		"no block":         func(spec map[string]any) { spec["planSlug"] = "m" },
		"block, no addons": func(spec map[string]any) { spec["planSlug"] = "m"; withCommerce("plan.m", nil, nil)(spec) },
		"empty addons":     func(spec map[string]any) { spec["planSlug"] = "m"; withCommerce("plan.m", []any{}, nil)(spec) },
		"blank addons":     func(spec map[string]any) { spec["planSlug"] = "m"; withCommerce("plan.m", []any{"", "  "}, nil)(spec) },
	} {
		if err := s.SyncOrganization(ctx, orgUnstructured("acme", mutate)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := orgSourceOf(t, repo, "acme"); strings.Join(got.Addons, ",") != "waf" || repo.addonWrites != writes {
			t.Fatalf("%s: addons %v writes %d; the hand-set add-on must stay untouched", name, got.Addons, repo.addonWrites)
		}
	}
}

// TestSyncOrganizationPackageSKUMismatchWarnsAndKeepsPlanSlug: spec.planSlug
// is the plan; a packageSKU that names another package is said once per sync
// and changes nothing.
func TestSyncOrganizationPackageSKUMismatchWarnsAndKeepsPlanSlug(t *testing.T) {
	repo := commerceRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	org := orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "s"; withCommerce("plan.m", nil, nil)(spec) })
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("acme")
	if c.PlanSlug != "s" {
		t.Fatalf("plan = %q, want spec.planSlug s kept over the package", c.PlanSlug)
	}
	out := logs.String()
	if n := strings.Count(out, "disagrees with spec.planSlug"); n != 1 {
		t.Fatalf("mismatch WARN count = %d, want exactly one per sync:\n%s", n, out)
	}
	if !strings.Contains(out, "org=acme") || !strings.Contains(out, "plan=s") || !strings.Contains(out, "package_sku=plan.m") {
		t.Fatalf("WARN must name the org, the plan slug and the package SKU:\n%s", out)
	}
	// Agreement, in any case: silent.
	logs.Reset()
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "S"; withCommerce("PLAN.S", nil, nil)(spec) })); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "disagrees") {
		t.Fatalf("plan.s vs planSlug s must not warn:\n%s", logs.String())
	}
}

// TestSyncOrganizationPackageSKUAloneSetsThePlan: an Organization the
// storefront created with only the commerce block bills its package — the
// customer is on the package's plan, its Source on the plans book, and the
// order's add-ons attach against that plan.
func TestSyncOrganizationPackageSKUAloneSetsThePlan(t *testing.T) {
	repo := commerceRepo(t)
	logs := captureLogs(t)
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	org := orgUnstructured("shop", func(spec map[string]any) {
		delete(spec, "planSlug")
		withCommerce("plan.m", []any{"addon.backup"}, nil)(spec)
	})
	if err := s.SyncOrganization(ctx, org); err != nil {
		t.Fatal(err)
	}
	c, _ := repo.customerBySlug("shop")
	if c.PlanSlug != "m" {
		t.Fatalf("plan = %q, want m from plan.m", c.PlanSlug)
	}
	src := orgSourceOf(t, repo, "shop")
	if src.PriceBookID == nil || *src.PriceBookID != repo.planBook.ID || strings.Join(src.Addons, ",") != "backup" {
		t.Fatalf("source = book %v addons %v, want the plans book with backup", src.PriceBookID, src.Addons)
	}
	if strings.Contains(logs.String(), "disagrees") {
		t.Fatalf("a plan taken from the package cannot disagree with it:\n%s", logs.String())
	}
	// A package that is not one (flexi has no package) leaves the default
	// plan, and says so.
	logs.Reset()
	if err := s.SyncOrganization(ctx, orgUnstructured("flex", func(spec map[string]any) { delete(spec, "planSlug"); withCommerce("plan.flexi", nil, nil)(spec) })); err != nil {
		t.Fatal(err)
	}
	if c, _ = repo.customerBySlug("flex"); c.PlanSlug != "s" {
		t.Fatalf("plan.flexi alone → plan %q, want the default s", c.PlanSlug)
	}
	if !strings.Contains(logs.String(), "disagrees with spec.planSlug") {
		t.Fatalf("the default plan standing in for a non-package SKU must be said:\n%s", logs.String())
	}
}
