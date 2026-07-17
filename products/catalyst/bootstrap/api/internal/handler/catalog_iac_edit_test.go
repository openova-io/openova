// catalog_iac_edit_test.go — #3668 §5D coverage for the full-CR catalog
// IaC editor endpoint (PUT /api/v1/catalog/{name}/iac).
//
// The DoD §9.7: an "Edit IaC" action commits the WHOLE blueprint.yaml — a
// field the 7-field card form could never touch (e.g. spec.source.version) —
// to the SAME catalog-sovereign Gitea file the card edit writes, under the
// dedicated git budget, gated by the tier-admin authority. These pin: the
// happy-path commit lands the full CR; a non-admin is 403'd; a malformed /
// retargeted CR is 400'd (never committed); an unwired Gitea is reported, not
// faked green.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	yamlv3 "gopkg.in/yaml.v3"
)

// callCatalogIaCEdit drives PUT /api/v1/catalog/{name}/iac through a chi
// router so the {name} URL param resolves, injecting the supplied claims.
func callCatalogIaCEdit(t *testing.T, h *Handler, name, body string, claims *auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Put("/api/v1/catalog/{name}/iac", h.HandleCatalogBlueprintIaCEdit)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/catalog/"+name+"/iac", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// fullAlloyBlueprintYAML is a complete Blueprint CR with spec.source +
// spec.manifests — the install fields the card form can't edit.
func fullAlloyBlueprintYAML(version string) string {
	return `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-alloy
spec:
  version: "` + version + `"
  visibility: listed
  card:
    title: Grafana Alloy
    summary: Telemetry collector
  source:
    kind: HelmRepository
    type: oci
    url: oci://ghcr.io/openova-io
    chart: bp-alloy
    version: "` + version + `"
  manifests:
    chart: bp-alloy
`
}

// TestCatalogIaCEdit_CommitsFullCR — the happy path: a tier-admin PUTs the
// full blueprint.yaml (editing spec.source.version, a non-card field) → it
// lands at catalog-sovereign/bp-alloy/blueprint.yaml verbatim.
func TestCatalogIaCEdit_CommitsFullCR(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	body := `{"blueprintYaml":` + jsonQuote(fullAlloyBlueprintYAML("1.0.2")) + `}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, adminClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	key := giteaKey(catalogSovereignOrg, "bp-alloy", catalogEditGitBranch, catalogEditBlueprintPath)
	raw, ok := fg.files[key]
	if !ok {
		t.Fatalf("expected a committed blueprint.yaml at %s; keys=%v", key, fileKeys(fg))
	}
	// The full CR landed — including spec.source (a non-card field).
	var doc map[string]interface{}
	if err := yamlv3.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("committed CR did not parse: %v", err)
	}
	spec := doc["spec"].(map[string]interface{})
	if _, ok := spec["source"].(map[string]interface{}); !ok {
		t.Errorf("spec.source (a non-card field) must survive the full-CR commit; spec=%v", spec)
	}
	src := spec["source"].(map[string]interface{})
	if src["version"] != "1.0.2" {
		t.Errorf("edited spec.source.version must land; got %v", src["version"])
	}
}

// TestCatalogIaCEdit_403ForNonAdmin — a viewer caller is rejected by the
// tier-admin gate (the same authority as /blueprints/edit-pr).
func TestCatalogIaCEdit_403ForNonAdmin(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	body := `{"blueprintYaml":` + jsonQuote(fullAlloyBlueprintYAML("1.0.2")) + `}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, &auth.Claims{Tier: "viewer"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if len(fg.files) != 0 {
		t.Errorf("a forbidden edit must not commit anything; files=%v", fileKeys(fg))
	}
}

// TestCatalogIaCEdit_400OnRetargetedName — a CR whose metadata.name addresses
// a DIFFERENT blueprint must be rejected (it cannot be allowed to clobber
// another bp's file).
func TestCatalogIaCEdit_400OnRetargetedName(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// PUT to /catalog/bp-alloy/iac but the CR names bp-grafana.
	wrong := strings.Replace(fullAlloyBlueprintYAML("1.0.2"), "name: bp-alloy", "name: bp-grafana", 1)
	body := `{"blueprintYaml":` + jsonQuote(wrong) + `}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, adminClaims())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(fg.files) != 0 {
		t.Errorf("a retargeted CR must not commit; files=%v", fileKeys(fg))
	}
}

// TestCatalogIaCEdit_400OnMalformed — non-Blueprint / unparseable YAML is
// rejected before any commit.
func TestCatalogIaCEdit_400OnMalformed(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	body := `{"blueprintYaml":"kind: ConfigMap\nmetadata:\n  name: bp-alloy\n"}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, adminClaims())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(fg.files) != 0 {
		t.Errorf("a malformed CR must not commit; files=%v", fileKeys(fg))
	}
}

// TestCatalogIaCEdit_503WhenGiteaUnwired — without a local catalog git the
// editor is reported unavailable (503), never a false success.
func TestCatalogIaCEdit_503WhenGiteaUnwired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// no SetGiteaClient → h.giteaClient == nil
	body := `{"blueprintYaml":` + jsonQuote(fullAlloyBlueprintYAML("1.0.2")) + `}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, adminClaims())
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// jsonQuote produces a JSON-quoted string literal (incl. the surrounding
// quotes) for embedding a multi-line YAML doc into a JSON request body.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// ── #4896 / #5018 — read → edit → commit round-trip regression guards ──────
//
// hw242 live walk (UAT rows 148/154, #3668): the full-CR Edit-IaC commit
// "succeeded" (200 committed:true) but was silently INERT — it wrote only the
// unwatched per-Blueprint repo, never the Flux-watched aggregator leg the
// card path dual-writes, and always minted the `bp-` repo even when the read
// path (#4990/#4997 bare↔bp- toggle) resolves the bare-named one. These pin
// the durable contract: whatever the read path loads, the commit lands back
// in the SAME per-Blueprint file AND reaches the Flux aggregator tree that
// reconciles it into the live Blueprint CR.

// fullWordpressBlueprintYAML is the UAT row-148 resource shape: a deployed,
// structurally-different blueprint whose spec.manifests the editor edits in
// place. metadata.name carries the canonical bp- form (the shape the live CR
// / dry-run path uses).
func fullWordpressBlueprintYAML(manifestsNS string) string {
	return `apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: bp-wordpress
spec:
  version: "6.5.0"
  visibility: listed
  card:
    title: WordPress
    summary: CMS
  source:
    kind: HelmRepository
    type: oci
    url: oci://ghcr.io/openova-io
    chart: bp-wordpress
    version: "6.5.0"
  manifests:
    chart: bp-wordpress
    namespace: ` + manifestsNS + `
`
}

// bareAlloyBlueprintYAML is the UAT row-154 resource shape: the AUTHORED
// seed blueprint.yaml whose metadata.name is the BARE slug (`alloy`) while
// the repo/URL/CR use `bp-alloy` — the exact #4896 name divergence. The
// editor loads this document verbatim, so the commit path must accept it
// (never a defensive 400 on a name the read path itself produced).
func bareAlloyBlueprintYAML(version string) string {
	return strings.Replace(fullAlloyBlueprintYAML(version), "name: bp-alloy", "name: alloy", 1)
}

// TestCatalogIaCEdit_RoundTrip_ManifestsEditReachesFluxLeg — row 148: the
// editor loads bp-wordpress (seed-only bp- repo), edits spec.manifests, and
// commits. The commit must (a) land back in the SAME repo the read resolved,
// and (b) mirror into the Flux aggregator tree with the manifests edit so the
// live Blueprint CR reconciles — the leg whose absence made hw242's commit
// inert.
func TestCatalogIaCEdit_RoundTrip_ManifestsEditReachesFluxLeg(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// READ SOURCE: the seed-only per-BP repo the resolver serves (#4997).
	perBP := giteaKey(catalogSovereignOrg, "bp-wordpress", catalogEditGitBranch, catalogEditBlueprintPath)
	fg.files[perBP] = []byte(fullWordpressBlueprintYAML("wordpress"))

	// EDIT + COMMIT: the loaded document with spec.manifests.namespace changed.
	edited := fullWordpressBlueprintYAML("wordpress-uatproof")
	rec := callCatalogIaCEdit(t, h, "bp-wordpress", `{"blueprintYaml":`+jsonQuote(edited)+`}`, adminClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	// (a) The per-BP read source carries the edit (read→edit→commit→read).
	if got := string(fg.files[perBP]); got != edited {
		t.Errorf("per-BP read source must round-trip the committed document;\ngot:\n%s\nwant:\n%s", got, edited)
	}

	// (b) The Flux aggregator leg landed with the manifests edit.
	aggKey := giteaKey(catalogSovereignAggOrg, catalogSovereignAggRepo, catalogSovereignAggBranch,
		"clusters/t99.omani.works/catalog-sovereign/bp-wordpress.yaml")
	aggRaw, ok := fg.files[aggKey]
	if !ok {
		t.Fatalf("Flux aggregator write missing at %s (the #5018 inert-commit regression); keys=%v", aggKey, fileKeys(fg))
	}
	var aggDoc map[string]interface{}
	if err := yamlv3.Unmarshal(aggRaw, &aggDoc); err != nil {
		t.Fatalf("unmarshal aggregator CR: %v", err)
	}
	aggSpec, _ := aggDoc["spec"].(map[string]interface{})
	man, _ := aggSpec["manifests"].(map[string]interface{})
	if man == nil || asString(man["namespace"]) != "wordpress-uatproof" {
		t.Errorf("the spec.manifests edit must reach the Flux leg; got manifests=%v", aggSpec["manifests"])
	}
	meta, _ := aggDoc["metadata"].(map[string]interface{})
	if meta == nil || asString(meta["name"]) != "bp-wordpress" {
		t.Errorf("aggregator CR must target the live CR identity bp-wordpress; metadata=%v", meta)
	}
}

// TestCatalogIaCEdit_RoundTrip_VersionPersistsInFluxLeg_BareName — row 154 +
// the #4896 headline in one: the editor loads the authored seed document
// (metadata.name = bare `alloy`), bumps spec.version 1.0.2→1.0.3, and
// commits against URL bp-alloy. The commit must be ACCEPTED (no 400 on the
// name shape the read path itself produced) and the version bump must reach
// the Flux leg — NOT #4415-stripped: an explicit whole-CR commit owns
// spec.version (DoD §9.7), which is exactly the edit hw242 proved inert.
func TestCatalogIaCEdit_RoundTrip_VersionPersistsInFluxLeg_BareName(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// READ SOURCE: seed-only bp-alloy repo carrying the bare-named document.
	perBP := giteaKey(catalogSovereignOrg, "bp-alloy", catalogEditGitBranch, catalogEditBlueprintPath)
	fg.files[perBP] = []byte(bareAlloyBlueprintYAML("1.0.2"))

	edited := bareAlloyBlueprintYAML("1.0.3")
	rec := callCatalogIaCEdit(t, h, "bp-alloy", `{"blueprintYaml":`+jsonQuote(edited)+`}`, adminClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("a bare metadata.name the read path produced must commit (got %d, the #4896 400-shape); body=%s",
			rec.Code, rec.Body.String())
	}

	// The per-BP read source round-trips verbatim (bare name preserved —
	// the admin owns the document).
	if got := string(fg.files[perBP]); got != edited {
		t.Errorf("per-BP read source must round-trip verbatim;\ngot:\n%s\nwant:\n%s", got, edited)
	}

	aggKey := giteaKey(catalogSovereignAggOrg, catalogSovereignAggRepo, catalogSovereignAggBranch,
		"clusters/t99.omani.works/catalog-sovereign/bp-alloy.yaml")
	aggRaw, ok := fg.files[aggKey]
	if !ok {
		t.Fatalf("Flux aggregator write missing at %s; keys=%v", aggKey, fileKeys(fg))
	}
	var aggDoc map[string]interface{}
	if err := yamlv3.Unmarshal(aggRaw, &aggDoc); err != nil {
		t.Fatalf("unmarshal aggregator CR: %v", err)
	}
	meta, _ := aggDoc["metadata"].(map[string]interface{})
	if meta == nil || asString(meta["name"]) != "bp-alloy" {
		t.Errorf("aggregator CR name must be stamped to the live CR identity bp-alloy (was bare `alloy`); metadata=%v", meta)
	}
	aggSpec, _ := aggDoc["spec"].(map[string]interface{})
	if aggSpec == nil || asString(aggSpec["version"]) != "1.0.3" {
		t.Errorf("row 154: the explicit spec.version edit must reach the Flux leg un-stripped; got version=%v",
			aggSpec["version"])
	}
	if _, ok := aggSpec["source"].(map[string]interface{}); !ok {
		t.Errorf("an explicit full-CR commit keeps spec.source in the Flux leg (no #4415 card-strip); spec=%v", aggSpec)
	}
}

// TestCatalogIaCEdit_WritesRepoTheReadPathResolves — the split-brain half of
// #5018: a DEPLOYED blueprint lives in a bare-named repo (`wordpress`), which
// is what the read path resolves (#4997 tries the bare form first). The
// commit must land THERE — not mint a divergent `bp-wordpress` twin the read
// never consults.
func TestCatalogIaCEdit_WritesRepoTheReadPathResolves(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "") // mothership shape — isolates the repo-resolution assert
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	// READ SOURCE: deployed bare-named repo only.
	bareKey := giteaKey(catalogSovereignOrg, "wordpress", catalogEditGitBranch, catalogEditBlueprintPath)
	fg.files[bareKey] = []byte(fullWordpressBlueprintYAML("wordpress"))

	edited := fullWordpressBlueprintYAML("wordpress-moved")
	rec := callCatalogIaCEdit(t, h, "bp-wordpress", `{"blueprintYaml":`+jsonQuote(edited)+`}`, adminClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	if got := string(fg.files[bareKey]); got != edited {
		t.Errorf("commit must land in the bare repo the read path resolves;\ngot:\n%s\nwant:\n%s", got, edited)
	}
	twinKey := giteaKey(catalogSovereignOrg, "bp-wordpress", catalogEditGitBranch, catalogEditBlueprintPath)
	if _, ok := fg.files[twinKey]; ok {
		t.Errorf("commit must NOT mint a divergent bp- twin repo the read never consults; keys=%v", fileKeys(fg))
	}

	// The response path names the repo actually written.
	var resp catalogIaCEditResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Path != catalogSovereignOrg+"/wordpress/"+catalogEditBlueprintPath {
		t.Errorf("response path must name the repo written; got %q", resp.Path)
	}
}

// TestCatalogIaCEdit_AggregatorFailureSurfaced — a commit whose Flux
// reconcile leg fails must NOT report success: "Committed ✓" on an edit that
// never reaches the live Blueprint CR is the exact hw242 false-green (#5018).
func TestCatalogIaCEdit_AggregatorFailureSurfaced(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "t99.omani.works")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	// Fail ONLY the aggregator-repo write; the per-BP source write succeeds.
	fg.putFileErrFor = func(org, repo, _, _ string) error {
		if org == catalogSovereignAggOrg && repo == catalogSovereignAggRepo {
			return fmt.Errorf("aggregator repo unreachable (injected)")
		}
		return nil
	}
	h.SetGiteaClient(fg)

	body := `{"blueprintYaml":` + jsonQuote(fullAlloyBlueprintYAML("1.0.3")) + `}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, adminClaims())
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("a failed reconcile leg must surface 502, never a false success; got %d body=%s",
			rec.Code, rec.Body.String())
	}
	var resp catalogIaCEditResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Committed {
		t.Errorf("committed must be false when the edit cannot reach the live Blueprint CR")
	}
	if !strings.Contains(resp.Reason, "Flux reconcile leg failed") {
		t.Errorf("reason must name the failed reconcile leg; got %q", resp.Reason)
	}
}

