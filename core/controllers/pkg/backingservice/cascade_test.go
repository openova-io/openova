package backingservice

import (
	"testing"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// consumerHotStandby is keycloak's resolved placement: Primary region-a,
// Hot standby region-b (#3969 §5 concrete Hot example).
func consumerHotStandby() []bpv1.PlacementTarget {
	return []bpv1.PlacementTarget{
		{Region: "region-a", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1.DataRolePrimary},
		{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
	}
}

// TestGenerate_OwnedDepFollowsByDefault — an owned (private) instance's
// placement defaults to the consumer's targets (#3969 §8a/§8b).
func TestGenerate_OwnedDepFollowsByDefault(t *testing.T) {
	b, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres"}, // private (default) → keycloak-pg
		Input{
			ConsumerBlueprint: "bp-keycloak",
			ConsumerName:      "keycloak",
			ConsumerNamespace: "keycloak",
			Targets:           consumerHotStandby(),
		},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.InstanceName != "keycloak-pg" {
		t.Fatalf("instance = %q, want keycloak-pg", b.InstanceName)
	}
	if !b.Followed {
		t.Error("owned dep should follow by default")
	}
	if len(b.InstanceTargets) != 2 {
		t.Fatalf("InstanceTargets = %d, want 2 (cascaded from consumer)", len(b.InstanceTargets))
	}
	// Primary region-a, Standby(Hot) region-b — cascaded verbatim.
	if b.InstanceTargets[0].Region != "region-a" || b.InstanceTargets[0].Role != bpv1.DataRolePrimary {
		t.Errorf("target[0] = %+v, want region-a Primary", b.InstanceTargets[0])
	}
	if b.InstanceTargets[1].Region != "region-b" || b.InstanceTargets[1].Role != bpv1.DataRoleStandby ||
		b.InstanceTargets[1].StandbyType != bpv1.StandbyHot {
		t.Errorf("target[1] = %+v, want region-b Standby Hot", b.InstanceTargets[1])
	}
	if got := bpv1.DerivePattern(b.InstanceTargets, bpv1.CapabilityPrimaryStandby); got != bpv1.PatternActiveHotStandby {
		t.Errorf("derived pattern = %q, want active-hot-standby", got)
	}
}

// TestGenerate_OwnedDepDecoupled — Follow:false pins the owned instance
// (no cascade) (#3969 §8b switchover decouple).
func TestGenerate_OwnedDepDecoupled(t *testing.T) {
	b, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres"},
		Input{
			ConsumerName: "keycloak",
			Targets:      consumerHotStandby(),
			OwnedOverrides: []bpv1.OwnedDependencyOverride{
				{Name: "keycloak-pg", Follow: false},
			},
		},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.Followed {
		t.Error("decoupled owned dep must NOT be marked followed")
	}
	if len(b.InstanceTargets) != 0 {
		t.Errorf("decoupled owned dep must carry no cascaded targets, got %d", len(b.InstanceTargets))
	}
}

// TestGenerate_SwitchoverFlipsCascadedPrimary — re-running the resolver
// with flipped roles flips the owned instance's primary by default
// (#3969 §8b, DoD item 3). The prior Binding is NOT mutated (clone).
func TestGenerate_SwitchoverFlipsCascadedPrimary(t *testing.T) {
	before := consumerHotStandby()
	b1, _ := Generate(bpv1.BackingServiceSpec{Type: "postgres"},
		Input{ConsumerName: "keycloak", Targets: before})

	// Switchover: flip region-a→Standby(Hot), region-b→Primary.
	after := []bpv1.PlacementTarget{
		{Region: "region-a", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1.DataRoleStandby, StandbyType: bpv1.StandbyHot},
		{Region: "region-b", Cluster: "mgmt-B", VCluster: "mgmt", Role: bpv1.DataRolePrimary},
	}
	b2, _ := Generate(bpv1.BackingServiceSpec{Type: "postgres"},
		Input{ConsumerName: "keycloak", Targets: after})

	if b2.InstanceTargets[1].Region != "region-b" || b2.InstanceTargets[1].Role != bpv1.DataRolePrimary {
		t.Errorf("post-switchover Primary = %+v, want region-b Primary", b2.InstanceTargets[1])
	}
	// b1 must be untouched (no aliasing).
	if b1.InstanceTargets[0].Role != bpv1.DataRolePrimary || b1.InstanceTargets[0].Region != "region-a" {
		t.Errorf("prior binding mutated: %+v", b1.InstanceTargets[0])
	}
}

// TestGenerate_SharedDepNeverCascades — a shared binding never cascades
// the consumer's placement onto the instance (#3969 §8e).
func TestGenerate_SharedDepNeverCascades(t *testing.T) {
	b, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres", Mode: bpv1.BackingServiceModeShared, InstanceRef: "shared-pg", Database: "kc"},
		Input{ConsumerName: "keycloak", Targets: consumerHotStandby()},
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if b.Followed {
		t.Error("shared binding must NOT be marked followed")
	}
	if len(b.InstanceTargets) != 0 {
		t.Errorf("shared binding must carry no cascaded targets, got %d", len(b.InstanceTargets))
	}
}

// TestGenerate_SharedDepSoftRecommendation — an incompatible shared
// placement yields a recommendation, NEVER an error (#3969 §8e, DoD 6).
func TestGenerate_SharedDepSoftRecommendation(t *testing.T) {
	// Consumer Primary in region-b; shared instance only in region-a.
	consumer := []bpv1.PlacementTarget{
		{Region: "region-b", Cluster: "mgmt-B", Role: bpv1.DataRolePrimary},
	}
	b, err := Generate(
		bpv1.BackingServiceSpec{Type: "postgres", Mode: bpv1.BackingServiceModeShared, InstanceRef: "shared-pg", Database: "kc"},
		Input{
			ConsumerName: "keycloak",
			Targets:      consumer,
			SharedInstanceTargets: map[string][]bpv1.PlacementTarget{
				"shared-pg": {{Region: "region-a", Cluster: "mgmt-A", Role: bpv1.DataRolePrimary}},
			},
		},
	)
	if err != nil {
		t.Fatalf("incompatible shared placement must NOT error (recommend only): %v", err)
	}
	if len(b.Recommendations) == 0 {
		t.Fatal("expected a soft recommendation for incompatible shared placement")
	}

	// Compatible case → no recommendation.
	b2, _ := Generate(
		bpv1.BackingServiceSpec{Type: "postgres", Mode: bpv1.BackingServiceModeShared, InstanceRef: "shared-pg", Database: "kc"},
		Input{
			ConsumerName: "keycloak",
			Targets:      consumer,
			SharedInstanceTargets: map[string][]bpv1.PlacementTarget{
				"shared-pg": {{Region: "region-b", Cluster: "mgmt-B", Role: bpv1.DataRolePrimary}},
			},
		},
	)
	if len(b2.Recommendations) != 0 {
		t.Errorf("compatible shared placement should yield no recommendation, got %v", b2.Recommendations)
	}
}
