package controller

import (
	"testing"
)

// The region a customer picks for an Application decides where its legs run.
// On main every leg of the Blueprint-topology fan-out went to the region at
// the cluster letter's index in the REGISTRY (rtz-A → the Sovereign's first
// region) whatever spec.regions said, while status, the plan and the
// Continuum followed spec.regions.

func hrClustersIn(t *testing.T, fg *fakeGitea, region string) []string {
	t.Helper()
	var out []string
	for _, o := range regionFileObjs(t, fg, "acme", region, "site") {
		if o.GetKind() == "HelmRelease" {
			out = append(out, o.GetLabels()["catalyst.openova.io/cluster"])
		}
	}
	return out
}

// TestReconcile_Singleton_PickedSecondRegion_RunsThere — a singleton picked
// into the second region is written into that region's folder only.
func TestReconcile_Singleton_PickedSecondRegion_RunsThere(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology("bp-wp", "1.0.0", "", []string{"rtz-A", "rtz-B"})
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{testRegionB}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")
	if phase, reason, msg := readPhaseAndReason(t, readApp(t, r, "acme", "site")); phase == PhaseFailed {
		t.Fatalf("unexpected Failed: %s %s", reason, msg)
	}
	inB, inA := hrClustersIn(t, fg, testRegionB), hrClustersIn(t, fg, testRegionA)
	if len(inB) != 1 {
		t.Fatalf("the picked region %s holds %d HelmRelease(s) %v, want the one singleton leg", testRegionB, len(inB), inB)
	}
	if len(inA) != 0 {
		t.Fatalf("region %s was not picked but holds HelmRelease(s) %v", testRegionA, inA)
	}
}

// TestReconcile_HotStandby_PickedPrimaryFirst_ActiveLegInPrimary — regions
// [B, A]: the active leg (the Blueprint's first cluster) runs in B, the
// primary the customer picked and the Continuum names; the standby in A.
func TestReconcile_HotStandby_PickedPrimaryFirst_ActiveLegInPrimary(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology("bp-wp", "1.0.0", "", []string{"rtz-A", "rtz-B"})
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "active-hotstandby",
		[]string{testRegionB, testRegionA}, nil)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)

	reconcileFromCluster(t, r, "acme", "site")
	if phase, reason, msg := readPhaseAndReason(t, readApp(t, r, "acme", "site")); phase == PhaseFailed {
		t.Fatalf("unexpected Failed: %s %s", reason, msg)
	}
	inB, inA := hrClustersIn(t, fg, testRegionB), hrClustersIn(t, fg, testRegionA)
	if len(inB) != 1 || inB[0] != "rtz-A" {
		t.Fatalf("primary %s holds legs %v, want the active leg rtz-A", testRegionB, inB)
	}
	if len(inA) != 1 || inA[0] != "rtz-B" {
		t.Fatalf("standby %s holds legs %v, want the standby leg rtz-B", testRegionA, inA)
	}
}

// TestRegionFor_NoPickOrRegistryOrder_Unchanged — an Application that picks no
// region, or picks them in provisioning order, or names its clusters, maps
// exactly as before.
func TestRegionFor_NoPickOrRegistryOrder_Unchanged(t *testing.T) {
	r := &Reconciler{Cfg: Config{Regions: testRegions}}
	for _, picked := range [][]string{nil, {testRegionA}, {testRegionA, testRegionB}} {
		f := r.regionFor(nil, picked)
		if got := f("rtz-A"); got != testRegionA {
			t.Errorf("picked %v: rtz-A → %q, want %q", picked, got, testRegionA)
		}
		if got := f("mgmt-B"); got != testRegionB {
			t.Errorf("picked %v: mgmt-B → %q, want %q", picked, got, testRegionB)
		}
	}
	if got := pickedRegionsFor(appSpec{Regions: []string{testRegionB}, PlacementClusters: []string{"rtz-A"}}); got != nil {
		t.Errorf("an instance that names its clusters keeps the registry letters, got picked %v", got)
	}
	f := r.regionFor(nil, []string{testRegionB})
	if a, b := f("rtz-A"), f("rtz-B"); a != testRegionB || b != testRegionA {
		t.Errorf("picked [B]: rtz-A → %q, rtz-B → %q, want %q, %q", a, b, testRegionB, testRegionA)
	}
}
