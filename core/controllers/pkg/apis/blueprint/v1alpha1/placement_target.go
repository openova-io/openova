// Package v1alpha1 — #3969 application-centric Placement model.
//
// This file is the canonical, single-desired-state placement model that
// replaces the `declared / observed / effective / mode / DR` machinery
// (#3375 / #3373 / docs/topology-matrix.md). The model in one line:
//
//	ONE desired state = the Application's Placement (a list of targets,
//	each = region × cluster × vCluster × data-role Primary|Standby, with
//	a Standby type Hot|Cold); the Blueprint's `placementCapability`
//	(multi-primary | primary+standby) gates whether >1 target may be
//	Primary; the pattern name (singleton / active-passive /
//	active-hot-standby / active-active) is a DERIVED, read-only label —
//	never stored, never selected.
//
// There is NO "mode" enum and NO separately-stored "declared class". The
// pattern is computed for DISPLAY ONLY by DerivePattern. Status is the
// recon vocabulary (Reconciled / Reconciling / Degraded) — a mismatch is
// never rendered as a second contradictory value.
//
// Ref #3969.
package v1alpha1

import "strings"

// DataRole is the per-target data role: whether the target serves writes
// (Primary) or follows (Standby). LOCKED vocabulary (#3969 §4.2).
type DataRole string

const (
	// DataRolePrimary — this target serves writes.
	DataRolePrimary DataRole = "Primary"
	// DataRoleStandby — this target follows the Primary. It carries a
	// StandbyType (Hot|Cold).
	DataRoleStandby DataRole = "Standby"
)

// IsValid reports whether r is a recognised data role.
func (r DataRole) IsValid() bool {
	switch r {
	case DataRolePrimary, DataRoleStandby:
		return true
	}
	return false
}

// StandbyType is an ATTRIBUTE OF a Standby target (#3969 §4.2): Hot =
// live/streaming replica, fast promote; Cold = snapshot/bucket follower
// that runs no process and rebuilds on failover. It is NOT a separate
// role.
type StandbyType string

const (
	// StandbyHot — live streaming replica; promote ≈ seconds.
	StandbyHot StandbyType = "Hot"
	// StandbyCold — snapshot/bucket follower; runs no process, rebuilds
	// on failover (non-zero RPO).
	StandbyCold StandbyType = "Cold"
)

// IsValid reports whether s is a recognised standby type.
func (s StandbyType) IsValid() bool {
	switch s {
	case StandbyHot, StandbyCold:
		return true
	}
	return false
}

// PlacementCapability is the per-Blueprint capability that GATES whether
// more than one target may be Primary (#3969 §4.3). It is the only stored
// gate — the pattern itself is derived.
type PlacementCapability string

const (
	// CapabilityMultiPrimary — the Blueprint supports multiple live
	// Primary targets (active-active). Multi-master.
	CapabilityMultiPrimary PlacementCapability = "multi-primary"
	// CapabilityPrimaryStandby — exactly one Primary; the rest are
	// Standby. The default when a Blueprint omits the capability.
	CapabilityPrimaryStandby PlacementCapability = "primary+standby"
)

// IsValid reports whether c is a recognised capability.
func (c PlacementCapability) IsValid() bool {
	switch c {
	case CapabilityMultiPrimary, CapabilityPrimaryStandby:
		return true
	}
	return false
}

// NormalizeCapability folds an empty / unrecognised capability to the
// safe default (primary+standby) so a Blueprint that omits the field is
// treated as single-Primary, never accidentally multi-primary.
func NormalizeCapability(c PlacementCapability) PlacementCapability {
	if c == CapabilityMultiPrimary {
		return CapabilityMultiPrimary
	}
	return CapabilityPrimaryStandby
}

// Pattern is the DERIVED, read-only placement-pattern label (#3969 §4.4).
// It is NEVER stored and NEVER selected — DerivePattern computes it from
// the targets + roles (+ standby types) for display only.
type Pattern string

const (
	// PatternSingleton — 1 Primary, no Standby.
	PatternSingleton Pattern = "singleton"
	// PatternActivePassive — 1 Primary + ≥1 Cold Standby.
	PatternActivePassive Pattern = "active-passive"
	// PatternActiveHotStandby — 1 Primary + ≥1 Hot Standby.
	PatternActiveHotStandby Pattern = "active-hot-standby"
	// PatternActiveActive — ≥2 Primary (requires multi-primary).
	PatternActiveActive Pattern = "active-active"
)

