// applications_update_test.go — coverage for the EPIC-2 Slice T+O+P
// (#1097) update / delete / topology-preview / upgrade-preview endpoints.
//
// Test strategy mirrors applications_test.go: a fake dynamic client
// seeded with an existing Application CR, the same fakeCatalogClient
// stub from applications_test.go, and a per-test chi router that
// registers only the endpoint under test.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// helpers shared with applications_test.go:
//   - fakeApplicationDynamicFactory(seed ...) → factory + client
//   - newFakeCatalog / sampleWordpressBlueprint
//   - installUserAccessDeployment / callUserAccess

func registerApplicationUpdateRoutes(r chi.Router, h *Handler) {
	r.Put("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationUpdate)
	r.Delete("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationDelete)
	r.Post("/api/v1/sovereigns/{id}/applications/{name}/topology/preview", h.HandleApplicationTopologyPreview)
	r.Post("/api/v1/sovereigns/{id}/applications/{name}/upgrade/preview", h.HandleApplicationUpgradePreview)
}

func registerBlueprintRoutes(r chi.Router, h *Handler) {
	r.Post("/api/v1/sovereigns/{id}/blueprints/publish", h.HandleBlueprintPublish)
	r.Post("/api/v1/sovereigns/{id}/blueprints/curate", h.HandleBlueprintCurate)
	r.Get("/api/v1/sovereigns/{id}/blueprints/curatable", h.HandleBlueprintListCuratable)
	r.Post("/api/v1/sovereigns/{id}/blueprints/edit-pr", h.HandleBlueprintEditPR)
}

// seedAppFromObject creates the seed CR in a fake dynamic client by
// constructing an Unstructured directly (the dynamic-fake client takes
// runtime.Object seeds).
func makeAppCR(ns, name, version, mode string, regions []string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("apps.openova.io/v1")
	obj.SetKind("Application")
	obj.SetName(name)
	obj.SetNamespace(ns)
	regionsAny := make([]interface{}, len(regions))
	for i, r := range regions {
		regionsAny[i] = r
	}
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"environmentRef": "acme-prod",
		"blueprintRef": map[string]any{
			"name":    "bp-wordpress",
			"version": version,
		},
		"placement": mode,
		"regions":   regionsAny,
		"parameters": map[string]any{
			"domain":   "shop.acme.com",
			"replicas": float64(2),
		},
	}, "spec")
	return obj
}

// ── PUT happy path: parameter edit ───────────────────────────────────

