// application_controller_cnpg_hostside_4282_test.go — #4282 root-cause-B.
//
// A per-Org bp-postgres Application placed into a vCluster tier must render
// its `postgresql.cnpg.io/v1 Cluster` CR HOST-SIDE — never pivoted into the
// vCluster — because a vCluster apiserver registers NO postgresql.cnpg.io CRD
// and runs NO cnpg operator. The CNPG operator + its CRDs are cluster-
// singletons owned by the host cnpg-system release (bootstrap-kit slot 16) /
// the per-Org host-ns webhook-less operator the catalyst funnel installs. A HR
// pivoted into the vCluster renders a hollow `InstallSucceeded` (the chart's
// cluster.yaml is gated on `.Capabilities.APIVersions.Has
// "postgresql.cnpg.io/v1"`, FALSE there) → 0 Cluster CRs → the Application
// hangs Provisioning forever (the live shared-pg-d repro).
//
// This is the SAME host-side invariant the funnel's bp-cnpg-pair path already
// enforces (#4293 BLOCKER-1 / gitops TestCNPGPair_PrimaryStaysHostSide_Both
// Tiers): the Cluster CR stays where the operator+CRD live; the in-vCluster app
// pods reach the DB through the synced Service + the reflected credential
// Secret.
//
// What we assert:
//
//  1. A CNPG-bearing Blueprint (companion=bp-cnpg) placed in a vCluster tier
//     renders its HR in the Application's OWN host namespace (the Org host ns,
//     where the host cnpg-system operator reconciles), NOT the vCluster host
//     namespace, and the HR carries NO spec.kubeConfig pivot.
//
//  2. The `spec.depends[].blueprint: bp-cnpg` edge is an equivalent signal —
//     a Blueprint declaring it (but WITHOUT the companion label) routes
//     host-side too.
//
//  3. A NON-CNPG Blueprint placed in the SAME vCluster tier is UNCHANGED — it
//     keeps the proven vCluster pivot (kubeConfig secretRef → vc-<tier>, HR in
//     the vCluster host ns). The host-routing is CNPG-only, not a blanket
//     vCluster-pivot retirement.
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeBlueprintCNPGSingleton builds a CNPG-bearing data-instance Blueprint
// (mirrors bp-postgres): a single-target singleton placement into the given
// vCluster tier, carrying the `catalyst.openova.io/companion: bp-cnpg` label
// (the LIVE catalog-seed signal) when withCompanionLabel, and/or a
// `spec.depends[].blueprint: bp-cnpg` edge when withDependsEdge.
func makeBlueprintCNPGSingleton(name, version, tier, cluster string, withCompanionLabel, withDependsEdge bool) *unstructured.Unstructured {
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

// reconcileCNPGSingletonHRs reconciles a single-target (Primary) Application of
// the given Blueprint into the given vCluster tier and returns the rendered
// HRs in BOTH the Application's host namespace and the vCluster host namespace.
func reconcileCNPGSingletonHRs(t *testing.T, bp *unstructured.Unstructured, tier, cluster string) (hostNS, vclusterNS []unstructured.Unstructured) {
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

	reconcileFromCluster(t, r, "acme", "db")

	got := readApp(t, r, "acme", "db")
	if phase, reason, msg := readPhaseAndReason(t, got); phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	hostList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in host ns acme: %v", err)
	}
	vcHostNS := tier
	if vcHostNS == "" {
		vcHostNS = "mgmt"
	}
	vcList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace(vcHostNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in vcluster host ns %s: %v", vcHostNS, err)
	}
	return hostList.Items, vcList.Items
}

// TestReconcile_CNPGBlueprint_VClusterPlacement_RoutesHostSide — a CNPG-bearing
// Blueprint (companion=bp-cnpg) placed in the mgmt vCluster tier renders its HR
// host-side (Application ns, NO kubeConfig pivot) so the host cnpg-system
// operator reconciles the Cluster CR. #4282 root-cause-B.
func TestReconcile_CNPGBlueprint_VClusterPlacement_RoutesHostSide(t *testing.T) {
	bp := makeBlueprintCNPGSingleton("bp-postgres", "0.2.5", "mgmt", "mgmt-A", true, true)
	hostNS, vclusterNS := reconcileCNPGSingletonHRs(t, bp, "mgmt", "mgmt-A")

	if len(vclusterNS) != 0 {
		t.Fatalf("CNPG-bearing HR must NOT land in the vCluster host ns (mgmt) — it would render a hollow InstallSucceeded with 0 Cluster CRs; got %d HRs there", len(vclusterNS))
	}
	if len(hostNS) != 1 {
		t.Fatalf("CNPG-bearing HR must land in the Application's own host ns (acme) where the cnpg operator reconciles; want 1, got %d", len(hostNS))
	}
	hr := hostNS[0]
	if _, found, _ := unstructured.NestedMap(hr.Object, "spec", "kubeConfig"); found {
		t.Errorf("CNPG-bearing host-side HR must carry NO spec.kubeConfig pivot (routing the Cluster CR into a vCluster fails on the missing postgresql.cnpg.io CRD):\n%v", hr.Object["spec"])
	}
	if ns := hr.GetNamespace(); ns != "acme" {
		t.Errorf("CNPG-bearing host-side HR namespace = %q, want the Application host ns acme", ns)
	}
}

