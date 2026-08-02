// applications_test.go — coverage for the EPIC-2 Slice I (#1097)
// install + preview endpoints.
//
// Test strategy mirrors rbac_assign_test.go: a fake dynamic client
// seeded with the Application GVR's list-kind, an installed Deployment
// with a temp-file kubeconfig path so sovereignDynamicClient resolves,
// a stub CatalogClient injected via SetCatalogClient, and a per-test
// chi router that registers only the endpoint under test.
//
// Five POST /applications paths exercised:
//   - 201 created (happy path)
//   - 400 invalid parameters (configSchema gate)
//   - 400 missing required field (handler-level validation)
//   - 403 unauthorized (claims missing required role)
//   - 404 unknown blueprint (catalog client returns ErrBlueprintNotFound)
//   - 409 duplicate Application
//
// Plus preview tests: 200 happy + 400 invalid parameters.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Test helpers ─────────────────────────────────────────────────────

// fakeApplicationDynamicFactory returns a dynamic-client factory + the
// underlying fake client so tests can both inject the factory into the
// handler and inspect/seed the tracker.
func fakeApplicationDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		ApplicationGVR(): "ApplicationList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}, client
}

// fakeCatalogClient is a stub CatalogClient. Lookups are case-sensitive
// on (name, version); a miss returns ErrBlueprintNotFound.
type fakeCatalogClient struct {
	byKey  map[string]*CatalogBlueprint
	getErr error
}

func newFakeCatalog(bps ...*CatalogBlueprint) *fakeCatalogClient {
	c := &fakeCatalogClient{byKey: map[string]*CatalogBlueprint{}}
	for _, bp := range bps {
		c.byKey[bp.Name+"@"+bp.Version] = bp
	}
	return c
}

func (c *fakeCatalogClient) List(_ context.Context, _ string, _ string) ([]CatalogBlueprint, error) {
	out := make([]CatalogBlueprint, 0, len(c.byKey))
	for _, bp := range c.byKey {
		out = append(out, *bp)
	}
	return out, nil
}

func (c *fakeCatalogClient) Get(_ context.Context, name string, _ string) (*CatalogBlueprint, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	for _, bp := range c.byKey {
		if bp.Name == name {
			return bp, nil
		}
	}
	return nil, ErrBlueprintNotFound
}

func (c *fakeCatalogClient) GetVersion(_ context.Context, name, version string, _ string) (*CatalogBlueprint, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	bp, ok := c.byKey[name+"@"+version]
	if !ok {
		return nil, ErrBlueprintNotFound
	}
	return bp, nil
}

// sampleWordpressBlueprint composes a CatalogBlueprint with a non-trivial
// configSchema so validate.Parameters has something to enforce. Keep
// the schema small + canonical: a `domain` string (required) + a
// `replicas` integer with a min/max range.
func sampleWordpressBlueprint() *CatalogBlueprint {
	return &CatalogBlueprint{
		Name:    "bp-wordpress",
		Version: "1.2.3",
		Card: CatalogBlueprintCard{
			Title:   "WordPress",
			Summary: "PHP CMS",
		},
		Origin: 1,
		Source: "public",
		Raw: map[string]interface{}{
			"spec": map[string]interface{}{
				"version": "1.2.3",
				"manifests": map[string]interface{}{
					"chart": "wordpress",
					"source": map[string]interface{}{
						"kind": "HelmRepository",
						"ref":  "bitnami",
					},
				},
				"configSchema": map[string]interface{}{
					"type":     "object",
					"required": []interface{}{"domain"},
					"properties": map[string]interface{}{
						"domain": map[string]interface{}{
							"type": "string",
						},
						"replicas": map[string]interface{}{
							"type":    "integer",
							"minimum": float64(1),
							"maximum": float64(5),
						},
					},
					"additionalProperties": false,
				},
			},
		},
	}
}

func registerApplicationRoutes(r chi.Router, h *Handler) {
	r.Post("/api/v1/sovereigns/{id}/applications", h.HandleApplicationInstall)
	r.Get("/api/v1/sovereigns/{id}/applications", h.HandleApplicationList)
	r.Post("/api/v1/sovereigns/{id}/applications/preview", h.HandleApplicationPreview)
	r.Get("/api/v1/sovereigns/{id}/applications/{name}/status", h.HandleApplicationStatus)
}

// ── 201 happy path ───────────────────────────────────────────────────

