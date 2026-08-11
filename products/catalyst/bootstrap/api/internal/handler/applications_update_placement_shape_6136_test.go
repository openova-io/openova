package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// applications_update_placement_shape_6136_test.go — #6136 (UAT row 16).
//
// MEASURED on hw293.omantel.biz (dep a0077ba47e3720e5): the AppDetail Topology
// tab's Save issued PUT → 200, metadata.generation bumped, and spec.placement
// landed as a BARE STRING with spec.parameters.topology still `singleton`. The
// control on the same Sovereign, `uat50-ahs-pg`, holds spec.placement as an
// OBJECT — the CRD admits both forms, so the scalar was the producer's doing.
//
// MECHANISM, at the line. HandleApplicationUpdate wrote ONE field of the
// request and discarded the rest:
//
//	SetNestedField(patched.Object, canonicalizeTopology(body.Placement.Mode), "spec", "placement")
//
// so targets[] (the entire #3969 model the PlacementEditor submits), vcluster
// and clusters (#3373) never reached the CR, and an Application already holding
// the object form had that whole node replaced by a string.
//
// WHY IT SURVIVED. Every reader that RENDERS the posture is shape-tolerant —
// placementFromSpec (#5422), readTopology, the console's topologyLabel (#4897).
// So the console showed the right mode after a downgrading Save and the loss
// was invisible from the surface that caused it. TestPlacementFromSpec_
// ToleratesBothShapes_6136 below pins that tolerance deliberately, because the
// tolerance is load-bearing for old CRs and must not be "fixed" away.

// objectFormAppCR builds an Application CR whose spec.placement is the #3373
// OBJECT form — the `uat50-ahs-pg` shape, carrying WHERE fields that no PUT
// body restates. Deliberately NOT built via makeAppCR, whose placement is a
// bare string.
func objectFormAppCR(ns, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("apps.openova.io/v1")
	obj.SetKind("Application")
	obj.SetName(name)
	obj.SetNamespace(ns)
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"environmentRef": "acme-prod",
		"blueprintRef": map[string]any{
			"name":    "bp-wordpress",
			"version": "1.2.3",
		},
		"placement": map[string]any{
			"mode":     "active-hot-standby",
			"vcluster": "host",
			"clusters": []any{"mgmt-A", "mgmt-B"},
			"regions":  []any{"me-east-215-a", "me-east-215-b"},
		},
		"regions": []any{"me-east-215-a", "me-east-215-b"},
		"parameters": map[string]any{
			"domain": "shop.acme.example",
		},
	}, "spec")
	return obj
}

// ── The producer, unit level ─────────────────────────────────────────

