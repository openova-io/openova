package openova

import (
	"context"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Which rate card an Organization's platform source is put on is decided by
// its PLAN (EPIC #6867, founder direction 2026-09-10). Since the flexi plan
// was retired (DESIGN.md §22.11, founder direction 2026-10-10) every
// Organization is on a package and on the plans book: an Organization still
// on flexi is billed as S in grow mode — pay as you go above a commitment —
// and a source an earlier version left on the pay-per-use book is moved back.

// orgSourceOf returns the Organization's platform source.
func orgSourceOf(t *testing.T, repo *fakeRepo, slug string) store.CostSource {
	t.Helper()
	c, ok := repo.customerBySlug(slug)
	if !ok {
		t.Fatalf("no customer for %s", slug)
	}
	for _, src := range repo.sourcesOf(c.ID) {
		if src.Kind == SourceKindOrg {
			return src
		}
	}
	t.Fatalf("no platform source for %s: %+v", slug, repo.sourcesOf(c.ID))
	return store.CostSource{}
}

func bookNameOf(t *testing.T, repo *fakeRepo, slug string) string {
	t.Helper()
	src := orgSourceOf(t, repo, slug)
	if src.PriceBookID == nil {
		t.Fatalf("%s platform source has no price book — it rates at zero", slug)
	}
	return src.PriceBookName
}

// TestSyncAssignsTheBookThePlanCallsFor: every plan the catalog sells lands
// on the right one of the two platform books, and an unknown or absent plan
// keeps the plans book, which is the fallback that was already there.
func TestSyncAssignsTheBookThePlanCallsFor(t *testing.T) {
	for _, tc := range []struct {
		plan string
		want string
	}{
		{"s", store.PlanBookName},
		{"m", store.PlanBookName},
		{"l", store.PlanBookName},
		{"xl", store.PlanBookName},
		{"flexi", store.PlanBookName}, // retired: billed as S in grow mode
		{"FLEXI", store.PlanBookName},
		{"", store.PlanBookName},         // absent → the controller's default "s"
		{"platinum", store.PlanBookName}, // unknown → today's fallback, never invented
	} {
		repo := newFakeRepo()
		s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
		org := orgUnstructured("acme", func(spec map[string]any) {
			if tc.plan != "" {
				spec["planSlug"] = tc.plan
			}
		})
		if err := s.SyncOrganization(context.Background(), org); err != nil {
			t.Fatalf("plan %q: %v", tc.plan, err)
		}
		if got := bookNameOf(t, repo, "acme"); got != tc.want {
			t.Fatalf("plan %q went to the %q book, want %q", tc.plan, got, tc.want)
		}
	}
}

// TestSyncMapsTheRetiredFlexiPlanToSGrow: an Organization still on flexi is
// synced as S in grow mode on the plans book, and a source an earlier
// version put on the pay-per-use book for flexi is moved back to the plans
// book — the bill follows the S package the controller renders, and nothing
// rates flexi any more.
func TestSyncMapsTheRetiredFlexiPlanToSGrow(t *testing.T) {
	repo := newFakeRepo()
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	sync := func(plan string) {
		t.Helper()
		if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = plan })); err != nil {
			t.Fatalf("sync on plan %s: %v", plan, err)
		}
	}
	sync("m")
	if got := bookNameOf(t, repo, "acme"); got != store.PlanBookName {
		t.Fatalf("plan m is on the %q book", got)
	}
	// The flexi era: the source sat on the pay-per-use book.
	src := orgSourceOf(t, repo, "acme")
	if err := repo.SetSourcePriceBook(ctx, src.ID, repo.paygBook.ID); err != nil {
		t.Fatal(err)
	}
	repo.growPackages = map[string]store.PackageLimits{repo.planBook.ID + "|s": {PlanSlug: "s", GrowAllowed: true,
		Headline: store.GrowCeiling{VCPU: dp("1"), MemoryGB: dp("2")}, Ceiling: store.GrowCeiling{VCPU: dp("8"), MemoryGB: dp("16"), DiskGB: dp("250"), BandwidthMbps: dp("1000")}}}
	sync("flexi")
	if got := bookNameOf(t, repo, "acme"); got != store.PlanBookName {
		t.Fatalf("a flexi Organization is on the %q book, want the plans book (S in grow mode)", got)
	}
	c, _ := repo.customerBySlug("acme")
	if c.PlanSlug != "s" {
		t.Fatalf("customer plan = %q, want s — flexi is retired", c.PlanSlug)
	}
	if got := orgSourceOf(t, repo, "acme"); got.OverageMode != store.OverageModeGrow {
		t.Fatalf("overage mode = %q, want grow", got.OverageMode)
	}
	// Books are ensured once per sync and created once each.
	if repo.planCalls != 2 || repo.paygCalls != 2 {
		t.Fatalf("ensure calls = plans %d payg %d, want one of each per sync (2)", repo.planCalls, repo.paygCalls)
	}
}

// TestSyncNeverOverwritesAnOperatorsBook: a source an operator put on some
// other book — a negotiated clone, say — is left alone whatever the plan says
// and however often the plan changes.
func TestSyncNeverOverwritesAnOperatorsBook(t *testing.T) {
	repo := newFakeRepo()
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "flexi" })); err != nil {
		t.Fatal(err)
	}
	src := orgSourceOf(t, repo, "acme")
	const negotiated = "book-negotiated-payg"
	if err := repo.SetSourcePriceBook(ctx, src.ID, negotiated); err != nil {
		t.Fatal(err)
	}
	for _, plan := range []string{"flexi", "m", "flexi", "xl"} {
		if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = plan })); err != nil {
			t.Fatalf("sync on plan %s: %v", plan, err)
		}
		if got := orgSourceOf(t, repo, "acme"); got.PriceBookID == nil || *got.PriceBookID != negotiated {
			t.Fatalf("plan %s overwrote the operator's book with %v", plan, got.PriceBookID)
		}
	}
	// Clearing it hands the source back to the sync, which assigns by plan.
	if err := repo.SetSourcePriceBook(ctx, src.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "flexi" })); err != nil {
		t.Fatal(err)
	}
	if got := bookNameOf(t, repo, "acme"); got != store.PlanBookName {
		t.Fatalf("a bookless flexi source went to the %q book, want the plans book (flexi is S in grow mode)", got)
	}
}

// TestSyncSkipsAPlatformBookOfTheWrongScope: the "Organization PAYG" book
// shipped at CLOUD scope. It is still ensured on every sync (Organizations
// rated before flexi was retired keep their history on it), and a bad book
// must never wedge the Organization sync: customer updates, cost sources and
// the resume stamp all go on, and the Organization lands on the plans book.
func TestSyncSkipsAPlatformBookOfTheWrongScope(t *testing.T) {
	repo := newFakeRepo()
	repo.paygBook = &store.PriceBook{ID: "book-cloud-scoped", Name: store.PAYGBookName, Scope: store.LayerCloud, Currency: "OMR", AnnualDivisor: store.PAYGBookDivisor}
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "flexi" })); err != nil {
		t.Fatalf("a wrong-scope book must not wedge the Organization sync: %v", err)
	}
	c, ok := repo.customerBySlug("acme")
	if !ok || c.Status != "active" || c.PlanSlug != "s" {
		t.Fatalf("customer = %+v, want an active Organization synced as S regardless", c)
	}
	if got := bookNameOf(t, repo, "acme"); got != store.PlanBookName {
		t.Fatalf("the Organization is on the %q book", got)
	}
}
