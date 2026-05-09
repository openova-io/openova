// fleet_test.go — coverage for EPIC-6 Slice U-Fleet-1+2+3 (#1101) live
// multi-Sovereign aggregator endpoints. Mirrors the applications_test.go
// pattern: a fake dynamic client seeded with the Application + Continuum
// + Organization GVR list-kinds, an installed Deployment with a temp-file
// kubeconfig path so sovereignDynamicClient resolves, and a per-test chi
// router that registers only the endpoints under test.
//
// Coverage:
//   - /fleet/sovereigns: empty, single, multi, pagination, deterministic
//     ordering, adopted excluded
//   - /fleet/sovereigns/{id}/summary: 200 happy + 404 unknown
//   - /fleet/applications: 200 happy, filters (org/topology/drPosture),
//     DR posture matrix (none/active/alert/misconfigured), 400 invalid
//     filter
//   - DR posture derivation matrix as a pure-function test
//   - healthFromDeploymentStatus helper matrix
package handler

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// ── Test plumbing ────────────────────────────────────────────────────

// fakeFleetDynamicFactory builds a fake dynamic client seeded with all
// three GVRs the fleet handler reads (Application, Organization,
// Continuum) and returns the factory closure + the underlying tracker.
func fakeFleetDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		ApplicationGVR():  "ApplicationList",
		OrganizationGVR(): "OrganizationList",
		ContinuumGVR():    "ContinuumList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, seed...)
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}, client
}

// installFleetSovereign mirrors installUserAccessDeployment but lets the
// test set FQDN/Status/Region directly so the multi-Sov sort and health
// derivation paths are exercised deterministically.
func installFleetSovereign(t *testing.T, h *Handler, id, fqdn, status string) *Deployment {
	t.Helper()
	dep := &Deployment{
		ID:     id,
		Status: status,
		Request: provisioner.Request{
			SovereignFQDN: fqdn,
			Region:        "fsn1",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  fqdn,
			KubeconfigPath: "/dev/null", // dynamicFactory ignores contents
		},
		StartedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		mu:        sync.Mutex{},
	}
	h.deployments.Store(id, dep)
	return dep
}

func registerFleetRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/fleet/sovereigns", h.HandleFleetSovereigns)
	r.Get("/api/v1/fleet/sovereigns/{id}/summary", h.HandleFleetSovereignSummary)
	r.Get("/api/v1/fleet/applications", h.HandleFleetApplications)
}

// newAppCR composes a minimal Application unstructured for fleet-table tests.
func newAppCR(name, ns, blueprint, version, placement, phase string, regions ...string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetCreationTimestamp(metav1.NewTime(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)))
	u.SetLabels(map[string]string{
		"catalyst.openova.io/organization": ns, // org slug == namespace by convention
		"catalyst.openova.io/blueprint":    blueprint,
	})
	regAny := make([]any, 0, len(regions))
	for _, r := range regions {
		regAny = append(regAny, r)
	}
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"blueprintRef": map[string]any{"name": blueprint, "version": version},
		"placement":    placement,
		"regions":      regAny,
	}, "spec")
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	}
	return u
}

// newContinuumCR composes a minimal Continuum CR for DR-posture tests.
func newContinuumCR(name, ns, appRef, phase string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("dr.openova.io/v1")
	u.SetKind("Continuum")
	u.SetName(name)
	u.SetNamespace(ns)
	_ = unstructured.SetNestedField(u.Object, appRef, "spec", "applicationRef")
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	}
	return u
}

// newOrgCR composes a minimal Organization CR for org-count tests.
func newOrgCR(name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("orgs.openova.io/v1")
	u.SetKind("Organization")
	u.SetName(name)
	return u
}

// ── /fleet/sovereigns: empty ─────────────────────────────────────────

func TestHandleFleetSovereigns_EmptyOnFreshHandler(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetSovereignsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Sovereigns) != 0 {
		t.Fatalf("sovereigns: got %d want 0", len(resp.Sovereigns))
	}
	if resp.Total != 0 {
		t.Fatalf("total: got %d want 0", resp.Total)
	}
}

// ── /fleet/sovereigns: single + multi + ordering ─────────────────────

