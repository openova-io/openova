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
	r.Post("/api/v1/sovereigns/{id}/continuums/{name}/switchover", h.HandleContinuumSwitchoverRequest)
	r.Post("/api/v1/sovereigns/{id}/continuums/{name}/failback", h.HandleContinuumFailbackRequest)
	r.Post("/api/v1/sovereigns/{id}/continuums/{name}/failback/approve", h.HandleContinuumFailbackApprove)
	r.Get("/api/v1/sovereigns/{id}/audit/continuum", h.HandleContinuumAuditList)
	// qa-loop iter-16 Fix #169 — singular `/continuum/` aliases registered
	// in cmd/api/main.go are the surface the matrix runner hits. Mirror
	// them here so TC-pinning tests exercise the same paths.
	r.Post("/api/v1/sovereigns/{id}/continuum/{name}/switchover", h.HandleContinuumSwitchoverRequest)
	r.Post("/api/v1/sovereigns/{id}/continuum/{name}/switchover/preview", h.HandleContinuumSwitchoverPreview)
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

// #3492 / #3375 — GET derives a LIVE DR record from the cnpg cluster-pair
// when no Continuum CR exists but a real 2-region pair does. This is the
// fix for founder review #7 (the bp-grafana panel placeholder): the panel
// now reads a real record (primaryRegion / replicaRegion / phase /
// replicationHealthy) built from live cluster state instead of 404.
func TestHandleContinuumGet_DerivesLiveRecordFromCNPGPair(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("acme-pg", "acme", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	factory, _ := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	fixed := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-cont-live-get")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	// The UI queries dr-<app>; there is no Continuum CR — only the pair.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-grafana?namespace=acme", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (live record); body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumGetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status["primaryRegion"] != "hz-fsn-rtz-prod" {
		t.Errorf("status.primaryRegion: got %v want hz-fsn-rtz-prod", resp.Status["primaryRegion"])
	}
	if resp.Status["replicaRegion"] != "hz-hel-rtz-prod" {
		t.Errorf("status.replicaRegion: got %v want hz-hel-rtz-prod", resp.Status["replicaRegion"])
	}
	if resp.Status["replicationHealthy"] != true {
		t.Errorf("status.replicationHealthy: got %v want true", resp.Status["replicationHealthy"])
	}
	if resp.Status["phase"] != "Healthy" {
		t.Errorf("status.phase: got %v want Healthy", resp.Status["phase"])
	}
	// spec carries the generic mechanism (cnpg-pair) + the app ref, not a
	// hardcoded app name.
	sw, _ := resp.Spec["switchover"].(map[string]interface{})
	if sw == nil || sw["mechanism"] != "cnpg-pair" {
		t.Errorf("spec.switchover.mechanism: got %v want cnpg-pair", sw)
	}
	if resp.Spec["applicationRef"] != "grafana" {
		t.Errorf("spec.applicationRef: got %v want grafana", resp.Spec["applicationRef"])
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

	// #5731 — the handler PATCHES spec.switchover.requested; the K-Cont-2
	// reconciler executes afterwards. 202 Accepted + status "requested" is
	// the honest answer. It previously returned 200 "completed" + a 60s
	// duration the moment the PATCH landed.
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"status":"requested"`, "fromRegion", "toRegion"} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("body missing %q token; got %s", want, rec.Body.String())
		}
	}
	for _, forbidden := range []string{"completed", "durationSeconds", "lastSwitchoverDuration"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(forbidden)) {
			t.Errorf("body claims %q for a switchover the reconciler has not run; got %s",
				forbidden, rec.Body.String())
		}
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

	// qa-loop iter-16 Fix #169 — viewer now receives HTTP 200 + "403"
	// in body (Fix #160 wire-shape).
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"403"`)) {
		t.Errorf("body missing %q token; got %s", "403", rec.Body.String())
	}
}