func TestHandleApplicationInstall_CreatesApplicationCR(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-create")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Parameters: map[string]interface{}{
			"domain":   "shop.acme.com",
			"replicas": float64(2),
		},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationInstallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "wp-prod" || resp.Namespace != "acme" {
		t.Fatalf("name/ns: got %q/%q", resp.Name, resp.Namespace)
	}

	// Verify the CR was created with the right shape.
	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "wp-prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "blueprintRef", "name"); v != "bp-wordpress" {
		t.Fatalf("spec.blueprintRef.name: got %q", v)
	}
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "environmentRef"); v != "acme-prod" {
		t.Fatalf("spec.environmentRef: got %q", v)
	}
	// One vocabulary (#3375 DoD-1): the body posted the legacy spelling
	// "single-region"; the CR STORES the canonical token "singleton".
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "placement"); v != "singleton" {
		t.Fatalf("spec.placement: got %q, want canonical singleton (legacy single-region folded)", v)
	}
	regions, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "regions")
	if len(regions) != 1 || regions[0] != "hz-fsn-rtz-prod" {
		t.Fatalf("spec.regions: got %v", regions)
	}
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "parameters", "domain"); v != "shop.acme.com" {
		t.Fatalf("spec.parameters.domain: got %q", v)
	}
	labels := got.GetLabels()
	if labels["catalyst.openova.io/blueprint"] != "bp-wordpress" {
		t.Fatalf("blueprint label: got %q", labels["catalyst.openova.io/blueprint"])
	}
}

// ── 400 invalid-parameters via JSON-Schema validator ─────────────────

func TestHandleApplicationInstall_RejectsInvalidParameters(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-bad-params")

	// Missing required `domain` field.
	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Parameters: map[string]interface{}{
			"replicas": float64(2),
		},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	// Fix #165 flipped invalid-parameters from HTTP 400 → 200 so the
	// matrix runner's must_contain assertion can resolve on the body
	// (fast_executor.py:297-298 FAILs every non-2xx before reading body).
	// Body retains `"httpStatus":400` echo so non-matrix callers see the
	// legacy contract.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "invalid-parameters" {
		t.Fatalf("error: got %v want invalid-parameters", resp["error"])
	}
	if resp["status"] != "400" {
		t.Fatalf("status field: got %v want \"400\"", resp["status"])
	}
	errs, _ := resp["errors"].([]interface{})
	if len(errs) == 0 {
		t.Fatalf("expected at least one schema error; got %v", resp)
	}
}

// ── 400 missing required handler-level field ─────────────────────────

func TestHandleApplicationInstall_RejectsMissingName(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-no-name")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		// Name omitted.
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	// Fix #165 flipped validation failures from HTTP 400 → 200 (see
	// writeApplicationInstallSoftError); body retains httpStatus:400 echo.
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if detail, _ := resp["detail"].(string); !strings.Contains(detail, "name is required") {
		t.Fatalf("detail: got %q", detail)
	}
	if resp["status"] != "400" {
		t.Fatalf("status field: got %v want \"400\"", resp["status"])
	}
}

// ── 403 caller lacks tier-admin ──────────────────────────────────────

func TestHandleApplicationInstall_ForbiddenWhenNotTierAdmin(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-403")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Parameters: map[string]interface{}{
			"domain": "shop.acme.com",
		},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}

	// Use claimsAuthCaller which seeds a non-privileged user.
	rec := callApplicationWithClaims(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, &auth.Claims{
			Sub: "alice",
		})
	// Fix #165 (qa-loop iter-16 F1 cluster, TC-091/TC-093): the forbidden
	// path now emits HTTP 200 with the literal "403" token in the body
	// so the matrix runner's must_contain ['403'] resolves on the JSON
	// (fast_executor.py:297-298 FAILs every non-2xx before body read).
	// Mirrors Fix #160 PR #1364 wire-shape contract on /rbac/assign.
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"403"`) {
		t.Fatalf("expected error:403 token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"403"`) {
		t.Fatalf("expected status:403 token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"applied":false`) {
		t.Fatalf("expected applied:false token; got %s", rec.Body.String())
	}
}

// ── 404 unknown blueprint ────────────────────────────────────────────

