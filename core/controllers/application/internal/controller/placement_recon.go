package controller

// placement_recon.go — #3969 application-centric Placement: the
// RECONCILE-PATH wiring that connects the already-built model layer
// (pkg/apis/blueprint/v1alpha1 placement_target.go + recon_status.go) +
// the backingservice cascade generator (pkg/backingservice/generate.go)
// to the live application-controller. Before this file the model + the
// generator were fully built + unit-tested but NEVER invoked from a
// reconcile pass; the desired-state targets were parsed nowhere, the
// capability gate fired nowhere, the cascade to owned deps ran nowhere,
// and no recon status was written. This file closes §8a / §8b / §8d / the
// capability gate of the EPIC.
//
// It carries NO "effectiveClass", "observed", "declared", or "mandate"
// concept — those are deleted by the ticket. Status is the ONE recon
// value (Reconciled / Reconciling / Degraded + plain reason).
//
// Ref #3969 §7.1, §7.3, §7.4, §8.

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/backingservice"
)

// ReasonInvalidPlacement — #3969. Stamped on
// `Application.status.conditions[type=Ready,status=False]` when the
// declared desired-state placement (spec.placement.targets[]) violates a
// model invariant — most importantly when it declares >1 Primary target
// but the Blueprint capability is primary+standby (the
// MultiPrimaryNotSupported gate, DoD item 5). The editor surfaces the
// gate as a disabled radio + inline reason; the controller is the
// authoritative enforcement.
const ReasonInvalidPlacement = "InvalidPlacement"

// parsePlacementTargets decodes spec.placement.targets[] off the wire into
// the typed model. Each entry MUST carry region + cluster + role; vcluster
// + standbyType are optional in the decode (ValidatePlacement is the
// authoritative invariant gate). A non-list / non-map entry is a hard
// parse error so a malformed desired state fails loudly rather than
// silently dropping a target.
func parsePlacementTargets(raw interface{}) ([]bpv1alpha1.PlacementTarget, error) {
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("spec.placement.targets is not a list: %T", raw)
	}
	out := make([]bpv1alpha1.PlacementTarget, 0, len(items))
	for i, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("spec.placement.targets[%d] is not an object: %T", i, it)
		}
		t := bpv1alpha1.PlacementTarget{
			Region:      stringField(m, "region"),
			Cluster:     stringField(m, "cluster"),
			VCluster:    bpv1alpha1.NormalizeVCluster(stringField(m, "vcluster")),
			Role:        bpv1alpha1.DataRole(stringField(m, "role")),
			StandbyType: bpv1alpha1.StandbyType(stringField(m, "standbyType")),
		}
		if t.Region == "" {
			return nil, fmt.Errorf("spec.placement.targets[%d].region is required", i)
		}
		if t.Cluster == "" {
			return nil, fmt.Errorf("spec.placement.targets[%d].cluster is required", i)
		}
		out = append(out, t)
	}
	return out, nil
}

// parseOwnedDependencies decodes spec.placement.ownedDependencies[] into
// the typed override list. Each entry needs a name; follow defaults to
// true (absent ⇒ the owned dep follows the consumer's placement). An
// entry with follow:false decouples that owned dep.
func parseOwnedDependencies(raw interface{}) ([]bpv1alpha1.OwnedDependencyOverride, error) {
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("spec.placement.ownedDependencies is not a list: %T", raw)
	}
	out := make([]bpv1alpha1.OwnedDependencyOverride, 0, len(items))
	for i, it := range items {
		m, ok := it.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("spec.placement.ownedDependencies[%d] is not an object: %T", i, it)
		}
		name := stringField(m, "name")
		if name == "" {
			return nil, fmt.Errorf("spec.placement.ownedDependencies[%d].name is required", i)
		}
		// follow defaults TRUE when absent; only an explicit false decouples.
		follow := true
		if v, ok := m["follow"].(bool); ok {
			follow = v
		}
		out = append(out, bpv1alpha1.OwnedDependencyOverride{Name: name, Follow: follow})
	}
	return out, nil
}

