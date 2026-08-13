package handler

// UAT row 60 — "pick active-hot-standby → Provision → that app's Topology tab
// shows a 2-region pair (region-a primary + region-b replica + armed
// Switchover)".
//
// THE DEFECT. Both write doors validated `len(placement.regions) >= 1` for
// EVERY mode, so `{"mode":"active-hot-standby","regions":["one-region"]}` was
// accepted, persisted and reported back as a hot-standby Application. Nothing
// downstream can make that true — placement.Resolve puts regions[0] Primary
// and iterates regions[1..] for the standbys, so a one-region list yields zero
// standbys, and buildContinuumPlan then skips CR production precisely because
// `len(standbys) == 0`. No Continuum means nothing for the Topology tab's
// Switchover to arm against. Refs #6033.
//
// The failure was SILENT at every layer an operator can see: HTTP 201,
// `phase: Ready`, `spec.placement.mode: active-hot-standby` — with
// `status.perCluster[0].role: singleton` sitting next to it.
//
// These tests assert the RULE, both directions, on both doors. The negative
// cases matter as much as the positive ones: a gate that refuses everything
// would satisfy the "1 region is rejected" half on its own.

import (
	"strings"
	"testing"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

func row60InstallRequest(mode string, regions []string) applicationInstallRequest {
	return applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-postgres", Version: "1.2.3"},
		Name:            "ahs-pg",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement:       applicationPlacement{Mode: mode, Regions: regions},
	}
}

// TestInstallDoor_Row60_MultiRegionModeNeedsTwoRegions is the RED test for the
// create door — the one row 60's walk goes through.
func TestInstallDoor_Row60_MultiRegionModeNeedsTwoRegions(t *testing.T) {
	cases := []struct {
		name       string
		mode       string
		regions    []string
		wantReject bool
		why        string
	}{
		{
			name:       "active-hot-standby over ONE region is refused",
			mode:       "active-hot-standby",
			regions:    []string{"me-east-215-a"},
			wantReject: true,
			why:        "there is no second region for the standby, so no Continuum is minted and Switchover can never arm",
		},
		{
			name:       "active-passive over ONE region is refused",
			mode:       "active-passive",
			regions:    []string{"me-east-215-a"},
			wantReject: true,
			why:        "active-passive shares the primary+standby fan-out plan",
		},
		{
			name:       "active-active over ONE region is refused",
			mode:       "active-active",
			regions:    []string{"me-east-215-a"},
			wantReject: true,
			why:        "a single-region active-active is a singleton wearing another name",
		},
		{
			name:       "the LEGACY spelling is refused too",
			mode:       "active-hotstandby",
			regions:    []string{"me-east-215-a"},
			wantReject: true,
			why:        "the rule canonicalises first, so it cannot be side-stepped by spelling",
		},
		{
			name:       "TWO ENTRIES naming the SAME region is refused",
			mode:       "active-hot-standby",
			regions:    []string{"me-east-215-a", "me-east-215-a"},
			wantReject: true,
			why:        "two entries for one place is still one place; the rule counts DISTINCT regions",
		},
		// ── CONTROLS: what must keep working ──
		{
			name:       "CONTROL active-hot-standby over TWO DISTINCT regions is accepted",
			mode:       "active-hot-standby",
			regions:    []string{"me-east-215-a", "me-east-215-b"},
			wantReject: false,
			why:        "this is the shape row 60 needs to succeed; a gate that refused it would be worse than the defect",
		},
		{
			name:       "CONTROL singleton over ONE region is accepted",
			mode:       "singleton",
			regions:    []string{"me-east-215-a"},
			wantReject: false,
			why:        "singleton IS the one-region posture — the rule must discriminate by mode, not refuse every short list",
		},
		{
			name:       "CONTROL active-active over THREE regions is accepted",
			mode:       "active-active",
			regions:    []string{"me-east-215-a", "me-east-215-b", "me-east-215-c"},
			wantReject: false,
			why:        "the rule is a floor, not an equality",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, ok := validateApplicationInstallRequest(row60InstallRequest(tc.mode, tc.regions))
			if tc.wantReject && ok {
				t.Fatalf("install door ACCEPTED %s over %v — %s", tc.mode, tc.regions, tc.why)
			}
			if !tc.wantReject && !ok {
				t.Fatalf("install door REJECTED %s over %v (%s) — %s", tc.mode, tc.regions, msg, tc.why)
			}
			if tc.wantReject {
				// The message must NAME the problem and the remedy, or the
				// operator sees a 400 they cannot act on.
				if !strings.Contains(msg, "2 DISTINCT regions") {
					t.Errorf("rejection message does not name the rule: %q", msg)
				}
				if !strings.Contains(msg, "singleton") {
					t.Errorf("rejection message offers no remedy: %q", msg)
				}
			}
		})
	}
}

