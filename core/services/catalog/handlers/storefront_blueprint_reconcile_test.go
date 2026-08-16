package handlers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// The storefront and the Blueprint catalog are two independent lists and
// NOTHING reconciles them. That single fact produced two live failures from
// opposite directions:
//
//   - UAT rows 90/95 (#6360): a customer bought uptime-kuma, checkout
//     succeeded, the Organization was created, the GitOps overlay applied
//     cleanly — and the pod sat in ImagePullBackOff forever. harbor-prewarm's
//     Phase A3 enumerates images FROM PINNED CATALOG CHARTS, so an app with no
//     Blueprint has no chart to walk, its images are never mirrored, and after
//     the cutover severs upstream the pull cannot succeed. A paid dead end.
//
//   - UAT rows 219/G9: bp-agenity had a Blueprint, was `visibility: listed`,
//     and was absent from the storefront — so no customer could ever select it.
//
// This test pins the FIRST direction: nothing may be sold that the platform has
// no Blueprint for. The 24 apps that are already in that state are listed
// explicitly rather than waived silently, so the debt is visible and the list
// can only shrink — adding a NEW unbacked app fails immediately.
//
// Why an allowlist instead of a hard failure: making all 24 pass today would
// mean either deleting the storefront's catalogue or inventing 24 Blueprints,
// neither of which belongs in this change. What must not happen is a 25th
// slipping in unnoticed, which is exactly how the first 24 accumulated.
var storefrontAppsWithoutBlueprint = map[string]string{
	"bookstack":     "#6360 — no Blueprint; never prewarmed",
	"cal-com":       "#6360 — no Blueprint; never prewarmed",
	"chatwoot":      "#6360 — no Blueprint; never prewarmed",
	"dify":          "#6360 — no Blueprint; never prewarmed",
	"documenso":     "#6360 — no Blueprint; never prewarmed",
	"erpnext":       "#6360 — no Blueprint; never prewarmed",
	"formbricks":    "#6360 — no Blueprint; never prewarmed",
	"ghost":         "#6360 — no Blueprint; never prewarmed",
	"immich":        "#6360 — no Blueprint; never prewarmed",
	"invoiceshelf":  "#6360 — no Blueprint; never prewarmed",
	"jitsi-meet":    "#6360 — no Blueprint; never prewarmed",
	"listmonk":      "#6360 — no Blueprint; never prewarmed",
	"medusa":        "#6360 — no Blueprint; never prewarmed",
	"nextcloud":     "#6360 — no Blueprint; never prewarmed",
	"nocodb":        "#6360 — no Blueprint; never prewarmed",
	"plane":         "#6360 — no Blueprint; never prewarmed",
	"postiz":        "#6360 — no Blueprint; never prewarmed",
	"rocket-chat":   "#6360 — no Blueprint; never prewarmed",
	"stalwart-mail": "#6360 — no Blueprint; never prewarmed",
	"twenty":        "#6360 — no Blueprint; never prewarmed",
	"umami":         "#6360 — no Blueprint; never prewarmed",
	"uptime-kuma":   "#6360 — the app a customer actually paid for on hw298",
	"vaultwarden":   "#6360 — no Blueprint; never prewarmed",
	"wordpress":     "#6360 — no Blueprint; never prewarmed",
}

// blueprintIDs reads the generated Blueprint catalog. It is the same artifact
// the console renders from, so this asserts against what the platform really
// ships rather than against a second hand-maintained list.
func blueprintSlugs(t *testing.T) map[string]bool {
	t.Helper()
	// walk up to the repo root — the test's own package path is not stable
	// enough to hardcode a relative depth.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var path string
	for i := 0; i < 8; i++ {
		cand := filepath.Join(dir, "products", "catalyst", "bootstrap", "api", "internal", "catalog", "blueprints.json")
		if _, err := os.Stat(cand); err == nil {
			path = cand
			break
		}
		dir = filepath.Dir(dir)
	}
	if path == "" {
		t.Skip("blueprints.json not found from this working directory — NOTHING WAS ASSERTED")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read blueprints.json: %v", err)
	}
	// The file is an OBJECT wrapping the list ({generatedAt, blueprints[],
	// bootstrapKit[]}), not a bare array. Asserting the wrapper shape here
	// rather than guessing is deliberate: my first draft unmarshalled into a
	// slice, which fails loudly — but a shape guess that happened to yield an
	// EMPTY slice would have made every assertion below pass vacuously.
	var doc struct {
		Blueprints []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"blueprints"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse blueprints.json: %v", err)
	}
	entries := doc.Blueprints
	if len(entries) == 0 {
		t.Fatal("blueprints.json parsed to ZERO entries — the assertion below would pass vacuously")
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.Slug != "" {
			out[e.Slug] = true
		}
		if len(e.ID) > 3 && e.ID[:3] == "bp-" {
			out[e.ID[3:]] = true
		}
	}
	return out
}

// TestStorefrontAppsHaveBlueprints — rows 90/95 (#6360).
func TestStorefrontAppsHaveBlueprints(t *testing.T) {
	bp := blueprintSlugs(t)
	rows := seedAppRows(time.Now().UTC())
	if len(rows) == 0 {
		t.Fatal("seedAppRows returned nothing — vacuous")
	}

	var unexpected []string
	for _, a := range rows {
		if bp[a.Slug] {
			continue
		}
		if _, known := storefrontAppsWithoutBlueprint[a.Slug]; known {
			continue
		}
		unexpected = append(unexpected, a.Slug)
	}
	sort.Strings(unexpected)
	if len(unexpected) > 0 {
		t.Errorf("storefront sells %d app(s) with NO Blueprint and no recorded exception: %v\n"+
			"An app with no Blueprint has no pinned chart, so harbor-prewarm Phase A3 never mirrors its\n"+
			"images and it will ImagePullBackOff on a post-cutover Sovereign — the customer pays and the\n"+
			"app never starts (#6360). Either give it a Blueprint, or add it to\n"+
			"storefrontAppsWithoutBlueprint with the reason.", len(unexpected), unexpected)
	}

	// The allowlist may only SHRINK. An entry that no longer applies is debt
	// that was paid — remove it, so the list keeps meaning something.
	for slug := range storefrontAppsWithoutBlueprint {
		if bp[slug] {
			t.Errorf("%q now HAS a Blueprint — remove it from storefrontAppsWithoutBlueprint so the list stays honest", slug)
		}
	}
}