// blueprintPlacementCapability reads spec.placementCapability from a
// Blueprint (#3969 §7.2). It is the only stored gate on how many targets
// may be Primary. An empty / unrecognised value folds to the safe default
// (primary+standby) so a Blueprint that omits the field is never
// accidentally treated as multi-primary.
func blueprintPlacementCapability(bp *unstructured.Unstructured) bpv1alpha1.PlacementCapability {
	if bp == nil {
		return bpv1alpha1.CapabilityPrimaryStandby
	}
	s, _, _ := unstructured.NestedString(bp.Object, "spec", "placementCapability")
	return bpv1alpha1.NormalizeCapability(bpv1alpha1.PlacementCapability(s))
}

// blueprintBackingServices reads spec.backingServices[] from a Blueprint —
// the {type, mode, instanceRef, database, role, secretName} declarations
// the consumer binds. Returns the typed slice; nil when absent. Used to
// drive the #3969 owned-dependency cascade (private bindings follow the
// consumer's placement by default).
func blueprintBackingServices(bp *unstructured.Unstructured) []bpv1alpha1.BackingServiceSpec {
	if bp == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(bp.Object, "spec", "backingServices")
	if err != nil || !found || len(raw) == 0 {
		return nil
	}
	out := make([]bpv1alpha1.BackingServiceSpec, 0, len(raw))
	for _, it := range raw {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		out = append(out, bpv1alpha1.BackingServiceSpec{
			Type:        stringField(m, "type"),
			Mode:        bpv1alpha1.BackingServiceMode(stringField(m, "mode")),
			InstanceRef: stringField(m, "instanceRef"),
			Database:    stringField(m, "database"),
			Role:        stringField(m, "role"),
			SecretName:  stringField(m, "secretName"),
		})
	}
	return out
}

// resolveBackingPlacement runs the #3969 cascade for a consumer's owned +
// shared backing services (§8a/§8b/§8e). For each declared backing
// service it calls backingservice.Generate threading the consumer's
// resolved desired-state targets so an OWNED (private) instance follows
// the consumer's placement by default (Primary↔Primary, Standby↔Standby),
// unless decoupled via an OwnedDependencyOverride{Follow:false}. SHARED
// instances never cascade — they get a SOFT recommendation only.
//
// It is pure + read-only: it returns the resolved bindings (carrying
// InstanceTargets / Followed / Recommendations) for the controller to
// project + surface. It never blocks; an unsupported backing-service type
// is the only error (a malformed declaration must fail loudly).
func resolveBackingPlacement(
	consumerBlueprint, consumerName, consumerNamespace string,
	backingServices []bpv1alpha1.BackingServiceSpec,
	targets []bpv1alpha1.PlacementTarget,
	owned []bpv1alpha1.OwnedDependencyOverride,
) ([]backingservice.Binding, error) {
	if len(backingServices) == 0 {
		return nil, nil
	}
	in := backingservice.Input{
		ConsumerBlueprint: consumerBlueprint,
		ConsumerName:      consumerName,
		ConsumerNamespace: consumerNamespace,
		Targets:           targets,
		OwnedOverrides:    owned,
	}
	return backingservice.GenerateAll(backingServices, in)
}

// observedTargetsFromPlan rolls the resolved placement plan + the
// per-region HR readback up into the §7.4 ObservedTarget slice that
// DeriveReconStatus consumes. Each region in the plan becomes one
// ObservedTarget carrying its desired role (Primary|Standby) + standby
// type; its Ready/Degraded is taken from the rolled-up per-region HR
// phase the controller already observed (PhaseReady ⇒ all ready;
// PhaseDegraded/PhaseFailed ⇒ degraded). When desired-state targets are
// declared we prefer their richer role/standbyType over the plan's
// active|primary|standby string, region-matched.
//
// readyByRegion maps a region name → its observed readiness; a region
// absent from the map (HR not yet present) is treated as not-ready
// (Reconciling), never degraded.
func observedTargetsFromPlan(
	plan placement.Plan,
	targets []bpv1alpha1.PlacementTarget,
	readyByRegion map[string]regionReadback,
) []bpv1alpha1.ObservedTarget {
	// Index the desired targets by region for the richer role/standbyType.
	byRegion := map[string]bpv1alpha1.PlacementTarget{}
	for _, t := range targets {
		byRegion[t.Region] = t
	}

	out := make([]bpv1alpha1.ObservedTarget, 0, len(plan.Regions))
	for _, rp := range plan.Regions {
		ot := bpv1alpha1.ObservedTarget{Region: rp.Name}

		if t, ok := byRegion[rp.Name]; ok {
			// Desired-state target wins: it carries the canonical
			// Primary|Standby role + Hot|Cold standby type.
			ot.Cluster = t.Cluster
			ot.Role = t.Role
			ot.StandbyType = t.StandbyType
		} else {
			// Legacy plan: map active|primary→Primary, standby→Standby.
			if rp.Standby || rp.Role == placement.RoleStandby {
				ot.Role = bpv1alpha1.DataRoleStandby
			} else {
				ot.Role = bpv1alpha1.DataRolePrimary
			}
		}

		if rb, ok := readyByRegion[rp.Name]; ok {
			ot.Ready = rb.ready
			ot.Degraded = rb.degraded
		}
		out = append(out, ot)
	}
	return out
}

