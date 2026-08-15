// Package handler — applications_placement_declared_roles_6347.go (#6347 /
// UAT row 60).
//
// THE DEFECT, measured on hw298 (dep 2540d866403f1f7c) AFTER #6345 landed and
// the mothership was rolled to `catalyst-api:8c41df2`:
//
//	shared-pg      active-hot-standby → 2 targets  Primary(cluster set) + Standby(cluster set)
//	spine-gitea    active-hot-standby → 3 targets  Primary(set) + Primary(set) + Standby(cluster "")
//	spine-keycloak active-hot-standby → 3 targets  Primary(set) + Primary(set) + Standby(cluster "")
//
// #6345 made the Primary resolve — and nothing downstream turns two occupied
// regions into ONE Primary and ONE Standby. `bpv1.DerivePattern` reads a
// 2-Primary list as `active-active`, and `bpv1.ValidatePlacement` rejects it
// outright as `MultiPrimaryNotSupported` under the default `primary+standby`
// capability: the projection was emitting a placement its own validator
// refuses.
//
// WHY THE CONTROL ESCAPES, AND WHY THAT IS NOT A PROPERTY OF THE APP.
// `derivePlacementTargets` assigns roles from the `openova.io/cnpg-role` label
// when it sees one — positive, per-leg evidence of which half serves writes.
// `shared-pg` has it; a Deployment-backed app does not. Its last arm therefore
// reads "every occupied region serves traffic → Primary", which is a fair
// reading for a bootstrap component that declares nothing, and the wrong one
// for an app that declares `active-hot-standby`. gitea and keycloak really do
// run in both region clusters — `10-gitea.yaml` / `09-keycloak.yaml` carry no
// `catalyst.openova.io/region-role` gate, and "no gate ⇒ every region".
//
// WHAT THIS FILE ADDS. The declaration the app already carries, used ONLY where
// no runtime evidence exists. `placement.Resolve`
// (core/controllers/internal/placement/placement.go) puts `regions[0]` Primary
// and `regions[1..]` Standby for both asymmetric modes; `placementRegionCountError`
// states the same rule back to any caller that tries to write a one-region
// multi-region placement ("regions[0] is the primary and regions[1..] are the
// standbys"). So regions[0] is not a heuristic invented here — it is the rule
// the write doors already enforce, read back on the projection side.
//
// THE THREE LINES IT WILL NOT CROSS:
//
//   - It never OVERRULES an observation. A CNPG pair that has failed over
//     reports its live primary in region B while `spec.regions[0]` still names
//     region A; the labels win, always. A declaration is not evidence, and a
//     projection that tracked config here would point an operator at the region
//     that no longer holds the write path — worse than the defect being fixed.
//   - It never INVENTS a leg. Everything here re-labels targets that occupancy
//     already produced. An app with no Pods in a region still has no target
//     there, the #6268 half-pair refusal still fires, and an app occupied only
//     in its declared STANDBY region now says so honestly (a lone Standby,
//     `unresolvedPrimary: true`) instead of promoting that leg to writer.
//   - It never SPEAKS where the declaration is silent or degenerate. No
//     Application CR, no `spec.placement`, a `singleton` / `active-active`
//     posture, or fewer than two distinct regions ⇒ byte-identical behaviour.
//     `active-active` is multi-primary BY DEFINITION and must keep both
//     Primaries.
//
// AND THE REGION VOCABULARY IS LOAD-BEARING. A target's region comes from the
// `openova.io/region` node label — the full cluster name
// `hw-me-east-215-a-rtz-prod` — while a spine Application's `spec.regions[]`
// carries the bare cloud region `me-east-215-a` (post_handover_spine_apps.go
// takes them from `RegionSpec.CloudRegion`), and #5482 recorded all three
// divergent spellings on one Application. Comparing the two raw resolves
// NOTHING: the fix would read as shipped and change no wire byte. The
// controller already folds the two vocabularies together for exactly this
// reason (`normalizeRegion`, core/controllers/application/internal/controller/
// placement_projection.go) — `internal/` puts that package out of reach from
// this module, so the fold is mirrored here, token set for token set, and
// pinned by its own table.
//
// Refs #6347, #6344, #6345, #3375 (UAT row 60).
package handler

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	bpv1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// declaredPlacement is an Application's OWN statement of its posture: the
// canonical mode plus the ordered region list `placement.Resolve` reads. The
// zero value means the component declared nothing — a bootstrap-kit HelmRelease
// with no Application CR, or a CR with no placement — and every method below
// answers "no opinion" for it, which is what keeps the un-declared components
// on their pre-#6347 projection.
type declaredPlacement struct {
	mode    string   // canonical (canonicalizeTopology)
	regions []string // spec.regions[], order load-bearing: [0] is the primary
}

// asymmetric reports whether this declaration names ONE writer and one or more
// followers — the `primary+standby` capability class. Both members of that
// class are included because both resolve the same way in placement.Resolve
// (regions[0] primary, regions[1..] standby); they differ only in the standby
// TYPE. `active-active` is deliberately absent: it is multi-primary by
// definition, so its two Primaries are correct and must not be touched.
//
// Two DISTINCT regions are required, mirroring placementRegionCountError:
// `["a","a"]` is one place, and a declaration that names one place cannot say
// which of two observed legs is the follower.
func (d declaredPlacement) asymmetric() bool {
	switch d.mode {
	case "active-hot-standby", "active-passive":
		return len(d.normalizedRegions()) >= 2
	}
	return false
}

