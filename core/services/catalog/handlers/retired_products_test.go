package handlers

import (
	"strings"
	"testing"
	"time"
)

// A retired product must not be purchasable on the marketplace storefront.
//
// WHY THESE ASSERT HERE AND NOT ON THE GENERATED CATALOG (#5920). There are two
// catalogs and only one of them is what a paying customer sees. The generated
// catalog (products/catalyst/bootstrap/api/internal/catalog/blueprints.json and
// ui/src/shared/constants/catalog.generated.ts) is the SOVEREIGN-ADMIN console's
// catalog, built from every blueprint.yaml. The customer storefront
// (core/marketplace) renders GET /catalog/apps?published=true, which this
// service answers from its own App rows — seeded by seedAppRows and gated by
// store.ListPublishedApps on `published && !system && deployable`.
//
// So flipping a Blueprint to `visibility: unlisted` delists it from the admin
// surface and changes nothing about what is on sale. Sandbox was retired on
// 2026-06-30 and was still a FREE card with a live "Add to stack" button on
// hw292 six weeks later. A `grep -rn sandbox core/marketplace/src/` returns two
// incidental comments, so the sweep that should have caught it came back clean.
// The producer is seedAppRows + DeployableAppSlugs + migrateAppPublished, all in
// this package, and that is where the assertion belongs.
//
// Each test states the exact storefront predicate it defends, so a future reader
// can tell what breaks if it is deleted.

// vacuity: every test below is meaningless if the retired set is empty, so this
// runs first and asserts the fixture has teeth.
func TestRetiredAppSlugsIsNotEmpty(t *testing.T) {
	retired := RetiredAppSlugs()
	if len(retired) == 0 {
		t.Fatal("RetiredAppSlugs() is empty — every other test in this file " +
			"would pass by examining nothing")
	}
	for slug, why := range retired {
		if strings.TrimSpace(slug) == "" {
			t.Error("retired set contains an empty slug")
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("retired slug %q carries no reason — the reason is what stops "+
				"a future reader re-listing it as an accident", slug)
		}
	}
	if _, ok := retired["sandbox"]; !ok {
		t.Error("sandbox is not in the retired set; it was retired 2026-06-30 " +
			"by founder decision and superseded by agenity + openova-mcp")
	}
}

// Defends: a fresh Sovereign must not be seeded with the row at all.
func TestRetiredProductIsNotSeededIntoTheCatalog(t *testing.T) {
	rows := seedAppRows(time.Now().UTC())
	if len(rows) == 0 {
		t.Fatal("seedAppRows returned no rows — nothing was checked")
	}
	retired := RetiredAppSlugs()
	for _, a := range rows {
		if why, bad := retired[a.Slug]; bad {
			t.Errorf("seedAppRows still seeds retired product %q (%s) — a fresh "+
				"Sovereign would create the row and the storefront would sell it",
				a.Slug, why)
		}
	}
}

// Defends: a fresh Sovereign must not be seeded with a PRICE LIST for a product
// that is not for sale (#5920).
//
// The App row and the Plan rows are two different producers, and only the App
// row was closed. seedMissingSandboxPlans — the path that re-creates missing
// tiers on an EXISTING Sovereign — already returns early on RetiredAppSlugs,
// and its comment states the rule outright: "Retiring the product retires its
// price list with it." The FRESH-seed path appended the same tiers
// unconditionally, so every newly provisioned Sovereign was still seeded with
// sandbox-free (0 OMR), sandbox-pro (9 OMR, Popular=true) and sandbox-ent
// (49 OMR), and served them on the PUBLIC GET /catalog/plans.
//
// That is the same product being off the shelf and still in the price list.
func TestRetiredProductHasNoPlanTiersOnAFreshSovereign(t *testing.T) {
	plans := seedPlanRows()
	if len(plans) == 0 {
		t.Fatal("seedPlanRows returned no rows — nothing was checked")
	}
	retired := RetiredAppSlugs()
	for _, p := range plans {
		if why, bad := retired[p.ProductSlug]; bad {
			t.Errorf("seedPlanRows still seeds plan %q at %d OMR for retired product %q (%s) — "+
				"a fresh Sovereign would create the tier and GET /catalog/plans would serve it",
				p.Slug, p.PriceOMR, p.ProductSlug, why)
		}
		// Belt and braces: catch a tier named for the retired product even if
		// its ProductSlug were cleared, which would make it look like a generic
		// compute tier and put it next to S/M/L/XL.
		for slug := range retired {
			if strings.HasPrefix(p.Slug, slug+"-") {
				t.Errorf("seedPlanRows seeds plan %q, named for retired product %q, "+
					"with ProductSlug=%q", p.Slug, slug, p.ProductSlug)
			}
		}
	}
}

