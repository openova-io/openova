// Tests for #3969 — the application-centric Placement reconcile-path
// wiring this PR adds: spec.placement.targets[] / ownedDependencies[]
// parsing, the Blueprint capability reader, the owned-dep cascade input,
// the observed-target rollup, and the ONE recon status block.
//
// The model layer (ValidatePlacement / DerivePattern / DeriveReconStatus)
// + the backingservice cascade are unit-tested in their own packages;
// these tests pin the CONTROLLER-SIDE glue that was previously absent
// (the gap the EPIC's §8 called out: "model built, never invoked").
package controller

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// --- parseSpec: #3969 desired-state targets + owned-dep overrides ---------

func TestParseSpec_PlacementTargets(t *testing.T) {
	app := makeAppWithPlacement("acme", "kc", "acme-prod", "bp-keycloak", "1.0.0",
		map[string]interface{}{
			"targets": []interface{}{
				map[string]interface{}{
					"region": "region-a", "cluster": "mgmt-A", "vcluster": "mgmt", "role": "Primary",
				},
				map[string]interface{}{
					"region": "region-b", "cluster": "mgmt-B", "vcluster": "mgmt",
					"role": "Standby", "standbyType": "Hot",
				},
			},
			"ownedDependencies": []interface{}{
				map[string]interface{}{"name": "keycloak-pg", "follow": false},
			},
		},
		[]string{"region-a", "region-b"})

	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if len(spec.PlacementTargets) != 2 {
		t.Fatalf("PlacementTargets len = %d, want 2", len(spec.PlacementTargets))
	}
	p := spec.PlacementTargets[0]
	if p.Region != "region-a" || p.Cluster != "mgmt-A" || p.VCluster != "mgmt" || p.Role != bpv1alpha1.DataRolePrimary {
		t.Errorf("target[0] = %+v, want region-a/mgmt-A/mgmt/Primary", p)
	}
	if p.StandbyType != "" {
		t.Errorf("Primary target[0] must carry no standbyType, got %q", p.StandbyType)
	}
	s := spec.PlacementTargets[1]
	if s.Role != bpv1alpha1.DataRoleStandby || s.StandbyType != bpv1alpha1.StandbyHot {
		t.Errorf("target[1] = role %q type %q, want Standby/Hot", s.Role, s.StandbyType)
	}
	if len(spec.OwnedDependencies) != 1 || spec.OwnedDependencies[0].Name != "keycloak-pg" || spec.OwnedDependencies[0].Follow {
		t.Errorf("OwnedDependencies = %+v, want [{keycloak-pg follow:false}]", spec.OwnedDependencies)
	}
}

func TestParseSpec_OwnedDependencyFollowDefaultsTrue(t *testing.T) {
	app := makeAppWithPlacement("acme", "kc", "acme-prod", "bp-keycloak", "1.0.0",
		map[string]interface{}{
			"targets": []interface{}{
				map[string]interface{}{"region": "region-a", "cluster": "mgmt-A", "role": "Primary"},
			},
			// follow omitted ⇒ must default true.
			"ownedDependencies": []interface{}{
				map[string]interface{}{"name": "keycloak-pg"},
			},
		},
		[]string{"region-a"})

	spec, err := parseSpec(app)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if len(spec.OwnedDependencies) != 1 || !spec.OwnedDependencies[0].Follow {
		t.Errorf("absent follow must default true, got %+v", spec.OwnedDependencies)
	}
}

func TestParseSpec_PlacementTargetMissingRegionErrors(t *testing.T) {
	app := makeAppWithPlacement("acme", "kc", "acme-prod", "bp-keycloak", "1.0.0",
		map[string]interface{}{
			"targets": []interface{}{
				map[string]interface{}{"cluster": "mgmt-A", "role": "Primary"}, // no region
			},
		},
		[]string{"region-a"})
	if _, err := parseSpec(app); err == nil {
		t.Fatal("parseSpec must error on a target missing region")
	}
}

// --- capability gate (the MultiPrimaryNotSupported reason flows through) ---

