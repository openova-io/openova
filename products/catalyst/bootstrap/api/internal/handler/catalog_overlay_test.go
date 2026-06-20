// catalog_overlay_test.go — #3602 (EPIC #3597) coverage for the
// editable-catalog overlay merge.
//
// The DoD walk: "an edit persists + overlays the seed; an un-edited entry
// returns the seed." These tests pin exactly that on the pure merge
// (overlayCatalogEdits), on the store-row → edit decode (fetchCatalogEdits
// against an httptest stand-in for GET /catalog/apps), and end-to-end
// through HandleCatalogList with a fake Organization catalog upstream.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// seedItem builds a minimal seed CatalogBlueprint as the in-cluster CR
// fallback / catalyst-catalog would return it (the values the admin can
// later override).
func seedItem(name, title, summary, icon string) CatalogBlueprint {
	return CatalogBlueprint{
		Name:    name,
		Version: "1.0.0",
		Origin:  3,
		Source:  "in-cluster",
		Card: CatalogBlueprintCard{
			Title:   title,
			Summary: summary,
			Icon:    icon,
		},
	}
}

func TestOverlayCatalogEdits_EditWinsUneditedKeepsSeed(t *testing.T) {
	items := []CatalogBlueprint{
		seedItem("bp-grafana", "Grafana", "Seed summary", "grafana.svg"),
		seedItem("bp-keycloak", "Keycloak", "Identity", "keycloak.svg"),
	}
	// Only grafana is edited (store keys by bare slug).
	edits := map[string]catalogEdit{
		"grafana": {
			Slug:                "grafana",
			Name:                "Grafana (Edited)",
			Tagline:             "Admin-curated summary",
			Icon:                "custom-grafana.svg",
			IconLight:           "https://cdn/light.svg",
			IconDark:            "https://cdn/dark.svg",
			SupportedTopologies: []string{"single-region", "active-active"},
		},
	}

	out := overlayCatalogEdits(items, edits)

	// grafana reflects the edit.
	var g *CatalogBlueprint
	var k *CatalogBlueprint
	for i := range out {
		switch out[i].Name {
		case "bp-grafana":
			g = &out[i]
		case "bp-keycloak":
			k = &out[i]
		}
	}
	if g == nil || k == nil {
		t.Fatalf("expected both entries in output, got %+v", out)
	}
	if g.Card.Title != "Grafana (Edited)" {
		t.Errorf("edited title: got %q want %q", g.Card.Title, "Grafana (Edited)")
	}
	if g.Card.Summary != "Admin-curated summary" {
		t.Errorf("edited summary: got %q", g.Card.Summary)
	}
	if g.Card.Icon != "custom-grafana.svg" {
		t.Errorf("edited icon: got %q", g.Card.Icon)
	}
	if g.Card.IconLight != "https://cdn/light.svg" || g.Card.IconDark != "https://cdn/dark.svg" {
		t.Errorf("edited theme icons: light=%q dark=%q", g.Card.IconLight, g.Card.IconDark)
	}
	if len(g.SupportedTopologies) != 2 || g.SupportedTopologies[0] != "single-region" {
		t.Errorf("edited supportedTopologies: got %v", g.SupportedTopologies)
	}

	// keycloak is UNedited → seed unchanged.
	if k.Card.Title != "Keycloak" || k.Card.Summary != "Identity" || k.Card.Icon != "keycloak.svg" {
		t.Errorf("un-edited entry must keep the seed; got title=%q summary=%q icon=%q",
			k.Card.Title, k.Card.Summary, k.Card.Icon)
	}
	if k.Card.IconLight != "" || k.Card.IconDark != "" || len(k.SupportedTopologies) != 0 {
		t.Errorf("un-edited entry must NOT gain overlay fields; got light=%q dark=%q topo=%v",
			k.Card.IconLight, k.Card.IconDark, k.SupportedTopologies)
	}
}