// #3492 / #3375 — when NO Continuum CR exists AND there is no live
// cnpg-pair backing the app, the switchover handler returns an HONEST
// "no-live-dr-pair" body (applied:false) — NOT a fabricated "completed".
// This replaces the prior synthesized-completion theater (the anti-pattern
// the brief bans). The positive case (a live pair IS present → real flip)
// is covered by TestHandleContinuumSwitchover_LivePairFlip below.
func TestHandleContinuumSwitchover_HonestWhenNoCRAndNoPair(t *testing.T) {
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

	// Wire-shape: still a 200 envelope (UAT runner reads the body), but the
	// truth is carried in the body fields — NOT a fake completion.
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"completed"`)) || bytes.Contains(rec.Body.Bytes(), []byte(`"applied":true`)) {
		t.Errorf("must NOT fabricate a completion when no live pair exists; got %s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("no-live-dr-pair")) {
		t.Errorf("body missing honest %q error token; got %s", "no-live-dr-pair", rec.Body.String())
	}
}

// 🔒 TENANT ISOLATION: a switchover with NO namespace and a pair that is
// NOT app-labelled must NOT resolve to that pair (it could belong to
// another Organization) — the handler returns the honest no-live-dr-pair
// body and touches NOTHING, rather than flipping a foreign tenant's DB.
func TestHandleContinuumSwitchover_NoCrossOrgFlipWithoutNamespace(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// A real 2-region pair, but in org "other" and NOT app-labelled for
	// "grafana". The caller omits ?namespace=.
	primary, replica := newCNPGPairFixture("other-pg", "other", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	factory, client := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-xorg")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-grafana/switchover", nil) // no namespace
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("no-live-dr-pair")) {
		t.Errorf("cross-org pair must NOT resolve without a namespace; got %s", rec.Body.String())
	}
	// The foreign tenant's clusters must be UNTOUCHED.
	gp, _ := client.Resource(cnpgClusterGVR).Namespace("other").Get(context.Background(), "other-pg-primary", metav1.GetOptions{})
	if en, _, _ := unstructured.NestedBool(gp.Object, "spec", "replica", "enabled"); en {
		t.Error("foreign primary spec.replica.enabled was flipped (cross-org write!)")
	}
	gr, _ := client.Resource(cnpgClusterGVR).Namespace("other").Get(context.Background(), "other-pg-replica", metav1.GetOptions{})
	if en, _, _ := unstructured.NestedBool(gr.Object, "spec", "replica", "enabled"); !en {
		t.Error("foreign replica spec.replica.enabled was flipped (cross-org write!)")
	}
}

// TestHandleContinuumSwitchover_LivePairFlip proves the generic happy
// path: with NO Continuum CR but a live 2-region cnpg cluster-pair seeded,
// POST /switchover drives the REAL region-kill promotion — flipping
// spec.replica.enabled on the two Cluster halves (the hw128 mechanism) —
// and returns a genuine completed/applied body reflecting what happened.
func TestHandleContinuumSwitchover_LivePairFlip(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	primary, replica := newCNPGPairFixture("acme-pg", "acme", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	factory, client := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	fixed := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	h.SetContinuumClock(func() time.Time { return fixed })
	dep := installUserAccessDeployment(t, h, "dep-cont-live-sw")

	// Empty body — handler resolves the standby region from the live pair.
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-grafana/switchover?namespace=acme", nil)
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"applied":true`, "completed", "hz-fsn-rtz-prod", "hz-hel-rtz-prod"} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("body missing %q; got %s", want, rec.Body.String())
		}
	}

	// The REAL state change: replica half promoted (enabled=false), old
	// primary half demoted to follower (enabled=true).
	gotReplica, err := client.Resource(cnpgClusterGVR).Namespace("acme").Get(context.Background(), "acme-pg-replica", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch replica: %v", err)
	}
	if en, _, _ := unstructured.NestedBool(gotReplica.Object, "spec", "replica", "enabled"); en {
		t.Error("replica half spec.replica.enabled: got true want false (should be promoted)")
	}
	gotPrimary, err := client.Resource(cnpgClusterGVR).Namespace("acme").Get(context.Background(), "acme-pg-primary", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch primary: %v", err)
	}
	if en, _, _ := unstructured.NestedBool(gotPrimary.Object, "spec", "replica", "enabled"); !en {
		t.Error("old primary half spec.replica.enabled: got false want true (should be demoted to follower)")
	}

	// One real continuum-switchover audit event.
	if evs := bus.List(dep.ID, IsContinuumAuditType, 100); len(evs) != 1 {
		t.Fatalf("audit events: got %d want 1", len(evs))
	}
}

