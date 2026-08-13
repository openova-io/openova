package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// #6268 / UAT row 60 — the Topology tab showed ONE card for an
// active-hot-standby app whose backing pair was fully healthy.
//
// Walked live on hw296 2026-08-14. Everything below the console was correct:
// primary + standby (not the two-primaries shape of #6200), armed=true, the
// Continuum CR Healthy and holding the lease, CNPG 3/3, the Application Ready.
// The tab still drew `Pattern: not reported`, one target card with no Primary,
// and a DISABLED Switchover control. The endpoint returned a single target
// carrying an empty `cluster` while its own `regionsObserved` said 2.
//
// Cause: derivePlacementTargets keys on the component's OWN pods. For an app
// whose stateful identity IS its CNPG pair, those pods carry the DATABASE's
// labels, so occupancy legitimately matched nothing and returned []. The
// standby augmentation then appended a follower to that empty list — emitting
// a Standby with no Primary, which renders identically to an honest
// single-region app. That indistinguishability is the defect, not the missing
// card: row 60 exists precisely to catch a two-region app collapsing to one.

// pairFixture builds a resolved 2-distinct-region CNPG pair — the state
// findCNPGPairForApp returns and the only state mergeCNPGPairIntoTargets is
// ever reached with.
func pairFixture() *cnpgPairState {
	return &cnpgPairState{
		PairName:           "dbapp-pair",
		Namespace:          "dbapp",
		PrimaryClusterName: "dbapp-db",
		ReplicaClusterName: "dbapp-db-replica",
		PrimaryRegion:      "me-east-215-a",
		ReplicaRegion:      "me-east-215-b",
	}
}

// THE ROW-60 CASE. Occupancy found nothing (the app's pods carry the database
// identity), so the pair must supply BOTH legs — not just the follower.
//
// Falsifiable: on pre-#6268 code this returns 1 target (the Standby alone) and
// every assertion below fails for the reason row 60 reported.
func TestRow60_PairWithNoOccupancy_EmitsBothLegs_6268(t *testing.T) {
	st := pairFixture()
	got := mergeCNPGPairIntoTargets(nil, st)

	if len(got) != 2 {
		t.Fatalf("#6268 row 60: got %d target(s) want 2 — a pair with no occupancy must emit BOTH legs, not just the follower (%+v)", len(got), got)
	}
	if !hasPrimaryTarget(got) {
		t.Fatalf("#6268 row 60: no Primary in %+v — this is the half-pair the Topology tab drew as one card with Switchover disabled", got)
	}
	if !hasStandbyTarget(got) {
		t.Fatalf("#6268 row 60: no Standby in %+v", got)
	}

	var primary, standby *bpv1.PlacementTarget
	for i := range got {
		switch got[i].Role {
		case bpv1.DataRolePrimary:
			primary = &got[i]
		case bpv1.DataRoleStandby:
			standby = &got[i]
		}
	}
	// The Primary must carry the pair's OWN primary half — region AND cluster.
	// An empty `cluster` was one of the two symptoms reported live.
	if primary.Region != st.PrimaryRegion {
		t.Fatalf("#6268: Primary region %q want %q", primary.Region, st.PrimaryRegion)
	}
	if primary.Cluster != st.PrimaryClusterName {
		t.Fatalf("#6268: Primary cluster %q want %q — an empty cluster is the live symptom", primary.Cluster, st.PrimaryClusterName)
	}
	if standby.Region != st.ReplicaRegion {
		t.Fatalf("#6268: Standby region %q want %q", standby.Region, st.ReplicaRegion)
	}
	if standby.StandbyType != bpv1.StandbyHot {
		t.Fatalf("#6268: Standby type %q want Hot", standby.StandbyType)
	}

	// The assertion row 60 actually makes: the chosen topology is rendered.
	if p := bpv1.DerivePattern(got, bpv1.CapabilityPrimaryStandby); p != bpv1.PatternActiveHotStandby {
		t.Fatalf("#6268 row 60: pattern %q want active-hot-standby — the tab showed `Pattern: not reported` (%+v)", p, got)
	}
}

// When occupancy DID supply a Primary, the pair must not add a second one —
// two Primaries is the #6200 shape this app is explicitly not in.
func TestRow60_PairWithExistingPrimary_DoesNotDuplicate_6268(t *testing.T) {
	st := pairFixture()
	occupancy := []bpv1.PlacementTarget{{
		Region:  st.PrimaryRegion,
		Cluster: "observed-from-pods",
		Role:    bpv1.DataRolePrimary,
	}}
	got := mergeCNPGPairIntoTargets(occupancy, st)

	if len(got) != 2 {
		t.Fatalf("#6268: got %d target(s) want 2 (%+v)", len(got), got)
	}
	primaries := 0
	for _, tg := range got {
		if tg.Role == bpv1.DataRolePrimary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("#6268: %d Primaries want 1 — the pair must not duplicate a leg occupancy already observed (%+v)", primaries, got)
	}
	// The observed leg wins: occupancy read real pods, the pair is the fallback.
	if got[0].Cluster != "observed-from-pods" {
		t.Fatalf("#6268: occupancy-derived Primary was overwritten (%+v)", got)
	}
}

