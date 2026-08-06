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
			// #5515 — this case previously read `want: PatternSingleton`.
			// The suite ASSERTED the fail-open: "no placement data at all"
			// was pinned as identical to "one Primary, in one region,
			// deliberately, with no cross-region failover wanted".
			name:    "empty targets — not-reported, NEVER singleton",
			targets: nil,
			cap:     CapabilityPrimaryStandby,
			want:    PatternNotReported,
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

// TestDerivePattern_5515_NeverFailsOpen is the #5515 regression gate on the
// GO source of truth (the frontend mirror took the same guard in PR #5519 —
// this function is the one it declares itself a mirror OF, and it kept
// failing open for another six days).
//
// It asserts on the RETURNED VALUE in both directions, because either
// direction alone is vacuous here:
//
//   - the negative block alone would pass against a DerivePattern hard-wired
//     to `return PatternNotReported`;
//   - the positive block alone is what the pre-fix suite had, and it stayed
//     green for the entire life of the defect.
func TestDerivePattern_5515_NeverFailsOpen(t *testing.T) {
	// ── Negative: nothing derivable ⇒ not-reported, and specifically NOT a
	//    confident pattern name. ────────────────────────────────────────
	notDerivable := []struct {
		name    string
		targets []PlacementTarget
	}{
		{
			name:    "nil target list — the /placement endpoint reported nothing",
			targets: nil,
		},
		{
			name:    "empty target list",
			targets: []PlacementTarget{},
		},
		{
			name: "roles all unrecognised — same counters as empty",
			targets: []PlacementTarget{
				{Region: "region-a", Cluster: "mgmt-A", VCluster: "mgmt", Role: DataRole("")},
				{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt", Role: DataRole("Whatever")},
			},
		},
		{
			name: "Standby-only, Hot — an active-* pattern with no active",
			targets: []PlacementTarget{
				p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
			},
		},
		{
			name: "Standby-only, Cold",
			targets: []PlacementTarget{
				p("region-b", "mgmt-B", DataRoleStandby, StandbyCold),
			},
		},
		{
			name: "two Standbys, no Primary",
			targets: []PlacementTarget{
				p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
				p("region-c", "mgmt-C", DataRoleStandby, StandbyCold),
			},
		},
	}
	for _, tc := range notDerivable {
		t.Run("not-derivable/"+tc.name, func(t *testing.T) {
			got := DerivePattern(tc.targets, CapabilityPrimaryStandby)
			if got == PatternSingleton {
				t.Fatalf("#5515 FAIL-OPEN: DerivePattern returned the confident %q for a "+
					"target list with no Primary. `singleton` means \"one Primary, one "+
					"region, no cross-region failover, and that is fine\" — asserting it "+
					"here turns missing data into a healthy DR verdict.", got)
			}
			if got != PatternNotReported {
				t.Fatalf("DerivePattern = %q, want %q (no Primary ⇒ no pattern)", got, PatternNotReported)
			}
		})
	}

	// ── Control: a genuine placement still derives its real pattern. Without
	//    these, "always return not-reported" would pass the block above. ──
	derivable := []struct {
		name    string
		targets []PlacementTarget
		cap     PlacementCapability
		want    Pattern
	}{
		{
			name:    "genuine singleton — exactly one Primary, no standby",
			targets: []PlacementTarget{p("region-a", "mgmt-A", DataRolePrimary, "")},
			cap:     CapabilityPrimaryStandby,
			want:    PatternSingleton,
		},
		{
			name: "genuine active-hot-standby",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
			},
			cap:  CapabilityPrimaryStandby,
			want: PatternActiveHotStandby,
		},
		{
			name: "genuine active-passive",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRoleStandby, StandbyCold),
			},
			cap:  CapabilityPrimaryStandby,
			want: PatternActivePassive,
		},
		{
			name: "genuine active-active",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				p("region-b", "mgmt-B", DataRolePrimary, ""),
			},
			cap:  CapabilityMultiPrimary,
			want: PatternActiveActive,
		},
		{
			name: "one Primary alongside an unrecognised role still derives singleton",
			targets: []PlacementTarget{
				p("region-a", "mgmt-A", DataRolePrimary, ""),
				{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt", Role: DataRole("")},
			},
			cap:  CapabilityPrimaryStandby,
			want: PatternSingleton,
		},
	}
	for _, tc := range derivable {
		t.Run("derivable/"+tc.name, func(t *testing.T) {
			got := DerivePattern(tc.targets, tc.cap)
			if got == PatternNotReported {
				t.Fatalf("VACUITY: DerivePattern returned %q for a genuinely derivable "+
					"placement — a guard that always reports \"not reported\" is as "+
					"useless as one that always reports `singleton`", got)
			}
			if got != tc.want {
				t.Fatalf("DerivePattern = %q, want %q", got, tc.want)
			}
		})
	}

	// ── The token itself must not collide with a real pattern name, or the
	//    two blocks above could never disagree. ───────────────────────────
	for _, real := range []Pattern{PatternSingleton, PatternActivePassive, PatternActiveHotStandby, PatternActiveActive} {
		if PatternNotReported == real {
			t.Fatalf("PatternNotReported collides with the real pattern %q", real)
		}
	}
}

// TestDerivePattern_5515_AgreesWithValidatePlacement pins the two halves of
// the model against each other: every target list ValidatePlacement rejects
// as `NoPrimary` is exactly the set DerivePattern cannot derive. Before the
// fix they contradicted — the gate said "invalid, no Primary" while the label
// said "singleton", and a caller that trusted the label never saw the gate.
func TestDerivePattern_5515_AgreesWithValidatePlacement(t *testing.T) {
	noPrimary := Placement{Targets: []PlacementTarget{
		p("region-b", "mgmt-B", DataRoleStandby, StandbyHot),
	}}
	err := ValidatePlacement(noPrimary, CapabilityPrimaryStandby)
	if err == nil {
		t.Fatalf("ValidatePlacement must reject a Standby-only placement")
	}
	var pe *PlacementError
	if !errors.As(err, &pe) || pe.Reason != "NoPrimary" {
		t.Fatalf("ValidatePlacement reason = %v, want NoPrimary", err)
	}
	if got := DerivePattern(noPrimary.Targets, CapabilityPrimaryStandby); got != PatternNotReported {
		t.Fatalf("the gate rejects this placement as NoPrimary but the label calls it %q — "+
			"the two halves of the model disagree (#5515)", got)
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