// Idempotency: once the replica is promoted (replica.enabled=false), a
// SECOND bare switchover POST must NOT ping-pong the primary back — it
// returns a no-op rather than re-flipping. Guards against a double-click
// silently double-switching (cleanup-review finding).
func TestHandleContinuumSwitchover_LivePairIdempotentNoPingPong(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Replica already promoted: replica half enabled=false, primary half
	// enabled=true (post-switchover steady state).
	primary, replica := newCNPGPairFixture("acme-pg", "acme", "hz-fsn-rtz-prod", "hz-hel-rtz-prod")
	_ = unstructured.SetNestedField(replica.Object, false, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(primary.Object, true, "spec", "replica", "enabled")
	factory, client := fakeContinuumDynamicFactory(primary, replica)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-cont-live-idem")

	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuums/dr-grafana/switchover?namespace=acme", nil)
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Must be a no-op — NOT applied, NOT a fresh completion.
	if bytes.Contains(rec.Body.Bytes(), []byte(`"applied":true`)) {
		t.Errorf("second switchover must be a no-op, not applied:true; got %s", rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("switchover-noop")) {
		t.Errorf("body missing %q; got %s", "switchover-noop", rec.Body.String())
	}
	// State must be UNCHANGED — primary half still the follower, replica
	// half still promoted (no ping-pong).
	gotPrimary, _ := client.Resource(cnpgClusterGVR).Namespace("acme").Get(context.Background(), "acme-pg-primary", metav1.GetOptions{})
	if en, _, _ := unstructured.NestedBool(gotPrimary.Object, "spec", "replica", "enabled"); !en {
		t.Error("primary half spec.replica.enabled flipped on a no-op (ping-pong!): got false want true")
	}
	gotReplica, _ := client.Resource(cnpgClusterGVR).Namespace("acme").Get(context.Background(), "acme-pg-replica", metav1.GetOptions{})
	if en, _, _ := unstructured.NestedBool(gotReplica.Object, "spec", "replica", "enabled"); en {
		t.Error("replica half spec.replica.enabled flipped on a no-op (ping-pong!): got true want false")
	}
}

func TestHandleContinuumSwitchover_400WhenTargetRegionMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// No hot-standby on the CR so the handler can't auto-default.
	cr := newContinuumUnstructured("dr-wp", "acme", "wp-prod", "hz-fsn-rtz-prod", nil)
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

	// qa-loop iter-16 Fix #169 — 400 → 200 + "missing-target-region"
	// body token (Fix #160 wire-shape).
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"missing-target-region", `"400"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("body missing %q token; got %s", want, rec.Body.String())
		}
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

	// qa-loop iter-16 Fix #169 — 409 → 200 + "switchover-noop" body
	// token (Fix #160 wire-shape).
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"switchover-noop", `"409"`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("body missing %q token; got %s", want, rec.Body.String())
		}
	}
}

// ── qa-loop iter-16 Fix #169 — wire-shape parity tests (TC-pinning) ─

// TestHandleContinuumSwitchover_TC312_HappyPath pins TC-312.
//
// #5731 — the assertion was `must_contain: ["completed", "60"]`, and
// the handler emitted both tokens unconditionally, so TC-312 could not
// fail. It is now pointed at OBSERVED state: a real Continuum CR is
// installed on the fake cluster and the test asserts the switchover
// request landed ON THE CR (spec.switchover.*), not that two literals
// appeared in a response body.
func TestHandleContinuumSwitchover_TC312_HappyPath(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("cont-omantel", "qa-omantel", "qa-wp", "fsn1", []string{"hz-hel-rtz-prod"})
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-tc312")

	body := continuumSwitchoverRequest{Target: "hz-hel-rtz-prod"}
	raw, _ := json.Marshal(body)
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-omantel/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("TC-312 status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	// The measurement: did the request reach the CR?
	got, err := client.Resource(ContinuumGVR()).Namespace("qa-omantel").
		Get(context.Background(), "cont-omantel", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("TC-312 re-fetch CR: %v", err)
	}
	requested, _, _ := unstructured.NestedBool(got.Object, "spec", "switchover", "requested")
	if !requested {
		t.Error("TC-312 spec.switchover.requested: got false want true")
	}
	target, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "targetRegion")
	if target != "hz-hel-rtz-prod" {
		t.Errorf("TC-312 spec.switchover.targetRegion: got %q want hz-hel-rtz-prod", target)
	}
	// No duration and no completion may be claimed — the reconciler has
	// not run. These were the tokens the old assertion demanded.
	for _, forbidden := range []string{"completed", "durationSeconds", "timeout", "failed"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(forbidden)) {
			t.Errorf("TC-312 body has forbidden %q token; got %s", forbidden, rec.Body.String())
		}
	}
}

// TestHandleContinuumSwitchover_TC324_FailbackToPrimary pins TC-324.
//
// #5731 — was `must_contain: ["completed", "fsn1"]`. The `fsn1` token
// resolved because the handler echoed a HARDCODED Hetzner region on the
// fallback path. It now asserts the caller-supplied failback target is
// what landed on the CR, and the region literal comes from the fixture
// the test itself installed, not from a constant in the handler.
func TestHandleContinuumSwitchover_TC324_FailbackToPrimary(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// Continuum currently primary=hel (post-switchover), failback to fsn1.
	cr := newContinuumUnstructured("cont-omantel", "qa-omantel", "qa-wp", "hz-hel-rtz-prod", []string{"fsn1"})
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-tc324")

	body := continuumSwitchoverRequest{Target: "fsn1"}
	raw, _ := json.Marshal(body)
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-omantel/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("TC-324 status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	got, err := client.Resource(ContinuumGVR()).Namespace("qa-omantel").
		Get(context.Background(), "cont-omantel", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("TC-324 re-fetch CR: %v", err)
	}
	target, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "targetRegion")
	if target != "fsn1" {
		t.Errorf("TC-324 spec.switchover.targetRegion: got %q want fsn1 (the failback target)", target)
	}
	for _, forbidden := range []string{"completed", "durationSeconds", "failed"} {
		if bytes.Contains(rec.Body.Bytes(), []byte(forbidden)) {
			t.Errorf("TC-324 body has forbidden %q token; got %s", forbidden, rec.Body.String())
		}
	}
}

// TestHandleContinuumSwitchover_TC331_ViewerForbidden pins TC-331:
//
//	must_contain: ["403"], must_not_contain: ["completed"]
//
// Wire-shape contract: viewer caller → HTTP 200 + body "403" (Fix #160).
func TestHandleContinuumSwitchover_TC331_ViewerForbidden(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("cont-omantel", "qa-omantel", "qa-wp", "fsn1", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-tc331")

	body := continuumSwitchoverRequest{Target: "hz-hel-rtz-prod"}
	raw, _ := json.Marshal(body)
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-omantel/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "viewer@acme.io", Tier: "viewer"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("TC-331 status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("403")) {
		t.Errorf("TC-331 body missing %q token; got %s", "403", rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"status":"completed"`)) {
		t.Errorf("TC-331 body has forbidden 'completed' status; got %s", rec.Body.String())
	}
}

