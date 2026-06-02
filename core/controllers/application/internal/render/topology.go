// Package render implements the G117.6 (W2.C1) application-controller
// topology resolution + per-cluster HelmRelease fan-out.
//
// Before G117.6 the application-controller reduced every install to a
// singleton on the primary cluster (per G92 / the topology audit in
// docs/sessions/2026-06-02-per-blueprint-topology-audit.md). With W1.B1
// landed (typed Blueprint.spec.topology via
// core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go) this
// package wires the topology choice from the Application + Blueprint
// pair into a per-cluster work plan that the rest of the controller
// renders into HelmRelease CRs.
//
// Architectural notes:
//
//  1. Pure functions only. No K8s client calls, no I/O. The reconcile
//     loop calls ResolveTopology + FanoutHRs and the unstructured-
//     dynamic-client write path (already in application_controller.go)
//     persists the results. This keeps the package trivially
//     testable with table-driven cases per the locked enum.
//
//  2. We accept Blueprint.spec.topology as the typed
//     v1alpha1.Topology struct produced by the admission webhook. The
//     reconciler reads it out of the unstructured.Unstructured via
//     `unstructured.NestedMap(... "spec", "topology")` + json round-
//     trip into v1alpha1.Topology. The schema gate
//     (platform/_schemas/blueprint-topology.json) catches malformed
//     manifests at admission time, so this code can trust the shape.
//
//  3. The Sovereign-region-count is the single input that drives
//     default-topology selection (locked decision #7: `len(Sovereign.
//     spec.regions) > 1` ⇒ multi-region). We accept an `int` directly
//     rather than a typed Sovereign struct because (a) Wave-1 did not
//     land a typed Sovereign type and (b) the controller already
//     reads the per-tenant Environment's `spec.regions[]` slice — the
//     `int` shape is the natural seam. When the project later promotes
//     a typed Sovereign, callers map `len(sov.Spec.Regions)` to this
//     int without touching the resolver.
//
//  4. Roles are stamped on each HelmRelease as a label so the
//     observability layer + bp-continuum can query active/passive HRs
//     per Application without re-parsing the Blueprint topology. The
//     label keys are stable contract: `catalyst.openova.io/role` and
//     `catalyst.openova.io/topology` (see
//     docs/sessions/2026-06-02-per-blueprint-topology-audit.md anti-
//     theater red-flag list — label-key typos are a verifier finding).
//
// Refs #2745 (G117.6) + #2737 (G117 EPIC).
package render

