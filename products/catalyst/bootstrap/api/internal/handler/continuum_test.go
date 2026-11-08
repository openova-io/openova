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
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
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
		// #3492 / #3375 — HandleContinuumGet + the switchover handler now
		// also LIST cnpg Cluster CRs (to derive the live DR record / drive
		// the region-kill flip when no Continuum CR exists). The fake
		// dynamic client must know the list-kind for every resource the
		// handlers LIST, else it panics.
		cnpgClusterGVR: "ClusterList",
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

// newCNPGPairFixture composes the primary + replica cnpg Cluster CRs of a
// 2-region active-hot-standby pair, with the canonical labels
// (catalyst.openova.io/cnpg-pair, openova.io/cnpg-role, openova.io/region),
// the replica in replica mode (spec.replica.enabled=true), and both halves
// reporting Ready=True. Mirrors the bp-cnpg-pair chart output the live-DR
// deriver + switchover flip read (#3492 / #3375).
func newCNPGPairFixture(pairName, namespace, primaryRegion, replicaRegion string) (primary, replica *unstructured.Unstructured) {
	mk := func(role, region string, replicaEnabled bool) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetAPIVersion("postgresql.cnpg.io/v1")
		u.SetKind("Cluster")
		u.SetName(pairName + "-" + role)
		u.SetNamespace(namespace)
		u.SetLabels(map[string]string{
			cnpgPairLabel:   pairName,
			cnpgRoleLabel:   role,
			cnpgRegionLabel: region,
		})
		_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
			"instances": int64(2),
			"replica": map[string]interface{}{
				"enabled": replicaEnabled,
			},
		}, "spec")
		_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
			"currentPrimary": pairName + "-" + role + "-1",
			"phase":          "Cluster in healthy state",
			"conditions": []interface{}{
				map[string]interface{}{"type": "Ready", "status": "True"},
			},
		}, "status")
		return u
	}
	return mk(cnpgRolePrimary, primaryRegion, false), mk(cnpgRoleReplica, replicaRegion, true)
}

func registerContinuumRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereigns/{id}/continuums/{name}", h.HandleContinuumGet)
	r.Get("/api/v1/sovereigns/{id}/audit/continuum", h.HandleContinuumAuditList)
	// qa-loop iter-16 Fix #169 — singular `/continuum/` aliases registered
	// in cmd/api/main.go are the surface the matrix runner hits. Mirror
	// them here so TC-pinning tests exercise the same paths.
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


// A live record is only derived for a GENUINE 2-region pair. A single
// region (both halves same region) must NOT fabricate a cross-region DR
// record — the honest 404 / placeholder stands.
func TestHandleContinuumGet_NoLiveRecordForSingleRegion(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Both halves pinned to the SAME region — not real DR.
	primary, replica := newCNPGPairFixture("acme-pg", "acme", "hz-fsn-rtz-prod", "hz-fsn-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-single")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-grafana?namespace=acme", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404 (no cross-region pair); body=%s", rec.Code, rec.Body.String())
	}
}

// ── POST /continuums/{name}/switchover ──────────────────────────────

// ── qa-loop iter-16 Fix #169 — wire-shape parity tests (TC-pinning) ─

// ── POST /continuums/{name}/failback ────────────────────────────────

// ── POST /continuums/{name}/failback/approve ────────────────────────

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
