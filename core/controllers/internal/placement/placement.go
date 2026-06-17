// Package placement encodes the per-Application fan-out logic that
// turns `Application.spec.placement` + `Application.spec.regions[]`
// into the per-region work plan the application-controller writes to
// Gitea.
//
// # ONE canonical placement vocabulary (#3375 §3(f), DoD-1)
//
// There is exactly ONE placement/topology vocabulary on the wire,
// shared by every surface (catalog placementSchema, the console + the
// bootstrap-ui editors, and the Application CR). The canonical tokens
// are:
//
//	singleton:          one cluster only (regions[0]). regions[1..]
//	                    in the spec (if any) are ignored — the schema
//	                    allows up to 5 entries because all modes share
//	                    the regions[] array, but only [0] is
//	                    materialised. (Legacy alias: single-region.)
//
//	active-active:      manifests fan out to ALL regions[]. Every
//	                    region renders an identical HelmRelease /
//	                    Kustomization with the same `replicas`. The
//	                    application is online in every region
//	                    simultaneously; load is balanced upstream
//	                    (Continuum / global ingress).
//
//	active-hot-standby: manifests fan out to ALL regions[]. regions[0]
//	                    is the PRIMARY — runs with `replicas:
//	                    parameters.replicas` (or whatever the Blueprint
//	                    declares). regions[1..] are STANDBYS — run
//	                    with `replicas: 0` (or `replica: false` on
//	                    CNPG-style resources). The Continuum
//	                    controller (#1101) flips replicas back to a
//	                    non-zero value on switchover. (Legacy alias:
//	                    active-hotstandby.)
//
//	active-passive:     same fan-out shape as active-hot-standby
//	                    (primary in regions[0], standbys with
//	                    replicas:0), but the secondary is a cold/warm
//	                    standby (no in-flight sync; backup/restore
//	                    cutover). Distinguished from active-hot-standby
//	                    only by the Blueprint's replication/switchover
//	                    knobs, not by the fan-out plan.
//
// Legacy spellings (`single-region`, `active-hotstandby`) are still
// ACCEPTED everywhere they were before — Canonicalize folds them onto
// the canonical token so no existing Application CR, blueprint.yaml
// `placementSchema.modes`, or in-flight POST breaks. New emitters
// (the editors, the catalog-served modes) emit the canonical spelling.
//
// This package is pure functions — no I/O, no K8s client calls. It is
// consumed by `internal/render` which serializes the per-region plan
// to YAML manifests, and by the controller's status writer which uses
// `Plan.PrimaryRegion` to populate `status.primaryRegion`. It is the
// single Go source of truth for the canonical vocabulary; the
// catalyst-api handler + the FE editors mirror CanonicalModes() and a
// CI drift test pins them together.
package placement

import (
	"fmt"
	"sort"
	"strings"
)

// Mode constants — the ONE canonical placement vocabulary (#3375
// DoD-1). Mirror Blueprint.spec.topology.supported[] in
// core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go and
// the catalyst-api canonicaliser (endpoint_handler.go
// canonicalizeTopology). Legacy spellings live in the Legacy* aliases
// below; Canonicalize folds them onto these.
const (
	ModeSingleton        = "singleton"
	ModeActiveActive     = "active-active"
	ModeActiveHotStandby = "active-hot-standby"
	ModeActivePassive    = "active-passive"
)

// Legacy spelling aliases — accepted on input (existing CRs,
// blueprint.yaml placementSchema.modes, in-flight POSTs) and folded
// onto the canonical tokens by Canonicalize. NOT emitted by any new
// surface.
//
// ModeSingleRegion is retained (valued "single-region") so existing
// call sites + tests that reference it keep compiling and exercising
// the legacy spelling through Canonicalize.
//
// Deprecated: use ModeSingleton — the canonical token is "singleton";
// "single-region" is accepted on the wire only as a legacy alias.
const (
	ModeSingleRegion           = "single-region"
	LegacyModeActiveHotStandby = "active-hotstandby"
)

// CanonicalModes returns the ordered canonical vocabulary — the single
// source of truth every surface (catalog placementSchema, both
// editors, the CR resolver) must agree on. The CI drift test
// (placement_vocabulary_test.go) pins the FE + catalyst-api emitters
// to this exact set.
func CanonicalModes() []string {
	return []string{
		ModeSingleton,
		ModeActiveActive,
		ModeActiveHotStandby,
		ModeActivePassive,
	}
}