// TestPlacementValueForUpdate_PreservesShapeAndUnrestatedFields_6136 pins the
// producer directly. Each case names the CR shape going in, the body, and the
// value that must come out.
func TestPlacementValueForUpdate_PreservesShapeAndUnrestatedFields_6136(t *testing.T) {
	t.Parallel()

	objectCR := map[string]interface{}{
		"mode":     "active-hot-standby",
		"vcluster": "host",
		"clusters": []interface{}{"mgmt-A", "mgmt-B"},
	}

	t.Run("object-form CR is NOT downgraded to a scalar", func(t *testing.T) {
		// The Topology-tab Save: a mode + regions, no WHERE fields restated.
		got := placementValueForUpdate(objectCR, applicationPlacement{
			Mode:    "active-active",
			Regions: []string{"me-east-215-a", "me-east-215-b"},
		})
		obj, ok := got.(map[string]interface{})
		if !ok {
			t.Fatalf("placement collapsed to %T (%v) — a Save replaced the object form with a "+
				"scalar and dropped vcluster/clusters the body never mentioned (#6136)", got, got)
		}
		if obj["mode"] != "active-active" {
			t.Errorf("mode = %v, want active-active", obj["mode"])
		}
		// The load-bearing half: fields the body did not restate SURVIVE.
		if obj["vcluster"] != "host" {
			t.Errorf("vcluster = %v, want host — an untouched field was deleted by an unrelated edit", obj["vcluster"])
		}
		if !reflect.DeepEqual(obj["clusters"], []interface{}{"mgmt-A", "mgmt-B"}) {
			t.Errorf("clusters = %v, want [mgmt-A mgmt-B] — an untouched field was deleted", obj["clusters"])
		}
	})

	t.Run("targets[] from the editor are persisted as an object", func(t *testing.T) {
		got := placementValueForUpdate(nil, applicationPlacement{
			Mode:    "active-hot-standby",
			Regions: []string{"me-east-215-a", "me-east-215-b"},
			Targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Cluster: "mgmt-A", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Cluster: "mgmt-B", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
			},
		})
		obj, ok := got.(map[string]interface{})
		if !ok {
			t.Fatalf("placement = %T (%v), want the object form — a body carrying targets[] cannot be "+
				"stored as a bare string, so the editor's whole model is discarded (#6136)", got, got)
		}
		targets, ok := obj["targets"].([]interface{})
		if !ok || len(targets) != 2 {
			t.Fatalf("targets = %v, want 2 entries", obj["targets"])
		}
		primary, _ := targets[0].(map[string]interface{})
		if primary["region"] != "me-east-215-a" || primary["cluster"] != "mgmt-A" || primary["role"] != "Primary" {
			t.Errorf("targets[0] = %v, want region/cluster/role preserved verbatim", primary)
		}
		// The CRD's admission CEL FORBIDS standbyType on a Primary — emitting
		// an empty one would have the apiserver reject the whole update.
		if _, present := primary["standbyType"]; present {
			t.Errorf("targets[0] carries standbyType=%v on a Primary — the CRD's CEL forbids it", primary["standbyType"])
		}
		standby, _ := targets[1].(map[string]interface{})
		if standby["role"] != "Standby" || standby["standbyType"] != "Hot" {
			t.Errorf("targets[1] = %v, want role=Standby standbyType=Hot", standby)
		}
	})

	// CONTROL — shares the suspect property (it goes through the same producer,
	// same canonicalisation) and must stay on the legacy wire byte-for-byte.
	t.Run("CONTROL: legacy mode-only body over a string-form CR still stores a bare string", func(t *testing.T) {
		got := placementValueForUpdate("singleton", applicationPlacement{
			Mode:    "active-active",
			Regions: []string{"me-east-215-a"},
		})
		s, ok := got.(string)
		if !ok {
			t.Fatalf("placement = %T (%v), want the bare string — the legacy caller's wire must not "+
				"change shape underneath it", got, got)
		}
		if s != "active-active" {
			t.Errorf("placement = %q, want active-active", s)
		}
	})

	// CONTROL — a nil current placement with a mode-only body is the fresh
	// legacy case and must also stay scalar.
	t.Run("CONTROL: absent current placement + mode-only body stays a bare string", func(t *testing.T) {
		got := placementValueForUpdate(nil, applicationPlacement{Mode: "single-region"})
		s, ok := got.(string)
		if !ok {
			t.Fatalf("placement = %T, want string", got)
		}
		// One vocabulary (#3375 DoD-1): the legacy dialect still folds.
		if s != "singleton" {
			t.Errorf("placement = %q, want singleton (canonicalised from single-region)", s)
		}
	})
}

// TestPlacementValueForUpdate_VacuityCheck_6136 proves the guards above can
// FAIL. It feeds the producer the two inputs whose outputs the fix changed and
// asserts the results DIFFER in shape from each other — a stubbed producer that
// always returned a string, or always returned an object, collapses this
// distinction and the test goes red.
func TestPlacementValueForUpdate_VacuityCheck_6136(t *testing.T) {
	t.Parallel()

	scalar := placementValueForUpdate(nil, applicationPlacement{Mode: "singleton"})
	object := placementValueForUpdate(nil, applicationPlacement{
		Mode:    "singleton",
		Targets: []bpv1.PlacementTarget{{Region: "me-east-215-a", Cluster: "mgmt-A", Role: bpv1.DataRolePrimary}},
	})

	if _, ok := scalar.(string); !ok {
		t.Fatalf("the mode-only case produced %T — this suite would pass vacuously on a "+
			"producer that emits objects unconditionally", scalar)
	}
	if _, ok := object.(map[string]interface{}); !ok {
		t.Fatalf("the targets case produced %T — this suite would pass vacuously on a "+
			"producer that emits strings unconditionally", object)
	}
	if reflect.TypeOf(scalar) == reflect.TypeOf(object) {
		t.Fatal("both inputs produced the same type — the producer is not resolving the dual form at all")
	}
}