func TestHandleFleetSovereigns_ReturnsMultipleSorted(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	installFleetSovereign(t, h, "sov-c", "z-acme.example.com", "ready")
	installFleetSovereign(t, h, "sov-a", "a-acme.example.com", "phase1-watching")
	installFleetSovereign(t, h, "sov-b", "m-acme.example.com", "failed")

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetSovereignsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 {
		t.Fatalf("total: got %d want 3", resp.Total)
	}
	if len(resp.Sovereigns) != 3 {
		t.Fatalf("len: got %d want 3", len(resp.Sovereigns))
	}
	// FQDN-alphabetical ordering.
	if resp.Sovereigns[0].FQDN != "a-acme.example.com" {
		t.Fatalf("ordering[0]: got %q", resp.Sovereigns[0].FQDN)
	}
	if resp.Sovereigns[1].FQDN != "m-acme.example.com" {
		t.Fatalf("ordering[1]: got %q", resp.Sovereigns[1].FQDN)
	}
	if resp.Sovereigns[2].FQDN != "z-acme.example.com" {
		t.Fatalf("ordering[2]: got %q", resp.Sovereigns[2].FQDN)
	}
	// Health vocabulary derivation.
	if resp.Sovereigns[0].Health != healthYellow {
		t.Fatalf("health[0]: got %q want yellow", resp.Sovereigns[0].Health)
	}
	if resp.Sovereigns[1].Health != healthRed {
		t.Fatalf("health[1]: got %q want red", resp.Sovereigns[1].Health)
	}
	if resp.Sovereigns[2].Health != healthGreen {
		t.Fatalf("health[2]: got %q want green", resp.Sovereigns[2].Health)
	}
	// Sovereign IDs preserved.
	if resp.Sovereigns[0].ID != "sov-a" {
		t.Fatalf("id[0]: got %q", resp.Sovereigns[0].ID)
	}
}

// ── /fleet/sovereigns: pagination ────────────────────────────────────

func TestHandleFleetSovereigns_Pagination(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	for i := 0; i < 7; i++ {
		id := "sov-" + string(rune('a'+i))
		installFleetSovereign(t, h, id, id+".example.com", "ready")
	}

	// Page 1, pageSize 3 → first 3.
	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns?page=1&pageSize=3", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("p1 status: got %d", rec.Code)
	}
	var p1 fleetSovereignsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &p1)
	if p1.Total != 7 || len(p1.Sovereigns) != 3 || p1.Page != 1 || p1.PageSize != 3 {
		t.Fatalf("p1: total=%d len=%d page=%d size=%d", p1.Total, len(p1.Sovereigns), p1.Page, p1.PageSize)
	}
	if p1.Sovereigns[0].ID != "sov-a" {
		t.Fatalf("p1[0]: %q", p1.Sovereigns[0].ID)
	}

	// Page 3, pageSize 3 → last 1.
	rec = callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns?page=3&pageSize=3", nil, registerFleetRoutes)
	var p3 fleetSovereignsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &p3)
	if len(p3.Sovereigns) != 1 || p3.Sovereigns[0].ID != "sov-g" {
		t.Fatalf("p3: %+v", p3.Sovereigns)
	}

	// Page out of range returns empty + total preserved.
	rec = callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns?page=99&pageSize=3", nil, registerFleetRoutes)
	var pOut fleetSovereignsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &pOut)
	if len(pOut.Sovereigns) != 0 || pOut.Total != 7 {
		t.Fatalf("pOut: %+v", pOut)
	}

	// pageSize > max caps to 50.
	rec = callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns?pageSize=999", nil, registerFleetRoutes)
	var pCap fleetSovereignsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &pCap)
	if pCap.PageSize != fleetMaxPageSize {
		t.Fatalf("cap: got %d want %d", pCap.PageSize, fleetMaxPageSize)
	}
}

// ── /fleet/sovereigns: adopted excluded ──────────────────────────────

func TestHandleFleetSovereigns_AdoptedExcluded(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	installFleetSovereign(t, h, "sov-live", "live.example.com", "ready")
	adopted := installFleetSovereign(t, h, "sov-handed", "handed.example.com", "adopted")
	t0 := time.Now().UTC()
	adopted.AdoptedAt = &t0

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns", nil, registerFleetRoutes)
	var resp fleetSovereignsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Sovereigns[0].ID != "sov-live" {
		t.Fatalf("expected only sov-live; got %+v", resp.Sovereigns)
	}
}

// ── /fleet/sovereigns/{id}/summary: 200 happy ────────────────────────