// TestCatalogIaCEdit_MothershipSkipsAggregatorLeg — on the Catalyst-Zero
// mothership (SOVEREIGN_FQDN empty) there is no local catalog Flux reconcile
// by design: the commit succeeds on the per-BP source alone and no aggregator
// key is written.
func TestCatalogIaCEdit_MothershipSkipsAggregatorLeg(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	body := `{"blueprintYaml":` + jsonQuote(fullAlloyBlueprintYAML("1.0.2")) + `}`
	rec := callCatalogIaCEdit(t, h, "bp-alloy", body, adminClaims())
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	for k := range fg.files {
		if strings.HasPrefix(k, catalogSovereignAggOrg+"/"+catalogSovereignAggRepo+"/") {
			t.Errorf("mothership must not write an aggregator leg; found %s", k)
		}
	}
}

// ── #5124 — GET /api/v1/catalog/{name}/iac read-back seam ───────────────────
//
// The Edit-IaC editor previously seeded its YAML buffer from the store's stale
// CatalogItem.raw, so committing from that seed silently reverted card-form
// edits (icon/title/summary) already present in the committed blueprint.yaml.
// This GET returns the CURRENTLY-COMMITTED file — the same one the PUT writes,
// resolved via the SAME resolveCatalogEditRepo seam — so the FE seeds the
// editor from the source of truth (falling back to store raw only on 404).