// PlacementTarget — one desired target in an Application's Placement
// (#3969 §4.1): region × cluster × vCluster × data-role. A Standby target
// carries a StandbyType.
type PlacementTarget struct {
	// Region — the catalyst region id this target runs in
	// (e.g. region-a / me-east-215-a). REQUIRED.
	Region string `json:"region"`

	// Cluster — the canonical cluster id (Sovereign.spec.clusters name,
	// e.g. mgmt-A). REQUIRED.
	Cluster string `json:"cluster"`

	// VCluster — the vCluster tier the target lands in: "mgmt" | "dmz" |
	// "rtz" | "host" (or "" = host). Mirrors InstancePlacement.VCluster.
	VCluster string `json:"vcluster,omitempty"`

	// Role — Primary | Standby. REQUIRED.
	Role DataRole `json:"role"`

	// StandbyType — Hot | Cold. REQUIRED iff Role==Standby; FORBIDDEN iff
	// Role==Primary (admission CEL).
	StandbyType StandbyType `json:"standbyType,omitempty"`
}

// OwnedDependencyOverride — a per-owned-dependency cascade override
// (#3969 §7.1). Absent ⇒ the owned dep follows the consumer's placement
// by default. `follow:false` decouples it (the user pins it).
type OwnedDependencyOverride struct {
	// Name — the owned backing-service instance name (e.g. keycloak-pg).
	Name string `json:"name"`
	// Follow — default true; false = decoupled (user-edited). The cascade
	// resolver honours the consumer's placement for this dep ONLY when
	// Follow is true.
	Follow bool `json:"follow"`
}

// Placement is the desired-state placement of an Application (#3969 §7.1).
// It replaces the legacy InstancePlacement.Mode posture enum: the WHERE +
// the ROLE live in Targets[]; the pattern is derived, never stored.
type Placement struct {
	// Targets — the desired target list. Empty ⇒ inherit the Blueprint's
	// defaultPlacement (the wizard pre-fill).
	Targets []PlacementTarget `json:"targets,omitempty"`

	// OwnedDependencies — cascade overrides. Absent entries follow by
	// default; an entry with Follow:false decouples that owned dep.
	OwnedDependencies []OwnedDependencyOverride `json:"ownedDependencies,omitempty"`
}

// DerivePattern computes the read-only pattern label from the targets +
// the Blueprint capability (#3969 §7.3). This is the ONLY place patterns
// come from. It NEVER stores; callers use it for display.
//
//	pattern(targets, capability):
//	  primaries = [t for t in targets if t.role == Primary]
//	  standbys  = [t for t in targets if t.role == Standby]
//	  if len(primaries) >= 2:             -> "active-active"
//	  if len(standbys)  == 0:             -> "singleton"
//	  if any(s.standbyType == Hot for s): -> "active-hot-standby"
//	  else:                               -> "active-passive"
//
// The capability argument is accepted for symmetry with ValidatePlacement
// (callers pass both) — the derivation itself reads only the targets, so
// an >2-Primary placement still derives "active-active" even when the
// capability would reject it; ValidatePlacement is the gate, DerivePattern
// is the label.
func DerivePattern(targets []PlacementTarget, _ PlacementCapability) Pattern {
	var primaries, standbys int
	var anyHot bool
	for _, t := range targets {
		switch t.Role {
		case DataRolePrimary:
			primaries++
		case DataRoleStandby:
			standbys++
			if t.StandbyType == StandbyHot {
				anyHot = true
			}
		}
	}
	switch {
	case primaries >= 2:
		return PatternActiveActive
	case standbys == 0:
		return PatternSingleton
	case anyHot:
		return PatternActiveHotStandby
	default:
		return PatternActivePassive
	}
}

// MultiPrimaryNotSupportedReason is the canonical admission/validation
// reason emitted when a placement declares >1 Primary target but the
// Blueprint capability is primary+standby (#3969 §7.3, DoD item 5). The
// editor surfaces it as a disabled radio + inline reason.
const MultiPrimaryNotSupportedReason = "MultiPrimaryNotSupported"