// TestPlacementFromSpec_ToleratesBothShapes_6136 pins the CONSUMER TOLERANCE
// that hid this defect, so it is documented rather than accidental. Every
// posture reader falls back from the string read to `.mode`; that is what let a
// downgrading Save look correct in the console. The tolerance must REMAIN — old
// CRs genuinely hold the string form — which is exactly why the producer, not
// the reader, is the thing that had to change.
func TestPlacementFromSpec_ToleratesBothShapes_6136(t *testing.T) {
	t.Parallel()
	mk := func(v any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"placement": v},
		}}
	}
	if got := placementFromSpec(mk("active-hot-standby")); got != "active-hot-standby" {
		t.Errorf("string form resolved to %q, want active-hot-standby", got)
	}
	if got := placementFromSpec(mk(map[string]interface{}{"mode": "active-hot-standby"})); got != "active-hot-standby" {
		t.Errorf("object form resolved to %q, want active-hot-standby", got)
	}
	// Both shapes read the SAME posture — which is the property that made the
	// shape change undetectable from any rendering surface.
	if placementFromSpec(mk("active-active")) != placementFromSpec(mk(map[string]interface{}{"mode": "active-active"})) {
		t.Error("the two shapes no longer read alike — old CRs would start rendering differently")
	}
}

// ── The producer, through the real PUT handler ───────────────────────

// TestHandleApplicationUpdate_ObjectFormSurvivesTheSave_6136 drives the whole
// handler against the `uat50-ahs-pg` shape. Pre-fix, spec.placement came back
// as a bare string and vcluster/clusters were gone.
func TestHandleApplicationUpdate_ObjectFormSurvivesTheSave_6136(t *testing.T) {
	cr := objectFormAppCR("acme", "uat50-ahs-pg")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-6136-objform")

	// A scale-UP (active-hot-standby → active-active, same 2 regions) so the
	// destructive-transition gate is not what is under test here.
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/uat50-ahs-pg?namespace=acme",
		json.RawMessage(`{"placement":{"mode":"active-active","regions":["me-east-215-a","me-east-215-b"]}}`),
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "uat50-ahs-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}

	if s, isString, _ := unstructured.NestedString(got.Object, "spec", "placement"); isString {
		t.Fatalf("spec.placement came back as the bare string %q — the Save DOWNGRADED an "+
			"object-form placement and deleted the vcluster/clusters the body never restated "+
			"(#6136, control uat50-ahs-pg on hw293)", s)
	}
	if got, _, _ := unstructured.NestedString(got.Object, "spec", "placement", "mode"); got != "active-active" {
		t.Errorf("spec.placement.mode = %q, want active-active", got)
	}
	if v, found, _ := unstructured.NestedString(got.Object, "spec", "placement", "vcluster"); !found || v != "host" {
		t.Errorf("spec.placement.vcluster = %q (found=%v), want host — an untouched field was deleted", v, found)
	}
	clusters, found, _ := unstructured.NestedStringSlice(got.Object, "spec", "placement", "clusters")
	if !found || !reflect.DeepEqual(clusters, []string{"mgmt-A", "mgmt-B"}) {
		t.Errorf("spec.placement.clusters = %v (found=%v), want [mgmt-A mgmt-B] — an untouched field was deleted", clusters, found)
	}

	// The response must also REPORT the posture. Pre-fix the raw NestedString
	// read returned ok=false on the object form and omitempty dropped the key,
	// so the console was handed nothing and defaulted.
	var resp applicationUpdateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Placement != "active-active" {
		t.Errorf("response placement = %q, want active-active — an object-form CR must not answer "+
			"with an empty posture the console then has to guess at", resp.Placement)
	}
}