// standbyType is the follower type the DECLARED mode implies. It is the only
// evidence available for it: `StandbyHot` asserts a live streaming replica that
// promotes in seconds, which nothing on this path observes, so the type is
// mirrored from the declaration rather than assumed — and for `active-passive`
// that means the weaker claim (Cold), never the stronger one.
func (d declaredPlacement) standbyType() bpv1.StandbyType {
	if d.mode == "active-passive" {
		return bpv1.StandbyCold
	}
	return bpv1.StandbyHot
}

// normalizedRegions is the declared list folded onto one region vocabulary,
// de-duplicated, ORDER PRESERVED — so entry 0 stays the primary.
func (d declaredPlacement) normalizedRegions() []string {
	out := make([]string, 0, len(d.regions))
	seen := make(map[string]struct{}, len(d.regions))
	for _, r := range d.regions {
		r = normalizePlacementRegion(r)
		if r == "" {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}

// roleForRegion returns the role the DECLARATION assigns to a region, and
// ok=false when it assigns none — no asymmetric declaration, an unresolvable
// region, or a region the declaration simply does not name (a component running
// somewhere its own spec never mentioned; the caller then keeps whatever the
// runtime derived, because guessing is what this file exists to stop).
func (d declaredPlacement) roleForRegion(region string) (bpv1.DataRole, bpv1.StandbyType, bool) {
	if !d.asymmetric() {
		return "", "", false
	}
	key := normalizePlacementRegion(region)
	if key == "" {
		return "", "", false
	}
	regions := d.normalizedRegions()
	if key == regions[0] {
		return bpv1.DataRolePrimary, "", true
	}
	for _, r := range regions[1:] {
		if key == r {
			return bpv1.DataRoleStandby, d.standbyType(), true
		}
	}
	return "", "", false
}

// declaredPlacementForComponent reads the posture off the Application CR the
// route id names, through the SAME selector the identity resolution uses
// (applicationCRForComponent) — one CR, one Organization-isolation rule. A
// component with no CR, or a cache that cannot list Applications, yields the
// zero value.
func (h *Handler) declaredPlacementForComponent(primaryID, name, ns string) declaredPlacement {
	return declaredPlacementFromCR(h.applicationCRForComponent(primaryID, componentNameCandidates(name), ns))
}

// declaredPlacementFromCR projects the CR's declaration. `spec.placement` is
// read through placementFromSpec so BOTH stored shapes resolve (the legacy bare
// string and the #3373 object whose posture rides in `mode`), and the region
// list is `spec.regions[]` — the exact slice placement.Resolve is called with
// (application_controller.go: `placement.Resolve(spec.Placement, spec.Regions)`)
// — falling back to the object form's own `regions` only when `spec.regions` is
// absent entirely.
func declaredPlacementFromCR(cr *unstructured.Unstructured) declaredPlacement {
	if cr == nil {
		return declaredPlacement{}
	}
	d := declaredPlacement{mode: canonicalizeTopology(placementFromSpec(cr))}
	if regs, ok, err := unstructured.NestedStringSlice(cr.Object, "spec", "regions"); err == nil && ok && len(regs) > 0 {
		d.regions = regs
	} else if regs, ok, err := unstructured.NestedStringSlice(cr.Object, "spec", "placement", "regions"); err == nil && ok {
		d.regions = regs
	}
	return d
}

// placementClusterName{Providers,BuildingBlocks,EnvTypes} are the CLOSED token
// sets that identify a `{prov}-{reg}-{bb}-{env_type}` host-cluster name. They
// mirror the controller's sets (placement_projection.go); a token added there
// belongs here too.
var (
	placementClusterNameProviders      = map[string]bool{"hw": true, "hz": true, "aws": true, "gcp": true, "azure": true}
	placementClusterNameBuildingBlocks = map[string]bool{"rtz": true, "dmz": true, "mgt": true, "mgmt": true}
	placementClusterNameEnvTypes       = map[string]bool{"prod": true, "stg": true, "staging": true, "dev": true, "uat": true}
)

// normalizePlacementRegion folds a host-cluster name (hw-me-east-215-a-rtz-prod)
// down to its bare region label (me-east-215-a) so both spellings of one place
// compare equal. IDEMPOTENT: a value that is already a bare region comes back
// unchanged.
//
// Detection anchors on the CLOSED token sets, not the segment count — that is
// what tells the two 4-segment shapes apart: `me-east-215-a` (region: first
// segment is not a provider, last is not an env_type) versus `hz-fsn-rtz-prod`
// (cluster: provider first, building-block second-to-last, env_type last). Any
// value that does not match every anchor is returned VERBATIM — the
// `platform-bootstrap-owned-host` placeholder, the seed door's literal
// "primary", or anything unexpected — so an unrecognised region stays visible
// instead of being silently mangled into a false match.
func normalizePlacementRegion(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "-")
	if len(parts) < 4 {
		return s
	}
	if !placementClusterNameProviders[parts[0]] ||
		!placementClusterNameBuildingBlocks[parts[len(parts)-2]] ||
		!placementClusterNameEnvTypes[parts[len(parts)-1]] {
		return s
	}
	region := strings.Join(parts[1:len(parts)-2], "-")
	if region == "" {
		return s
	}
	return region
}

// samePlacementRegion is the single spelling-tolerant region comparison, so no
// caller re-hand-rolls it with a raw `==` and silently stops matching.
func samePlacementRegion(a, b string) bool {
	na, nb := normalizePlacementRegion(a), normalizePlacementRegion(b)
	return na != "" && na == nb
}
