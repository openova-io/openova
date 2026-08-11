package handler

// UAT row 16 — a topology Save must not SCALARIZE an object `spec.placement`.
//
// THE DEFECT. `spec.placement` is dual-form by design (#3373,
// `products/catalyst/chart/crds/application.yaml:121-159`,
// `x-kubernetes-preserve-unknown-fields: true` and deliberately no `type:`):
// the STRING form carries the DR posture on its own, the OBJECT form
// `{mode, vcluster, regions, clusters}` additionally carries WHERE the
// instance runs. The CREATE door honours both (`applications.go:1116-1136`).
// The UPDATE door did not: it called
//
//	unstructured.SetNestedField(patched.Object, canonicalizeTopology(...), "spec", "placement")
//
// unconditionally, and `SetNestedField` with a scalar REPLACES an existing map
// wholesale. So a PUT that only meant to change the posture silently erased
// `vcluster` and `clusters` — and the Application controller reads exactly
// those to pick the Flux `kubeConfig.secretRef` vCluster pivot
// (`core/controllers/application/internal/controller/application_controller.go:3423-3452`).
// The write returned HTTP 200 and bumped `metadata.generation`, so every signal
// the console reads reported success while the model lost the placement.
//
// WHY NO EXISTING TEST CAUGHT IT. Every placement SHAPE assertion in this
// package covers a CREATE door (`placement_3373_test.go`). On the UPDATE door
// the two tests that exist
// (`applications_update_test.go` TopologyScaleUp,
// `applications_update_4950_owndeps_test.go` ConsolePlacementApply) both seed a
// CR whose placement is already a STRING and then assert with
// `unstructured.NestedString`. String-in/string-out is CORRECT, so those tests
// are true — they simply never exercise the case that loses data. Both are kept
// green by the fix and are re-asserted here as CONTROLS.
//
// ASSERTIONS ARE ON VALUES AND SHAPES, never key presence: a guard that only
// checked `spec.placement` exists passes just as happily on the scalar that
// destroyed the object.