// TestHandleApplicationUpdate_ObjectFormStillGuardsDestructiveTransitions_6136
// is the NEW CONSTRAINT the shape fix makes reachable. The `?force=true`
// confirmation on a scale-down was blind against object-form CRs, because the
// gate read the current posture with a raw NestedString that returns "" there.
func TestHandleApplicationUpdate_ObjectFormStillGuardsDestructiveTransitions_6136(t *testing.T) {
	cr := objectFormAppCR("acme", "uat50-ahs-pg")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-6136-guard")

	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/uat50-ahs-pg?namespace=acme",
		json.RawMessage(`{"placement":{"mode":"singleton","regions":["me-east-215-a","me-east-215-b"]}}`),
		registerApplicationUpdateRoutes)

	if rec.Code == http.StatusOK {
		t.Fatalf("active-hot-standby → singleton was accepted WITHOUT ?force=true on an object-form CR: "+
			"the gate read the current posture as \"\" and could not see the standby drop (#6136); body=%s",
			rec.Body.String())
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: want 409 got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "placement-transition-blocked" {
		t.Errorf("error = %v, want placement-transition-blocked", body["error"])
	}
	// Assert on the message VALUE: it must name the posture actually being left,
	// which is the fact the empty read destroyed.
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "active-hot-standby") {
		t.Errorf("409 detail %q does not name the current posture — the gate would be firing "+
			"without knowing what it is protecting", detail)
	}
}

// TestHandleApplicationUpdate_StringFormCallerIsUnchanged_6136 is the CONTROL
// at handler level: the legacy string-form CR with a legacy mode-only body must
// behave exactly as before, INCLUDING keeping its scalar shape. It shares the
// suspect property — it is the same handler, the same producer, the same
// canonicalisation — and stays green in both states of the fix.
func TestHandleApplicationUpdate_StringFormCallerIsUnchanged_6136(t *testing.T) {
	cr := makeAppCR("acme", "wp-prod", "1.2.3", "singleton", []string{"me-east-215-a"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-6136-legacy")

	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme",
		json.RawMessage(`{"placement":{"mode":"active-active","regions":["me-east-215-a","me-east-215-b"]}}`),
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "wp-prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}
	s, isString, _ := unstructured.NestedString(got.Object, "spec", "placement")
	if !isString {
		t.Fatalf("a legacy string-form CR edited by a legacy mode-only body changed shape — "+
			"the fix must not migrate CRs nobody asked it to migrate; got %v",
			got.Object["spec"].(map[string]interface{})["placement"])
	}
	if s != "active-active" {
		t.Errorf("spec.placement = %q, want active-active", s)
	}
}

// TestHandleApplicationUpdate_EditorTargetsReachTheCR_6136 drives the EXACT
// console PlacementEditor body (the #4950 wire-compat fixture) and asserts the
// per-target model reaches the CR. Pre-fix `spec.placement.targets` was written
// by nothing at all, while TopologyTab reads it as rung 2 of its chain.
func TestHandleApplicationUpdate_EditorTargetsReachTheCR_6136(t *testing.T) {
	instances.SetAvailableVClusterTiers("mgmt")
	t.Cleanup(func() { instances.SetAvailableVClusterTiers("") })

	cr := makeAppCR("acme", "shared-pg", "1.2.3", "singleton", []string{"me-east-215-a"})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-6136-targets")

	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/shared-pg?namespace=acme",
		json.RawMessage(realConsolePlacementPUT), registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "shared-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}
	targets, found, _ := unstructured.NestedSlice(got.Object, "spec", "placement", "targets")
	if !found {
		t.Fatalf("spec.placement.targets absent after the editor's own Apply — the per-region model "+
			"the User selected did not survive a Save that answered 200 (#6136)")
	}
	if len(targets) != 2 {
		t.Fatalf("spec.placement.targets = %v, want the 2 submitted targets", targets)
	}
	// Assert on VALUES: role and standbyType are the fields the editor exists
	// to set, and a targets list that lost them is as useless as no list.
	primary, _ := targets[0].(map[string]interface{})
	if primary["role"] != "Primary" || primary["region"] != "me-east-215-a" || primary["vcluster"] != "mgmt" {
		t.Errorf("targets[0] = %v, want the Primary on me-east-215-a/mgmt", primary)
	}
	standby, _ := targets[1].(map[string]interface{})
	if standby["role"] != "Standby" || standby["standbyType"] != "Hot" || standby["region"] != "me-east-215-b" {
		t.Errorf("targets[1] = %v, want the Hot Standby on me-east-215-b", standby)
	}
	// The posture still reads correctly through the tolerant seam.
	if p := placementFromSpec(got); p != "active-hot-standby" {
		t.Errorf("posture = %q, want active-hot-standby", p)
	}
}

// ── parameters.topology lockstep ─────────────────────────────────────

// postgresAppCR builds a bp-postgres Application in the measured hw293 shape:
// a singleton placement whose `parameters.topology.mode` is the value the chart
// actually renders from.
func postgresAppCR(ns, name string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("apps.openova.io/v1")
	obj.SetKind("Application")
	obj.SetName(name)
	obj.SetNamespace(ns)
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"environmentRef": "acme-prod",
		"blueprintRef":   map[string]any{"name": "bp-postgres", "version": "1.0.0"},
		"placement":      "singleton",
		"regions":        []any{"me-east-215-a"},
		"parameters": map[string]any{
			"topology": map[string]any{"mode": "singleton"},
		},
	}, "spec")
	return obj
}

