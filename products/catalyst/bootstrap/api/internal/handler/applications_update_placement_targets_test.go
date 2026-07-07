package handler

import (
	"reflect"
	"testing"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// TestApplicationUpdateNormalize_TargetsDeriveModeAndRegions (#3969 / row G10):
// the console PlacementEditor PUTs the per-region targets model with no
// mode/regions. applicationUpdateRequestNormalize must fold it onto the
// canonical spec.placement(mode)+spec.regions the CR stores — Mode via
// bpv1.DerivePattern, Regions Primary-first (regions[0]==primary) — so the
// switchover / add-target flows stop 400'ing on "placement.mode is required".
func TestApplicationUpdateNormalize_TargetsDeriveModeAndRegions(t *testing.T) {
	cases := []struct {
		name        string
		targets     []bpv1.PlacementTarget
		wantMode    string
		wantRegions []string
	}{
		{
			name:        "singleton (1 primary)",
			targets:     []bpv1.PlacementTarget{{Region: "me-east-215-a", Role: bpv1.DataRolePrimary}},
			wantMode:    "singleton",
			wantRegions: []string{"me-east-215-a"},
		},
		{
			name: "add-target: singleton -> active-hot-standby",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
			},
			wantMode:    "active-hot-standby",
			wantRegions: []string{"me-east-215-a", "me-east-215-b"},
		},
		{
			name: "switchover: primary flips to region-b (regions[0]==primary)",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-b", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-a", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
			},
			wantMode:    "active-hot-standby",
			wantRegions: []string{"me-east-215-b", "me-east-215-a"},
		},
		{
			name: "active-passive (cold standby)",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyCold},
			},
			wantMode:    "active-passive",
			wantRegions: []string{"me-east-215-a", "me-east-215-b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := applicationUpdateRequestNormalize(applicationUpdateRequest{
				Placement: &applicationPlacement{Targets: tc.targets},
			})
			if got.Placement == nil {
				t.Fatalf("placement dropped")
			}
			if got.Placement.Mode != tc.wantMode {
				t.Errorf("mode = %q, want %q", got.Placement.Mode, tc.wantMode)
			}
			if !reflect.DeepEqual(got.Placement.Regions, tc.wantRegions) {
				t.Errorf("regions = %v, want %v", got.Placement.Regions, tc.wantRegions)
			}
			// The whole point: validation must now PASS (previously 400'd
			// "placement.mode is required when placement is set").
			if msg, ok := validateApplicationUpdateRequest(got); !ok {
				t.Errorf("validation failed after targets fold: %s", msg)
			}
		})
	}
}

// TestApplicationUpdateNormalize_ExplicitModeWins: a legacy {mode,regions}
// caller (or one that sets both) is byte-unchanged — the targets fold only
// fires when Mode is empty.
func TestApplicationUpdateNormalize_ExplicitModeWins(t *testing.T) {
	got := applicationUpdateRequestNormalize(applicationUpdateRequest{
		Placement: &applicationPlacement{
			Mode:    "singleton",
			Regions: []string{"me-east-215-a"},
			Targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-b", Role: bpv1.DataRolePrimary},
			},
		},
	})
	if got.Placement.Mode != "singleton" {
		t.Errorf("explicit mode overwritten: got %q", got.Placement.Mode)
	}
	if !reflect.DeepEqual(got.Placement.Regions, []string{"me-east-215-a"}) {
		t.Errorf("explicit regions overwritten: got %v", got.Placement.Regions)
	}
}