// callGetCatalogIaC drives GET /api/v1/catalog/{name}/iac through a chi router
// so the {name} URL param resolves.
func callGetCatalogIaC(t *testing.T, h *Handler, name string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/v1/catalog/{name}/iac", h.HandleGetCatalogBlueprintIaC)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/"+name+"/iac", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestGetCatalogIaC_ReturnsCommittedFile — the committed blueprint.yaml is
// returned verbatim (the seed source of truth), NOT the stale store raw.
func TestGetCatalogIaC_ReturnsCommittedFile(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	committed := fullAlloyBlueprintYAML("9.9.9")
	fg.files[giteaKey(catalogSovereignOrg, "bp-alloy", catalogEditGitBranch, catalogEditBlueprintPath)] = []byte(committed)

	rec := callGetCatalogIaC(t, h, "bp-alloy")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response did not parse: %v; body=%s", err, rec.Body.String())
	}
	if resp["blueprintYaml"] != committed {
		t.Errorf("blueprintYaml must be the committed file verbatim;\n got=%q\nwant=%q", resp["blueprintYaml"], committed)
	}
	if resp["path"] != "catalog-sovereign/bp-alloy/blueprint.yaml" {
		t.Errorf("path: got %q", resp["path"])
	}
}

// TestGetCatalogIaC_ResolvesBareRepo — when the committed file lives in the
// BARE-named repo (the #4896/#4997 bare↔bp- divergence), the resolver finds it
// and the GET returns it, exactly as the PUT read path does.
func TestGetCatalogIaC_ResolvesBareRepo(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	committed := bareAlloyBlueprintYAML("3.2.1")
	fg.files[giteaKey(catalogSovereignOrg, "alloy", catalogEditGitBranch, catalogEditBlueprintPath)] = []byte(committed)

	rec := callGetCatalogIaC(t, h, "bp-alloy")
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["blueprintYaml"] != committed {
		t.Errorf("bare-repo committed file must be returned; got=%q", resp["blueprintYaml"])
	}
	if resp["path"] != "catalog-sovereign/alloy/blueprint.yaml" {
		t.Errorf("path must reflect the resolved bare repo; got %q", resp["path"])
	}
}

// TestGetCatalogIaC_404WhenNotCommitted — nothing committed yet ⇒ 404 so the
// FE knows to fall back to its store raw / live CR (never a fabricated blank).
func TestGetCatalogIaC_404WhenNotCommitted(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	rec := callGetCatalogIaC(t, h, "bp-neverseen")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestGetCatalogIaC_503WhenGiteaUnwired — no local catalog git ⇒ 503, never a
// false empty-file success.
func TestGetCatalogIaC_503WhenGiteaUnwired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// no SetGiteaClient → h.giteaClient == nil
	rec := callGetCatalogIaC(t, h, "bp-alloy")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}