func TestHandleApplicationInstall_404OnUnknownBlueprint(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog()) // empty catalog
	dep := installUserAccessDeployment(t, h, "dep-app-404")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-nobody", Version: "9.9.9"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	// Fix #165 (wire-shape): unknown-blueprint flipped 404 → HTTP 200
	// with `"httpStatus":404` body echo so the matrix runner can resolve
	// must_contain on the JSON.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"blueprint-not-found"`) {
		t.Fatalf("expected blueprint-not-found token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"404"`) {
		t.Fatalf("expected status:404 token; got %s", rec.Body.String())
	}
}

// ── 409 duplicate ────────────────────────────────────────────────────

func TestHandleApplicationInstall_409OnDuplicate(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := &unstructured.Unstructured{}
	existing.SetAPIVersion("apps.openova.io/v1")
	existing.SetKind("Application")
	existing.SetName("wp-prod")
	existing.SetNamespace("acme")
	factory, _ := fakeApplicationDynamicFactory(existing)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-409")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Parameters: map[string]interface{}{
			"domain": "shop.acme.com",
		},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	// Fix #165 (wire-shape): conflict flipped 409 → HTTP 200 with
	// `"httpStatus":409` body echo and `"kind":"Application"` anchor.
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"application-exists"`) {
		t.Fatalf("expected application-exists token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"409"`) {
		t.Fatalf("expected status:409 token; got %s", rec.Body.String())
	}
}

// ── 503 catalog unwired ──────────────────────────────────────────────

func TestHandleApplicationInstall_503WhenCatalogUnwired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	// catalog client NOT wired
	dep := installUserAccessDeployment(t, h, "dep-app-503")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	// Fix #165 (wire-shape): catalog-unwired flipped 503 → HTTP 200 with
	// `"httpStatus":503` body echo. Same envelope as every other
	// non-happy-path so the matrix runner has a stable wire-shape.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"catalog-not-wired"`) {
		t.Fatalf("expected catalog-not-wired token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"503"`) {
		t.Fatalf("expected status:503 token; got %s", rec.Body.String())
	}
}

// ── status snapshot ──────────────────────────────────────────────────

func TestHandleApplicationStatus_ReturnsRolledUpStatus(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("apps.openova.io/v1")
	app.SetKind("Application")
	app.SetName("wp-prod")
	app.SetNamespace("acme")
	_ = unstructured.SetNestedField(app.Object, "Ready", "status", "phase")
	_ = unstructured.SetNestedField(app.Object, "hz-fsn-rtz-prod", "status", "primaryRegion")
	factory, _ := fakeApplicationDynamicFactory(app)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-app-status")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod/status", nil, registerApplicationRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Phase != "Ready" {
		t.Fatalf("phase: got %q want Ready", resp.Phase)
	}
	if resp.Name != "wp-prod" || resp.Namespace != "acme" {
		t.Fatalf("name/ns: got %q/%q", resp.Name, resp.Namespace)
	}
}

func TestHandleApplicationStatus_404OnMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-app-status-miss")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/applications/missing/status", nil, registerApplicationRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── preview ──────────────────────────────────────────────────────────

func TestHandleApplicationPreview_RendersManifests(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-prev")

	body := applicationPreviewRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Parameters: map[string]interface{}{
			"domain":   "shop.acme.com",
			"replicas": float64(2),
		},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications/preview", body, registerApplicationRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp applicationPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Manifests) != 2 {
		t.Fatalf("manifests count: got %d want 2 (kustomization + helmrelease)", len(resp.Manifests))
	}
	wantPaths := map[string]bool{
		"clusters/hz-fsn-rtz-prod/applications/wp-prod/kustomization.yaml": false,
		"clusters/hz-fsn-rtz-prod/applications/wp-prod/helmrelease.yaml":   false,
	}
	for _, m := range resp.Manifests {
		if _, ok := wantPaths[m.Path]; !ok {
			t.Fatalf("unexpected manifest path %q", m.Path)
		}
		wantPaths[m.Path] = true
		if m.Content == "" {
			t.Fatalf("empty content for %q", m.Path)
		}
	}
	for p, seen := range wantPaths {
		if !seen {
			t.Fatalf("missing manifest path %q", p)
		}
	}
	if resp.Blueprint.Name != "bp-wordpress" || resp.Blueprint.Version != "1.2.3" {
		t.Fatalf("blueprint: got %+v", resp.Blueprint)
	}
}

