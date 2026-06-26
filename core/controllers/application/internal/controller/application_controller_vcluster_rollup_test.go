// application_controller_vcluster_rollup_test.go — #4282 status-rollup.
//
// The LAST #4282 gap: a per-Org Application placed in a vCluster never left
// `status.phase: Provisioning` even when its per-cluster HelmRelease was
// Ready. Two bugs combined:
//
//  1. status.perCluster[].status was HARDCODED to "Pending" with no flip to
//     the HR's observed readiness.
//  2. observeRegionHelmReleases read every fan-out HR from app.GetNamespace()
//     instead of the namespace the HR was AUTHORED in (a fan-out HR lands in
//     its WriteNamespace — the host app-ns for #4398 CNPG routing or the
//     vCluster host-ns for the legacy pivot), so a vCluster-placed HR GET-ed
//     in the wrong namespace returned NotFound → Provisioning forever.
//
// These tests assert the rollup now leaves Provisioning when the per-cluster
// HR is Ready, stays Provisioning when it is not, and that the host
// single-HR path is unchanged.
package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
)

// readyHR returns a fan-out HelmRelease with the given name/namespace whose
// Ready condition matches `status` ("True"|"False").
func readyHR(name, namespace, status string) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetAPIVersion("helm.toolkit.fluxcd.io/v2")
	hr.SetKind("HelmRelease")
	hr.SetNamespace(namespace)
	hr.SetName(name)
	reason := "InstallSucceeded"
	msg := "Helm install succeeded"
	if status != "True" {
		reason = "InstallFailed"
		msg = "Helm install failed"
	}
	hr.Object["status"] = map[string]interface{}{
		"conditions": []interface{}{
			map[string]interface{}{
				"type": "Ready", "status": status, "reason": reason, "message": msg,
			},
		},
	}
	return hr
}

// perClusterStatuses reads status.perCluster[] back as a cluster→status map.
func perClusterStatuses(t *testing.T, app *unstructured.Unstructured) map[string]string {
	t.Helper()
	out := map[string]string{}
	rows, _, _ := unstructured.NestedSlice(app.Object, "status", "perCluster")
	for _, row := range rows {
		m, ok := row.(map[string]interface{})
		if !ok {
			continue
		}
		cluster, _ := m["cluster"].(string)
		status, _ := m["status"].(string)
		out[cluster] = status
	}
	return out
}

// TestReconcile_VClusterPlaced_HRReady_RollsUpOutOfProvisioning — the
// #4282 acceptance. A vCluster-placed (tier=mgmt) fan-out Application whose
// local-region per-cluster HR (obs-mgmt-a) is Ready=True rolls up out of
// Provisioning AND flips that cluster's perCluster[].status to Ready. The
// remote-region cluster (mgmt-B, split-side, no local HR) leaves the phase
// in Provisioning until its own region's Flux reports in — so we assert on
// the single-cluster (singleton) shape to get a clean Ready rollup.
func TestReconcile_VClusterPlaced_HRReady_RollsUpOutOfProvisioning(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A"},
	)
	env := makeEnv("acme-prod", "acme", "prod") // single-region → singleton variant
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		nil,
	)
	// Pre-seed the per-cluster HR Flux installs, Ready=True, in the
	// vCluster host namespace (mgmt) where the fan-out authors it.
	hr := readyHR("obs-mgmt-a", "mgmt", "True")

	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp, hr)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, _, _ := readPhaseAndReason(t, got)
	if phase == PhaseProvisioning {
		t.Fatalf("vCluster-placed Application with a Ready per-cluster HR stayed phase=Provisioning — the #4282 rollup gap")
	}
	if phase != PhaseReady {
		t.Fatalf("phase = %q, want %q (the per-cluster HR obs-mgmt-a is Ready=True in ns mgmt)", phase, PhaseReady)
	}
	pc := perClusterStatuses(t, got)
	if pc["mgmt-A"] != PhaseReady {
		t.Errorf("perCluster[mgmt-A].status = %q, want %q (flipped from the hardcoded Provisioning seed)", pc["mgmt-A"], PhaseReady)
	}
}

// TestReconcile_VClusterPlaced_HRNotReady_StaysProvisioning — the negative.
// A vCluster-placed fan-out Application whose per-cluster HR has NOT
// materialised (Flux still pulling) stays phase=Provisioning and its
// perCluster row stays Provisioning. Guards against the fix over-rotating
// every app to Ready.
func TestReconcile_VClusterPlaced_HRNotReady_StaysProvisioning(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-postgres", "0.2.6", "mgmt",
		[]string{"mgmt-A"},
	)
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-postgres", "0.2.6",
		"single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		nil,
	)
	// NO HR seeded — the reconciler authors it but Flux has not reported a
	// Ready condition yet (the freshly-created HR carries no status).
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "obs")

	got := readApp(t, r, "acme", "obs")
	phase, _, _ := readPhaseAndReason(t, got)
	if phase != PhaseProvisioning {
		t.Fatalf("phase = %q, want %q (the per-cluster HR has no Ready condition yet)", phase, PhaseProvisioning)
	}
	pc := perClusterStatuses(t, got)
	if pc["mgmt-A"] != PhaseProvisioning {
		t.Errorf("perCluster[mgmt-A].status = %q, want %q (HR not Ready)", pc["mgmt-A"], PhaseProvisioning)
	}
}

// TestObserveRegionHelmReleases_FanoutHR_NamespaceTravels — the namespace
// half of #4282 in isolation. A fan-out HR authored in a DIFFERENT namespace
// than the Application's own (the vCluster host-ns) is observed Ready when
// the ref carries that namespace, and would be NotFound (Provisioning) if the
// rollup defaulted to app.GetNamespace().
func TestObserveRegionHelmReleases_FanoutHR_NamespaceTravels(t *testing.T) {
	hr := readyHR("obs-mgmt-a", "mgmt", "True") // authored in mgmt, NOT the app-ns

	fg := newFakeGitea()
	r := newReconciler(t, fg, hr)

	app := &unstructured.Unstructured{}
	app.SetNamespace("acme") // app-ns differs from the HR-ns
	app.SetName("obs")
	plan := placement.Plan{Regions: []placement.RegionPlan{{Name: "hetzner-fsn-rtz-prod", Role: "primary"}}}

	// Ref carries the HR's authored namespace → Ready.
	phase, _, _, perHR := r.observeRegionHelmReleases(context.Background(), app, plan,
		[]hrRef{{name: "obs-mgmt-a", namespace: "mgmt", region: "obs-mgmt-a"}})
	if phase != PhaseReady {
		t.Errorf("namespace-carrying ref: phase=%q, want %q", phase, PhaseReady)
	}
	if perHR["obs-mgmt-a"] != "True" {
		t.Errorf("per-HR readiness = %q, want True", perHR["obs-mgmt-a"])
	}

	// Ref WITHOUT the namespace → defaults to app-ns (acme) → NotFound →
	// Provisioning (proves the namespace must travel with the ref).
	phaseWrongNS, _, _, _ := r.observeRegionHelmReleases(context.Background(), app, plan,
		[]hrRef{{name: "obs-mgmt-a", region: "obs-mgmt-a"}})
	if phaseWrongNS != PhaseProvisioning {
		t.Errorf("ref without namespace (defaults to app-ns): phase=%q, want %q", phaseWrongNS, PhaseProvisioning)
	}
}
