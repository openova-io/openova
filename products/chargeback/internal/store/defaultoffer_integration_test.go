package store_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/offer"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// offerSnapshot is everything the default offer writes, read back: the plans
// book's items, every feature, every cell and package setting of the book,
// the group icons — with their timestamps, so "nothing was written" is a
// byte comparison.
func offerSnapshot(t *testing.T, st *store.Store) string {
	t.Helper()
	ctx := context.Background()
	out := map[string]any{}
	if pb, err := st.GetPriceBookByName(ctx, store.PlanBookName); err == nil {
		items, err := st.ListPriceItems(ctx, pb.ID)
		if err != nil {
			t.Fatal(err)
		}
		cells, err := st.PackageCells(ctx, pb.ID)
		if err != nil {
			t.Fatal(err)
		}
		settings, err := st.PackageSettingsOf(ctx, pb.ID)
		if err != nil {
			t.Fatal(err)
		}
		out["book"], out["items"], out["cells"], out["settings"] = pb, items, cells, settings
	}
	features, err := st.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := st.FeatureGroupIcons(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out["features"], out["groups"] = features, groups
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ensureOffer(t *testing.T, st *store.Store, o offer.Offer) store.DefaultOfferResult {
	t.Helper()
	res, err := st.EnsureDefaultOffer(context.Background(), o, "test")
	if err != nil {
		t.Fatalf("EnsureDefaultOffer: %v", err)
	}
	return res
}

func countOffer(t *testing.T, st *store.Store) (items, features, cells, settings int) {
	t.Helper()
	ctx := context.Background()
	pb, err := st.GetPriceBookByName(ctx, store.PlanBookName)
	if err != nil {
		t.Fatal(err)
	}
	it, _ := st.ListPriceItems(ctx, pb.ID)
	fs, _ := st.ListFeatures(ctx)
	cs, _ := st.PackageCells(ctx, pb.ID)
	ss, _ := st.PackageSettingsOf(ctx, pb.ID)
	return len(it), len(fs), len(cs), len(ss)
}

// TestIntegrationDefaultOfferWritesOnceAndSurvivesRestartAndUpgrade: a fresh
// database gets the whole offer on the first start; a restart writes
// nothing; a sovereign-admin's edits survive a restart AND an upgrade whose
// file says something else, because the decision is recorded once.
func TestIntegrationDefaultOfferWritesOnceAndSurvivesRestartAndUpgrade(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	o := offer.MustDefault()

	res := ensureOffer(t, st, o)
	if res.State != store.DefaultOfferApplied || !res.Wrote {
		t.Fatalf("first start = %+v, want applied and written", res)
	}
	if items, features, cells, settings := countOffer(t, st); items != 12 || features != 27 || cells != 76 || settings != 4 {
		t.Fatalf("written: %d items, %d features, %d cells, %d settings; want 12, 27 (8 floor + 19), 76, 4", items, features, cells, settings)
	}
	first := offerSnapshot(t, st)

	// A restart: the same file, nothing written.
	if res := ensureOffer(t, st, o); res.State != store.DefaultOfferApplied || res.Wrote {
		t.Fatalf("restart = %+v, want applied and not written", res)
	}
	if got := offerSnapshot(t, st); got != first {
		t.Fatal("a restart changed the offer")
	}

	// A sovereign-admin edits the offer: a price, a cell, a feature's name,
	// a package's tagline, and removes a feature with its cells.
	pb, err := st.GetPriceBookByName(ctx, store.PlanBookName)
	if err != nil {
		t.Fatal(err)
	}
	annual, unit := store.Decimal("33"), store.Decimal("0.00376712")
	if _, err := st.UpdatePriceItem(ctx, pb.ID, "plan.s", store.PriceItemPatch{UnitPrice: &unit, AnnualPrice: &annual}); err != nil {
		t.Fatalf("edit the S price: %v", err)
	}
	if _, err := st.PutEntitlement(ctx, pb.ID, "plan.s", "backup", store.EntitlementInput{State: store.EntitlementNotOffered}); err != nil {
		t.Fatalf("edit a cell: %v", err)
	}
	name := "Backups, daily"
	if _, err := st.UpdateFeature(ctx, "backup", store.FeaturePatch{Name: &name}); err != nil {
		t.Fatalf("rename a feature: %v", err)
	}
	settings, err := st.PackageSettingsOf(ctx, pb.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := settings["plan.m"]
	if _, err := st.PutPackageSettings(ctx, pb.ID, "plan.m", store.PackageSettingsInput{Tagline: "For a growing shop", Recommended: m.Recommended, VCPU: m.VCPU, MemoryGB: m.MemoryGB, DiskGB: m.DiskGB, Accent: m.Accent, Badge: m.Badge}); err != nil {
		t.Fatalf("edit a package: %v", err)
	}
	for _, plan := range []string{"plan.s", "plan.m", "plan.l", "plan.xl"} {
		if err := st.DeleteEntitlement(ctx, pb.ID, plan, "maintenance"); err != nil {
			t.Fatalf("remove a cell: %v", err)
		}
	}
	if _, err := st.DeleteFeature(ctx, "maintenance"); err != nil {
		t.Fatalf("remove a feature: %v", err)
	}
	edited := offerSnapshot(t, st)
	if edited == first {
		t.Fatal("the edits changed nothing; the test proves nothing")
	}

	// A restart after the edits.
	if res := ensureOffer(t, st, o); res.Wrote {
		t.Fatalf("restart after an edit wrote the offer again: %+v", res)
	}
	if got := offerSnapshot(t, st); got != edited {
		t.Fatal("a restart undid a sovereign-admin's edit")
	}

	// An upgrade: a newer release ships a different offer.
	upgraded := o
	upgraded.PriceItems = append([]offer.PriceItem(nil), o.PriceItems...)
	upgraded.PriceItems[0].AnnualPrice = "99"
	upgraded.Document.Features = append([]offer.Feature{{Key: "new_feature", Name: "New", Group: "features", Kind: "access",
		Cells: map[string]offer.Cell{"plan.s": {State: "included"}}}}, o.Document.Features...)
	if res := ensureOffer(t, st, upgraded); res.Wrote || res.State != store.DefaultOfferApplied {
		t.Fatalf("upgrade = %+v, want the recorded decision and nothing written", res)
	}
	if got := offerSnapshot(t, st); got != edited {
		t.Fatal("an upgrade undid a sovereign-admin's edit")
	}
}

// TestIntegrationDefaultOfferLeavesAConfiguredBookAlone: a database that
// already carries an offer (a Sovereign upgraded to this release) is
// recorded as skipped and never written, on this start or any later one.
func TestIntegrationDefaultOfferLeavesAConfiguredBookAlone(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFeature(ctx, store.FeatureInput{Key: "console", Name: "Our console", Kind: store.FeatureKindAccess, Group: store.FeatureGroupAccess}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutEntitlement(ctx, pb.ID, "plan.s", "console", store.EntitlementInput{State: store.EntitlementIncluded}); err != nil {
		t.Fatal(err)
	}
	before := offerSnapshot(t, st)
	res := ensureOffer(t, st, offer.MustDefault())
	if res.State != store.DefaultOfferSkipped || res.Wrote || !strings.Contains(res.Detail, "feature") {
		t.Fatalf("configured book = %+v, want skipped naming the features", res)
	}
	if got := offerSnapshot(t, st); got != before {
		t.Fatal("the default offer wrote into a configured book")
	}
	// The decision is final: even with the offer removed by hand, a later
	// start writes nothing.
	if err := st.DeleteEntitlement(ctx, pb.ID, "plan.s", "console"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteFeature(ctx, "console"); err != nil {
		t.Fatal(err)
	}
	if res := ensureOffer(t, st, offer.MustDefault()); res.State != store.DefaultOfferSkipped || res.Wrote {
		t.Fatalf("a later start = %+v, want the recorded skip", res)
	}
	if _, features, _, _ := countOffer(t, st); features != 0 {
		t.Fatalf("%d features after a recorded skip, want 0", features)
	}
}

// TestIntegrationDefaultOfferFillsABookWithOnlyItsPlans: the plans book the
// Organization sync creates holds its four plan items and no offer; the
// default offer fills it and keeps its prices.
func TestIntegrationDefaultOfferFillsABookWithOnlyItsPlans(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	pb, _, err := st.EnsurePlanBook(ctx)
	if err != nil {
		t.Fatal(err)
	}
	annual, unit := store.Decimal("31.2"), store.Decimal("0.00356164")
	if _, err := st.UpdatePriceItem(ctx, pb.ID, "plan.s", store.PriceItemPatch{UnitPrice: &unit, AnnualPrice: &annual}); err != nil {
		t.Fatal(err)
	}
	if res := ensureOffer(t, st, offer.MustDefault()); res.State != store.DefaultOfferApplied || !res.Wrote {
		t.Fatalf("book with only its plans = %+v, want applied", res)
	}
	if items, features, cells, settings := countOffer(t, st); items != 12 || features != 27 || cells != 76 || settings != 4 {
		t.Fatalf("written: %d items, %d features, %d cells, %d settings", items, features, cells, settings)
	}
	it, err := st.GetPriceItem(ctx, pb.ID, "plan.s")
	if err != nil {
		t.Fatal(err)
	}
	if it.AnnualPrice == nil || !strings.HasPrefix(string(*it.AnnualPrice), "31.2") {
		t.Fatalf("plan.s annual = %v; the price already in the book must stay", it.AnnualPrice)
	}
}

// TestIntegrationDefaultOfferFinishesAStartThatDied: a start that recorded
// `started` and died mid-way is finished by the next one.
func TestIntegrationDefaultOfferFinishesAStartThatDied(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	o := offer.MustDefault()
	// What the dead start got to: the book, one floor item, the marker.
	if _, _, err := st.EnsurePlanBook(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateFeature(ctx, store.FeatureInput{Key: "applications", Name: "Applications", Kind: store.FeatureKindBoolean, Group: store.FeatureGroupFloor, SortOrder: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetDefaultOfferStateForTest(ctx, store.DefaultOfferStarted); err != nil {
		t.Fatal(err)
	}
	if res := ensureOffer(t, st, o); res.State != store.DefaultOfferApplied || !res.Wrote {
		t.Fatalf("after a dead start = %+v, want applied", res)
	}
	if items, features, cells, settings := countOffer(t, st); items != 12 || features != 27 || cells != 76 || settings != 4 {
		t.Fatalf("written: %d items, %d features, %d cells, %d settings", items, features, cells, settings)
	}
}

// TestIntegrationDefaultOfferTwoReplicasWriteItOnce: two replicas start at
// the same moment; one writes, the other reads its decision.
func TestIntegrationDefaultOfferTwoReplicasWriteItOnce(t *testing.T) {
	st := testdb.Open(t)
	o := offer.MustDefault()
	var wg sync.WaitGroup
	results := make([]store.DefaultOfferResult, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = st.EnsureDefaultOffer(context.Background(), o, "test")
		}(i)
	}
	wg.Wait()
	wrote := 0
	for i, r := range results {
		if errs[i] != nil {
			t.Fatalf("replica %d: %v", i, errs[i])
		}
		if r.State != store.DefaultOfferApplied {
			t.Fatalf("replica %d = %+v", i, r)
		}
		if r.Wrote {
			wrote++
		}
	}
	if wrote != 1 {
		t.Fatalf("%d replicas wrote the offer, want 1", wrote)
	}
	if _, features, cells, _ := countOffer(t, st); features != 27 || cells != 76 {
		t.Fatalf("%d features, %d cells", features, cells)
	}
}
