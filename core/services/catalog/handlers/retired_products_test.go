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