func TestHandleApplicationUpdate_PatchesParameters(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"hz-fsn-rtz-prod"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-update-params")

	body := applicationUpdateRequest{
		Parameters: map[string]interface{}{
			"domain":   "newdomain.acme.com",
			"replicas": float64(3),
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme", body, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	dom, _, _ := unstructured.NestedString(got.Object, "spec", "parameters", "domain")
	if dom != "newdomain.acme.com" {
		t.Fatalf("domain not patched: got %q", dom)
	}
}

// ── PUT topology change: scale-up allowed, scale-down blocked ────────

func TestHandleApplicationUpdate_TopologyScaleUp_Succeeds(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"hz-fsn-rtz-prod"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-scale-up")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:    "active-hotstandby",
			Regions: []string{"hz-fsn-rtz-prod", "hz-hel-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme", body, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, _ := client.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-prod", metav1.GetOptions{})
	mode, _, _ := unstructured.NestedString(got.Object, "spec", "placement")
	// One vocabulary (#3375 DoD-1): the PUT posted the legacy spelling
	// "active-hotstandby"; the CR STORES the canonical "active-hot-standby".
	if mode != "active-hot-standby" {
		t.Fatalf("placement not patched to canonical: got %q, want active-hot-standby", mode)
	}
}

func TestHandleApplicationUpdate_TopologyScaleDown_BlockedWithoutForce(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "active-active", []string{"a", "b", "c"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-scale-down")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"a"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme", body, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleApplicationUpdate_TopologyScaleDown_AllowedWithForce(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "active-active", []string{"a", "b"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-force-scale-down")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"a"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme&force=true", body, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// ── PUT 403 forbidden ────────────────────────────────────────────────

func TestHandleApplicationUpdate_ForbiddenWhenNotTierAdmin(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"a"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-update-403")

	body := applicationUpdateRequest{
		Parameters: map[string]interface{}{"domain": "x.com"},
	}
	r := chi.NewRouter()
	registerApplicationUpdateRoutes(r, h)
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, "/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{Tier: "viewer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// ── DELETE happy path ────────────────────────────────────────────────

func TestHandleApplicationDelete_RemovesCR(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"a"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-app-delete")

	rec := callUserAccess(t, h, http.MethodDelete,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme", nil, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	_, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(context.Background(), "wp-prod", metav1.GetOptions{})
	if err == nil {
		t.Fatalf("expected NotFound after delete")
	}
}

func TestHandleApplicationDelete_404OnUnknown(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-app-delete-404")

	rec := callUserAccess(t, h, http.MethodDelete,
		"/api/v1/sovereigns/"+dep.ID+"/applications/nonexistent", nil, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── Topology preview happy path ──────────────────────────────────────

func TestHandleApplicationTopologyPreview_RendersManifests(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"hz-fsn-rtz-prod"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-topo-preview")

	body := applicationChangePreviewRequest{
		Placement: &applicationPlacement{
			Mode:    "active-hotstandby",
			Regions: []string{"hz-fsn-rtz-prod", "hz-hel-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod/topology/preview?namespace=acme", body, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Two regions × 2 manifests each = 4.
	if len(resp.Manifests) != 4 {
		t.Fatalf("manifests: got %d want 4", len(resp.Manifests))
	}
	if resp.Blueprint.Name != "bp-wordpress" {
		t.Fatalf("bp.name: got %q", resp.Blueprint.Name)
	}
}

// ── Upgrade preview happy path ───────────────────────────────────────

func TestHandleApplicationUpgradePreview_UsesTargetVersionQuery(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "single-region", []string{"hz-fsn-rtz-prod"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory

	bp143 := sampleWordpressBlueprint()
	bp143 = &CatalogBlueprint{
		Name:    bp143.Name,
		Version: "1.4.3",
		Card:    bp143.Card,
		Origin:  bp143.Origin,
		Source:  bp143.Source,
		Raw:     bp143.Raw,
	}
	// ensure the Raw spec.version matches the new pinned version so
	// the version field is internally consistent.
	if specMap, ok := bp143.Raw["spec"].(map[string]interface{}); ok {
		specMap["version"] = "1.4.3"
	}
	h.SetCatalogClient(newFakeCatalog(bp143))
	dep := installUserAccessDeployment(t, h, "dep-app-upgrade-preview")

	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod/upgrade/preview?namespace=acme&targetVersion=1.4.3",
		applicationChangePreviewRequest{}, registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationPreviewResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Blueprint.Version != "1.4.3" {
		t.Fatalf("bp.version: got %q want 1.4.3", resp.Blueprint.Version)
	}
}

// ── Validators ───────────────────────────────────────────────────────

func TestValidateApplicationUpdateRequest_AcceptsEmpty(t *testing.T) {
	if msg, ok := validateApplicationUpdateRequest(applicationUpdateRequest{}); !ok {
		t.Fatalf("expected empty body to validate; got %q", msg)
	}
}

func TestValidateApplicationUpdateRequest_RejectsBadMode(t *testing.T) {
	if _, ok := validateApplicationUpdateRequest(applicationUpdateRequest{
		Placement: &applicationPlacement{Mode: "weird", Regions: []string{"a"}},
	}); ok {
		t.Fatal("expected reject for bad mode")
	}
}

func TestTopologyTransitionAllowed_BlocksScaleDown(t *testing.T) {
	if msg, ok := topologyTransitionAllowed("active-active", []string{"a", "b"}, "single-region", []string{"a"}); ok {
		t.Fatalf("expected scale-down block; got ok msg=%q", msg)
	}
	if _, ok := topologyTransitionAllowed("single-region", []string{"a"}, "active-hotstandby", []string{"a", "b"}); !ok {
		t.Fatal("expected scale-up to be allowed")
	}
}

// silenceUnused — reference imports if compile-time unused.
var _ = strings.TrimSpace
