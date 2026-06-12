// Tests for #3373 — instance-level placement.
//
// Founder-ratified law: placement is an INSTANCE property
// (`Application.spec.placement` object form {vcluster, regions,
// clusters, mode}), defaulted from the Blueprint
// (`spec.defaultPlacement` + `spec.allowedPlacements`), rendered by
// the ONE generic mechanism — the Flux `spec.kubeConfig.secretRef`
// pivot (G92.1, fanout.go + pkg/render). Zero per-app placement code.
//
// Matrix:
//
//  1. parseSpec dual-form — legacy string preserved verbatim.
//  2. parseSpec object form — vcluster/clusters/regions/mode parsed;
//     "host" normalizes to ""; unknown vcluster errors.
//  3. Instance placement.vcluster=rtz → rendered HR pivots into
//     vc-rtz (HR ns rtz) even when the Blueprint declares nothing.
//  4. Blueprint spec.defaultPlacement.vcluster=mgmt → instance with
//     NO placement inherits the suggestion (silent-accept law §2.1).
//  5. Instance choice OVERRIDES Blueprint defaultPlacement.
//  6. Explicit instance `vcluster: host` overrides a Blueprint
//     default into host placement (no kubeConfig pivot).
//  7. allowedPlacements rejection → phase=Failed reason=Invalid.
//  8. Git-is-truth (#3373 DoD-7 mechanic): edit spec.placement on the
//     stored CR (≙ a direct Git edit applied by Flux) → re-reconcile
//     → the rendered HR moves to the new vCluster and
//     status.placement reflects it. No console involvement.
//  9. status.placement reflects the EFFECTIVE placement + source.
package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeAppWithPlacement builds an Application whose spec.placement is
// the #3373 OBJECT form.
func makeAppWithPlacement(namespace, name, env, bpName, bpVer string, placement map[string]interface{}, regions []string) *unstructured.Unstructured {
	u := makeApp(namespace, name, env, bpName, bpVer, "single-region", regions, nil)
	spec := u.Object["spec"].(map[string]interface{})
	spec["placement"] = placement
	return u
}

// makeBlueprintWithDefaultPlacement builds a Blueprint declaring the
// #3373 class-level suggestion + allow-list.
func makeBlueprintWithDefaultPlacement(name, version string, defaultVCluster string, allowed []string) *unstructured.Unstructured {
	bp := makeBlueprint(name, version, nil, []string{"single-region"})
	spec := bp.Object["spec"].(map[string]interface{})
	if defaultVCluster != "" {
		spec["defaultPlacement"] = map[string]interface{}{
			"vcluster": defaultVCluster,
		}
	}
	if allowed != nil {
		al := make([]interface{}, len(allowed))
		for i, a := range allowed {
			al[i] = a
		}
		spec["allowedPlacements"] = al
	}
	return bp
}

func getCommittedHR(t *testing.T, fg *fakeGitea, org, app, region string) string {
	t.Helper()
	hrBytes, ok := fg.get(org, app, "main",
		"clusters/"+region+"/applications/"+app+"/helmrelease.yaml")
	if !ok {
		t.Fatalf("HelmRelease for %s/%s on %s should have been committed to Gitea", org, app, region)
	}
	return string(hrBytes)
}

// --- 1+2. parseSpec dual-form ---------------------------------------------

func TestParseSpec_PlacementLegacyString(t *testing.T) {
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.Placement != "single-region" {
		t.Errorf("Placement = %q, want single-region", spec.Placement)
	}
	if spec.PlacementVClusterSet {
		t.Error("PlacementVClusterSet must be false for the legacy string form")
	}
}