func TestHandleFleetSovereignSummary_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installFleetSovereign(t, h, "sov-1", "acme.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory(
		newOrgCR("acme"),
		newOrgCR("widget"),
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
		newAppCR("api", "acme", "bp-django", "0.9", topologyActiveActive, "Failed", "hz-fsn-rtz-prod", "hz-hel-rtz-prod"),
		newAppCR("etl", "widget", "bp-airflow", "2.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns/"+dep.ID+"/summary", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetSovereignDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Sovereign.ID != "sov-1" {
		t.Fatalf("sovereign id: %q", resp.Sovereign.ID)
	}
	if resp.Orgs != 2 {
		t.Fatalf("orgs: got %d want 2", resp.Orgs)
	}
	if resp.Applications.Total != 3 {
		t.Fatalf("apps total: got %d want 3", resp.Applications.Total)
	}
	if resp.Applications.Active != 2 {
		t.Fatalf("apps active: got %d want 2", resp.Applications.Active)
	}
	if resp.Applications.Failing != 1 {
		t.Fatalf("apps failing: got %d want 1", resp.Applications.Failing)
	}
	// Regions union, sorted.
	if len(resp.Regions) != 2 ||
		resp.Regions[0] != "hz-fsn-rtz-prod" ||
		resp.Regions[1] != "hz-hel-rtz-prod" {
		t.Fatalf("regions: %+v", resp.Regions)
	}
	if resp.Alerts != 0 {
		t.Fatalf("alerts placeholder: got %d want 0", resp.Alerts)
	}
}

// TestHandleFleetSovereignSummary_AlertsFromCompliance — slice Z2.
//
// summarizeSovereign() must populate `alerts` from the EPIC-1 score
// aggregator's per-cluster violation count. This test seeds 2 failing
// (resource, policy) pairs into a minimal ComplianceHandler and
// asserts the /summary endpoint surfaces alerts=2.
func TestHandleFleetSovereignSummary_AlertsFromCompliance(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installFleetSovereign(t, h, "sov-z2", "z2.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
	)
	h.dynamicFactory = factory

	// Build a ComplianceHandler directly (no goroutine needed — we
	// seed state under the lock and don't subscribe to anything).
	c := &ComplianceHandler{
		state:       map[string]map[string]*resourceState{},
		labels:      map[string]map[string]map[string]string{},
		policySrc:   map[string]string{},
		subscribers: map[int64]*complianceSubscriber{},
		stop:        make(chan struct{}),
	}
	now := time.Now()
	c.state[dep.ID] = map[string]*resourceState{
		"deployment/ns1/web": {
			resource: "deployment/ns1/web", namespace: "ns1", application: "web",
			results: map[string]policyVerdict{
				"probes-present": {result: "fail", at: now},
				"flux-managed":   {result: "fail", at: now},
				"hpa-effective":  {result: "pass", at: now}, // not an alert
			},
		},
	}
	h.SetComplianceHandler(c)

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns/"+dep.ID+"/summary", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetSovereignDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Alerts != 2 {
		t.Fatalf("alerts: got %d want 2 (only fail verdicts count)", resp.Alerts)
	}
}

// TestHandleFleetSovereignSummary_AlertsZeroWhenComplianceNil — guards
// the nil-tolerant path: a catalyst-api Pod with the compliance
// aggregator unwired keeps the dashboard green at 0 alerts.
func TestHandleFleetSovereignSummary_AlertsZeroWhenComplianceNil(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installFleetSovereign(t, h, "sov-z2-nil", "z2nil.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory()
	h.dynamicFactory = factory
	// h.compliance left as nil.

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns/"+dep.ID+"/summary", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetSovereignDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Alerts != 0 {
		t.Fatalf("alerts: got %d want 0 (compliance not wired)", resp.Alerts)
	}
}

// ── /fleet/sovereigns/{id}/summary: 404 unknown ──────────────────────

