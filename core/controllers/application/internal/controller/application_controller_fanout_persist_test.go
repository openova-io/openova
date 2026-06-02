// application_controller_fanout_persist_test.go — G117.6a (Refs #2796)
// integration coverage for the perClusterFanout persistence loop.
//
// What we assert:
//
//  1. An Application against a Blueprint with `spec.topology` declaring
//     2 clusters under the chosen variant produces 2 HelmReleases on
//     the host k3s — one per cluster.
//
//  2. Each HR carries `spec.kubeConfig.secretRef.name = vc-<cluster>`
//     (lowercased) — the G92.1 #2674 pivot pattern. Without this the
//     HR would install onto the local cluster instead of the per-Org
//     vCluster.
//
//  3. Each HR lands in the vCluster's host namespace
//     (`r.Cfg.VClusterPlacements[tier].HostNamespace`) so Flux v2
//     resolves the SecretReference from the same namespace.
//
//  4. The two HRs carry the canonical role labels
//     (`catalyst.openova.io/role` ∈ {active, passive, singleton}).
//
//  5. Idempotency — re-reconcile leaves the HRs byte-equal (no
//     spurious updates).
//
// We exercise the production code path by injecting a Blueprint with
// `spec.topology` and asserting on the HRs the fake dynamic client
// recorded.
package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// makeBlueprintWithActiveHotStandbyTopology returns a Blueprint with
// `spec.topology` declaring 2-cluster active-hot-standby on tier=mgmt.
// Mirrors fixtureGrafanaTopology() in internal/render/topology_test.go
// (the canonical 2-cluster fixture) but emits the spec via
// unstructured so the controller's parseBlueprintTopology JSON round-
// trip exercises the production code path.
func makeBlueprintWithActiveHotStandbyTopology(name, version, tier string, clusters []string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	u.SetGeneration(1)

	clustersAny := make([]interface{}, len(clusters))
	for i, c := range clusters {
		clustersAny[i] = c
	}
	roles := map[string]interface{}{}
	for i, c := range clusters {
		switch i {
		case 0:
			roles[c] = "active"
		default:
			roles[c] = "passive"
		}
	}
	spec := map[string]interface{}{
		"version": version,
		"card":    map[string]interface{}{"title": name},
		// placementSchema.modes must allow the "active-hotstandby"
		// placement the Application picks — without it the reconciler
		// rejects placement BEFORE the topology fan-out runs.
		"placementSchema": map[string]interface{}{
			"modes": []interface{}{"active-hotstandby", "single-region"},
		},
		"manifests": map[string]interface{}{
			"chart": name,
			"source": map[string]interface{}{
				"kind": "HelmRepository",
				"ref":  "openova-catalog",
			},
		},
		"topology": map[string]interface{}{
			"supported": []interface{}{"active-hot-standby", "singleton"},
			"defaults": map[string]interface{}{
				"multi-region":  "active-hot-standby",
				"single-region": "singleton",
			},
			"perTopology": map[string]interface{}{
				"active-hot-standby": map[string]interface{}{
					"placement": map[string]interface{}{
						"tier":     tier,
						"clusters": clustersAny,
						"roles":    roles,
					},
				},
				"singleton": map[string]interface{}{
					"placement": map[string]interface{}{
						"tier":     tier,
						"clusters": []interface{}{clusters[0]},
						"roles":    map[string]interface{}{clusters[0]: "singleton"},
					},
				},
			},
		},
	}
	u.Object["spec"] = spec
	u.Object["status"] = map[string]interface{}{
		"ociDigest": "sha256:topology-fixture",
	}
	return u
}

// makeMultiRegionEnv returns an Environment with 2 regions — enough to
// trip the controller's `sovRegions > 1` multi-region detection so the
// Blueprint's defaultOnMultiRegion (active-hot-standby) is picked.
func makeMultiRegionEnv(name, org, envType string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Environment")
	u.SetName(name)
	u.SetGeneration(1)
	u.Object["spec"] = map[string]interface{}{
		"organizationRef": org,
		"envType":         envType,
		"placement":       "active-hotstandby",
		"regions": []interface{}{
			map[string]interface{}{
				"provider": "hetzner", "region": "fsn", "buildingBlock": "rtz",
			},
			map[string]interface{}{
				"provider": "hetzner", "region": "nbg", "buildingBlock": "rtz",
			},
		},
	}
	return u
}