func TestParseSpec_PlacementObjectForm(t *testing.T) {
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{
			"vcluster": "rtz",
			"mode":     "single-region",
			"clusters": []interface{}{"mgmt-A"},
			"regions":  []interface{}{"hetzner-nbg-rtz-prod"},
		},
		[]string{"hetzner-fsn-rtz-prod"})
	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if !spec.PlacementVClusterSet || spec.PlacementVCluster != "rtz" {
		t.Errorf("PlacementVCluster = (%q, set=%v), want (rtz, true)",
			spec.PlacementVCluster, spec.PlacementVClusterSet)
	}
	if spec.Placement != "single-region" {
		t.Errorf("mode → Placement = %q, want single-region", spec.Placement)
	}
	if len(spec.PlacementClusters) != 1 || spec.PlacementClusters[0] != "mgmt-A" {
		t.Errorf("PlacementClusters = %v, want [mgmt-A]", spec.PlacementClusters)
	}
	// placement.regions wins over the legacy top-level spec.regions[].
	if len(spec.Regions) != 1 || spec.Regions[0] != "hetzner-nbg-rtz-prod" {
		t.Errorf("Regions = %v, want [hetzner-nbg-rtz-prod] (placement.regions override)", spec.Regions)
	}
}

func TestParseSpec_PlacementHostSentinelNormalized(t *testing.T) {
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "host"},
		[]string{"hetzner-fsn-rtz-prod"})
	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if !spec.PlacementVClusterSet {
		t.Error("explicit vcluster: host must register as SET (presence-aware)")
	}
	if spec.PlacementVCluster != "" {
		t.Errorf("PlacementVCluster = %q, want \"\" (host normalizes to empty)", spec.PlacementVCluster)
	}
}

func TestParseSpec_PlacementUnknownVClusterRejected(t *testing.T) {
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "warp-zone"},
		[]string{"hetzner-fsn-rtz-prod"})
	if _, err := parseSpec(app); err == nil {
		t.Fatal("parseSpec must reject an unknown vcluster tier")
	}
}

// --- 3. Instance placement drives the kubeConfig pivot --------------------

func TestReconcile_InstancePlacementVClusterRTZ(t *testing.T) {
	// Blueprint declares NO placement at all — the instance field is
	// the only truth (law §2.1: placement is an INSTANCE property).
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "rtz", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	hr := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	if !strings.Contains(hr, "namespace: rtz") {
		t.Errorf("HR.metadata.namespace must be the rtz vCluster host namespace, got:\n%s", hr)
	}
	if !strings.Contains(hr, "name: vc-rtz") {
		t.Errorf("HR.spec.kubeConfig.secretRef.name must be vc-rtz, got:\n%s", hr)
	}
	if !strings.Contains(hr, "kubeConfig:") {
		t.Errorf("HR must carry the ONE generic kubeConfig pivot, got:\n%s", hr)
	}
}

// --- 4. Blueprint defaultPlacement inherited silently ---------------------

func TestReconcile_BlueprintDefaultPlacementInherited(t *testing.T) {
	bp := makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", nil)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	// Instance declares NOTHING — the user accepted the suggestion
	// silently ("he doesn't even need to know the vcluster detail").
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	hr := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	if !strings.Contains(hr, "name: vc-mgmt") {
		t.Errorf("Blueprint defaultPlacement.vcluster=mgmt must pivot the HR into vc-mgmt, got:\n%s", hr)
	}
}

// --- 5. Instance choice overrides the Blueprint default -------------------

func TestReconcile_InstanceOverridesBlueprintDefault(t *testing.T) {
	bp := makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", nil)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "dmz", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	hr := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	if !strings.Contains(hr, "name: vc-dmz") {
		t.Errorf("instance vcluster=dmz must win over Blueprint default mgmt, got:\n%s", hr)
	}
}

// --- 6. Explicit instance host overrides a vCluster default ---------------

func TestReconcile_InstanceHostOverridesBlueprintDefault(t *testing.T) {
	bp := makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", nil)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "host", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	hr := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	if strings.Contains(hr, "kubeConfig:") {
		t.Errorf("explicit instance host placement must NOT emit a kubeConfig pivot, got:\n%s", hr)
	}
}

// --- 7. allowedPlacements gate ---------------------------------------------