func TestValidatePlacement_MultiPrimaryGateReason(t *testing.T) {
	desired := bpv1alpha1.Placement{
		Targets: []bpv1alpha1.PlacementTarget{
			{Region: "region-a", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
			{Region: "region-b", Cluster: "mgmt-B", Role: bpv1alpha1.DataRolePrimary},
		},
	}
	// primary+standby blueprint → second Primary rejected with the stable reason.
	err := bpv1alpha1.ValidatePlacement(desired, bpv1alpha1.CapabilityPrimaryStandby)
	if err == nil {
		t.Fatal("two Primary targets under primary+standby must be rejected")
	}
	var pe *bpv1alpha1.PlacementError
	if !asPlacementErr(err, &pe) || pe.Reason != bpv1alpha1.MultiPrimaryNotSupportedReason {
		t.Errorf("reason = %v, want %s", err, bpv1alpha1.MultiPrimaryNotSupportedReason)
	}
	// multi-primary blueprint → the same two Primaries are accepted.
	if err := bpv1alpha1.ValidatePlacement(desired, bpv1alpha1.CapabilityMultiPrimary); err != nil {
		t.Errorf("multi-primary blueprint must accept two Primary targets, got %v", err)
	}
}

func asPlacementErr(err error, target **bpv1alpha1.PlacementError) bool {
	if pe, ok := err.(*bpv1alpha1.PlacementError); ok {
		*target = pe
		return true
	}
	return false
}

// --- Blueprint readers ----------------------------------------------------

func TestBlueprintPlacementCapability(t *testing.T) {
	bp := makeBlueprint("bp-x", "1.0.0", nil, []string{"single-region"})
	// absent ⇒ safe default primary+standby.
	if got := blueprintPlacementCapability(bp); got != bpv1alpha1.CapabilityPrimaryStandby {
		t.Errorf("absent capability = %q, want primary+standby", got)
	}
	bp.Object["spec"].(map[string]interface{})["placementCapability"] = "multi-primary"
	if got := blueprintPlacementCapability(bp); got != bpv1alpha1.CapabilityMultiPrimary {
		t.Errorf("declared capability = %q, want multi-primary", got)
	}
	// unrecognised ⇒ folds back to the safe default.
	bp.Object["spec"].(map[string]interface{})["placementCapability"] = "nonsense"
	if got := blueprintPlacementCapability(bp); got != bpv1alpha1.CapabilityPrimaryStandby {
		t.Errorf("unrecognised capability = %q, want primary+standby", got)
	}
}

func TestBlueprintBackingServices(t *testing.T) {
	bp := makeBlueprint("bp-keycloak", "1.0.0", nil, []string{"single-region"})
	bp.Object["spec"].(map[string]interface{})["backingServices"] = []interface{}{
		map[string]interface{}{"type": "postgres", "mode": "private"},
	}
	got := blueprintBackingServices(bp)
	if len(got) != 1 || got[0].Type != "postgres" || got[0].Mode != bpv1alpha1.BackingServiceModePrivate {
		t.Errorf("backingServices = %+v, want one private postgres", got)
	}
	if blueprintBackingServices(makeBlueprint("bp-y", "1.0.0", nil, nil)) != nil {
		t.Error("absent backingServices must return nil")
	}
}

// --- owned-dep cascade: the consumer's targets reach the owned instance ---

func TestResolveBackingPlacement_OwnedFollowsByDefault(t *testing.T) {
	targets := []bpv1alpha1.PlacementTarget{
		{Region: "region-a", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
		{Region: "region-b", Cluster: "mgmt-B", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
	}
	bss := []bpv1alpha1.BackingServiceSpec{{Type: "postgres", Mode: bpv1alpha1.BackingServiceModePrivate}}

	// No override ⇒ the owned instance follows by default + carries the targets.
	bindings, err := resolveBackingPlacement("bp-keycloak", "keycloak", "acme", bss, targets, nil)
	if err != nil {
		t.Fatalf("resolveBackingPlacement: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings len = %d, want 1", len(bindings))
	}
	if !bindings[0].Followed || len(bindings[0].InstanceTargets) != 2 {
		t.Errorf("owned dep must follow by default with 2 targets, got followed=%v targets=%d",
			bindings[0].Followed, len(bindings[0].InstanceTargets))
	}

	// Explicit follow:false ⇒ decoupled (no cascade).
	owned := []bpv1alpha1.OwnedDependencyOverride{{Name: "keycloak-pg", Follow: false}}
	bindings, err = resolveBackingPlacement("bp-keycloak", "keycloak", "acme", bss, targets, owned)
	if err != nil {
		t.Fatalf("resolveBackingPlacement decoupled: %v", err)
	}
	if bindings[0].Followed {
		t.Errorf("follow:false must decouple the owned dep, got followed=true")
	}
}

// --- recon status: ONE value, derived from the plan + HR phase rollup -----

func TestObservedTargetsFromPlan_PrefersDesiredRoles(t *testing.T) {
	plan := placement.Plan{
		PrimaryRegion: "region-a",
		Regions: []placement.RegionPlan{
			{Name: "region-a", Role: placement.RolePrimary, Standby: false},
			{Name: "region-b", Role: placement.RoleStandby, Standby: true},
		},
	}
	targets := []bpv1alpha1.PlacementTarget{
		{Region: "region-a", Cluster: "mgmt-A", Role: bpv1alpha1.DataRolePrimary},
		{Region: "region-b", Cluster: "mgmt-B", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot},
	}
	ready := readbackByRegion(plan, PhaseReady)
	obs := observedTargetsFromPlan(plan, targets, ready)
	if len(obs) != 2 {
		t.Fatalf("observed len = %d, want 2", len(obs))
	}
	if obs[1].Role != bpv1alpha1.DataRoleStandby || obs[1].StandbyType != bpv1alpha1.StandbyHot {
		t.Errorf("region-b observed = role %q type %q, want Standby/Hot (from desired targets)", obs[1].Role, obs[1].StandbyType)
	}
	if !obs[0].Ready || !obs[1].Ready {
		t.Errorf("PhaseReady must mark every region ready, got %+v", obs)
	}
}

func TestObservedTargetsFromPlan_LegacyPlanWithoutTargets(t *testing.T) {
	plan := placement.Plan{
		Regions: []placement.RegionPlan{
			{Name: "region-a", Role: placement.RolePrimary},
			{Name: "region-b", Role: placement.RoleStandby, Standby: true},
		},
	}
	// No desired targets ⇒ map the legacy plan roles.
	obs := observedTargetsFromPlan(plan, nil, readbackByRegion(plan, PhaseProvisioning))
	if obs[0].Role != bpv1alpha1.DataRolePrimary || obs[1].Role != bpv1alpha1.DataRoleStandby {
		t.Errorf("legacy plan roles = %q/%q, want Primary/Standby", obs[0].Role, obs[1].Role)
	}
	if obs[0].Ready || obs[1].Degraded {
		t.Errorf("Provisioning ⇒ not-ready, not-degraded, got %+v", obs)
	}
}

func TestReconStatusBlock(t *testing.T) {
	// All ready ⇒ Reconciled, no reason.
	ready := []bpv1alpha1.ObservedTarget{
		{Region: "region-a", Role: bpv1alpha1.DataRolePrimary, Ready: true},
		{Region: "region-b", Role: bpv1alpha1.DataRoleStandby, StandbyType: bpv1alpha1.StandbyHot, Ready: true},
	}
	status, reason, rows := reconStatusBlock(ready)
	if status != string(bpv1alpha1.ReconStatusReconciled) || reason != "" {
		t.Errorf("all-ready = (%q,%q), want (Reconciled,'')", status, reason)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len = %d, want 2", len(rows))
	}
	row1 := rows[1].(map[string]interface{})
	if row1["standbyType"] != "Hot" || row1["role"] != "Standby" {
		t.Errorf("row[1] = %+v, want Standby/Hot", row1)
	}

	// A degraded target ⇒ Degraded with a plain (non-class) reason.
	degraded := []bpv1alpha1.ObservedTarget{
		{Region: "region-a", Role: bpv1alpha1.DataRolePrimary, Degraded: true},
	}
	status, reason, _ = reconStatusBlock(degraded)
	if status != string(bpv1alpha1.ReconStatusDegraded) || reason == "" {
		t.Errorf("degraded = (%q,%q), want (Degraded, <reason>)", status, reason)
	}
}

// guard: the recon status block must never use unstructured maps with a
// nil interface — ensure it builds a usable slice the dynamic client can
// marshal.
func TestReconStatusBlock_EmptyObserved(t *testing.T) {
	status, reason, rows := reconStatusBlock(nil)
	if status != string(bpv1alpha1.ReconStatusReconciling) || reason == "" {
		t.Errorf("empty observed = (%q,%q), want (Reconciling, <reason>)", status, reason)
	}
	if rows == nil {
		t.Error("rows must be a non-nil (possibly empty) slice")
	}
	_ = unstructured.Unstructured{} // keep the import meaningful in this file
}