// TestReconcile_CNPGBlueprint_DependsEdgeOnly_RoutesHostSide — the
// `spec.depends[].blueprint: bp-cnpg` edge alone (no companion label) is an
// equivalent host-routing signal for source-authored Blueprints.
func TestReconcile_CNPGBlueprint_DependsEdgeOnly_RoutesHostSide(t *testing.T) {
	bp := makeBlueprintCNPGSingleton("bp-postgres", "0.2.5", "rtz", "rtz-A", false, true)
	hostNS, vclusterNS := reconcileCNPGSingletonHRs(t, bp, "rtz", "rtz-A")

	if len(vclusterNS) != 0 {
		t.Fatalf("CNPG depends-edge HR must NOT land in the vCluster host ns (rtz); got %d HRs there", len(vclusterNS))
	}
	if len(hostNS) != 1 {
		t.Fatalf("CNPG depends-edge HR must land in the Application host ns (acme); want 1, got %d", len(hostNS))
	}
	if _, found, _ := unstructured.NestedMap(hostNS[0].Object, "spec", "kubeConfig"); found {
		t.Errorf("CNPG depends-edge host-side HR must carry NO spec.kubeConfig pivot")
	}
}

// TestReconcile_NonCNPGBlueprint_VClusterPlacement_KeepsPivot — a NON-CNPG
// Blueprint placed in the SAME vCluster tier is UNCHANGED: it keeps the proven
// vCluster pivot (kubeConfig secretRef → vc-mgmt, HR in the vCluster host ns).
// The host-routing is CNPG-only — never a blanket pivot retirement.
func TestReconcile_NonCNPGBlueprint_VClusterPlacement_KeepsPivot(t *testing.T) {
	bp := makeBlueprintCNPGSingleton("bp-grafana", "1.0.6", "mgmt", "mgmt-A", false, false)
	hostNS, vclusterNS := reconcileCNPGSingletonHRs(t, bp, "mgmt", "mgmt-A")

	if len(vclusterNS) != 1 {
		t.Fatalf("NON-CNPG HR must keep the vCluster pivot and land in the vCluster host ns (mgmt); want 1, got %d", len(vclusterNS))
	}
	if len(hostNS) != 0 {
		t.Fatalf("NON-CNPG HR must NOT be routed into the Application host ns (acme); got %d HRs there", len(hostNS))
	}
	kc, found, _ := unstructured.NestedMap(vclusterNS[0].Object, "spec", "kubeConfig")
	if !found {
		t.Fatalf("NON-CNPG vCluster HR must carry a spec.kubeConfig pivot")
	}
	if name, _, _ := unstructured.NestedString(kc, "secretRef", "name"); name != "vc-mgmt" {
		t.Errorf("NON-CNPG vCluster HR kubeConfig secretRef.name = %q, want vc-mgmt", name)
	}
}

// TestBlueprintRequiresHostCNPGOperator_Signals — unit coverage of the
// detection helper across the companion label, the depends edge, both, and
// neither.
func TestBlueprintRequiresHostCNPGOperator_Signals(t *testing.T) {
	cases := []struct {
		name           string
		companionLabel bool
		dependsEdge    bool
		want           bool
	}{
		{"companion-label-only", true, false, true},
		{"depends-edge-only", false, true, true},
		{"both", true, true, true},
		{"neither", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bp := makeBlueprintCNPGSingleton("bp-x", "1.0.0", "mgmt", "mgmt-A", tc.companionLabel, tc.dependsEdge)
			if got := blueprintRequiresHostCNPGOperator(bp); got != tc.want {
				t.Errorf("blueprintRequiresHostCNPGOperator() = %v, want %v", got, tc.want)
			}
		})
	}
	if blueprintRequiresHostCNPGOperator(nil) {
		t.Errorf("blueprintRequiresHostCNPGOperator(nil) must be false")
	}
}