// TestHandleApplicationUpdate_ParametersFollowThePlacement_6136 is the
// handler-level half of the parameters lockstep. The unit test below pins the
// helper; this pins the CALL SITE, so removing the wiring cannot leave a green
// suite behind.
func TestHandleApplicationUpdate_ParametersFollowThePlacement_6136(t *testing.T) {
	cr := postgresAppCR("acme", "uat50-ahs-pg")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-6136-params")

	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/uat50-ahs-pg?namespace=acme",
		json.RawMessage(`{"placement":{"mode":"active-hot-standby","regions":["me-east-215-a","me-east-215-b"]}}`),
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "uat50-ahs-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}
	mode, found, _ := unstructured.NestedString(got.Object, "spec", "parameters", "topology", "mode")
	if !found || mode != "active-hot-standby" {
		t.Fatalf("spec.parameters.topology.mode = %q (found=%v) after a Save that set "+
			"spec.placement=active-hot-standby — the CR contradicts itself and the HelmRelease "+
			"keeps rendering the singleton, so the Save is a no-op at the layer that matters "+
			"(#6136, measured on hw293)", mode, found)
	}
	if p := placementFromSpec(got); p != "active-hot-standby" {
		t.Errorf("spec.placement posture = %q, want active-hot-standby", p)
	}
}

// TestHandleApplicationUpdate_NonPostgresParametersUntouched_6136 is the
// handler-level CONTROL. Same door, same placement change, same
// `parameters.topology` key present — only the blueprint differs. It must come
// back byte-identical, so the repoint above is a bp-postgres-scoped derivation
// rather than a handler that rewrites any tree it finds.
func TestHandleApplicationUpdate_NonPostgresParametersUntouched_6136(t *testing.T) {
	cr := postgresAppCR("acme", "wp-prod")
	_ = unstructured.SetNestedMap(cr.Object,
		map[string]any{"name": "bp-wordpress", "version": "1.2.3"}, "spec", "blueprintRef")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-app-6136-params-control")

	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-prod?namespace=acme",
		json.RawMessage(`{"placement":{"mode":"active-hot-standby","regions":["me-east-215-a","me-east-215-b"]}}`),
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: want 200 got %d body=%s", rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "wp-prod", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}
	mode, _, _ := unstructured.NestedString(got.Object, "spec", "parameters", "topology", "mode")
	if mode != "singleton" {
		t.Errorf("a bp-wordpress Application had parameters.topology.mode rewritten to %q — the "+
			"[singleton, active-hot-standby] enum being folded to is bp-postgres' own configSchema "+
			"and means nothing for another blueprint", mode)
	}
}