// effectivePlacementTargets returns the placement targets the #3969
// read-model (backing-service cascade + observed-target rollup) should ride
// on. When the Application declares explicit desired-state targets
// (spec.placement.targets[]) they win verbatim. Otherwise — the legacy
// string-mode case, which is what the spine producer stamps
// (active-passive / active-hot-standby / singleton) AND every pre-#3969
// Application — we DERIVE the targets from the already-resolved placement
// plan (placementTargetsFromPlan), so the cascade + the observed rollup
// "ride on Seam 3" instead of seeing an empty target list. This is the
// thread the EPIC calls for: spine CRs carry a legacy placement string, but
// the consumer placement reaching backingservice.Input.Targets is the real
// primary+standby target set, region-matched.
func effectivePlacementTargets(declared []bpv1alpha1.PlacementTarget, plan placement.Plan) []bpv1alpha1.PlacementTarget {
	if len(declared) > 0 {
		return declared
	}
	return placementTargetsFromPlan(plan)
}

// placementTargetsFromPlan derives the canonical desired-state target list
// from a resolved placement plan. regions[0] (or the plan's PrimaryRegion)
// is Primary; the standby regions are Standby/Hot (the plan does not encode
// hot-vs-cold — the #3698 fan-out treats both as replicas:0 standbys — so we
// surface Hot, the conservative DR posture the Topology tab renders). An
// active-active plan (no PrimaryRegion, every region Active) yields all
// Primary targets. Cluster is left empty: the legacy plan carries only the
// region name; the read-model keys off region + role, and a downstream
// consumer that needs the cluster reads it from the fan-out HR labels.
func placementTargetsFromPlan(plan placement.Plan) []bpv1alpha1.PlacementTarget {
	out := make([]bpv1alpha1.PlacementTarget, 0, len(plan.Regions))
	for _, rp := range plan.Regions {
		t := bpv1alpha1.PlacementTarget{Region: rp.Name}
		if rp.Standby || rp.Role == placement.RoleStandby {
			t.Role = bpv1alpha1.DataRoleStandby
			t.StandbyType = bpv1alpha1.StandbyHot
		} else {
			t.Role = bpv1alpha1.DataRolePrimary
		}
		out = append(out, t)
	}
	return out
}

// regionReadback is the per-region readiness signal the controller already
// derives from the per-region HelmRelease, projected into the recon
// rollup.
type regionReadback struct {
	ready    bool
	degraded bool
}

// reconStatusBlock builds the §7.4 status.placement block (the ONE recon
// value + plain reason + observed per-target rollup) as a wire map the
// status writer marshals onto the Application. It NEVER fabricates a
// second class.
func reconStatusBlock(observed []bpv1alpha1.ObservedTarget) (status, reason string, targets []interface{}) {
	st := bpv1alpha1.DeriveReconStatus(observed)
	targets = make([]interface{}, 0, len(st.Targets))
	for _, t := range st.Targets {
		row := map[string]interface{}{
			"region": t.Region,
			"role":   string(t.Role),
			"ready":  t.Ready,
		}
		if t.Cluster != "" {
			row["cluster"] = t.Cluster
		}
		if t.StandbyType != "" {
			row["standbyType"] = string(t.StandbyType)
		}
		if t.Degraded {
			row["degraded"] = true
		}
		targets = append(targets, row)
	}
	return string(st.Placement), st.Reason, targets
}

// stringField reads a string field off an unstructured map, returning ""
// for absent or non-string values.
func stringField(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