// TestHandleContinuumSwitchover_TC332_OperatorCanSwitchover pins TC-332.
//
// #5731 — was `must_contain: ["completed"]`, which the handler answered
// unconditionally. TC-332's real subject is AUTHORIZATION: an
// operator-tier caller must get through the tier gate and have the
// request land on the CR. That is what it now measures — the operator's
// identity is asserted in `spec.switchover.requestedBy`, which a
// synthesized body could never have carried.
func TestHandleContinuumSwitchover_TC332_OperatorCanSwitchover(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("cont-omantel", "qa-omantel", "qa-wp", "fsn1", []string{"hz-hel-rtz-prod"})
	factory, client := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-tc332")

	body := continuumSwitchoverRequest{Target: "hz-hel-rtz-prod"}
	raw, _ := json.Marshal(body)
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-omantel/switchover", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	// operator tier maps through applicationInstallCallerAuthorized.
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "op@acme.io", Tier: "operator"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("TC-332 status: got %d want 202; body=%s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"status":"403"`)) {
		t.Errorf("TC-332 body has forbidden 403 status; got %s", rec.Body.String())
	}
	got, err := client.Resource(ContinuumGVR()).Namespace("qa-omantel").
		Get(context.Background(), "cont-omantel", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("TC-332 re-fetch CR: %v", err)
	}
	requested, _, _ := unstructured.NestedBool(got.Object, "spec", "switchover", "requested")
	if !requested {
		t.Error("TC-332 operator tier did not reach the CR: spec.switchover.requested is false")
	}
	requestedBy, _, _ := unstructured.NestedString(got.Object, "spec", "switchover", "requestedBy")
	if requestedBy != "op@acme.io" {
		t.Errorf("TC-332 spec.switchover.requestedBy: got %q want op@acme.io", requestedBy)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("completed")) {
		t.Errorf("TC-332 body claims completion for a switchover the reconciler has not run; got %s",
			rec.Body.String())
	}
}

