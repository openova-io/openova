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
