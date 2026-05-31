// Package placement encodes the per-Application fan-out logic that
// turns `Application.spec.placement` + `Application.spec.regions[]`
// into the per-region work plan the application-controller writes to
// Gitea.
//
// Per docs/EPICS-1-6-unified-design.md §5 (EPIC-2 contract) and the
// CRD's `placement` enum, three modes are supported:
//
//   single-region:      one cluster only (regions[0]). regions[1..]
//                       in the spec (if any) are ignored — the schema
//                       allows up to 5 entries because all 3 modes
//                       share the regions[] array, but only [0] is
//                       materialised.
//
//   active-active:      manifests fan out to ALL regions[]. Every
//                       region renders an identical HelmRelease /
//                       Kustomization with the same `replicas`. The
//                       application is online in every region
//                       simultaneously; load is balanced upstream
//                       (Continuum / global ingress).
//
//   active-hotstandby:  manifests fan out to ALL regions[]. regions[0]
//                       is the PRIMARY — runs with `replicas:
//                       parameters.replicas` (or whatever the Blueprint
//                       declares). regions[1..] are STANDBYS — run
//                       with `replicas: 0` (or `replica: false` on
//                       CNPG-style resources). The Continuum
//                       controller (#1101) flips replicas back to a
//                       non-zero value on switchover.
//
// This package is pure functions — no I/O, no K8s client calls. It is
// consumed by `internal/render` which serializes the per-region plan
// to YAML manifests, and by the controller's status writer which uses
// `Plan.PrimaryRegion` to populate `status.primaryRegion`.
package placement

import (
	"fmt"
	"sort"
)

// Mode constants — mirror the CRD enum at
// products/catalyst/chart/crds/application.yaml.
const (
	ModeSingleRegion     = "single-region"
	ModeActiveActive     = "active-active"
	ModeActiveHotStandby = "active-hotstandby"
)

// Role constants — written onto status.regions[].role and consumed by
// the Continuum controller for failover decisions.
const (
	RolePrimary = "primary"
	RoleStandby = "standby"
	RoleActive  = "active"
)

// RegionPlan is one row of the materialised plan.
type RegionPlan struct {
	// Name is the host-cluster identifier the controller writes the
	// manifest under (clusters/<Name>/applications/<app>/...).
	Name string

	// Role is one of {primary, standby, active}.
	Role string

	// Standby reports whether this region's HelmRelease should be
	// rendered with `replicas: 0` (active-hotstandby standbys). On the
	// renderer side this triggers the standby overlay branch; the
	// Continuum controller toggles it back via a `parameters.replicas`
	// override on switchover.
	Standby bool
}

// Plan is the resolved per-Application placement decision.
type Plan struct {
	// Mode is the input placement string (one of the Mode* constants).
	Mode string

	// PrimaryRegion is the host-cluster name the controller writes to
	// `status.primaryRegion`. Empty when Mode == active-active (no
	// "primary" concept under symmetric replication).
	PrimaryRegion string

	// Regions is the materialised work plan. For single-region this is
	// exactly len(1). For the multi-region modes it has len(spec.regions).
	Regions []RegionPlan
}

