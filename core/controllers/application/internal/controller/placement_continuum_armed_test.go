// placement_continuum_arm_test.go — #3969 §13.
//
// The §13 seam this exercises: the per-app Continuum lease-witness DR contract
// must be ARMED off the canonical desired-state placement (spec.placement.
// targets[]), NOT only off the deprecated Blueprint.spec.topology posture. The
// synthetic fan-out variant (placementVariantFromTargets) carries a synthesized
// Switchover block when — and only when — the targets describe a DR-capable
// placement (Primary + ≥1 Standby), so the SAME producer (buildContinuumPlan)
// mints the DR contract that makes a Hot standby armed for promotion.
//
// What we assert:
//
//  1. A {Primary + Hot Standby} placement synthesizes a Switchover block and
//     feeds buildContinuumPlan a DR contract (the §13 arming).
//  2. The Blueprint's declared replication backend (cnpg-pair) is carried
//     forward into the synthesized mechanism; absent ⇒ the generic stateless
//     Promoter.
//  3. A singleton (lone Primary) and an active-active (2 Primary) placement
//     synthesize NO Switchover — the producer skips them (no DR to arm).
//  4. A {Primary + Cold Standby} placement (active-passive) still arms a
//     contract (the standby region exists) but resolves to stateless when the
//     Blueprint declares no streaming backend.
package controller