func TestHandleFleetSovereignSummary_404OnUnknown(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/sovereigns/nope/summary", nil, registerFleetRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

// ── /fleet/applications: aggregated table ────────────────────────────

func TestHandleFleetApplications_AggregatesAcrossSovereigns(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	installFleetSovereign(t, h, "sov-a", "a.example.com", "ready")
	installFleetSovereign(t, h, "sov-b", "b.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
		newAppCR("api", "acme", "bp-django", "0.9", topologyActiveHotStandby, "Ready", "hz-fsn-rtz-prod", "hz-hel-rtz-prod"),
		newContinuumCR("api-cont", "acme", "api", "Healthy"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/applications", nil, registerFleetRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp fleetApplicationsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 2 apps × 2 sovereigns = 4 rows (the fake client serves the same
	// list to every kubeconfig — fine for shape verification).
	if resp.Total != 4 {
		t.Fatalf("total: got %d want 4", resp.Total)
	}
	// Find the active-hotstandby row and verify DR posture is "DR active".
	foundDR := false
	foundNone := false
	for _, row := range resp.Applications {
		if row.App.Name == "api" && row.DRPosture == drPostureActive {
			foundDR = true
		}
		if row.App.Name == "wp" && row.DRPosture == drPostureNone {
			foundNone = true
		}
	}
	if !foundDR {
		t.Fatalf("expected DR active row for api; got %+v", resp.Applications)
	}
	if !foundNone {
		t.Fatalf("expected — DR posture for wp; got %+v", resp.Applications)
	}
}

// ── /fleet/applications: filters ─────────────────────────────────────

func TestHandleFleetApplications_OrgFilter(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	installFleetSovereign(t, h, "sov-1", "s.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
		newAppCR("etl", "widget", "bp-airflow", "2.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet, "/api/v1/fleet/applications?org=acme", nil, registerFleetRoutes)
	var resp fleetApplicationsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Applications[0].App.Name != "wp" {
		t.Fatalf("org filter: got %+v", resp.Applications)
	}
}

func TestHandleFleetApplications_TopologyFilter(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	installFleetSovereign(t, h, "sov-1", "s.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
		newAppCR("api", "acme", "bp-django", "0.9", topologyActiveHotStandby, "Ready", "hz-fsn-rtz-prod"),
		newContinuumCR("api-cont", "acme", "api", "Healthy"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/fleet/applications?topology=active-hotstandby", nil, registerFleetRoutes)
	var resp fleetApplicationsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Applications[0].App.Name != "api" {
		t.Fatalf("topology filter: %+v", resp.Applications)
	}
}

func TestHandleFleetApplications_DRPostureFilter(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	installFleetSovereign(t, h, "sov-1", "s.example.com", "ready")
	factory, _ := fakeFleetDynamicFactory(
		newAppCR("wp", "acme", "bp-wordpress", "1.0", topologySingleRegion, "Ready", "hz-fsn-rtz-prod"),
		newAppCR("api", "acme", "bp-django", "0.9", topologyActiveHotStandby, "Ready", "hz-fsn-rtz-prod"),
		newContinuumCR("api-cont", "acme", "api", "Healthy"),
		// app "broken" has active-hotstandby but no Continuum CR → Misconfigured.
		newAppCR("broken", "acme", "bp-broken", "0.1", topologyActiveHotStandby, "Ready", "hz-fsn-rtz-prod"),
	)
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/fleet/applications?drPosture=Misconfigured", nil, registerFleetRoutes)
	var resp fleetApplicationsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Applications[0].App.Name != "broken" {
		t.Fatalf("dr posture filter: %+v", resp.Applications)
	}
}

func TestHandleFleetApplications_400OnInvalidTopology(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/fleet/applications?topology=random-thing", nil, registerFleetRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

func TestHandleFleetApplications_400OnInvalidDRPosture(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/fleet/applications?drPosture=BadValue", nil, registerFleetRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

// ── DR posture derivation matrix ─────────────────────────────────────

func TestDeriveDRPosture(t *testing.T) {
	cases := []struct {
		name         string
		topology     string
		hasContinuum bool
		phase        string
		want         string
	}{
		{"single-region no-continuum", topologySingleRegion, false, "", drPostureNone},
		{"active-active no-continuum", topologyActiveActive, false, "", drPostureNone},
		{"active-hotstandby no-continuum", topologyActiveHotStandby, false, "", drPostureMisconfigured},
		{"active-hotstandby with continuum healthy", topologyActiveHotStandby, true, "Healthy", drPostureActive},
		{"active-hotstandby with continuum failed", topologyActiveHotStandby, true, "Failed", drPostureAlert},
		{"single-region with continuum (defensive)", topologySingleRegion, true, "Healthy", drPostureActive},
		{"failed phase case-insensitive", topologyActiveHotStandby, true, "failed", drPostureAlert},
	}
	for _, tc := range cases {
		got := deriveDRPosture(tc.topology, tc.hasContinuum, tc.phase)
		if got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

// ── Health derivation matrix ─────────────────────────────────────────

func TestHealthFromDeploymentStatus(t *testing.T) {
	cases := map[string]string{
		"ready":              healthGreen,
		"phase1-watching":    healthYellow,
		"flux-bootstrapping": healthYellow,
		"tofu-applying":      healthYellow,
		"provisioning":       healthYellow,
		"pending":            healthYellow,
		"failed":             healthRed,
		"error":              healthRed,
		"":                   healthUnknown,
		"WHO-KNOWS":          healthYellow, // unknown branch → yellow (still trying)
	}
	for in, want := range cases {
		if got := healthFromDeploymentStatus(in); got != want {
			t.Errorf("status=%q: got %q want %q", in, got, want)
		}
	}
}

// ── Pagination helper ────────────────────────────────────────────────

func TestFleetParsePagination_Defaults(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	page, size := fleetParsePagination(r)
	if page != 1 || size != fleetDefaultPageSize {
		t.Fatalf("defaults: page=%d size=%d", page, size)
	}
}