// TargetMissingRegionReason is the canonical validation reason emitted when
// a DECLARED placement target carries no region (#5639).
//
// A target with an empty region is not "unspecified, fill in a default" — it
// renders literally, as `openova.io/region: ""` on the workload and
// `nodeAffinity ... values: [""]` in its required node selector. No node ever
// carries the empty-string region, so the Pod is unschedulable FOREVER while
// the HelmRelease still reports install succeeded. hw292's per-Org
// bp-postgres sat that way for 2d9h in `Setting up primary`, 0 ready, with a
// green badge on every status surface.
//
// So the placement gate fails CLOSED on it: an empty region is rejected here,
// naming the target, rather than accepted and rendered into an impossible
// constraint downstream. The frontend mirrors this in
// products/catalyst/bootstrap/ui/src/shared/lib/placement.ts.
const TargetMissingRegionReason = "TargetMissingRegion"

// ValidatePlacement enforces the capability gate + the role/standbyType
// invariants (#3969 §7.1, §7.3). It returns a non-nil *PlacementError with
// a stable Reason the caller maps onto the Application condition / API
// response. It does NOT mutate the inputs.
//
// Invariants enforced:
//   - every target names a region (#5639 — an empty one is unschedulable,
//     not "default", see TargetMissingRegionReason);
//   - every target has a valid Role;
//   - a Standby target has a valid StandbyType; a Primary target has none;
//   - at least one Primary (when any targets are declared);
//   - >1 Primary requires capability==multi-primary
//     (else MultiPrimaryNotSupported).
func ValidatePlacement(p Placement, capability PlacementCapability) error {
	cap := NormalizeCapability(capability)
	var primaries int
	for i, t := range p.Targets {
		// #5639 — checked FIRST because it is the invariant that decides
		// whether the target can run at all. A target that names no region
		// renders `openova.io/region In [""]`; no node carries the empty
		// string, so the workload never schedules while the install still
		// reports success.
		if strings.TrimSpace(t.Region) == "" {
			return &PlacementError{
				Reason: TargetMissingRegionReason,
				Msg: "target[" + itoa(i) + "] declares no region; an empty region renders " +
					"openova.io/region In [\"\"], which no node can satisfy, so the workload is " +
					"unschedulable forever while the install still reports success (#5639)",
			}
		}
		if !t.Role.IsValid() {
			return &PlacementError{
				Reason: "InvalidRole",
				Msg:    "target[" + itoa(i) + "] role " + quote(string(t.Role)) + " is not Primary|Standby",
			}
		}
		switch t.Role {
		case DataRolePrimary:
			primaries++
			if t.StandbyType != "" {
				return &PlacementError{
					Reason: "PrimaryHasStandbyType",
					Msg:    "target[" + itoa(i) + "] is Primary but carries standbyType " + quote(string(t.StandbyType)),
				}
			}
		case DataRoleStandby:
			if !t.StandbyType.IsValid() {
				return &PlacementError{
					Reason: "StandbyMissingType",
					Msg:    "target[" + itoa(i) + "] is Standby but standbyType " + quote(string(t.StandbyType)) + " is not Hot|Cold",
				}
			}
		}
	}
	if len(p.Targets) > 0 && primaries == 0 {
		return &PlacementError{Reason: "NoPrimary", Msg: "placement declares no Primary target"}
	}
	if primaries >= 2 && cap != CapabilityMultiPrimary {
		return &PlacementError{
			Reason: MultiPrimaryNotSupportedReason,
			Msg:    "placement declares " + itoa(primaries) + " Primary targets but the blueprint capability is primary+standby (only one Primary allowed)",
		}
	}
	return nil
}

// PlacementError is a validation error carrying a stable Reason the caller
// stamps on the Application condition / surfaces in the editor.
type PlacementError struct {
	Reason string
	Msg    string
}

func (e *PlacementError) Error() string { return e.Reason + ": " + e.Msg }

// ── small local helpers (avoid pulling fmt/strconv into the types pkg
//    for two trivial calls; keeps the package dependency-light) ────────

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func quote(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "'") + "\"" }
