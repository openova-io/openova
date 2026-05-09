// continuum_test.go — coverage for the EPIC-6 Slice U-DR-1 (#1101)
// Continuum DR REST + SSE handlers.
//
// Test strategy mirrors applications_test.go + rbac_audit_test.go:
//   - Fake dynamic client seeded with the Continuum GVR's list-kind so
//     sovereignDynamicClient resolves to a deterministic in-memory CR.
//   - Installed Deployment with a temp-file kubeconfig path
//     (installUserAccessDeployment helper, shared with slice U).
//   - Per-test chi router that registers only the endpoint under test.
//   - Fixed-clock injection via SetContinuumClock so requestedAt is
//     reproducible across runs.
//   - For audit-list/stream tests the in-process audit.Bus is wired
//     directly (mirrors rbac_audit_test.go).
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// withClaims is a tiny test helper that injects Claims into a request
// context via the canonical auth.ClaimsKey.
func withClaims(ctx context.Context, claims *auth.Claims) context.Context {
	return context.WithValue(ctx, auth.ClaimsKey, claims)
}

// ── Test helpers ─────────────────────────────────────────────────────

func continuumListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		ContinuumGVR(): "ContinuumList",
	}
}

func fakeContinuumDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, continuumListKinds(), seed...)
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}, client
}

// newContinuumUnstructured composes a minimum-viable Continuum CR for
// the handler tests. The CRD fields populated mirror the docs at
// products/catalyst/chart/crds/continuum.yaml.
func newContinuumUnstructured(name, namespace, appRef, primaryRegion string, hotStandbys []string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("dr.openova.io/v1")
	u.SetKind("Continuum")
	u.SetName(name)
	u.SetNamespace(namespace)
	hotAny := make([]interface{}, 0, len(hotStandbys))
	for _, hs := range hotStandbys {
		hotAny = append(hotAny, hs)
	}
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"applicationRef":    appRef,
		"primaryRegion":     primaryRegion,
		"hotStandbyRegions": hotAny,
		"leaseClient": map[string]interface{}{
			"kind": "in-memory",
		},
	}, "spec")
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"phase":         "Healthy",
		"primaryRegion": primaryRegion,
		"leaseHolder":   primaryRegion,
	}, "status")
	return u
}

func registerContinuumRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereigns/{id}/continuums/{name}", h.HandleContinuumGet)
	r.Post("/api/v1/sovereigns/{id}/continuums/{name}/switchover", h.HandleContinuumSwitchoverRequest)
	r.Post("/api/v1/sovereigns/{id}/continuums/{name}/failback", h.HandleContinuumFailbackRequest)
	r.Post("/api/v1/sovereigns/{id}/continuums/{name}/failback/approve", h.HandleContinuumFailbackApprove)
	r.Get("/api/v1/sovereigns/{id}/audit/continuum", h.HandleContinuumAuditList)
}

// ── IsContinuumAuditType pure-function tests ─────────────────────────

func TestIsContinuumAuditType(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"continuum-switchover", true},
		{"continuum-failback-pending", true},
		{"continuum-failback-completed", true},
		{"continuum-lease-lost", true},
		{"continuum-lease-acquired", true},
		{"continuum-cnpg-lag-breach", true},
		{"continuum-cnpg-promotable", true},
		{"continuum-error", true},
		{"continuum-reconcile-success", true},
		{"continuum-switchover-requested", true},
		{"continuum-failback-requested", true},
		{"continuum-failback-approved", true},
		{"rbac-grant-created", false},
		{"continuum", false},
		{"", false},
	}
	for _, tc := range tests {
		got := IsContinuumAuditType(tc.in)
		if got != tc.want {
			t.Errorf("IsContinuumAuditType(%q): got %v want %v", tc.in, got, tc.want)
		}
	}
}

// ── GET /continuums/{name} ───────────────────────────────────────────