import (
	"context"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

// makeAppCRObjectPlacement seeds an Application whose `spec.placement` is the
// OBJECT form — the shape the live control `uat50-ahs-pg` holds on hw293.
func makeAppCRObjectPlacement(ns, name, mode, vcluster string, clusters []string) *unstructured.Unstructured {
	obj := makeAppCR(ns, name, "1.2.3", mode, []string{"me-east-215-a"})
	clustersAny := make([]any, len(clusters))
	for i, c := range clusters {
		clustersAny[i] = c
	}
	_ = unstructured.SetNestedMap(obj.Object, map[string]any{
		"mode":     mode,
		"vcluster": vcluster,
		"clusters": clustersAny,
	}, "spec", "placement")
	return obj
}

// TestHandleApplicationUpdate_Row16_ObjectPlacementSurvivesPUT is the RED test.
//
// Before the fix it failed at the very first assertion — `spec.placement` came
// back a string, so `NestedMap` reported ok=false.
func TestHandleApplicationUpdate_Row16_ObjectPlacementSurvivesPUT(t *testing.T) {
	cr := makeAppCRObjectPlacement("acme", "ahs-pg", "single-region", "rtz", []string{"mgmt-A"})

	// VACUITY CHECK — prove the fixture genuinely carries the OBJECT form
	// before the PUT. Without this the test could pass on a seed that was
	// never a map, which is the shape it exists to defend.
	seeded, ok, err := unstructured.NestedMap(cr.Object, "spec", "placement")
	if err != nil || !ok {
		t.Fatalf("VACUITY: fixture must seed an OBJECT spec.placement; ok=%v err=%v", ok, err)
	}
	if seeded["vcluster"] != "rtz" {
		t.Fatalf("VACUITY: fixture vcluster = %v, want rtz", seeded["vcluster"])
	}

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-row16-object")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:    "active-hotstandby",
			Regions: []string{"me-east-215-a", "me-east-215-b"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/ahs-pg?namespace=acme", body,
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, err := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "ahs-pg", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get patched CR: %v", err)
	}

	pl, ok, err := unstructured.NestedMap(got.Object, "spec", "placement")
	if err != nil || !ok {
		// The pre-fix failure lands here.
		scalar, isStr, _ := unstructured.NestedString(got.Object, "spec", "placement")
		t.Fatalf("spec.placement was SCALARIZED by the PUT: NestedMap ok=%v err=%v; "+
			"as string=%q (isString=%v). The object form carries the vCluster pivot and "+
			"must survive a posture-only change.", ok, err, scalar, isStr)
	}

	// The posture DID change — the PUT is not a no-op.
	if pl["mode"] != "active-hot-standby" {
		t.Fatalf("spec.placement.mode = %v, want active-hot-standby (canonical)", pl["mode"])
	}
	// ...and the WHERE fields the body never mentioned SURVIVED.
	if pl["vcluster"] != "rtz" {
		t.Fatalf("spec.placement.vcluster = %v, want rtz preserved — the PUT named no "+
			"vcluster, so it must not clear one", pl["vcluster"])
	}
	clusters, ok, _ := unstructured.NestedStringSlice(got.Object, "spec", "placement", "clusters")
	if !ok || len(clusters) != 1 || clusters[0] != "mgmt-A" {
		t.Fatalf("spec.placement.clusters = %v (ok=%v), want [mgmt-A] preserved", clusters, ok)
	}
}

// TestHandleApplicationUpdate_Row16_Control_StringPlacementStaysString is the
// CONTROL that shares the suspect property: it drives the SAME handler, the
// SAME branch and the same posture change, differing ONLY in the seeded shape.
// A "fix" that promoted every placement to an object would turn this red, so it
// is what keeps the change from over-reaching.
func TestHandleApplicationUpdate_Row16_Control_StringPlacementStaysString(t *testing.T) {
	cr := makeAppCR("acme", "wp-str", "1.2.3", "single-region", []string{"me-east-215-a"})

	// VACUITY CHECK — the control is only meaningful if the seed really is a
	// string. (`NestedMap` must NOT resolve here.)
	if _, ok, _ := unstructured.NestedMap(cr.Object, "spec", "placement"); ok {
		t.Fatalf("VACUITY: control fixture must seed a STRING spec.placement, got a map")
	}

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-row16-string")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:    "active-hotstandby",
			Regions: []string{"me-east-215-a", "me-east-215-b"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-str?namespace=acme", body,
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, _ := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "wp-str", metav1.GetOptions{})
	mode, ok, _ := unstructured.NestedString(got.Object, "spec", "placement")
	if !ok {
		t.Fatalf("control regressed: a STRING placement must stay a string when the " +
			"body names no WHERE fields; it became a non-string")
	}
	if mode != "active-hot-standby" {
		t.Fatalf("spec.placement = %q, want canonical active-hot-standby", mode)
	}
}

// TestHandleApplicationUpdate_Row16_BodyWhereFieldsPromoteToObject pins the
// remaining half of the dual-form contract on this door: when the PUT itself
// names a WHERE field, the update door must emit the OBJECT form exactly as the
// create door does (`applications.go:1116-1136`), rather than dropping the
// vCluster the caller just asked for.
func TestHandleApplicationUpdate_Row16_BodyWhereFieldsPromoteToObject(t *testing.T) {
	cr := makeAppCR("acme", "wp-promote", "1.2.3", "single-region", []string{"me-east-215-a"})

	// #5616 — the body places on `rtz`; validate it as a Sovereign that
	// installs that tier would, rather than weakening the availability gate.
	instances.SetAvailableVClusterTiers("rtz")
	t.Cleanup(func() { instances.SetAvailableVClusterTiers("") })

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeApplicationDynamicFactory(cr)
	h.dynamicFactory = factory
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	dep := installUserAccessDeployment(t, h, "dep-row16-promote")

	body := applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:     "active-hotstandby",
			Regions:  []string{"me-east-215-a", "me-east-215-b"},
			VCluster: "rtz",
			Clusters: []string{"mgmt-A", "mgmt-B"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/applications/wp-promote?namespace=acme", body,
		registerApplicationUpdateRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	got, _ := client.Resource(ApplicationGVR()).Namespace("acme").Get(
		context.Background(), "wp-promote", metav1.GetOptions{})
	pl, ok, err := unstructured.NestedMap(got.Object, "spec", "placement")
	if err != nil || !ok {
		t.Fatalf("a PUT naming vcluster/clusters must store the OBJECT form; ok=%v err=%v", ok, err)
	}
	if pl["mode"] != "active-hot-standby" {
		t.Fatalf("spec.placement.mode = %v, want active-hot-standby", pl["mode"])
	}
	if pl["vcluster"] != "rtz" {
		t.Fatalf("spec.placement.vcluster = %v, want rtz (the body named it)", pl["vcluster"])
	}
	clusters, _, _ := unstructured.NestedStringSlice(got.Object, "spec", "placement", "clusters")
	if len(clusters) != 2 || clusters[0] != "mgmt-A" || clusters[1] != "mgmt-B" {
		t.Fatalf("spec.placement.clusters = %v, want [mgmt-A mgmt-B]", clusters)
	}
}