import (
	"testing"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// twoRegionPlanAB is the resolved plan the producer reads (primary region-a,
// standby region-b) — the producer keys hotStandbyRegions off it.
func twoRegionPlanAB() placement.Plan {
	return placement.Plan{
		PrimaryRegion: "hetzner-fsn-rtz-prod",
		Regions: []placement.RegionPlan{
			{Name: "hetzner-fsn-rtz-prod", Role: placement.RolePrimary},
			{Name: "hetzner-nbg-rtz-prod", Role: placement.RoleStandby, Standby: true},
		},
	}
}

func hotStandbyTargets() []bpv1alpha1.PlacementTarget {
	return []bpv1alpha1.PlacementTarget{
		{Region: "hetzner-fsn-rtz-prod", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1alpha1.DataRolePrimary},
		{Region: "hetzner-nbg-rtz-prod", Cluster: "mgmt-B", VCluster: "mgmt", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
	}
}

// TestPlacementVariantFromTargets_ArmsContinuum_HotStandby — the central §13
// acceptance: a desired-state Hot-standby placement synthesizes a Switchover
// block so buildContinuumPlan mints a DR contract.
func TestPlacementVariantFromTargets_ArmsContinuum_HotStandby(t *testing.T) {
	choice, variant := placementVariantFromTargets(
		hotStandbyTargets(), bpv1alpha1.CapabilityPrimaryStandby, nil)

	if choice != bpv1alpha1.BcpActiveHotStandby {
		t.Fatalf("choice = %q, want active-hot-standby", choice)
	}
	if variant.Switchover == nil || variant.Switchover.Mechanism == "" {
		t.Fatalf("Hot-standby placement must synthesize a Switchover block, got %+v", variant.Switchover)
	}

	// The producer must now mint a DR contract off the synthetic variant.
	cp, ok := buildContinuumPlan("acme/obs", choice, variant, twoRegionPlanAB(), nil)
	if !ok {
		t.Fatal("buildContinuumPlan skipped a Hot-standby placement — the §13 arming did not reach the producer")
	}
	if cp.PrimaryRegion != "hetzner-fsn-rtz-prod" {
		t.Errorf("primaryRegion = %q, want hetzner-fsn-rtz-prod", cp.PrimaryRegion)
	}
	if len(cp.StandbyRegions) != 1 || cp.StandbyRegions[0] != "hetzner-nbg-rtz-prod" {
		t.Errorf("standbyRegions = %v, want [hetzner-nbg-rtz-prod]", cp.StandbyRegions)
	}
	// No Blueprint replication backend declared ⇒ generic stateless Promoter.
	if cp.Mechanism != "stateless" {
		t.Errorf("mechanism = %q, want stateless (no declared backend)", cp.Mechanism)
	}
}

// TestPlacementVariantFromTargets_CarriesCNPGBackend — when the Blueprint
// still declares a cnpg-pair replication backend for this pattern, the
// synthesized switchover resolves to cnpg-pair (the CNPG primary streaming
// flip), not the generic stateless Promoter.
func TestPlacementVariantFromTargets_CarriesCNPGBackend(t *testing.T) {
	bpTopo := &bpv1alpha1.Topology{
		Supported: []bpv1alpha1.BcpTopology{bpv1alpha1.BcpActiveHotStandby},
		PerTopology: map[bpv1alpha1.BcpTopology]bpv1alpha1.TopologyVariant{
			bpv1alpha1.BcpActiveHotStandby: {
				Replication: &bpv1alpha1.ReplicationSpec{Backend: "cnpg-pair", Mode: "sync"},
			},
		},
	}
	choice, variant := placementVariantFromTargets(
		hotStandbyTargets(), bpv1alpha1.CapabilityPrimaryStandby, bpTopo)

	cp, ok := buildContinuumPlan("acme/obs", choice, variant, twoRegionPlanAB(), nil)
	if !ok {
		t.Fatal("buildContinuumPlan skipped a cnpg-pair Hot-standby placement")
	}
	if cp.Mechanism != "cnpg-pair" {
		t.Errorf("mechanism = %q, want cnpg-pair (carried from the Blueprint backend)", cp.Mechanism)
	}
}

// TestPlacementVariantFromTargets_SingletonNotArmed — a lone Primary is a
// singleton: NO Switchover synthesized, the producer skips it.
func TestPlacementVariantFromTargets_SingletonNotArmed(t *testing.T) {
	targets := []bpv1alpha1.PlacementTarget{
		{Region: "hetzner-fsn-rtz-prod", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1alpha1.DataRolePrimary},
	}
	choice, variant := placementVariantFromTargets(
		targets, bpv1alpha1.CapabilityPrimaryStandby, nil)

	if choice != bpv1alpha1.BcpSingleton {
		t.Fatalf("choice = %q, want singleton", choice)
	}
	if variant.Switchover != nil {
		t.Errorf("singleton must NOT synthesize a Switchover, got %+v", variant.Switchover)
	}

	singletonPlan := placement.Plan{
		PrimaryRegion: "hetzner-fsn-rtz-prod",
		Regions:       []placement.RegionPlan{{Name: "hetzner-fsn-rtz-prod", Role: placement.RolePrimary}},
	}
	if _, ok := buildContinuumPlan("acme/obs", choice, variant, singletonPlan, nil); ok {
		t.Error("buildContinuumPlan must skip a singleton placement (no DR to arm)")
	}
}

// TestPlacementVariantFromTargets_ActiveActiveNotArmed — two Primary targets
// is active-active (multi-master): no primary/standby flip, NO Switchover.
func TestPlacementVariantFromTargets_ActiveActiveNotArmed(t *testing.T) {
	targets := []bpv1alpha1.PlacementTarget{
		{Region: "hetzner-fsn-rtz-prod", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1alpha1.DataRolePrimary},
		{Region: "hetzner-nbg-rtz-prod", Cluster: "mgmt-B", VCluster: "mgmt", Role: bpv1alpha1.DataRolePrimary},
	}
	choice, variant := placementVariantFromTargets(
		targets, bpv1alpha1.CapabilityMultiPrimary, nil)

	if choice != bpv1alpha1.BcpActiveActive {
		t.Fatalf("choice = %q, want active-active", choice)
	}
	if variant.Switchover != nil {
		t.Errorf("active-active must NOT synthesize a Switchover, got %+v", variant.Switchover)
	}
}

// TestPlacementVariantFromTargets_ColdStandbyArmsStateless — a Cold standby
// (active-passive) still arms a DR contract (the standby region exists), but
// with no streaming backend resolves to the stateless Promoter.
func TestPlacementVariantFromTargets_ColdStandbyArmsStateless(t *testing.T) {
	targets := []bpv1alpha1.PlacementTarget{
		{Region: "hetzner-fsn-rtz-prod", Cluster: "mgmt-A", VCluster: "mgmt", Role: bpv1alpha1.DataRolePrimary},
		{Region: "hetzner-nbg-rtz-prod", Cluster: "mgmt-B", VCluster: "mgmt", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyCold},
	}
	choice, variant := placementVariantFromTargets(
		targets, bpv1alpha1.CapabilityPrimaryStandby, nil)

	if choice != bpv1alpha1.BcpActivePassive {
		t.Fatalf("choice = %q, want active-passive", choice)
	}
	if variant.Switchover == nil {
		t.Fatalf("active-passive (Cold standby) must synthesize a Switchover so the standby region has a DR contract")
	}
	cp, ok := buildContinuumPlan("acme/obs", choice, variant, twoRegionPlanAB(), nil)
	if !ok {
		t.Fatal("buildContinuumPlan skipped an active-passive placement")
	}
	if cp.Mechanism != "stateless" {
		t.Errorf("mechanism = %q, want stateless (Cold standby, no streaming backend)", cp.Mechanism)
	}
}