// Canonicalize folds any accepted spelling — canonical OR legacy alias
// (and the underscore / hyphen variants the FE/UI historically posted)
// — onto the ONE canonical token. Empty input returns empty (so the
// "derive the Blueprint default" path is preserved). An unrecognised
// non-empty value is returned trimmed + lower-cased unchanged, so the
// caller still rejects it as unsupported with a clean error rather
// than this function silently mangling it.
//
// This is the Go single source of truth for the alias mapping; the
// catalyst-api handler (canonicalizeTopology) and the FE editors
// (canonicalizeMode / canonicalizeTopologyMode) implement the SAME
// mapping and the drift test asserts they agree.
func Canonicalize(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "singleton", "single-region", "single_region":
		return ModeSingleton
	case "active-active", "active_active":
		return ModeActiveActive
	case "active-hot-standby", "active-hotstandby", "active_hot_standby":
		return ModeActiveHotStandby
	case "active-passive", "active_passive":
		return ModeActivePassive
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// IsCanonicalMode reports whether the (already-canonicalised) mode is
// one of the four canonical tokens.
func IsCanonicalMode(mode string) bool {
	switch mode {
	case ModeSingleton, ModeActiveActive, ModeActiveHotStandby, ModeActivePassive:
		return true
	}
	return false
}

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
//   - mode must canonicalise to one of the four canonical constants
//     (legacy spellings single-region / active-hotstandby are accepted
//     and folded onto the canonical token via Canonicalize)
//   - regions must be non-empty
//   - singleton must have exactly one entry (the schema's minItems
//     does not enforce maxItems for singleton; we validate here)
//
// The returned Plan.Mode always carries the CANONICAL token regardless
// of which spelling the caller passed, so downstream status writers +
// the fan-out renderer see one vocabulary.
//
// Returns a typed error so the controller can map "spec invariant
// violated" to a status.phase=Failed Condition with reason=Invalid.
func Resolve(mode string, regions []string) (Plan, error) {
	if len(regions) == 0 {
		return Plan{}, fmt.Errorf("placement: regions[] is empty")
	}
	// One vocabulary: fold any legacy spelling onto the canonical token
	// BEFORE switching, so callers passing single-region / active-
	// hotstandby resolve identically to the canonical spelling.
	canon := Canonicalize(mode)
	switch canon {
	case ModeSingleton:
		// Per CRD description: "singleton = one cluster only.
		// regions[1..] (if any) are ignored." We could enforce
		// len==1 here, but the schema allows up to 5 entries on the
		// shared regions[] array. We surface the constraint as an
		// Invalid Condition rather than silently dropping.
		if len(regions) != 1 {
			return Plan{}, fmt.Errorf("placement: singleton requires exactly 1 entry in regions[], got %d", len(regions))
		}
		return Plan{
			Mode:          canon,
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
			Mode:          canon,
			PrimaryRegion: "",
			Regions:       out,
		}, nil

	case ModeActiveHotStandby, ModeActivePassive:
		// Asymmetric: regions[0] is primary (runs hot), regions[1..]
		// are standby (replicas: 0). Order is load-bearing — preserved
		// from the spec. active-passive shares the SAME fan-out plan as
		// active-hot-standby (primary + replicas:0 standbys); the two
		// differ only by the Blueprint's replication/switchover knobs
		// (sync vs cold/warm), which are not expressed in the per-region
		// work plan.
		out := make([]RegionPlan, len(regions))
		out[0] = RegionPlan{Name: regions[0], Role: RolePrimary, Standby: false}
		for i := 1; i < len(regions); i++ {
			out[i] = RegionPlan{Name: regions[i], Role: RoleStandby, Standby: true}
		}
		return Plan{
			Mode:          canon,
			PrimaryRegion: regions[0],
			Regions:       out,
		}, nil

	default:
		return Plan{}, fmt.Errorf("placement: unknown mode %q (expected one of: %s, %s, %s, %s)",
			mode, ModeSingleton, ModeActiveActive, ModeActiveHotStandby, ModeActivePassive)
	}
}

// AllowedByBlueprint reports whether `mode` is in the Blueprint's
// `spec.placementSchema.modes[]` list. An empty `allowed` slice is
// treated as "Blueprint did not constrain placement" — every mode is
// allowed (the legacy default for Blueprints authored before the
// placementSchema was added; per BLUEPRINT-AUTHORING.md §3 the field
// is optional).
//
// Both `mode` and each `allowed` entry are canonicalised before
// comparison, so a Blueprint that declares the legacy spelling
// `single-region` in `placementSchema.modes` still admits a canonical
// `singleton` instance (and vice versa) — the one-vocabulary rule must
// not regress the 71-blueprint corpus that pre-dates the canonical
// spelling.
func AllowedByBlueprint(mode string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	want := Canonicalize(mode)
	for _, m := range allowed {
		if Canonicalize(m) == want {
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
//
// The env the chart stamps still carries the legacy provisioner
// spelling (`single-region` / `active-hotstandby`), so these retain
// the legacy values; EffectiveDefault canonicalises before comparing.
const (
	SovereignTopologySingleRegion     = "single-region"
	SovereignTopologyActiveHotStandby = "active-hotstandby"
	SovereignTopologyActiveActive     = "active-active"
)

// EffectiveDefault picks the right placement mode when the Application
// CR's spec.placement is empty:
//
//  1. If the Sovereign is multi-region (active-hot-standby /
//     active-active) AND the Blueprint declares
//     `placementSchema.defaultOnMultiRegion`, return that (canonicalised).
//     This is the G93.2 (Refs #2667) target-state — every CNPG-backed
//     Blueprint sets defaultOnMultiRegion so a fresh marketplace install
//     on a 2-region Sovereign lands Pillar 3 zero-touch.
//  2. Otherwise fall back to `placementSchema.default` (canonicalised).
//  3. If neither is set, return "singleton" as the safe last resort.
//
// The returned value is always a CANONICAL token (legacy spellings in
// the Blueprint declarations are folded). The Sovereign-topology input
// is canonicalised before the multi-region test so the legacy env
// spelling (`active-hotstandby`) resolves the same as the canonical.
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
	switch Canonicalize(sovereignTopology) {
	case ModeActiveHotStandby, ModeActiveActive, ModeActivePassive:
		if blueprintDefaultOnMultiRegion != "" {
			return Canonicalize(blueprintDefaultOnMultiRegion)
		}
	}
	if blueprintDefault != "" {
		return Canonicalize(blueprintDefault)
	}
	return ModeSingleton
}