// TestReconcile_TopologyFanout_TwoClustersTwoHRs — the central #2796
// acceptance: a Blueprint with 2 clusters in its active-hot-standby
// variant produces 2 HelmReleases on the host k3s after reconcile.
// Each HR pivots into a distinct vCluster via spec.kubeConfig.secretRef.
func TestReconcile_TopologyFanout_TwoClustersTwoHRs(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt",
		[]string{"mgmt-A", "mgmt-B"},
	)
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "obs")

	// Application reconcile should not have failed.
	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	// List the HelmReleases the reconciler wrote in the mgmt host
	// namespace (the per-Tier vCluster host namespace per the default
	// VClusterPlacements convention).
	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in mgmt: %v", err)
	}
	if got := len(hrList.Items); got != 2 {
		t.Fatalf("want 2 per-cluster HRs in mgmt, got %d", got)
	}

	// Assert per-HR shape — both must carry the canonical labels and
	// the kubeConfig pivot pointing at a distinct per-cluster secret.
	seenSecrets := map[string]bool{}
	seenClusters := map[string]bool{}
	seenRoles := map[string]bool{}
	for _, hr := range hrList.Items {
		labels := hr.GetLabels()
		cluster := labels["catalyst.openova.io/cluster"]
		if cluster == "" {
			t.Errorf("HR %s missing catalyst.openova.io/cluster label", hr.GetName())
		}
		seenClusters[cluster] = true
		seenRoles[labels["catalyst.openova.io/role"]] = true

		kc, _, _ := unstructured.NestedMap(hr.Object, "spec", "kubeConfig")
		if kc == nil {
			t.Errorf("HR %s missing spec.kubeConfig (G92.1 pivot)", hr.GetName())
			continue
		}
		ref, _, _ := unstructured.NestedMap(kc, "secretRef")
		if ref == nil {
			t.Errorf("HR %s missing spec.kubeConfig.secretRef", hr.GetName())
			continue
		}
		secName, _, _ := unstructured.NestedString(ref, "name")
		if secName == "" {
			t.Errorf("HR %s has empty kubeConfig.secretRef.name", hr.GetName())
		}
		seenSecrets[secName] = true

		// Topology + app labels.
		if labels["catalyst.openova.io/app"] != "obs" {
			t.Errorf("HR %s app label = %q, want obs", hr.GetName(), labels["catalyst.openova.io/app"])
		}
		if labels["catalyst.openova.io/topology"] != "active-hot-standby" {
			t.Errorf("HR %s topology label = %q, want active-hot-standby", hr.GetName(), labels["catalyst.openova.io/topology"])
		}
	}

	// Two distinct clusters, two distinct secrets, both roles present.
	if !seenClusters["mgmt-A"] || !seenClusters["mgmt-B"] {
		t.Errorf("expected HRs covering mgmt-A and mgmt-B; got %v", seenClusters)
	}
	if !seenSecrets["vc-mgmt-a"] || !seenSecrets["vc-mgmt-b"] {
		t.Errorf("expected per-cluster kubeConfig secrets vc-mgmt-a + vc-mgmt-b; got %v", seenSecrets)
	}
	if !seenRoles["active"] || !seenRoles["passive"] {
		t.Errorf("expected both active + passive roles; got %v", seenRoles)
	}
}

// TestReconcile_TopologyFanout_Idempotent — re-reconciling the same
// Application MUST NOT spawn duplicate HRs nor mutate the existing
// pair's spec. upsertHostResource's byte-equal short-circuit is the
// canonical guard against the reconcile loop becoming a write-storm.
func TestReconcile_TopologyFanout_Idempotent(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt",
		[]string{"mgmt-A", "mgmt-B"},
	)
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	// First reconcile.
	reconcileFromCluster(t, r, "acme", "obs")
	firstList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs after first reconcile: %v", err)
	}
	firstSpecs := map[string]map[string]interface{}{}
	for _, hr := range firstList.Items {
		s, _, _ := unstructured.NestedMap(hr.Object, "spec")
		firstSpecs[hr.GetName()] = s
	}

	// Second reconcile — should be a no-op on the HR spec.
	reconcileFromCluster(t, r, "acme", "obs")
	secondList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs after second reconcile: %v", err)
	}
	if len(secondList.Items) != len(firstList.Items) {
		t.Fatalf("HR count diverged after re-reconcile: %d → %d",
			len(firstList.Items), len(secondList.Items))
	}
	for _, hr := range secondList.Items {
		s, _, _ := unstructured.NestedMap(hr.Object, "spec")
		expect := firstSpecs[hr.GetName()]
		if expect == nil {
			t.Errorf("re-reconcile created NEW HR %s — should be steady state", hr.GetName())
			continue
		}
		// Spec must be byte-equal (the upsertHostResource path's
		// specsEqual short-circuit prevents update writes).
		if !specsEqual(s, expect) {
			t.Errorf("HR %s spec diverged on re-reconcile (drift-restore should have been a no-op)", hr.GetName())
		}
	}
}