func TestHandleContinuumGet_HappyPath(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-get")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "dr-wp" || resp.Namespace != "acme" {
		t.Errorf("identity: got %+v want dr-wp/acme", resp)
	}
	if appRef, _, _ := unstructured.NestedString(resp.Spec, "applicationRef"); appRef != "wp-prod" {
		t.Errorf("applicationRef: got %q want wp-prod", appRef)
	}
	if phase, _, _ := unstructured.NestedString(resp.Status, "phase"); phase != "Healthy" {
		t.Errorf("phase: got %q want Healthy", phase)
	}
}

func TestHandleContinuumGet_404WhenMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-get-404")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/missing", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── POST /continuums/{name}/switchover ──────────────────────────────

func TestHandleContinuumSwitchover_PatchesSpec(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	fixed := time.Date(2026, 5, 9, 10, 30, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-cont-sw")

	body := continuumSwitchoverRequest{
		TargetRegion: "hz-hel-rtz-prod",
		Reason:       "operator-drill",
	}
	raw, _ := json.Marshal(body)

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	// Inject claims so authorization passes.
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}

	// Read back the CR via the fake client.
	got, err := client.Resource(ContinuumGVR()).Namespace("acme").Get(context.Background(), "dr-wp", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch: %v", err)
	}
	requested, _, _ := unstructured.NestedBool(got.Object, "spec", "switchover", "requested")
	if !requested {
		t.Error("spec.switchover.requested: got false want true")
	}
	target, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "targetRegion")
	if target != "hz-hel-rtz-prod" {
		t.Errorf("targetRegion: got %q want hz-hel-rtz-prod", target)
	}
	requestedBy, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "requestedBy")
	if requestedBy != "owner@acme.io" {
		t.Errorf("requestedBy: got %q want owner@acme.io", requestedBy)
	}
	requestedAt, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "requestedAt")
	if requestedAt != fixed.Format(time.RFC3339) {
		t.Errorf("requestedAt: got %q want %q", requestedAt, fixed.Format(time.RFC3339))
	}
	reason, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "reason")
	if reason != "operator-drill" {
		t.Errorf("reason: got %q want operator-drill", reason)
	}

	// Audit Bus should have one continuum-switchover-requested event.
	auditEvents := bus.List(dep.ID, IsContinuumAuditType, 100)
	if len(auditEvents) != 1 {
		t.Fatalf("audit events: got %d want 1", len(auditEvents))
	}
	if auditEvents[0].AuditType != "continuum-switchover-requested" {
		t.Errorf("audit-type: got %q want continuum-switchover-requested", auditEvents[0].AuditType)
	}
	if auditEvents[0].Actor != "owner@acme.io" {
		t.Errorf("actor: got %q want owner@acme.io", auditEvents[0].Actor)
	}
	if auditEvents[0].TargetApplication != "wp-prod" {
		t.Errorf("targetApp: got %q want wp-prod", auditEvents[0].TargetApplication)
	}
}

func TestHandleContinuumSwitchover_403WhenNotOwner(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-sw-403")

	body := continuumSwitchoverRequest{TargetRegion: "hz-hel-rtz-prod"}
	raw, _ := json.Marshal(body)

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "viewer@acme.io", Tier: "viewer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleContinuumSwitchover_404WhenContinuumMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeContinuumDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-sw-404")

	body := continuumSwitchoverRequest{TargetRegion: "hz-hel-rtz-prod"}
	raw, _ := json.Marshal(body)

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleContinuumSwitchover_400WhenTargetRegionMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-sw-400")

	body := continuumSwitchoverRequest{Reason: "missing-target"}
	raw, _ := json.Marshal(body)

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleContinuumSwitchover_409WhenTargetEqualsCurrent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-sw-409")

	body := continuumSwitchoverRequest{TargetRegion: "hz-fsn-rtz-prod"}
	raw, _ := json.Marshal(body)

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// ── POST /continuums/{name}/failback ────────────────────────────────

