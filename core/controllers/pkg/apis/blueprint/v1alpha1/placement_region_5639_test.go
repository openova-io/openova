// placement_region_5639_test.go — #5639 guard on the SERVER side of the
// same gate, both directions.
//
// The frontend `validatePlacement` in
// products/catalyst/bootstrap/ui/src/shared/lib/placement.ts documents
// itself as "the FE gate, mirroring Go ValidatePlacement". Before #5639
// NEITHER side looked at Target.Region, so a placement target with an empty
// region passed admission AND the reconciler and landed as
//
//	openova.io/region: ""
//	nodeAffinity ... values: [""]
//
// which is what left hw292's per-Org bp-postgres Pending for 2d9h while the
// HelmRelease reported install succeeded. A region no node carries is not a
// slow schedule, it is an impossible one — and the install still reports
// success, so every status surface stays green over a dark database.
//
// Fixing only the console leaves the API accepting the same object from Git
// or a direct PUT, so the gate has to live here as well.
package v1alpha1

import (
	"errors"
	"testing"
)

func reasonOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return ""
	}
	var pe *PlacementError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v is not a *PlacementError", err)
	}
	return pe.Reason
}

// The defect direction: a declared target with no region is REJECTED, with a
// stable reason naming the offending target.
func TestValidatePlacement_5639_EmptyRegionRejected(t *testing.T) {
	cases := []struct {
		name    string
		targets []PlacementTarget
	}{
		{
			name: "sole Primary carries no region",
			targets: []PlacementTarget{
				{Region: "", Cluster: "c-a", VCluster: "mgmt", Role: DataRolePrimary},
			},
		},
		{
			name: "whitespace-only region is not a region",
			targets: []PlacementTarget{
				{Region: "   ", Cluster: "c-a", VCluster: "mgmt", Role: DataRolePrimary},
			},
		},
		{
			// The hw292 shape exactly: a good Primary and a Hot Standby that
			// the editor could not assign a second region to.
			name: "active-hot-standby standby carries no region",
			targets: []PlacementTarget{
				{Region: "hw-me-east-215-a-rtz-prod", Cluster: "c-a", VCluster: "mgmt", Role: DataRolePrimary},
				{Region: "", Cluster: "c-b", VCluster: "mgmt", Role: DataRoleStandby, StandbyType: StandbyHot},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePlacement(Placement{Targets: tc.targets}, CapabilityPrimaryStandby)
			if err == nil {
				t.Fatalf("ValidatePlacement accepted a target with no region — that renders "+
					"openova.io/region In [\"\"], which no node can satisfy (#5639); targets=%+v", tc.targets)
			}
			if got := reasonOf(t, err); got != TargetMissingRegionReason {
				t.Errorf("reason = %q, want %q", got, TargetMissingRegionReason)
			}
		})
	}
}

// NON-VACUITY. Everything below must stay green: a gate that rejected every
// placement would satisfy the assertions above while breaking placement
// entirely. These are the cases an operator legitimately relies on.
func TestValidatePlacement_5639_RealRegionsStillAccepted(t *testing.T) {
	cases := []struct {
		name    string
		targets []PlacementTarget
		cap     PlacementCapability
	}{
		{
			// singleton on a single-region Sovereign — the case the fix must
			// never block.
			name: "singleton with one real region",
			targets: []PlacementTarget{
				{Region: "hw-me-east-215-a-rtz-prod", Cluster: "c-a", VCluster: "mgmt", Role: DataRolePrimary},
			},
			cap: CapabilityPrimaryStandby,
		},
		{
			name: "active-hot-standby with two real regions",
			targets: []PlacementTarget{
				{Region: "hw-me-east-215-a-rtz-prod", Cluster: "c-a", VCluster: "mgmt", Role: DataRolePrimary},
				{Region: "hw-me-east-215-b-rtz-prod", Cluster: "c-b", VCluster: "mgmt", Role: DataRoleStandby, StandbyType: StandbyHot},
			},
			cap: CapabilityPrimaryStandby,
		},
		{
			name: "active-active with two Primary targets on a multi-primary blueprint",
			targets: []PlacementTarget{
				{Region: "hw-me-east-215-a-rtz-prod", Cluster: "c-a", VCluster: "mgmt", Role: DataRolePrimary},
				{Region: "hw-me-east-215-b-rtz-prod", Cluster: "c-b", VCluster: "mgmt", Role: DataRolePrimary},
			},
			cap: CapabilityMultiPrimary,
		},
		{
			// The bootstrap-owned host placement uses a non-region-shaped
			// token; it is still a NON-EMPTY region and must stay accepted.
			name: "platform-bootstrap-owned-host token is a region for this gate",
			targets: []PlacementTarget{
				{Region: "platform-bootstrap-owned-host", Cluster: "host", VCluster: "host", Role: DataRolePrimary},
			},
			cap: CapabilityPrimaryStandby,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePlacement(Placement{Targets: tc.targets}, tc.cap); err != nil {
				t.Fatalf("ValidatePlacement rejected a valid placement: %v", err)
			}
		})
	}
}

// An EMPTY target list stays a no-op — the legacy posture-string path drives
// the fan-out unchanged, and admission short-circuits on it. Turning "no
// targets" into an error here would reject every Application that has not
// adopted the #3969 target model.
func TestValidatePlacement_5639_NoTargetsIsStillANoOp(t *testing.T) {
	if err := ValidatePlacement(Placement{Targets: nil}, CapabilityPrimaryStandby); err != nil {
		t.Fatalf("an empty target list must remain valid, got %v", err)
	}
}

// The reason must be distinguishable from the pre-existing ones, otherwise a
// caller mapping it onto an Application condition cannot tell an unschedulable
// region from a multi-Primary violation.
func TestValidatePlacement_5639_ReasonIsDistinct(t *testing.T) {
	for _, other := range []string{"InvalidRole", "PrimaryHasStandbyType", "StandbyMissingType", "NoPrimary", MultiPrimaryNotSupportedReason} {
		if TargetMissingRegionReason == other {
			t.Errorf("TargetMissingRegionReason collides with the existing reason %q", other)
		}
	}
}
