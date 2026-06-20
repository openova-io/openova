package v1alpha1

import (
	"errors"
	"testing"
)

func p(region, cluster string, role DataRole, st StandbyType) PlacementTarget {
	return PlacementTarget{Region: region, Cluster: cluster, VCluster: "mgmt", Role: role, StandbyType: st}
}

func TestDerivePattern(t *testing.T) {
	cases := []struct {
		name    string
		targets []PlacementTarget
		cap     PlacementCapability
		want    Pattern
	}{
		{
			name:    "singleton — one Primary, no standby",
			targets: []PlacementTarget{p("region-a", "mgmt-A", DataRolePrimary, "")},
			cap:     CapabilityPrimaryStandby,
			want:    PatternSingleton,
		},
		{
			name: "active-hot-standby — Primary + Hot standby",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
			},
			cap:  CapabilityPrimaryStandby,
			want: PatternActiveHotStandby,
		},
		{
			name: "active-passive — Primary + Cold standby",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRoleStandby, StandbyCold),
			},
			cap:  CapabilityPrimaryStandby,
			want: PatternActivePassive,
		},
		{
			name: "active-active — two Primary",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRolePrimary, ""),
			},
			cap:  CapabilityMultiPrimary,
			want: PatternActiveActive,
		},
		{
			name: "active-hot-standby wins when a Hot AND a Cold standby exist",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRoleStandby, StandbyCold),
				p("region-c", "mgmt-C", DataRoleStandby, StandbyHot),
			},
			cap:  CapabilityPrimaryStandby,
			want: PatternActiveHotStandby,
		},
		{
			name:    "empty targets — singleton",
			targets: nil,
			cap:     CapabilityPrimaryStandby,
			want:    PatternSingleton,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DerivePattern(tc.targets, tc.cap); got != tc.want {
				t.Errorf("DerivePattern = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidatePlacement_MultiPrimaryGate(t *testing.T) {
	twoPrimary := Placement{Targets: []PlacementTarget{
		p("region-a", "mgmt-A", DataRolePrimary, ""),
		p("region-b", "mgmt-B", DataRolePrimary, ""),
	}}

	// primary+standby rejects a 2nd Primary with the canonical reason.
	err := ValidatePlacement(twoPrimary, CapabilityPrimaryStandby)
	var pe *PlacementError
	if !errors.As(err, &pe) || pe.Reason != MultiPrimaryNotSupportedReason {
		t.Fatalf("err = %v, want reason %s", err, MultiPrimaryNotSupportedReason)
	}

	// multi-primary accepts it.
	if err := ValidatePlacement(twoPrimary, CapabilityMultiPrimary); err != nil {
		t.Fatalf("multi-primary should accept 2 Primary: %v", err)
	}

	// empty capability defaults to primary+standby (rejects 2nd Primary).
	if err := ValidatePlacement(twoPrimary, ""); err == nil {
		t.Fatal("empty capability should default to primary+standby and reject 2 Primary")
	}
}

func TestValidatePlacement_RoleStandbyTypeInvariants(t *testing.T) {
	// Standby without a type → StandbyMissingType.
	err := ValidatePlacement(Placement{Targets: []PlacementTarget{
		p("region-a", "mgmt-A", DataRolePrimary, ""),
		p("region-b", "mgmt-B", DataRoleStandby, ""),
	}}, CapabilityPrimaryStandby)
	if err == nil {
		t.Fatal("Standby with no type should be rejected")
	}

	// Primary with a type → PrimaryHasStandbyType.
	err = ValidatePlacement(Placement{Targets: []PlacementTarget{
		p("region-a", "mgmt-A", DataRolePrimary, StandbyHot),
	}}, CapabilityPrimaryStandby)
	if err == nil {
		t.Fatal("Primary carrying a standbyType should be rejected")
	}

	// No Primary at all (but targets present) → NoPrimary.
	err = ValidatePlacement(Placement{Targets: []PlacementTarget{
		p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
	}}, CapabilityPrimaryStandby)
	if err == nil {
		t.Fatal("a placement with targets but no Primary should be rejected")
	}

	// A valid hot-standby placement passes.
	if err := ValidatePlacement(Placement{Targets: []PlacementTarget{
		p("region-a", "mgmt-A", DataRolePrimary, ""),
		p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
	}}, CapabilityPrimaryStandby); err != nil {
		t.Fatalf("valid hot-standby should pass: %v", err)
	}
}