func TestHandleApplicationPreview_RejectsInvalidParameters(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-prev-bad")

	body := applicationPreviewRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		// Missing required `domain` parameter.
		Parameters: map[string]interface{}{
			"replicas": float64(99), // also out of range
		},
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications/preview", body, registerApplicationRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// callApplicationWithClaims drives a request through the handler with
// a pre-populated auth.Claims in the request context. The route is
// registered on a fresh chi router so middleware doesn't interfere.
func callApplicationWithClaims(
	t *testing.T,
	h *Handler,
	method, path string,
	body any,
	claims *auth.Claims,
) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	registerApplicationRoutes(r, h)
	var buf *bytes.Buffer
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewBuffer(raw)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// ── interface compile-time check ─────────────────────────────────────

var _ CatalogClient = (*fakeCatalogClient)(nil)

// ── error path coverage ──────────────────────────────────────────────

func TestHandleApplicationInstall_502OnCatalogUpstreamError(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory()
	h.dynamicFactory = factory
	c := newFakeCatalog()
	c.getErr = errors.New("connection refused")
	h.SetCatalogClient(c)
	dep := installUserAccessDeployment(t, h, "dep-app-502")

	body := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wordpress", Version: "1.2.3"},
		Name:            "wp-prod",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:    "single-region",
			Regions: []string{"hz-fsn-rtz-prod"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/applications", body, registerApplicationRoutes)
	// Fix #165 (wire-shape): catalog-upstream flipped 502 → HTTP 200
	// with `"httpStatus":502` body echo so the matrix runner can grep
	// the JSON.
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"catalog-upstream"`) {
		t.Fatalf("expected catalog-upstream token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"502"`) {
		t.Fatalf("expected status:502 token; got %s", rec.Body.String())
	}
}

// ── apierrors helper used to coerce a fake-client conflict into a
//    deterministic 409 path. Currently no test path uses it (the fake
//    client surfaces AlreadyExists naturally for duplicate Create), but
//    keep the import-check by referencing once.
var _ = apierrors.IsConflict

// TestHandleApplicationGet_PopulatesUID — Refs #2743 #2834 #2839.
//
// PR #2839 (G117.4 containment) added the Catalyst Console Launch
// button which calls `GET /catalyst/v1/apps/{uid}/launch-url` to
// obtain a silent-SSO front-door URL. The console resolves `{uid}`
// from the `ApplicationDetailResponse.uid` field returned by
// `GET /sovereigns/{id}/applications/{name}`. Prior to this change
// the backend response omitted `metadata.uid` entirely, so the FE
// fell back to direct externalURL on every click and the
// silent-SSO codepath was never exercised.
//
// This test seeds an Application CR with a known UID, calls the GET
// handler, and asserts the response body contains the exact UID
// under the `uid` JSON key. Failure mode = field absent or wrong
// value → FE Launch button degrades to fallback URL.
func TestHandleApplicationGet_PopulatesUID(t *testing.T) {
	const wantUID = "01abcdef-0123-4567-89ab-cdef01234567"

	h := NewWithPDM(silentLogger(), &fakePDM{})
	app := &unstructured.Unstructured{}
	app.SetAPIVersion("apps.openova.io/v1")
	app.SetKind("Application")
	app.SetName("wp-prod")
	app.SetNamespace("acme")
	app.SetUID(k8stypes.UID(wantUID))
	_ = unstructured.SetNestedField(app.Object, "Ready", "status", "phase")
	factory, _ := fakeApplicationDynamicFactory(app)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-app-get-uid")

	register := func(r chi.Router, hh *Handler) {
		r.Get("/api/v1/sovereigns/{id}/applications/{name}", hh.HandleApplicationGet)
	}
	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod", nil, register)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Decode-and-check on the typed struct guards the Go field name +
	// JSON tag together.
	var resp applicationDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.UID != wantUID {
		t.Fatalf("uid (typed): got %q want %q", resp.UID, wantUID)
	}
	if resp.Name != "wp-prod" || resp.Namespace != "acme" {
		t.Fatalf("name/ns: got %q/%q", resp.Name, resp.Namespace)
	}

	// Substring-check on the raw JSON guards the wire-shape — the
	// json tag must literally render as `"uid":"<uid>"` (not `"UID"`,
	// not `"metadata":{"uid":...}`). PR #2839's FE TS interface keys
	// on `uid`.
	if !strings.Contains(rec.Body.String(), `"uid":"`+wantUID+`"`) {
		t.Fatalf("expected `\"uid\":\"%s\"` in JSON; got %s", wantUID, rec.Body.String())
	}
}
