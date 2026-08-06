// placement_notreported_5515_test.go — #5515.
//
// `bpv1alpha1.DerivePattern` no longer fails open into `singleton` for a
// target list with no Primary; it returns `PatternNotReported`. This file
// pins the two consequences of that inside the application-controller's
// fan-out, both of which are load-bearing and neither of which the model
// test can see:
//
//  1. `patternToBcpTopology` has no honest counterpart for the new token —
//     BcpTopology carries no "unknown" member — so the safety of the mapping
//     rests ENTIRELY on `not-reported` never reaching it. That is an
//     ORDERING claim about the reconcile loop, so it is pinned as one.
//
//  2. The four real patterns must still map to their real BcpTopology, or
//     "map everything to singleton" would satisfy (1) trivially.
package controller

import (
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// TestPatternNotReported_5515_UnreachableInFanout pins the ordering the
// fan-out's safety depends on: application_controller.go runs
// ValidatePlacement at step 5.1 (and markFailed's on its error) BEFORE
// placementVariantFromTargets at §8c. `not-reported` is derived only from a
// no-Primary target list, and ValidatePlacement rejects exactly that list as
// `NoPrimary` whenever any target is declared — so no reconcile can carry an
// un-derivable pattern into patternToBcpTopology, where it would be mapped
// onto `singleton` and re-open #5515 one layer down.
//
// The empty list is the other half: it derives `not-reported` and
// ValidatePlacement legitimately accepts it (nothing was declared), so the
// fan-out is instead gated by `fanoutFromTargets := len(...) > 0`, which is
// asserted here too.
func TestPatternNotReported_5515_UnreachableInFanout(t *testing.T) {
	std := bpv1alpha1.CapabilityPrimaryStandby

	// Every non-empty target list that derives `not-reported` must be
	// rejected by the step-5.1 gate.
	noPrimaryLists := map[string][]bpv1alpha1.PlacementTarget{
		"standby only (Hot)": {
			{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt",
				Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
		},
		"standby only (Cold)": {
			{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt",
				Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyCold},
		},
		"unrecognised roles": {
			{Region: "region-a", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1alpha1.DataRole("")},
		},
	}
	for name, targets := range noPrimaryLists {
		t.Run("gated/"+name, func(t *testing.T) {
			if got := bpv1alpha1.DerivePattern(targets, std); got != bpv1alpha1.PatternNotReported {
				t.Fatalf("precondition: DerivePattern = %q, want %q", got, bpv1alpha1.PatternNotReported)
			}
			if err := bpv1alpha1.ValidatePlacement(bpv1alpha1.Placement{Targets: targets}, std); err == nil {
				t.Fatalf("#5515: this list derives `not-reported` but ValidatePlacement ACCEPTS it, "+
					"so it reaches patternToBcpTopology, which maps it onto %q — the fail-open "+
					"is back one layer down", bpv1alpha1.BcpSingleton)
			}
		})
	}

	// The empty list derives `not-reported` and is legitimately valid; the
	// fan-out's own len>0 guard is what keeps it out.
	t.Run("gated/empty list is valid but never fans out", func(t *testing.T) {
		var empty []bpv1alpha1.PlacementTarget
		if got := bpv1alpha1.DerivePattern(empty, std); got != bpv1alpha1.PatternNotReported {
			t.Fatalf("precondition: DerivePattern = %q, want %q", got, bpv1alpha1.PatternNotReported)
		}
		if err := bpv1alpha1.ValidatePlacement(bpv1alpha1.Placement{Targets: empty}, std); err != nil {
			t.Fatalf("an empty placement declares nothing and must stay valid, got %v", err)
		}
		if len(empty) > 0 {
			t.Fatalf("the fan-out guard `len(spec.PlacementTargets) > 0` must exclude the empty list")
		}
	})

	// Control — the four real patterns still map to their real topology, so
	// a patternToBcpTopology that returned `singleton` for everything (which
	// would make the assertions above vacuous) fails here.
	for pattern, want := range map[bpv1alpha1.Pattern]bpv1alpha1.BcpTopology{
		bpv1alpha1.PatternSingleton:        bpv1alpha1.BcpSingleton,
		bpv1alpha1.PatternActivePassive:    bpv1alpha1.BcpActivePassive,
		bpv1alpha1.PatternActiveHotStandby: bpv1alpha1.BcpActiveHotStandby,
		bpv1alpha1.PatternActiveActive:     bpv1alpha1.BcpActiveActive,
	} {
		if got := patternToBcpTopology(pattern); got != want {
			t.Errorf("patternToBcpTopology(%q) = %q, want %q", pattern, got, want)
		}
	}
}

// TestSynthesizeSwitchover_5515_NotReportedNeverArms — the fan-out arms the
// per-app Continuum lease-witness off the DERIVED pattern. Pre-fix, a
// Standby-only target list derived `active-hot-standby` (the `standbys == 0`
// arm was skipped), i.e. a DR contract with no primary to fail over FROM.
// Post-fix it derives `not-reported` → BcpSingleton → no Switchover. Pinned
// with the DR-capable control beside it so "never arm anything" fails.
func TestSynthesizeSwitchover_5515_NotReportedNeverArms(t *testing.T) {
	standbyOnly := []bpv1alpha1.PlacementTarget{
		{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt",
			Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
	}
	choice := patternToBcpTopology(bpv1alpha1.DerivePattern(standbyOnly, bpv1alpha1.CapabilityPrimaryStandby))
	if sw, _ := synthesizeSwitchover(standbyOnly, choice, nil); sw != nil {
		t.Errorf("a Standby-only placement has no primary to fail over from; it must not arm a "+
			"switchover (choice=%q)", choice)
	}

	// Control — a genuine Primary + Hot Standby still arms.
	drCapable := []bpv1alpha1.PlacementTarget{
		{Region: "region-a", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1alpha1.DataRolePrimary},
		{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt",
			Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
	}
	drChoice := patternToBcpTopology(bpv1alpha1.DerivePattern(drCapable, bpv1alpha1.CapabilityPrimaryStandby))
	if drChoice != bpv1alpha1.BcpActiveHotStandby {
		t.Fatalf("control: derived choice = %q, want %q", drChoice, bpv1alpha1.BcpActiveHotStandby)
	}
	if sw, _ := synthesizeSwitchover(drCapable, drChoice, nil); sw == nil {
		t.Errorf("VACUITY: a genuine Primary + Hot Standby placement must still arm a switchover")
	}
}