func TestOverlayCatalogEdits_EmptyEditFieldsNeverClobberSeed(t *testing.T) {
	items := []CatalogBlueprint{seedItem("bp-grafana", "Grafana", "Seed summary", "grafana.svg")}
	// A store row that shares the slug but carries NO overlay content
	// (the common pre-seeded commerce row). It must be skipped entirely so
	// the seed survives.
	edits := map[string]catalogEdit{
		"grafana": {Slug: "grafana"}, // all edit fields empty
	}
	out := overlayCatalogEdits(items, edits)
	if out[0].Card.Title != "Grafana" || out[0].Card.Summary != "Seed summary" || out[0].Card.Icon != "grafana.svg" {
		t.Fatalf("empty store row clobbered the seed: %+v", out[0].Card)
	}
}

func TestOverlayCatalogEdits_PartialEditOnlyTouchesProvidedFields(t *testing.T) {
	items := []CatalogBlueprint{seedItem("bp-grafana", "Grafana", "Seed summary", "grafana.svg")}
	// Admin renamed the entry but left summary + icon as-is.
	edits := map[string]catalogEdit{
		"grafana": {Slug: "grafana", Name: "Observability"},
	}
	out := overlayCatalogEdits(items, edits)
	if out[0].Card.Title != "Observability" {
		t.Errorf("name should be edited: got %q", out[0].Card.Title)
	}
	if out[0].Card.Summary != "Seed summary" {
		t.Errorf("untouched summary must keep the seed: got %q", out[0].Card.Summary)
	}
	if out[0].Card.Icon != "grafana.svg" {
		t.Errorf("untouched icon must keep the seed: got %q", out[0].Card.Icon)
	}
}

func TestOverlayCatalogEdits_NoEditsIsNoop(t *testing.T) {
	items := []CatalogBlueprint{seedItem("bp-grafana", "Grafana", "Seed summary", "grafana.svg")}
	out := overlayCatalogEdits(items, map[string]catalogEdit{})
	if len(out) != 1 || out[0].Card.Title != "Grafana" {
		t.Fatalf("empty edits map must be a no-op; got %+v", out)
	}
}

