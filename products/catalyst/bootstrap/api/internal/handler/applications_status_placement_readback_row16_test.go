package handler

// UAT row 16 — "change topology → Save PERSISTS".
//
// #6136 (PR #6142) fixed the WRITE half: a Topology-tab Save no longer
// scalarises `spec.placement`, so the PlacementEditor's `targets[]` reach the
// Application CR. This file covers the READ half, which was still dropping
// them — the round trip is what the row asserts, and half of it is not it.
//
// THE DEFECT. GET /applications/{name}/status projected the CR's placement
// through one line onto a `string` field:
//
//	spec.Placement = placementForTopology(readTopology(obj))
//
// `spec.placement` is dual-form by CRD design (#3373): a bare posture STRING,
// or an OBJECT additionally carrying WHERE the instance runs — `vcluster`,
// `clusters`, `regions`, and since #3969 the per-region `targets[]`. A string
// cannot carry any of that, so the object form never reached the console.
//
// WHY THAT IS THE ROW. This endpoint is the ONLY read of the DESIRED
// placement the Topology tab has. Its resolver
// (`pages/sovereign/AppDetail/TopologyTab.tsx`) walks three rungs, and rung 2
// is `if (specPlacement && typeof specPlacement === 'object') { ...targets }`,
// documented as "the #3969 desired-state the operator explicitly chose in the
// editor (used pre-rollout / when the data plane is unavailable)". Against
// this endpoint `typeof specPlacement` was ALWAYS 'string', so rung 2 — the
// one rung that renders a placement just Saved but whose Pods have not moved
// yet — could never execute, and neither could the sibling
// `spec.placement.ownedDependencies` read. The operator changed the topology,
// the object accepted it, and the tab went on rendering the pre-Save posture.
//
// ASSERTIONS ARE ON THE WIRE, and on VALUES. The response is decoded as raw
// JSON rather than into applicationStatusResponse, because the defect is a
// SHAPE on the wire: a test that read a typed `string` field would have
// compiled and passed against the very code that lost the data.

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// registerApplicationRoundTripRoutes wires the PUT the editor Saves through
// and the GET the tab reads back — the round trip in ONE router, because the
// row is about the two agreeing, not about either in isolation.
func registerApplicationRoundTripRoutes(r chi.Router, h *Handler) {
	r.Put("/api/v1/sovereigns/{id}/applications/{name}", h.HandleApplicationUpdate)
	r.Get("/api/v1/sovereigns/{id}/applications/{name}/status", h.HandleApplicationStatus)
}

// statusSpecPlacement pulls `spec.placement` off the RAW response body.
func statusSpecPlacement(t *testing.T, body []byte) any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode status body: %v; body=%s", err, string(body))
	}
	spec, ok := envelope["spec"].(map[string]any)
	if !ok {
		t.Fatalf("status response carries no `spec` object; body=%s", string(body))
	}
	return spec["placement"]
}

// TestApplicationStatus_Row16_EditorTargetsSurviveTheRoundTrip is the RED
// test. It drives the ACTUAL PlacementEditor body — targets[] only, no
// mode/regions, exactly what widgets/topology/PlacementEditor.tsx `apply()`
// sends — through the PUT, then reads the tab's own endpoint back.
//
// Before the fix it failed at the shape assertion: `spec.placement` came back
// the JSON string "active-hot-standby", so there was nowhere for `targets` to
// be.
func TestApplicationStatus_Row16_EditorTargetsSurviveTheRoundTrip(t *testing.T) {
	cr := makeAppCR("acme", "ahs-app", "1.2.3", "singleton", []string{"me-east-215-a"})

	// VACUITY CHECK — the seed must NOT already carry targets, or the test
	// would pass on a fixture that was never changed by the PUT.
	if _, ok, _ := unstructured.NestedSlice(cr.Object, "spec", "placement", "targets"); ok {
		t.Fatalf("VACUITY: fixture must seed a CR with no placement targets")
	}

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-row16-roundtrip")

	// The editor's body verbatim: targets only, ownedDependencies, no mode.
	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Cluster: "hw-me-east-215-a-rtz-prod", VCluster: "host", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Cluster: "hw-me-east-215-b-rtz-prod", VCluster: "host", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
			},
		},
	}
	put := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/ahs-app?namespace=acme", body,
		registerApplicationRoundTripRoutes)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status: got %d want 200; body=%s", put.Code, put.Body.String())
	}

	get := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/applications/ahs-app/status?namespace=acme", nil,
		registerApplicationRoundTripRoutes)
	if get.Code != http.StatusOK {
		t.Fatalf("GET /status: got %d want 200; body=%s", get.Code, get.Body.String())
	}

	placement := statusSpecPlacement(t, get.Body.Bytes())
	obj, isObject := placement.(map[string]any)
	if !isObject {
		t.Fatalf("spec.placement came back %T (%v) — the pre-fix failure. The Topology "+
			"tab's rung-2 read is `typeof specPlacement === 'object'`, so a string means "+
			"the targets the operator just Saved are unreadable", placement, placement)
	}

	// The posture is present AND canonical — one vocabulary on the wire.
	if obj["mode"] != "active-hot-standby" {
		t.Fatalf("spec.placement.mode = %v, want active-hot-standby (derived from the targets)", obj["mode"])
	}

	targets, ok := obj["targets"].([]any)
	if !ok {
		t.Fatalf("spec.placement.targets missing from the status response: %v", obj)
	}
	if len(targets) != 2 {
		t.Fatalf("spec.placement.targets has %d entries, want 2", len(targets))
	}

	// Assert on the VALUES: which region got which role is the whole content
	// of the operator's edit. A test that only counted targets would pass on a
	// projection that returned two identical primaries (#6200's shape).
	roleByRegion := map[string]string{}
	standbyTypeByRegion := map[string]string{}
	for _, raw := range targets {
		tgt, isMap := raw.(map[string]any)
		if !isMap {
			t.Fatalf("target entry is %T, want an object: %v", raw, raw)
		}
		region, _ := tgt["region"].(string)
		role, _ := tgt["role"].(string)
		roleByRegion[region] = role
		if st, present := tgt["standbyType"].(string); present {
			standbyTypeByRegion[region] = st
		}
	}
	if roleByRegion["me-east-215-a"] != string(bpv1.DataRolePrimary) {
		t.Errorf("region me-east-215-a role = %q, want Primary", roleByRegion["me-east-215-a"])
	}
	if roleByRegion["me-east-215-b"] != string(bpv1.DataRoleStandby) {
		t.Errorf("region me-east-215-b role = %q, want Standby", roleByRegion["me-east-215-b"])
	}
	if standbyTypeByRegion["me-east-215-b"] != string(bpv1.StandbyHot) {
		t.Errorf("region me-east-215-b standbyType = %q, want Hot", standbyTypeByRegion["me-east-215-b"])
	}
}

