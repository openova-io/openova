package handler

import (
	"fmt"
	"strings"
)

// placementRegionCountError enforces the one rule that separates a DR promise
// from a DR posture: a multi-region topology must name more than one region
// (UAT row 60).
//
// THE DEFECT IT CLOSES. Both write doors — POST /applications
// (validateApplicationInstallRequest) and PUT /applications/{name}
// (validateApplicationUpdateRequest) — checked only `len(regions) >= 1`, for
// EVERY mode. So `{"mode":"active-hot-standby","regions":["me-east-215-a"]}`
// was accepted, persisted, and reported back as a hot-standby Application.
// Nothing downstream can make that true:
//
//   - placement.Resolve (core/controllers/internal/placement/placement.go:251)
//     puts regions[0] Primary and iterates regions[1..] for the standbys, so a
//     one-region list yields ZERO standby regions;
//   - buildContinuumPlan (core/controllers/application/internal/controller/
//     continuum.go:165) then returns (zero, false) precisely because
//     `len(standbys) == 0`, so NO Continuum CR is minted — and the Continuum CR
//     is what the per-app Topology tab arms its Switchover against;
//   - applications_preview.go marks a region standby only when `i > 0`, so the
//     rendered manifest set is a single primary.
//
// The result was a DR promise that reported success at every layer: HTTP 201,
// `phase: Ready`, `spec.placement.mode: active-hot-standby` — over one region,
// with `status.perCluster[0].role: singleton` sitting next to it and no
// standby anywhere (#6033, measured on hw293). Refs #6033.
//
// So the rule fails CLOSED here, where it can still be named, rather than
// resolving into a silent single-region install. This is NOT a new rule — it
// is an unenforced one. Both surfaces that already ask an operator for a
// multi-region placement enforce it in the browser and always have:
// `core/marketplace/src/components/BCPStep.svelte:230` ("Primary and replica
// regions must differ — pick two distinct regions to enable
// active-hot-standby") and
// `products/catalyst/bootstrap/ui/src/pages/sovereign/AppDetail/InstancesSection.tsx`
// ("#3599 validation — multi-region modes need ≥2 regions"). The doors behind
// them did not, so any caller that skipped those forms — including the
// console's own POST /applications install page — could write the shape the
// forms refuse.
//
// DISTINCT regions, not entry count: `["a","a"]` is one place. The update
// door reaches this rule through applicationUpdateRequestNormalize, which
// folds the #3969 `targets[]` onto mode+regions via
// regionsFromPlacementTargets — and that helper DEDUPES, so a PlacementEditor
// Apply carrying a Primary and a Standby that name the same region arrives
// here as active-hot-standby over one region. Counting entries would let it
// through.
//
// Returns "" when the placement is acceptable, so callers read it as
// `if msg := placementRegionCountError(...); msg != "" { reject }`.
func placementRegionCountError(mode string, regions []string) string {
	canon := canonicalizeTopology(mode)
	if !placementModeRequiresMultipleRegions(canon) {
		return ""
	}
	seen := make(map[string]bool, len(regions))
	distinct := make([]string, 0, len(regions))
	for _, r := range regions {
		r = strings.TrimSpace(r)
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		distinct = append(distinct, r)
	}
	if len(distinct) >= 2 {
		return ""
	}
	named := "none"
	if len(distinct) == 1 {
		named = fmt.Sprintf("%q", distinct[0])
	}
	// State the consequence per CLASS. active-active has no standby by design —
	// its problem is that one region makes it a singleton under another name —
	// so a shared "has no standby" sentence would be wrong for it, and an error
	// an operator can tell is wrong is one they learn to skip.
	consequence := "regions[0] is the primary and regions[1..] are the standbys, so a " +
		"single-region " + canon + " has no standby to fail over to and no Continuum is created for it"
	if canon == "active-active" {
		consequence = "active-active means every region serves traffic, so over one region it is a " +
			"singleton under another name"
	}
	return fmt.Sprintf(
		"placement.mode %q needs at least 2 DISTINCT regions and got %d (%s) — %s. "+
			"Name a second region, or use placement.mode singleton for a one-region install",
		canon, len(distinct), named, consequence)
}

// placementModeRequiresMultipleRegions reports whether a canonical topology
// mode is a MULTI-REGION posture. Mirrors the frontend's MULTI_REGION_MODES
// (products/catalyst/bootstrap/ui/src/widgets/topology/modes.ts) — the same
// three classes, on the canonical vocabulary (#3375 DoD-1).
//
// `singleton` is deliberately absent: it is the one-region posture, and
// placement.Resolve hard-errors on a singleton carrying more than one region
// entry, so nothing here needs to second-guess it.
func placementModeRequiresMultipleRegions(canonicalMode string) bool {
	switch canonicalMode {
	case "active-active", "active-hot-standby", "active-passive":
		return true
	default:
		return false
	}
}