// TestRepointPostgresTopologyMode_6136 pins the second half of the measured
// row: `spec.parameters.topology` stayed `singleton` while spec.placement said
// otherwise, so the HelmRelease kept rendering a singleton and the Save was a
// no-op at the layer that matters.
func TestRepointPostgresTopologyMode_6136(t *testing.T) {
	t.Parallel()

	t.Run("singleton parameters follow a placement move to HA", func(t *testing.T) {
		params := map[string]interface{}{
			"topology": map[string]interface{}{"mode": "singleton"},
		}
		out, changed := repointPostgresTopologyMode(params, "bp-postgres", "active-hot-standby")
		if !changed {
			t.Fatalf("parameters left at %v while spec.placement declares active-hot-standby — "+
				"the CR contradicts itself and the chart renders the stale half (#6136)", params)
		}
		topo, _ := out["topology"].(map[string]interface{})
		if topo["mode"] != "active-hot-standby" {
			t.Errorf("topology.mode = %v, want active-hot-standby", topo["mode"])
		}
		// The input map must not be mutated in place — the handler decides
		// whether to persist based on the returned flag.
		if orig, _ := params["topology"].(map[string]interface{}); orig["mode"] != "singleton" {
			t.Errorf("the input parameters were mutated in place (topology.mode=%v)", orig["mode"])
		}
	})

	t.Run("broad placement tokens fold to the configSchema enum", func(t *testing.T) {
		for _, mode := range []string{"active-active", "active-passive"} {
			params := map[string]interface{}{"topology": map[string]interface{}{"mode": "singleton"}}
			out, changed := repointPostgresTopologyMode(params, "bp-postgres", mode)
			if !changed {
				t.Fatalf("%s did not repoint the parameters", mode)
			}
			topo, _ := out["topology"].(map[string]interface{})
			// bp-postgres' configSchema enum is the NARROW data-plane set; a
			// broad placement token written through verbatim would fail the
			// application-controller's configSchema validation outright.
			if topo["mode"] != "active-hot-standby" {
				t.Errorf("%s → topology.mode = %v, want active-hot-standby (the only HA mode the "+
					"bp-postgres configSchema enum admits)", mode, topo["mode"])
			}
		}
	})

	// CONTROLS — each shares the suspect property (a placement change reaching
	// the same helper) and must produce NO change.
	t.Run("CONTROL: a non-postgres blueprint is untouched", func(t *testing.T) {
		params := map[string]interface{}{"topology": map[string]interface{}{"mode": "singleton"}}
		if _, changed := repointPostgresTopologyMode(params, "bp-wordpress", "active-hot-standby"); changed {
			t.Error("a non-postgres blueprint had its topology repointed — the enum being folded to "+
				"is bp-postgres' own and means nothing elsewhere")
		}
	})

	t.Run("CONTROL: an undeclared topology is not newly declared", func(t *testing.T) {
		if _, changed := repointPostgresTopologyMode(map[string]interface{}{}, "bp-postgres", "active-hot-standby"); changed {
			t.Error("a parameters tree with NO topology got one — #4283's rule is that we never start " +
				"declaring a mode where none was declared, which would silently promote a " +
				"backing-service postgres to the cross-region pair shape")
		}
		params := map[string]interface{}{"topology": map[string]interface{}{"crossRegion": false}}
		if _, changed := repointPostgresTopologyMode(params, "bp-postgres", "active-hot-standby"); changed {
			t.Error("a topology object with no `mode` string got one")
		}
	})

	t.Run("CONTROL: an already-agreeing mode is left alone", func(t *testing.T) {
		params := map[string]interface{}{"topology": map[string]interface{}{"mode": "active-hot-standby"}}
		if _, changed := repointPostgresTopologyMode(params, "bp-postgres", "active-active"); changed {
			t.Error("an already-HA topology was rewritten — both tokens fold to active-hot-standby, " +
				"so there was nothing to change and the CR should not churn")
		}
	})
}