// Vacuity for the test above. If expectedSandboxPlans() ever returned an empty
// slice, or stopped setting ProductSlug, then filtering it out of the fresh seed
// would assert nothing and the test above would pass by examining nothing.
//
// This proves the excluded set has teeth: it is non-empty, it is priced, and it
// carries the exact ProductSlug the filter keys on.
func TestRetiredPlanExclusionIsNotVacuous(t *testing.T) {
	excluded := expectedSandboxPlans()
	if len(excluded) == 0 {
		t.Fatal("expectedSandboxPlans() is empty — excluding it from the fresh seed " +
			"would be a no-op and TestRetiredProductHasNoPlanTiersOnAFreshSovereign " +
			"would pass against an unguarded seed")
	}
	if _, retired := RetiredAppSlugs()["sandbox"]; !retired {
		t.Fatal("sandbox is not retired, so the exclusion never fires")
	}
	var priced int
	for _, p := range excluded {
		if p.ProductSlug != "sandbox" {
			t.Errorf("excluded plan %q carries ProductSlug=%q, want %q — the fresh-seed "+
				"filter keys on ProductSlug and would not catch this row",
				p.Slug, p.ProductSlug, "sandbox")
		}
		if p.PriceOMR > 0 {
			priced++
		}
	}
	if priced == 0 {
		t.Error("no excluded tier carries a price — the 'still on sale' claim this " +
			"test defends would have no teeth")
	}

	// And the exclusion must actually remove them: every excluded slug must be
	// absent from the fresh seed.
	seeded := make(map[string]bool)
	for _, p := range seedPlanRows() {
		seeded[p.Slug] = true
	}
	for _, p := range excluded {
		if seeded[p.Slug] {
			t.Errorf("plan %q is in expectedSandboxPlans AND in the fresh seed — "+
				"the retirement guard did not fire", p.Slug)
		}
	}
}

// Control. Without this, `return nil` from seedPlanRows would satisfy both tests
// above and leave a Sovereign with no price list at all — checkout would fail
// for every product, not just the retired one.
func TestLiveComputeTiersAreStillSeeded(t *testing.T) {
	plans := seedPlanRows()
	seeded := make(map[string]int, len(plans))
	for _, p := range plans {
		seeded[p.Slug] = p.PriceOMR
	}
	// The generic compute ladder a customer actually buys.
	for _, slug := range []string{"s", "m", "l", "xl", "flexi"} {
		if _, ok := seeded[slug]; !ok {
			t.Errorf("live compute tier %q is no longer seeded — the retirement change "+
				"removed more than it should have", slug)
		}
	}
	// Assert on a VALUE, not just presence: an empty/zeroed ladder would still
	// have the keys. M is the Popular default at 9 OMR.
	if got := seeded["m"]; got != 9 {
		t.Errorf("tier m PriceOMR = %d, want 9 — the live price ladder was damaged", got)
	}
	var popular []string
	for _, p := range plans {
		if p.Popular {
			popular = append(popular, p.Slug)
		}
	}
	if len(popular) != 1 || popular[0] != "m" {
		t.Errorf("Popular tiers = %v, want exactly [m] — the plan picker's default "+
			"selection is broken (sandbox-pro also carried Popular=true)", popular)
	}
}

// Defends: store.ListPublishedApps requires deployable==true. While the slug is
// in DeployableAppSlugs, migrateAppDeployable keeps flipping existing rows back
// to deployable, which is half of what keeps the card on sale.
func TestRetiredProductIsNotDeployable(t *testing.T) {
	deployable := DeployableAppSlugs()
	if len(deployable) == 0 {
		t.Fatal("DeployableAppSlugs() is empty — nothing was checked")
	}
	for slug, why := range RetiredAppSlugs() {
		if deployable[slug] {
			t.Errorf("retired product %q is still in DeployableAppSlugs (%s). "+
				"migrateAppDeployable converges existing rows onto this map, so it "+
				"would re-mark the row deployable on every seed pass", slug, why)
		}
	}
}

// Defends: store.ListPublishedApps requires published==true, and
// migrateAppPublished converges every row onto PublishedForSlug. Before #5920
// that loop only flipped false->true, so a retired row already present on a
// provisioned Sovereign could never be taken off sale by a release.
func TestPublishMigrationUnpublishesRetiredProducts(t *testing.T) {
	for slug := range RetiredAppSlugs() {
		if PublishedForSlug(slug) {
			t.Errorf("PublishedForSlug(%q) = true; a retired product must converge "+
				"on published=false", slug)
		}
		// The case that actually matters: the row already exists AND is already
		// published, which is the state of every Sovereign provisioned before
		// the retirement. The migration must issue a write here. The pre-#5920
		// one-way loop returned change=false for exactly this input, which is
		// why hw292 kept selling Sandbox for six weeks.
		want, change := appPublishAction(slug, true)
		if want {
			t.Errorf("appPublishAction(%q, published=true) wants published=true", slug)
		}
		if !change {
			t.Errorf("appPublishAction(%q, published=true) reports no change needed — "+
				"an already-published retired row would never be taken off sale on a "+
				"Sovereign that already has it", slug)
		}
		// idempotent once healed
		if _, change := appPublishAction(slug, false); change {
			t.Errorf("appPublishAction(%q, published=false) still wants a write — "+
				"the migration would rewrite the row on every seed pass", slug)
		}
	}
}

// Control. Without this, "return false for everything" would satisfy the test
// above and silently empty the entire storefront. Live apps must stay published
// by default (#710: operators opt OUT per app, not IN).
func TestLiveProductsStayPublishedByDefault(t *testing.T) {
	live := []string{"wordpress", "gitea", "nextcloud", "openclaw", "stalwart-mail"}
	for _, slug := range live {
		if !PublishedForSlug(slug) {
			t.Errorf("PublishedForSlug(%q) = false — the opt-OUT default is broken "+
				"and the storefront would go empty", slug)
		}
	}
	// and they must still be seeded + deployable
	rows := seedAppRows(time.Now().UTC())
	seeded := make(map[string]bool, len(rows))
	for _, a := range rows {
		seeded[a.Slug] = true
	}
	deployable := DeployableAppSlugs()
	for _, slug := range []string{"wordpress", "gitea", "nextcloud"} {
		if !seeded[slug] {
			t.Errorf("live product %q is no longer seeded — the retirement change "+
				"removed more than it should have", slug)
		}
		if !deployable[slug] {
			t.Errorf("live product %q is no longer deployable", slug)
		}
	}
}