func TestReconcile_PlacementNotInAllowedPlacements(t *testing.T) {
	bp := makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", []string{"mgmt", "rtz"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "dmz", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	phase, reason, _ := readPhaseAndReason(t, got)
	if phase != PhaseFailed {
		t.Errorf("phase = %q, want %q", phase, PhaseFailed)
	}
	if reason != ReasonInvalid {
		t.Errorf("reason = %q, want %q", reason, ReasonInvalid)
	}
}

// --- 8. Git-is-truth: a direct Git edit of spec.placement re-homes --------

func TestReconcile_GitEditOfPlacementRehomesInstance(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithPlacement("acme", "site", "acme-prod", "bp-wp", "1.0.0",
		map[string]interface{}{"vcluster": "mgmt", "mode": "single-region"},
		[]string{"hetzner-fsn-rtz-prod"})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")
	hr := getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	if !strings.Contains(hr, "name: vc-mgmt") {
		t.Fatalf("precondition: HR must pivot into vc-mgmt first, got:\n%s", hr)
	}

	// The advanced user edits spec.placement.vcluster DIRECTLY in the
	// Git-committed IaC; Flux applies the CR change. No console, no
	// API — the platform reconciles to whatever Git says (law §2.4).
	ctx := context.Background()
	latest, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Get(ctx, "site", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if err := unstructured.SetNestedField(latest.Object, "rtz", "spec", "placement", "vcluster"); err != nil {
		t.Fatalf("set placement: %v", err)
	}
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Update(ctx, latest, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update app: %v", err)
	}

	reconcileFromCluster(t, r, "acme", "site")

	hr = getCommittedHR(t, fg, "acme", "site", "hetzner-fsn-rtz-prod")
	if !strings.Contains(hr, "name: vc-rtz") {
		t.Errorf("after the Git edit the HR must pivot into vc-rtz, got:\n%s", hr)
	}
	if strings.Contains(hr, "name: vc-mgmt") {
		t.Errorf("stale vc-mgmt pivot must be gone after the Git edit, got:\n%s", hr)
	}

	// The console reflects Git: status.placement carries the new home.
	got := readApp(t, r, "acme", "site")
	vc, _, _ := unstructured.NestedString(got.Object, "status", "placement", "vcluster")
	if vc != "rtz" {
		t.Errorf("status.placement.vcluster = %q, want rtz", vc)
	}
	src, _, _ := unstructured.NestedString(got.Object, "status", "placement", "source")
	if src != "instance" {
		t.Errorf("status.placement.source = %q, want instance", src)
	}
}

// --- 9. status.placement reflects the effective placement -----------------

func TestReconcile_StatusPlacementReflectsBlueprintDefault(t *testing.T) {
	bp := makeBlueprintWithDefaultPlacement("bp-wp", "1.0.0", "mgmt", nil)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	vc, _, _ := unstructured.NestedString(got.Object, "status", "placement", "vcluster")
	if vc != "mgmt" {
		t.Errorf("status.placement.vcluster = %q, want mgmt", vc)
	}
	src, _, _ := unstructured.NestedString(got.Object, "status", "placement", "source")
	if src != "blueprint-default" {
		t.Errorf("status.placement.source = %q, want blueprint-default", src)
	}
	regions, _, _ := unstructured.NestedSlice(got.Object, "status", "placement", "regions")
	if len(regions) != 1 || regions[0] != "hetzner-fsn-rtz-prod" {
		t.Errorf("status.placement.regions = %v, want [hetzner-fsn-rtz-prod]", regions)
	}
}

func TestReconcile_StatusPlacementHostDefault(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")

	got := readApp(t, r, "acme", "site")
	vc, _, _ := unstructured.NestedString(got.Object, "status", "placement", "vcluster")
	if vc != "host" {
		t.Errorf("status.placement.vcluster = %q, want host", vc)
	}
	src, _, _ := unstructured.NestedString(got.Object, "status", "placement", "source")
	if src != "host-default" {
		t.Errorf("status.placement.source = %q, want host-default", src)
	}
}
