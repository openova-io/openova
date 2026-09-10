package openova

import (
	"context"
	"testing"

	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Which rate card an Organization's platform source is put on is decided by
// its PLAN (EPIC #6867, founder direction 2026-09-10): the plans book for a
// committed size, the pay-per-use book for flexi. Getting this wrong is what
// made a flexi Organization free — it sat on the plans book, which prices no
// k8s.* meter, while the collector emitted no plan line for it either.

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
		{"flexi", store.PAYGBookName},
		{"FLEXI", store.PAYGBookName},
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

// TestSyncRepointsTheSourceWhenThePlanChanges: switching between flexi and a
// sized plan moves the source between the two books on the next sync. Without
// that the bill would not follow the plan — an Organization that moved to
// flexi would keep paying a plan line it no longer has, and one that moved
// off flexi would keep being metered per vCPU on top of its new plan.
func TestSyncRepointsTheSourceWhenThePlanChanges(t *testing.T) {
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
	sync("flexi")
	if got := bookNameOf(t, repo, "acme"); got != store.PAYGBookName {
		t.Fatalf("after moving to flexi the source is still on the %q book — pay-per-use would never be charged", got)
	}
	c, _ := repo.customerBySlug("acme")
	if c.PlanSlug != store.PlanFlexi {
		t.Fatalf("customer plan = %q", c.PlanSlug)
	}
	sync("l")
	if got := bookNameOf(t, repo, "acme"); got != store.PlanBookName {
		t.Fatalf("after moving off flexi the source is still on the %q book — the Organization would pay its plan AND per vCPU", got)
	}
	// Books are ensured once per sync and created once each.
	if repo.planCalls != 3 || repo.paygCalls != 3 {
		t.Fatalf("ensure calls = plans %d payg %d, want one of each per sync (3)", repo.planCalls, repo.paygCalls)
	}
	if repo.planBook == nil || repo.paygBook == nil || repo.planBook.ID == repo.paygBook.ID {
		t.Fatalf("the two platform books were not created as two books: %v / %v", repo.planBook, repo.paygBook)
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
	if got := bookNameOf(t, repo, "acme"); got != store.PAYGBookName {
		t.Fatalf("a bookless flexi source went to the %q book", got)
	}
}

// TestSyncSkipsAPlatformBookOfTheWrongScope: the "Organization PAYG" book
// shipped at CLOUD scope, which SetSourcePriceBook refuses for a platform
// source. Returning that error from the sync would wedge the Organization on
// every event - customer updates, cost sources and the resume stamp all stop
// - so the book is skipped with a warning and everything else still syncs.
func TestSyncSkipsAPlatformBookOfTheWrongScope(t *testing.T) {
	repo := newFakeRepo()
	repo.paygBook = &store.PriceBook{ID: "book-cloud-scoped", Name: store.PAYGBookName, Scope: store.LayerCloud, Currency: "OMR", AnnualDivisor: store.PAYGBookDivisor}
	s := &OrgSync{Core: k8sfake.NewSimpleClientset(), Repo: repo, Keys: testKeys(t), Metrics: metrics.New()}
	ctx := context.Background()
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "flexi" })); err != nil {
		t.Fatalf("a wrong-scope book must not wedge the Organization sync: %v", err)
	}
	c, ok := repo.customerBySlug("acme")
	if !ok || c.Status != "active" || c.PlanSlug != store.PlanFlexi {
		t.Fatalf("customer = %+v, want an active flexi Organization synced regardless", c)
	}
	if src := orgSourceOf(t, repo, "acme"); src.PriceBookID != nil {
		t.Fatalf("the unassignable book was assigned anyway: %v", src.PriceBookID)
	}
	// Fixing the scope assigns it on the next sync, with no other change.
	repo.paygBook.Scope = store.LayerPlatform
	if err := s.SyncOrganization(ctx, orgUnstructured("acme", func(spec map[string]any) { spec["planSlug"] = "flexi" })); err != nil {
		t.Fatal(err)
	}
	if got := bookNameOf(t, repo, "acme"); got != store.PAYGBookName {
		t.Fatalf("after the scope was fixed the source is on the %q book", got)
	}
}