// TestApplicationStatus_Row16_Control_DifferentTopologyRoundTripsDifferently
// is the CONTROL that discriminates. It runs the SAME endpoint over the SAME
// fixture shape and changes ONLY the topology the operator picks — a
// COLD standby, which is the active-passive class rather than
// active-hot-standby.
//
// Without it the test above also passes for a projection that hardcodes the
// hot-standby answer, which is exactly how a display-only defect hides: a
// constant is indistinguishable from a correct read until a second value is
// asked for.
func TestApplicationStatus_Row16_Control_DifferentTopologyRoundTripsDifferently(t *testing.T) {
	cr := makeAppCR("acme", "ap-app", "1.2.3", "singleton", []string{"me-east-215-a"})

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-row16-control-cold")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Cluster: "hw-me-east-215-a-rtz-prod", VCluster: "host", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Cluster: "hw-me-east-215-b-rtz-prod", VCluster: "host", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyCold},
			},
		},
	}
	put := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/ap-app?namespace=acme", body,
		registerApplicationRoundTripRoutes)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status: got %d want 200; body=%s", put.Code, put.Body.String())
	}

	get := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/applications/ap-app/status?namespace=acme", nil,
		registerApplicationRoundTripRoutes)
	if get.Code != http.StatusOK {
		t.Fatalf("GET /status: got %d want 200; body=%s", get.Code, get.Body.String())
	}

	obj, isObject := statusSpecPlacement(t, get.Body.Bytes()).(map[string]any)
	if !isObject {
		t.Fatalf("control: spec.placement is not an object")
	}
	// A DIFFERENT posture must round-trip DIFFERENTLY. If this reads
	// active-hot-standby the projection is printing a constant.
	if obj["mode"] != "active-passive" {
		t.Fatalf("spec.placement.mode = %v, want active-passive — a Cold standby is the "+
			"active-passive class, and reading active-hot-standby here would mean the "+
			"projection prints a constant rather than the placement", obj["mode"])
	}
	targets, _ := obj["targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("control: targets has %d entries, want 2", len(targets))
	}
	for _, raw := range targets {
		tgt, _ := raw.(map[string]any)
		if tgt["role"] == string(bpv1.DataRoleStandby) && tgt["standbyType"] != string(bpv1.StandbyCold) {
			t.Fatalf("control: standby standbyType = %v, want Cold", tgt["standbyType"])
		}
	}
}

// TestApplicationStatus_Row16_Control_StringPlacementStaysAString is the
// SECOND control, sharing the suspect property from the other side: a CR that
// declares ONLY a posture must still report only a posture. A "fix" that
// promoted every placement to an object would hand the tab a `targets[]` the
// operator never chose — a declared intention it could not tell apart from an
// observed fact, which is #5568's defect — and would turn this red.
func TestApplicationStatus_Row16_Control_StringPlacementStaysAString(t *testing.T) {
	cr := makeAppCR("acme", "wp-str", "1.2.3", "single-region", []string{"me-east-215-a"})

	// VACUITY CHECK — the control only means something if the seed is a string.
	if _, ok, _ := unstructured.NestedMap(cr.Object, "spec", "placement"); ok {
		t.Fatalf("VACUITY: control fixture must seed a STRING spec.placement, got a map")
	}

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-row16-string-readback")

	get := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-str/status?namespace=acme", nil,
		registerApplicationRoundTripRoutes)
	if get.Code != http.StatusOK {
		t.Fatalf("GET /status: got %d want 200; body=%s", get.Code, get.Body.String())
	}

	placement := statusSpecPlacement(t, get.Body.Bytes())
	mode, isString := placement.(string)
	if !isString {
		t.Fatalf("spec.placement came back %T (%v) — a CR declaring only a posture must "+
			"report only a posture; synthesising an object would invent a desired state",
			placement, placement)
	}
	// The legacy spelling still folds to the canonical token on the wire.
	if mode != "singleton" {
		t.Fatalf("spec.placement = %q, want canonical singleton (legacy single-region folded)", mode)
	}
}