// The pre-existing guard must survive the change: a Standby already present in
// the replica region means there is nothing to add, and the list is returned
// untouched. #6268 must not make this path emit a Primary.
func TestRow60_ExistingStandbyInReplicaRegion_Unchanged_6268(t *testing.T) {
	st := pairFixture()
	occupancy := []bpv1.PlacementTarget{{
		Region:      st.ReplicaRegion,
		Cluster:     "observed-replica",
		Role:        bpv1.DataRoleStandby,
		StandbyType: bpv1.StandbyHot,
	}}
	got := mergeCNPGPairIntoTargets(occupancy, st)

	if len(got) != 1 {
		t.Fatalf("#6268: got %d target(s) want 1 — an already-represented replica region must short-circuit unchanged (%+v)", len(got), got)
	}
}

// CALL SITE, not just the helper. Drives the real HTTP handler with a fixture
// whose ONLY matching pod is a CNPG replica — occupancy therefore produces a
// Standby and no Primary, and no dynamic client exists to resolve a pair. The
// endpoint must refuse to call that a runtime observation.
//
// Without this the projection answers one Standby card with
// `derivedFromRuntime: true`, which the FE trusts over its own spec/status
// fallback — a two-region app rendered as one region, asserted as observed.
func TestRow60_StandbyWithoutPrimary_NotDerivedFromRuntime_6268(t *testing.T) {
	depID := "dep-row60-halfpair"
	regionA, regionB := "me-east-215-a", "me-east-215-b"
	h := newPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{}, // primary half not visible from this apiserver
		[]*unstructured.Unstructured{placementFixturePod("dbapp", "dbapp-db-2", "dbapp-db", regionB, "replica")},
	)

	resp := callPlacement(t, h, depID, "dbapp-db")

	if len(resp.Targets) != 1 || resp.Targets[0].Role != bpv1.DataRoleStandby {
		t.Fatalf("#6268 fixture did not produce the half-pair it is testing — got %+v; the test would pass on anything", resp.Targets)
	}
	if !resp.UnresolvedPrimary {
		t.Fatalf("#6268: unresolvedPrimary=false for a Standby-with-no-Primary projection (%+v)", resp.Targets)
	}
	if resp.DerivedFromRuntime {
		t.Fatalf("#6268: derivedFromRuntime=true for a half-pair — the FE would draw a false singleton instead of keeping its spec/status fallback (%+v)", resp.Targets)
	}
}

// CONTROL — the new flag must not fire on healthy placements, or it would
// disable `derivedFromRuntime` across the board and silently move every app
// onto the legacy fallback. An honest single-region app has a Primary and no
// Standby: not a half-pair, and still a genuine runtime observation.
func TestRow60_HonestSingleton_StillDerived_Control_6268(t *testing.T) {
	depID := "dep-row60-control"
	regionA, regionB := "me-east-215-a", "me-east-215-b"
	h := newPlacementHandler(t, depID, regionA, regionB,
		[]*unstructured.Unstructured{placementFixturePod("apps", "solo-xyz", "solo", regionA, "")},
		[]*unstructured.Unstructured{},
	)

	resp := callPlacement(t, h, depID, "solo")

	if resp.UnresolvedPrimary {
		t.Fatalf("#6268 control: unresolvedPrimary fired on an honest singleton (%+v)", resp.Targets)
	}
	if !resp.DerivedFromRuntime {
		t.Fatalf("#6268 control: derivedFromRuntime went false on an honest singleton — the fix would move every app to the legacy fallback (%+v)", resp.Targets)
	}
}

// CONTROL — an empty projection is "nothing observed", NOT a half-pair.
// Collapsing the two would re-introduce the same indistinguishability one
// level up: `targets: []` is honest and must keep derivedFromRuntime=true
// (TestPlacementRuntime_Unknown_EmptyTargets asserts that contract).
func TestRow60_EmptyProjection_IsNotAHalfPair_Control_6268(t *testing.T) {
	if hasStandbyTarget(nil) {
		t.Fatal("#6268: an empty list must not report a Standby")
	}
	if hasPrimaryTarget(nil) {
		t.Fatal("#6268: an empty list must not report a Primary")
	}
}