// TestFetchCatalogEdits_DecodesBareArrayFromStore points orgCatalog() at an
// httptest server returning the BARE JSON array shape that
// core/services/catalog GET /catalog/apps emits (respond.OK on a slice),
// and checks the overlay map indexes only the EDITED rows by bare slug.
func TestFetchCatalogEdits_DecodesBareArrayFromStore(t *testing.T) {
	// GET /catalog/apps body: one edited row + one un-edited row.
	body := `[
		{"slug":"grafana","name":"Grafana (Edited)","icon_light":"l.svg","icon_dark":"d.svg","supported_topologies":["single-region"]},
		{"slug":"keycloak","name":"","tagline":"","icon":""}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/catalog/apps" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	restore := overrideOrgCatalog(srv.URL)
	defer restore()

	edits := fetchCatalogEdits(context.Background())
	if _, ok := edits["grafana"]; !ok {
		t.Fatalf("edited row 'grafana' must be in the overlay map; got %v", editKeys(edits))
	}
	if _, ok := edits["keycloak"]; ok {
		t.Errorf("un-edited row 'keycloak' (all-empty) must be skipped; got %v", editKeys(edits))
	}
	g := edits["grafana"]
	if g.Name != "Grafana (Edited)" || g.IconLight != "l.svg" || g.IconDark != "d.svg" {
		t.Errorf("decoded edit fields wrong: %+v", g)
	}
}

// TestFetchCatalogEdits_GracefulWhenCatalogAbsent — when the Organization catalog is
// unreachable (the marketplace.enabled=false Sovereigns), fetchCatalogEdits
// returns an empty map, NOT an error, so the catalog read serves the seed.
func TestFetchCatalogEdits_GracefulWhenCatalogAbsent(t *testing.T) {
	// Point at a closed port so the GET fails immediately.
	restore := overrideOrgCatalog("http://127.0.0.1:0")
	defer restore()
	edits := fetchCatalogEdits(context.Background())
	if edits == nil {
		t.Fatal("fetchCatalogEdits must return a non-nil empty map on failure")
	}
	if len(edits) != 0 {
		t.Fatalf("expected empty map on unreachable catalog; got %v", editKeys(edits))
	}
}

// TestHandleCatalogList_OverlaysStoreEdits is the end-to-end DoD walk: the
// seed (fake catalyst-catalog) carries bp-wordpress; the Organization store carries
// an edit for it; GET /api/v1/catalog returns the edited name.
func TestHandleCatalogList_OverlaysStoreEdits(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))

	// Organization store returns an edited wordpress row.
	storeBody := `[{"slug":"wordpress","name":"WordPress (Renamed)","tagline":"My summary","icon_dark":"dark.svg","supported_topologies":["single-region","active-active"]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(storeBody))
	}))
	defer srv.Close()
	restore := overrideOrgCatalog(srv.URL)
	defer restore()

	r := chi.NewRouter()
	r.Get("/api/v1/catalog", h.HandleCatalogList)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var env CatalogListResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if len(env.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(env.Items))
	}
	got := env.Items[0]
	// sampleWordpressBlueprint is named bp-wordpress with whatever seed
	// title; the overlay must have replaced the title + summary + dark
	// icon + supportedTopologies.
	if got.Card.Title != "WordPress (Renamed)" {
		t.Errorf("overlay title: got %q want %q", got.Card.Title, "WordPress (Renamed)")
	}
	if got.Card.Summary != "My summary" {
		t.Errorf("overlay summary: got %q", got.Card.Summary)
	}
	if got.Card.IconDark != "dark.svg" {
		t.Errorf("overlay dark icon: got %q", got.Card.IconDark)
	}
	if len(got.SupportedTopologies) != 2 {
		t.Errorf("overlay supportedTopologies: got %v", got.SupportedTopologies)
	}
}

// TestHandleCatalogList_SeedUnchangedWhenNoStoreEdit — when the Organization store
// has no matching edit, the seed entry passes through verbatim (the
// "un-edited entry returns the seed" half of the DoD, at the handler).
func TestHandleCatalogList_SeedUnchangedWhenNoStoreEdit(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))

	// Store returns an edit for a DIFFERENT app, so wordpress is un-edited.
	storeBody := `[{"slug":"some-other-app","name":"Other"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(storeBody))
	}))
	defer srv.Close()
	restore := overrideOrgCatalog(srv.URL)
	defer restore()

	r := chi.NewRouter()
	r.Get("/api/v1/catalog", h.HandleCatalogList)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var env CatalogListResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Items) != 1 || env.Items[0].Name != "bp-wordpress" {
		t.Fatalf("expected seed bp-wordpress, got %+v", env.Items)
	}
	// Seed title must be whatever the fake blueprint declared — NOT
	// overwritten. We assert it's non-empty and carries no overlay-only
	// fields.
	if strings.TrimSpace(env.Items[0].Card.Title) == "" {
		t.Errorf("seed title should be intact, got empty")
	}
	if env.Items[0].Card.IconDark != "" || len(env.Items[0].SupportedTopologies) != 0 {
		t.Errorf("un-edited seed must not gain overlay fields: dark=%q topo=%v",
			env.Items[0].Card.IconDark, env.Items[0].SupportedTopologies)
	}
}

// overrideOrgCatalog points orgCatalog() at the given baseURL for the
// duration of a test and returns a restore func that clears the override.
func overrideOrgCatalog(baseURL string) func() {
	prev := orgCatalogTestOverride
	orgCatalogTestOverride = &orgCatalogClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: orgCatalogProbeBudget},
	}
	return func() { orgCatalogTestOverride = prev }
}

// keysOf is a tiny test helper for clearer failure messages.
func editKeys(m map[string]catalogEdit) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
