// applications_update_placement_5515_test.go — #5515.
//
// The targets→mode fold in applicationUpdateRequestNormalize is the one
// DerivePattern call site whose output is PERSISTED: it becomes
// `spec.placement.mode` on the Application CR, and the CRD puts no enum on
// that field, so whatever the fold writes is what every downstream reader
// (the DR projection, the Topology tab, the showback rollups) believes.
//
// Pre-fix, DerivePattern returned a confident `singleton` for a target list
// with no Primary, so PUTting such a list stored a deliberate single-region
// DR posture for a placement the model's own gate rejects as `NoPrimary`.
// The fold now skips a non-derivable pattern, leaving Mode empty so the
// request fails CLOSED on the existing "placement.mode is required when
// placement is set" 400 — which names the problem instead of persisting one.
package handler

import (
	"strings"
	"testing"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func TestApplicationUpdateNormalize_5515_NoPrimaryNeverPersistsSingleton(t *testing.T) {
	noPrimary := []struct {
		name    string
		targets []bpv1.PlacementTarget
	}{
		{
			name: "standby only (Hot)",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-b", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
			},
		},
		{
			name: "standby only (Cold)",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-b", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyCold},
			},
		},
		{
			name: "unrecognised role",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Role: bpv1.DataRole("")},
			},
		},
	}
	for _, tc := range noPrimary {
		t.Run("no-primary/"+tc.name, func(t *testing.T) {
			got := applicationUpdateRequestNormalize(applicationUpdateRequest{
				Placement: &applicationPlacement{Targets: tc.targets},
			})
			if got.Placement == nil {
				t.Fatalf("placement dropped")
			}
			if got.Placement.Mode == "singleton" {
				t.Fatalf("#5515: a placement with NO Primary target was folded onto the stored "+
					"mode %q — the CR now records a deliberate single-region posture for a "+
					"placement ValidatePlacement rejects as NoPrimary", got.Placement.Mode)
			}
			if got.Placement.Mode != "" {
				t.Fatalf("mode = %q, want empty so the request fails closed on the "+
					"\"placement.mode is required\" 400", got.Placement.Mode)
			}
			// Fails CLOSED, with an actionable message — not silently accepted.
			msg, ok := validateApplicationUpdateRequest(got)
			if ok {
				t.Fatalf("a non-derivable placement must not validate")
			}
			if !strings.Contains(msg, "placement.mode is required") {
				t.Errorf("rejection message = %q, want the actionable placement.mode text", msg)
			}
		})
	}

	// ── Control: every derivable placement still folds exactly as before.
	//    Without this block, a fold that never wrote a mode would pass. ──
	derivable := []struct {
		name     string
		targets  []bpv1.PlacementTarget
		wantMode string
	}{
		{
			name:     "genuine singleton still folds to singleton",
			targets:  []bpv1.PlacementTarget{{Region: "me-east-215-a", Role: bpv1.DataRolePrimary}},
			wantMode: "singleton",
		},
		{
			name: "genuine active-hot-standby",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
			},
			wantMode: "active-hot-standby",
		},
		{
			name: "genuine active-passive",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyCold},
			},
			wantMode: "active-passive",
		},
		{
			name: "genuine active-active",
			targets: []bpv1.PlacementTarget{
				{Region: "me-east-215-a", Role: bpv1.DataRolePrimary},
				{Region: "me-east-215-b", Role: bpv1.DataRolePrimary},
			},
			wantMode: "active-active",
		},
	}
	for _, tc := range derivable {
		t.Run("derivable/"+tc.name, func(t *testing.T) {
			got := applicationUpdateRequestNormalize(applicationUpdateRequest{
				Placement: &applicationPlacement{Targets: tc.targets},
			})
			if got.Placement == nil {
				t.Fatalf("placement dropped")
			}
			if got.Placement.Mode != tc.wantMode {
				t.Fatalf("VACUITY CHECK: mode = %q, want %q — the fold must still fire for "+
					"every derivable placement", got.Placement.Mode, tc.wantMode)
			}
			if len(got.Placement.Regions) == 0 {
				t.Errorf("regions fold must still fire for a derivable placement")
			}
			if msg, ok := validateApplicationUpdateRequest(got); !ok {
				t.Errorf("validation failed after targets fold: %s", msg)
			}
		})
	}
}
