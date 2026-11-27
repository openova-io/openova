// application_controller_cnpg_hostside_4282_test.go — #4282 root-cause-B,
// generalised by NET-223.
//
// A per-Org bp-postgres Application must render its `postgresql.cnpg.io/v1
// Cluster` CR HOST-SIDE — never pivoted into a vCluster — because a vCluster
// apiserver registers NO postgresql.cnpg.io CRD and runs NO cnpg operator.
// #4282 kept CNPG-bearing Blueprints off the tier pivot; NET-223 removed the
// pivot, so EVERY Blueprint renders host-side, whatever tier a target names.
//
// What we assert, for a target that still names a vCluster tier:
//
//  1. A CNPG-bearing Blueprint (companion=bp-cnpg, or a
//     `spec.depends[].blueprint: bp-cnpg` edge) renders its HR in the
//     Application's OWN host namespace with NO spec.kubeConfig pivot.
//  2. A NON-CNPG Blueprint renders the same way: in the Application's own
//     namespace, no kubeConfig, nothing in the tier namespace.
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeBlueprintCNPGSingleton builds a data-instance Blueprint (mirrors
// bp-postgres), carrying the `catalyst.openova.io/companion: bp-cnpg` label
// (the LIVE catalog-seed signal) when withCompanionLabel, and/or a
// `spec.depends[].blueprint: bp-cnpg` edge when withDependsEdge.
func makeBlueprintCNPGSingleton(name, version string, withCompanionLabel, withDependsEdge bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)
	if withCompanionLabel {
		u.SetLabels(map[string]string{"catalyst.openova.io/companion": "bp-cnpg"})
	}
	spec := map[string]interface{}{
		"version": version,
		"card":    map[string]interface{}{"title": name},
		"manifests": map[string]interface{}{
			"chart": name,
			"source": map[string]interface{}{
				"kind": "HelmRepository",
				"ref":  "openova-catalog",
			},
		},
	}
	if withDependsEdge {
		spec["depends"] = []interface{}{
			map[string]interface{}{"blueprint": "bp-cnpg", "version": "^1", "alias": "cnpg"},
		}
	}
	u.Object["spec"] = spec
	u.Object["status"] = map[string]interface{}{"ociDigest": "sha256:cnpg-fixture"}
	return u
}

// reconcileTierTargetHRs reconciles a single-target (Primary) Application of
// the given Blueprint whose target names the given vCluster tier, and returns
// the rendered HRs in the Application's namespace and in the tier namespace.
func reconcileTierTargetHRs(t *testing.T, bp *unstructured.Unstructured, tier, cluster string) (appNS, tierNS []unstructured.Unstructured) {
	t.Helper()
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeAppWithTargets("acme", "db", "acme-prod", bp.GetName(), "0.2.5",
		"singleton",
		[]string{"hetzner-fsn-rtz-prod"},
		[]map[string]interface{}{
			{"region": "fsn", "cluster": cluster, "vcluster": tier, "role": "Primary"},
		},
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileAndApplyAll(t, r, fg, "acme", "db")

	got := readApp(t, r, "acme", "db")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	appList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in host ns acme: %v", err)
	}
	tierList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace(tier).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in tier ns %s: %v", tier, err)
	}
	return appList.Items, tierList.Items
}

func assertOneHostSideHR(t *testing.T, appNS, tierNS []unstructured.Unstructured, tier string) {
	t.Helper()
	if len(tierNS) != 0 {
		t.Fatalf("no HR may land in the tier namespace %s (the pivot is gone); got %d", tier, len(tierNS))
	}
	if len(appNS) != 1 {
		t.Fatalf("the HR must land in the Application's own namespace acme; want 1, got %d", len(appNS))
	}
	hr := appNS[0]
	if kc, found, _ := unstructured.NestedMap(hr.Object, "spec", "kubeConfig"); found {
		t.Errorf("the HR must carry NO spec.kubeConfig pivot; got %v", kc)
	}
	if ns := hr.GetNamespace(); ns != "acme" {
		t.Errorf("HR namespace = %q, want the Application namespace acme", ns)
	}
}

// TestReconcile_CNPGBlueprint_TierTarget_RendersHostSide — a CNPG-bearing
// Blueprint (companion=bp-cnpg) whose target names the mgmt tier renders its
// HR host-side so the host cnpg-system operator reconciles the Cluster CR.
func TestReconcile_CNPGBlueprint_TierTarget_RendersHostSide(t *testing.T) {
	bp := makeBlueprintCNPGSingleton("bp-postgres", "0.2.5", true, true)
	appNS, tierNS := reconcileTierTargetHRs(t, bp, "mgmt", "mgmt-A")
	assertOneHostSideHR(t, appNS, tierNS, "mgmt")
}

// TestReconcile_CNPGBlueprint_DependsEdgeOnly_RendersHostSide — the
// `spec.depends[].blueprint: bp-cnpg` edge alone (no companion label).
func TestReconcile_CNPGBlueprint_DependsEdgeOnly_RendersHostSide(t *testing.T) {
	bp := makeBlueprintCNPGSingleton("bp-postgres", "0.2.5", false, true)
	appNS, tierNS := reconcileTierTargetHRs(t, bp, "rtz", "rtz-A")
	assertOneHostSideHR(t, appNS, tierNS, "rtz")
}

// TestReconcile_NonCNPGBlueprint_TierTarget_RendersHostSide — a NON-CNPG
// Blueprint whose target names the mgmt tier is NOT pivoted any more: before
// NET-223 its HR went to the namespace mgmt with a kubeConfig pivot into the mgmt vCluster.
func TestReconcile_NonCNPGBlueprint_TierTarget_RendersHostSide(t *testing.T) {
	bp := makeBlueprintCNPGSingleton("bp-grafana", "1.0.6", false, false)
	appNS, tierNS := reconcileTierTargetHRs(t, bp, "mgmt", "mgmt-A")
	assertOneHostSideHR(t, appNS, tierNS, "mgmt")
}