// Resolve maps an input placement mode + spec.regions[] to a
// deterministic, ordered Plan. Output ordering is stable across calls —
// active-active sorts the region names so the controller's idempotency
// check (compare-this-pass-to-prior-pass) is byte-stable; single-region
// and active-hotstandby preserve input order so the operator's "primary
// is regions[0]" semantic from the CRD description is honored.
//
// Validation:
//
//   - mode must be one of the three constants
//   - regions must be non-empty
//   - single-region must have exactly one entry (the schema's minItems
//     does not enforce maxItems for single-region; we validate here)
//
// Returns a typed error so the controller can map "spec invariant
// violated" to a status.phase=Failed Condition with reason=Invalid.
func Resolve(mode string, regions []string) (Plan, error) {
	if len(regions) == 0 {
		return Plan{}, fmt.Errorf("placement: regions[] is empty")
	}
	switch mode {
	case ModeSingleRegion:
		// Per CRD description: "single-region = one cluster only.
		// regions[1..] (if any) are ignored." We could enforce
		// len==1 here, but the schema allows up to 5 entries on the
		// shared regions[] array. We surface the constraint as an
		// Invalid Condition rather than silently dropping.
		if len(regions) != 1 {
			return Plan{}, fmt.Errorf("placement: single-region requires exactly 1 entry in regions[], got %d", len(regions))
		}
		return Plan{
			Mode:          mode,
			PrimaryRegion: regions[0],
			Regions: []RegionPlan{
				{Name: regions[0], Role: RolePrimary, Standby: false},
			},
		}, nil

	case ModeActiveActive:
		// Symmetric: every region runs the same shape. PrimaryRegion
		// is empty by design (no asymmetric "this one is primary").
		// We sort the region list so the rendered manifest set is
		// byte-stable across reconcile passes — the input ordering
		// of spec.regions[] is informational, not load-bearing.
		ordered := append([]string(nil), regions...)
		sort.Strings(ordered)
		out := make([]RegionPlan, len(ordered))
		for i, r := range ordered {
			out[i] = RegionPlan{Name: r, Role: RoleActive, Standby: false}
		}
		return Plan{
			Mode:          mode,
			PrimaryRegion: "",
			Regions:       out,
		}, nil

	case ModeActiveHotStandby:
		// Asymmetric: regions[0] is primary (runs hot), regions[1..]
		// are standby (replicas: 0). Order is load-bearing — preserved
		// from the spec.
		out := make([]RegionPlan, len(regions))
		out[0] = RegionPlan{Name: regions[0], Role: RolePrimary, Standby: false}
		for i := 1; i < len(regions); i++ {
			out[i] = RegionPlan{Name: regions[i], Role: RoleStandby, Standby: true}
		}
		return Plan{
			Mode:          mode,
			PrimaryRegion: regions[0],
			Regions:       out,
		}, nil

	default:
		return Plan{}, fmt.Errorf("placement: unknown mode %q (expected one of: %s, %s, %s)",
			mode, ModeSingleRegion, ModeActiveActive, ModeActiveHotStandby)
	}
}

// AllowedByBlueprint reports whether `mode` is in the Blueprint's
// `spec.placementSchema.modes[]` list. An empty `allowed` slice is
// treated as "Blueprint did not constrain placement" — every mode is
// allowed (the legacy default for Blueprints authored before the
// placementSchema was added; per BLUEPRINT-AUTHORING.md §3 the field
// is optional).
func AllowedByBlueprint(mode string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, m := range allowed {
		if m == mode {
			return true
		}
	}
	return false
}

// SovereignTopology constants — Sovereign-wide BCP topology the
// Application is being installed under. Mirrors
// provisioner.Request.BcpTopology (G93.1, Refs #2666). Read at
// controller startup from the SOVEREIGN_BCP_TOPOLOGY env the
// bp-catalyst-platform chart slot 13 stamps from cloud-init. The
// controller passes this into EffectiveDefault below.
const (
	SovereignTopologySingleRegion     = "single-region"
	SovereignTopologyActiveHotStandby = "active-hotstandby"
	SovereignTopologyActiveActive     = "active-active"
)

// EffectiveDefault picks the right placement mode when the Application
// CR's spec.placement is empty:
//
//  1. If the Sovereign is multi-region (active-hotstandby /
//     active-active) AND the Blueprint declares
//     `placementSchema.defaultOnMultiRegion`, return that. This is the
//     G93.2 (Refs #2667) target-state — every CNPG-backed Blueprint
//     sets defaultOnMultiRegion="active-hotstandby" so a fresh
//     marketplace install on a 2-region Sovereign lands Pillar 3
//     zero-touch.
//  2. Otherwise fall back to `placementSchema.default` (the existing
//     single-knob default).
//  3. If neither is set, return "single-region" as the safe last
//     resort.
//
// Per-Blueprint declarative seam: the Blueprint author picks the
// preferred multi-region default; the application-controller honours it
// without per-controller switch statements. New Blueprints opt in by
// adding the field; existing single-region Blueprints don't need to
// change.
//
// Validation: the caller is responsible for ensuring the returned mode
// is in `placementSchema.modes[]` (use AllowedByBlueprint). If the
// chosen default is not in modes[], that's a Blueprint-author bug the
// blueprint-controller's validator catches at publish time
// (validate.go §placementSchema.defaultOnMultiRegion-in-modes).
func EffectiveDefault(sovereignTopology, blueprintDefault, blueprintDefaultOnMultiRegion string) string {
	switch sovereignTopology {
	case SovereignTopologyActiveHotStandby, SovereignTopologyActiveActive:
		if blueprintDefaultOnMultiRegion != "" {
			return blueprintDefaultOnMultiRegion
		}
	}
	if blueprintDefault != "" {
		return blueprintDefault
	}
	return ModeSingleRegion
}