// TestUpdateDoor_Row60_SameRuleFromTheSameHelper covers the CHANGE door (row
// 16's Save), which reaches the rule by a different route: the console's
// PlacementEditor sends targets[] with no mode/regions, and
// applicationUpdateRequestNormalize folds them onto mode+regions via
// regionsFromPlacementTargets — which DEDUPES. So a Primary and a Standby that
// name the SAME region arrive at the validator as active-hot-standby over ONE
// region.
//
// That is not hypothetical: PlacementEditor.addTarget picks the new target's
// region as `availableRegions.find(unused) ?? availableRegions[1] ??
// availableRegions[0]`, so on a Sovereign reporting a single region the
// fallback lands on the SAME region as the primary, and both the editor's own
// validatePlacement and this door used to accept it.
func TestUpdateDoor_Row60_SameRuleFromTheSameHelper(t *testing.T) {
	// The exact editor body: targets only, no mode, no regions.
	sameRegion := applicationUpdateRequestNormalize(applicationUpdateRequest{
		Placement: &applicationPlacement{Targets: []bpv1.PlacementTarget{
			{Region: "me-east-215-a", Cluster: "c-a", VCluster: "host", Role: bpv1.DataRolePrimary},
			{Region: "me-east-215-a", Cluster: "c-a", VCluster: "host", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
		}},
	})

	// VACUITY CHECK — prove the fold really did produce the dangerous shape
	// (a multi-region posture over one region). Without this the rejection
	// below could be for any reason at all.
	if sameRegion.Placement.Mode != "active-hot-standby" {
		t.Fatalf("VACUITY: fold produced mode %q, want active-hot-standby", sameRegion.Placement.Mode)
	}
	if len(sameRegion.Placement.Regions) != 1 {
		t.Fatalf("VACUITY: fold produced regions %v, want exactly 1 (the dedupe is the point)",
			sameRegion.Placement.Regions)
	}

	if msg, ok := validateApplicationUpdateRequest(sameRegion); ok {
		t.Fatalf("update door ACCEPTED a hot-standby whose standby shares the primary's "+
			"region — the Save would persist a DR posture with no standby (msg=%q)", msg)
	}

	// CONTROL — the same editor body across TWO regions must still be
	// accepted, or the Topology-tab Save (row 16) breaks.
	twoRegions := applicationUpdateRequestNormalize(applicationUpdateRequest{
		Placement: &applicationPlacement{Targets: []bpv1.PlacementTarget{
			{Region: "me-east-215-a", Cluster: "c-a", VCluster: "host", Role: bpv1.DataRolePrimary},
			{Region: "me-east-215-b", Cluster: "c-b", VCluster: "host", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
		}},
	})
	if msg, ok := validateApplicationUpdateRequest(twoRegions); !ok {
		t.Fatalf("update door REJECTED the canonical 2-region hot-standby Save: %s", msg)
	}

	// CONTROL — a singleton edit over one region is untouched.
	singleton := applicationUpdateRequestNormalize(applicationUpdateRequest{
		Placement: &applicationPlacement{Targets: []bpv1.PlacementTarget{
			{Region: "me-east-215-a", Cluster: "c-a", VCluster: "host", Role: bpv1.DataRolePrimary},
		}},
	})
	if singleton.Placement.Mode != "singleton" {
		t.Fatalf("VACUITY: control fold produced mode %q, want singleton", singleton.Placement.Mode)
	}
	if msg, ok := validateApplicationUpdateRequest(singleton); !ok {
		t.Fatalf("update door REJECTED a singleton over one region: %s", msg)
	}
}

// TestPlacementRegionCountError_IsAFunctionOfTheMode pins the discrimination
// itself. `placementRegionCountError` returning "" for singleton is what makes
// it a RULE rather than a blanket refusal, and the frontend mirror
// (widgets/topology/modes MULTI_REGION_MODES) must agree with this set.
func TestPlacementRegionCountError_IsAFunctionOfTheMode(t *testing.T) {
	one := []string{"me-east-215-a"}
	for _, mode := range []string{"active-active", "active-hot-standby", "active-passive"} {
		if msg := placementRegionCountError(mode, one); msg == "" {
			t.Errorf("mode %q over one region returned no error — it is a multi-region class", mode)
		}
		if !placementModeRequiresMultipleRegions(mode) {
			t.Errorf("placementModeRequiresMultipleRegions(%q) = false, want true", mode)
		}
	}
	if msg := placementRegionCountError("singleton", one); msg != "" {
		t.Errorf("singleton over one region was refused: %q", msg)
	}
	if placementModeRequiresMultipleRegions("singleton") {
		t.Errorf("placementModeRequiresMultipleRegions(singleton) = true, want false")
	}
	// An unknown mode is NOT this rule's business — the vocabulary check owns
	// that verdict, and returning an error here would report the wrong cause.
	if msg := placementRegionCountError("not-a-mode", one); msg != "" {
		t.Errorf("unknown mode was refused by the REGION rule: %q", msg)
	}

	// DRIFT GATE — the partition is stated against the CANONICAL VOCABULARY
	// itself (placement_vocabulary_drift_test.go's canonicalPlacementVocabulary),
	// not against a second list typed here. A fifth canonical mode therefore
	// cannot be added without a deliberate decision about which side of this
	// line it falls on, and the frontend mirror asserts the identical partition
	// (widgets/topology/placement-vocabulary.drift.test.tsx).
	for _, mode := range canonicalPlacementVocabulary {
		want := mode != "singleton"
		if got := placementModeRequiresMultipleRegions(mode); got != want {
			t.Errorf("placementModeRequiresMultipleRegions(%q) = %v, want %v — every "+
				"canonical mode is either the one-region posture or a multi-region one",
				mode, got, want)
		}
	}
}
