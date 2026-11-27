// application_controller_fanout_persist_test.go — G117.6a (Refs #2796)
// integration coverage for the perClusterFanout persistence loop.
//
// What we assert:
//
//  1. An Application against a Blueprint with `spec.topology` declaring
//     2 clusters under the chosen variant produces 2 HelmReleases on
//     the host k3s — one per cluster.
//
//  2. No HR carries a `spec.kubeConfig` pivot, although the Blueprint's
//     variant still names the tier mgmt (NET-223 removed the pivot):
//     each region's own Flux installs its HR onto its host cluster.
//
//  3. Each HR lands in the Application's own namespace, not the tier's.
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

	reconcileAndApplyAll(t, r, fg, "acme", "obs")

	// Application reconcile should not have failed.
	got := readApp(t, r, "acme", "obs")
	phase, reason, msg := readPhaseAndReason(t, got)
	if phase == PhaseFailed {
		t.Fatalf("unexpected Failed phase: reason=%q msg=%q", reason, msg)
	}

	// List the HelmReleases the reconciler wrote in the Application's
	// namespace; the tier namespace mgmt must stay empty.
	hrList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in acme: %v", err)
	}
	if got := len(hrList.Items); got != 2 {
		t.Fatalf("want 2 per-cluster HRs in acme, got %d", got)
	}
	tierList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("mgmt").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs in mgmt: %v", err)
	}
	if got := len(tierList.Items); got != 0 {
		t.Fatalf("want no HR in the tier namespace mgmt, got %d", got)
	}

	// Assert per-HR shape. A 2-region Sovereign is TWO SEPARATE k3s
	// clusters joined by ClusterMesh (cnpg-pair hw128 PASS); each
	// region's own Flux applies its own leg onto its host cluster, so no
	// leg carries a kubeConfig block (ADR-0015 D1, NET-223).
	kcByCluster := map[string]map[string]interface{}{}
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
		kcByCluster[cluster] = kc

		// Topology + app labels.
		if labels["catalyst.openova.io/app"] != "obs" {
			t.Errorf("HR %s app label = %q, want obs", hr.GetName(), labels["catalyst.openova.io/app"])
		}
		if labels["catalyst.openova.io/topology"] != "active-hot-standby" {
			t.Errorf("HR %s topology label = %q, want active-hot-standby", hr.GetName(), labels["catalyst.openova.io/topology"])
		}
	}

	// Two distinct clusters, both roles present.
	if !seenClusters["mgmt-A"] || !seenClusters["mgmt-B"] {
		t.Errorf("expected HRs covering mgmt-A and mgmt-B; got %v", seenClusters)
	}
	if !seenRoles["active"] || !seenRoles["passive"] {
		t.Errorf("expected both active + passive roles; got %v", seenRoles)
	}

	// Neither leg pivots: before NET-223 both pivoted into the mgmt vCluster.
	for _, c := range []string{"mgmt-A", "mgmt-B"} {
		if kc := kcByCluster[c]; kc != nil {
			t.Errorf("HR for %s must carry no spec.kubeConfig, got %v", c, kc)
		}
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
	reconcileAndApplyAll(t, r, fg, "acme", "obs")
	firstList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").
		List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list HRs after first reconcile: %v", err)
	}
	if len(firstList.Items) != 2 {
		t.Fatalf("want 2 per-cluster HRs in acme after the first reconcile, got %d", len(firstList.Items))
	}
	firstSpecs := map[string]map[string]interface{}{}
	for _, hr := range firstList.Items {
		s, _, _ := unstructured.NestedMap(hr.Object, "spec")
		firstSpecs[hr.GetName()] = s
	}

	// Second reconcile — should be a no-op on the HR spec.
	reconcileAndApplyAll(t, r, fg, "acme", "obs")
	secondList, err := r.Dynamic.Resource(FluxHelmReleaseGVR).
		Namespace("acme").
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