import (
	"errors"
	"fmt"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// LabelRole is the HelmRelease label that surfaces the per-cluster
// role assignment (active / passive / singleton). Stable contract
// consumed by bp-continuum + the observability rollup.
const LabelRole = "catalyst.openova.io/role"

// LabelTopology is the HelmRelease label that surfaces the resolved
// BCP topology choice the application-controller fanned out under.
// Stable contract — verifier-flagged red flag if mis-spelled.
const LabelTopology = "catalyst.openova.io/topology"

// LabelApp is the HelmRelease label that ties every per-cluster HR
// back to the parent Application CR. Used by the operator-console
// drill-down + by handleDeletion() to find every owned HR cheaply.
const LabelApp = "catalyst.openova.io/app"

// LabelCluster is the HelmRelease label that records the canonical
// cluster ID the HR was scheduled onto. Read by the per-cluster
// rollup ("which clusters carry this App today?") and by the
// continuum-switchover reconciler.
const LabelCluster = "catalyst.openova.io/cluster"

// RoleActive / RolePassive / RoleSingleton are the canonical strings
// the application-controller stamps on the HR `LabelRole` value. They
// mirror the Blueprint topology PlacementSpec.Roles map shape (locked
// per docs/sessions/2026-06-02-per-blueprint-topology-audit.md
// Legend: 🟢 A / 🟡 P / 🔵 S).
const (
	RoleActive    = "active"
	RolePassive   = "passive"
	RoleSingleton = "singleton"
)

// InvalidTopologyReason is the canonical condition-reason the
// reconciler stamps on `Application.status.conditions[type=Ready,
// status=False]` when the resolved topology is not in
// Blueprint.spec.topology.supported[]. The verifier (G117.6 acceptance)
// asserts this exact string.
const InvalidTopologyReason = "InvalidTopology"

// ErrInvalidTopology is returned by ResolveTopology when the requested
// topology is not in the Blueprint's supported list. The reconciler
// converts this into `Ready=False, reason=InvalidTopology` per the
// acceptance contract (G117.6 brief scope §1).
var ErrInvalidTopology = errors.New("topology not in Blueprint.spec.topology.supported[]")

// ErrMissingTopology is returned when the Blueprint declares no
// topology at all. Treated as a programmer / schema error — the
// admission webhook should have rejected the manifest first.
var ErrMissingTopology = errors.New("Blueprint.spec.topology is required (G117.1 / W1.B1 contract)")

// ResolveTopology picks the per-Application BCP topology choice given:
//
//   - explicit operator override at Application.spec.topology
//     (`appTopology`, empty when the operator didn't choose),
//   - the Blueprint's typed topology block (`bp`),
//   - the Sovereign's region count (`sovereignRegions`, the
//     `len(Sovereign.spec.regions)` per locked decision #7).
//
// It returns the resolved topology key + the matching
// `TopologyVariant` (the replication / switchover / placement triple).
// When the resolved key is not in `bp.Supported`, returns
// `ErrInvalidTopology` so the caller can flip the Application's Ready
// condition to `False, reason=InvalidTopology` per the brief.
//
// The resolver does NOT mutate the inputs. It returns a pointer into
// `bp.PerTopology[<choice>]` so callers can read the per-variant
// knobs without a second lookup.
func ResolveTopology(
	appTopology string,
	bp *bpv1alpha1.Topology,
	sovereignRegions int,
) (bpv1alpha1.BcpTopology, *bpv1alpha1.TopologyVariant, error) {
	if bp == nil {
		return "", nil, ErrMissingTopology
	}
	if len(bp.Supported) == 0 {
		return "", nil, ErrMissingTopology
	}

	// 1. Operator override wins when present and supported.
	if appTopology != "" {
		choice := bpv1alpha1.BcpTopology(appTopology)
		if !isSupported(choice, bp.Supported) {
			return "", nil, fmt.Errorf(
				"%w: %q (supported: %v)", ErrInvalidTopology, choice, bp.Supported)
		}
		variant := lookupVariant(choice, bp.PerTopology)
		return choice, variant, nil
	}

	// 2. Fall back to the Blueprint's default keyed on Sovereign region
	//    count (locked decision #7).
	var fallback bpv1alpha1.BcpTopology
	if sovereignRegions > 1 {
		fallback = bp.Defaults.MultiRegion
	} else {
		fallback = bp.Defaults.SingleRegion
	}

	if fallback == "" {
		// The schema gate should have rejected this, but defensively
		// surface a clear error rather than silently picking
		// supported[0] — that would mask the Blueprint-author's
		// missing default.
		return "", nil, fmt.Errorf(
			"%w: Blueprint.spec.topology.defaults missing %s default",
			ErrInvalidTopology, regionShape(sovereignRegions))
	}

	if !isSupported(fallback, bp.Supported) {
		return "", nil, fmt.Errorf(
			"%w: default %q for %s not in supported %v",
			ErrInvalidTopology, fallback, regionShape(sovereignRegions), bp.Supported)
	}

	variant := lookupVariant(fallback, bp.PerTopology)
	return fallback, variant, nil
}

// isSupported reports whether `choice` appears in the supported list.
func isSupported(choice bpv1alpha1.BcpTopology, supported []bpv1alpha1.BcpTopology) bool {
	for _, s := range supported {
		if s == choice {
			return true
		}
	}
	return false
}

// lookupVariant returns the per-topology variant pointer (nil-safe).
// Singleton variants frequently omit `replication` + `switchover` (no
// cross-cluster sync to manage), so a non-nil variant with empty
// sub-blocks is a valid shape that the caller MUST tolerate.
func lookupVariant(
	choice bpv1alpha1.BcpTopology,
	per map[bpv1alpha1.BcpTopology]bpv1alpha1.TopologyVariant,
) *bpv1alpha1.TopologyVariant {
	if per == nil {
		return nil
	}
	v, ok := per[choice]
	if !ok {
		return nil
	}
	return &v
}

// regionShape returns a human label ("multi-region" / "single-region")
// for diagnostic error messages. Mirrors the
// `TopologyDefaults.{MultiRegion,SingleRegion}` field names.
func regionShape(n int) string {
	if n > 1 {
		return "multi-region"
	}
	return "single-region"
}