// TestHandleContinuumSwitchoverPreview_TC339_DryRunPreflight pins TC-339.
//
// #5731 — was run against NO CR (`fakeContinuumDynamicFactory()` with no
// arguments), which is exactly the branch that synthesized
// `promotable:true` with an empty blockingChecks list. Asserting that
// the tokens `estimatedDuration` + `blockingChecks` merely APPEAR was
// satisfied by that fabrication. It now runs against a real CR and
// asserts the preflight VALUES are derived from it. The no-CR case is
// covered separately by the honesty guard
// (continuum_dr_synthesis_5731_test.go).
func TestHandleContinuumSwitchoverPreview_TC339_DryRunPreflight(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	cr := newContinuumUnstructured("cont-omantel", "qa-omantel", "qa-wp", "fsn1", []string{"hz-hel-rtz-prod"})
	factory, _ := fakeContinuumDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-tc339")

	body := continuumSwitchoverPreviewRequest{Target: "hz-hel-rtz-prod"}
	raw, _ := json.Marshal(body)
	r := chi.NewRouter()
	registerContinuumRoutes(r, h)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/continuum/cont-omantel/switchover/preview", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withClaims(req.Context(), &auth.Claims{Email: "owner@acme.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("TC-339 status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp continuumSwitchoverPreviewResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("TC-339 decode: %v", err)
	}
	// Derived from the CR the test installed — not from a constant.
	if resp.Continuum != "cont-omantel" {
		t.Errorf("TC-339 continuum: got %q want cont-omantel (read off the CR)", resp.Continuum)
	}
	if resp.Namespace != "qa-omantel" {
		t.Errorf("TC-339 namespace: got %q want qa-omantel (read off the CR)", resp.Namespace)
	}
	if resp.CurrentPrimary != "fsn1" {
		t.Errorf("TC-339 currentPrimary: got %q want fsn1 (spec.primaryRegion of the CR)", resp.CurrentPrimary)
	}
	if resp.TargetRegion != "hz-hel-rtz-prod" {
		t.Errorf("TC-339 targetRegion: got %q want hz-hel-rtz-prod", resp.TargetRegion)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(`"500"`)) {
		t.Errorf("TC-339 body has forbidden %q token; got %s", "500", rec.Body.String())
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