func TestHandleContinuumFailback_PatchesSpec(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	// Mark approvalRequired so the response carries it through.
	_ = unstructured.SetNestedMap(cr.Object, map[string]interface{}{
		"approvalRequired": true,
	}, "spec", "failback")
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	fixed := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-cont-fb")

	body := continuumFailbackRequest{Reason: "lag-recovered"}
	raw, _ := json.Marshal(body)

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/failback", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumFailbackResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.ApprovalRequired {
		t.Error("response.ApprovalRequired: got false want true")
	}
	if !strings.Contains(resp.Message, "approval") {
		t.Errorf("message should mention approval; got %q", resp.Message)
	}

	got, _ := client.Resource(ContinuumGVR()).Namespace("acme").Get(context.Background(), "dr-wp", metav1.GetOptions{})
	requested, _, _ := unstructured.NestedBool(got.Object, "spec", "failback", "requested")
	if !requested {
		t.Error("spec.failback.requested: got false want true")
	}

	auditEvents := bus.List(dep.ID, IsContinuumAuditType, 100)
	if len(auditEvents) != 1 || auditEvents[0].AuditType != "continuum-failback-requested" {
		t.Fatalf("audit events: %+v", auditEvents)
	}
}

func TestHandleContinuumFailback_AcceptsEmptyBody(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-fb-empty")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/failback", nil)
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleContinuumFailback_403WhenNotOwner(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-fb-403")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/failback", nil)
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "dev@acme.io", Tier: "developer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// ── POST /continuums/{name}/failback/approve ────────────────────────

func TestHandleContinuumFailbackApprove_PatchesSpec(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-cont-fb-ap")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/failback/approve", nil)
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "admin@acme.io", Tier: "admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}

	got, _ := client.Resource(ContinuumGVR()).Namespace("acme").Get(context.Background(), "dr-wp", metav1.GetOptions{})
	approved, _, _ := unstructured.NestedBool(got.Object, "spec", "failback", "approved")
	if !approved {
		t.Error("spec.failback.approved: got false want true")
	}

	auditEvents := bus.List(dep.ID, IsContinuumAuditType, 100)
	if len(auditEvents) != 1 || auditEvents[0].AuditType != "continuum-failback-approved" {
		t.Fatalf("audit events: %+v", auditEvents)
	}
}

func TestHandleContinuumFailbackApprove_403WhenNotSovereignAdmin(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-fb-ap-403")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-wp/failback/approve", nil)
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "dev@acme.io", Tier: "developer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// ── GET /audit/continuum ────────────────────────────────────────────

func TestHandleContinuumAuditList_FiltersOnPrefix(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-cont-audit")

	// Seed a mix of continuum + rbac events.
	for i, ty := range []string{
		"continuum-switchover",
		"continuum-failback-pending",
		"continuum-failback-completed",
		"continuum-error",
		"rbac-grant-created",
	} {
		bus.Publish(context.Background(), audit.Event{
			AuditType:   ty,
			SovereignID: dep.ID,
			Actor:       "alice@acme.io",
			Timestamp:   time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/continuum", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumAuditListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 4 {
		t.Errorf("total: got %d want 4", resp.Total)
	}
	for _, item := range resp.Items {
		if !IsContinuumAuditType(item.AuditType) {
			t.Errorf("non-continuum item leaked: %+v", item)
		}
	}
}

func TestHandleContinuumAuditList_503WhenBusNotWired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installUserAccessDeployment(t, h, "dep-cont-audit-503")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/continuum", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleContinuumAuditList_TypeNarrowing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-cont-audit-narrow")

	for i, ty := range []string{
		"continuum-switchover",
		"continuum-switchover",
		"continuum-failback-completed",
		"continuum-error",
	} {
		bus.Publish(context.Background(), audit.Event{
			AuditType:   ty,
			SovereignID: dep.ID,
			Actor:       "alice@acme.io",
			Timestamp:   time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/continuum?type=continuum-switchover", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumAuditListResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 2 {
		t.Errorf("total: got %d want 2 (only continuum-switchover events)", resp.Total)
	}
}
