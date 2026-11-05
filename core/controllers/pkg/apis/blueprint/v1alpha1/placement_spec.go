package v1alpha1

// placement_spec.go — the ONE stored shape of `Application.spec.placement`
// (#3969, the shape decision the EPIC's last comment asked for).
//
// # Contract
//
// `spec.placement` is an OBJECT. Always. The keys are:
//
//	mode              the posture token the fan-out plan is resolved against
//	                  (singleton / active-active / active-hot-standby /
//	                  active-passive). When `targets[]` is present the write
//	                  doors fold DerivePattern(targets) onto it, so it is the
//	                  derived label and never contradicts the targets.
//	targets[]         the #3969 desired state when the operator chose WHERE:
//	                  region × cluster × vCluster × role (Primary|Standby) and
//	                  a Standby type (Hot|Cold).
//	ownedDependencies the per-owned-dependency cascade overrides.
//	regions[]         ordered region list ([0] = primary) when the caller
//	                  declared one on the placement itself; `spec.regions[]`
//	                  stays the required top-level list the CRD has always
//	                  carried and is the fallback.
//	vcluster          the vCluster tier (host|mgmt|dmz|rtz).
//	clusters[]        fan-out narrowing to named clusters.
//
// Every key but `mode` is optional. A placement that declares nothing but a
// posture is `{mode: <token>}` — one shape, so no reader ever type-switches.
//
// # Why an object, not the string
//
// Measured on hw292 (2026-08-08) and again on hw307 (2026-11-06): every
// Application in the cluster carried the bare STRING form while the one
// field that can carry the #3969 model — `targets[]` — needs the object. A
// field with two shapes forces every reader to type-switch, and the one that
// does not silently gets a wrong answer (the EPIC thread records a false
// "placement not populated" reading from exactly that). The EPIC body names
// `placement.targets[]` as the canonical desired state, so the object is the
// contract and the string is the legacy spelling.
//
// # Legacy on READ only
//
// The bare string is still DECODED (DecodeApplicationPlacement) so a CR written
// before this change keeps reconciling, and `Legacy` reports that it was the
// old spelling so the application-controller can rewrite it once
// (canonicalizePlacement). No writer emits it any more.
//
// This file is dependency-light on purpose (the package rule): the mode fold
// mirrors core/controllers/internal/placement.Canonicalize, which this
// package cannot import without pulling that package into catalyst-api's
// build; a test in that package pins the two folds together.

import (
	"fmt"
	"strings"
)

// CanonicalMode folds every accepted posture spelling onto the canonical
// token. Mirrors placement.Canonicalize (core/controllers/internal/placement)
// exactly; the drift test there asserts the two agree. Empty stays empty (the
// "derive the Blueprint default" path); an unknown value is returned trimmed
// + lower-cased so the caller rejects it with a clean error.
func CanonicalMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "singleton", "single-region", "single_region":
		return string(BcpSingleton)
	case "active-active", "active_active":
		return string(BcpActiveActive)
	case "active-hot-standby", "active-hotstandby", "active_hot_standby":
		return string(BcpActiveHotStandby)
	case "active-passive", "active_passive":
		return string(BcpActivePassive)
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// ApplicationPlacement is the decoded `Application.spec.placement`.
type ApplicationPlacement struct {
	// Mode — canonical posture token; "" when the CR declares none (the
	// controller then derives the Blueprint default).
	Mode string
	// VCluster / VClusterSet — the declared tier, and whether it was
	// declared at all (an explicit "host" is a choice, not an absence).
	VCluster    string
	VClusterSet bool
	// Clusters — fan-out narrowing.
	Clusters []string
	// Regions — the placement's own ordered region list, when declared.
	Regions []string
	// Targets / OwnedDependencies — the #3969 desired state.
	Targets           []PlacementTarget
	OwnedDependencies []OwnedDependencyOverride
	// Legacy — the stored value was the bare posture string. The decoded
	// Mode is still canonical; this only tells the migration there is
	// something to rewrite.
	Legacy bool
	// Absent — spec.placement was not set at all.
	Absent bool
}

// DecodeApplicationPlacement reads `spec.placement` in any shape it has ever been
// stored in: absent (nil), the legacy bare string, or the canonical object.
// Mode is canonicalised on the way in. It does not validate the targets —
// ValidatePlacement is the gate — but it does refuse a malformed wire value
// (a non-string / non-object, or a list where a string belongs) rather than
// silently dropping it.
func DecodeApplicationPlacement(raw interface{}) (ApplicationPlacement, error) {
	out := ApplicationPlacement{}
	switch v := raw.(type) {
	case nil:
		out.Absent = true
		return out, nil
	case string:
		out.Mode = CanonicalMode(v)
		out.Legacy = true
		return out, nil
	case map[string]interface{}:
		if m, ok := v["mode"]; ok && m != nil {
			s, isStr := m.(string)
			if !isStr {
				return out, fmt.Errorf("spec.placement.mode is not a string: %T", m)
			}
			out.Mode = CanonicalMode(s)
		}
		if raw, exists := v["vcluster"]; exists {
			s, ok := raw.(string)
			if !ok {
				return out, fmt.Errorf("spec.placement.vcluster is not a string: %T", raw)
			}
			out.VCluster = s
			out.VClusterSet = true
		}
		var err error
		if out.Clusters, err = decodeStringList(v, "clusters"); err != nil {
			return out, err
		}
		if out.Regions, err = decodeStringList(v, "regions"); err != nil {
			return out, err
		}
		if raw, exists := v["targets"]; exists {
			items, ok := raw.([]interface{})
			if !ok {
				return out, fmt.Errorf("spec.placement.targets is not a list: %T", raw)
			}
			for i, it := range items {
				m, ok := it.(map[string]interface{})
				if !ok {
					return out, fmt.Errorf("spec.placement.targets[%d] is not an object: %T", i, it)
				}
				out.Targets = append(out.Targets, PlacementTarget{
					Region:      stringAt(m, "region"),
					Cluster:     stringAt(m, "cluster"),
					VCluster:    stringAt(m, "vcluster"),
					Role:        DataRole(stringAt(m, "role")),
					StandbyType: StandbyType(stringAt(m, "standbyType")),
				})
			}
		}
		if raw, exists := v["ownedDependencies"]; exists {
			items, ok := raw.([]interface{})
			if !ok {
				return out, fmt.Errorf("spec.placement.ownedDependencies is not a list: %T", raw)
			}
			for i, it := range items {
				m, ok := it.(map[string]interface{})
				if !ok {
					return out, fmt.Errorf("spec.placement.ownedDependencies[%d] is not an object: %T", i, it)
				}
				follow := true
				if b, ok := m["follow"].(bool); ok {
					follow = b
				}
				out.OwnedDependencies = append(out.OwnedDependencies, OwnedDependencyOverride{
					Name:   stringAt(m, "name"),
					Follow: follow,
				})
			}
		}
		return out, nil
	default:
		return out, fmt.Errorf("spec.placement is neither a string nor an object: %T", raw)
	}
}

// Canonical renders the ONE stored shape. Keys are emitted only when they
// carry a value, except `mode`, which is always present when known — a
// placement that declares only a posture is `{mode: <token>}`. The returned
// map is freshly allocated and safe to store on an unstructured object.
func (p ApplicationPlacement) Canonical() map[string]interface{} {
	out := map[string]interface{}{}
	if mode := CanonicalMode(p.Mode); mode != "" {
		out["mode"] = mode
	}
	if p.VClusterSet || p.VCluster != "" {
		out["vcluster"] = p.VCluster
	}
	if len(p.Regions) > 0 {
		out["regions"] = toInterfaceList(p.Regions)
	}
	if len(p.Clusters) > 0 {
		out["clusters"] = toInterfaceList(p.Clusters)
	}
	if len(p.Targets) > 0 {
		out["targets"] = PlacementTargetsToWire(p.Targets)
	}
	if len(p.OwnedDependencies) > 0 {
		deps := make([]interface{}, 0, len(p.OwnedDependencies))
		for _, d := range p.OwnedDependencies {
			deps = append(deps, map[string]interface{}{"name": d.Name, "follow": d.Follow})
		}
		out["ownedDependencies"] = deps
	}
	return out
}

// PlacementTargetsToWire renders targets as the unstructured list the CR
// stores. Keys a target does not declare are OMITTED — the CRD's admission
// CEL forbids `standbyType` on a Primary, and an empty `vcluster` would be
// read back as an explicit host choice.
func PlacementTargetsToWire(targets []PlacementTarget) []interface{} {
	out := make([]interface{}, 0, len(targets))
	for _, t := range targets {
		m := map[string]interface{}{
			"region": t.Region,
			"role":   string(t.Role),
		}
		if t.Cluster != "" {
			m["cluster"] = t.Cluster
		}
		if t.VCluster != "" {
			m["vcluster"] = t.VCluster
		}
		if t.StandbyType != "" {
			m["standbyType"] = string(t.StandbyType)
		}
		out = append(out, m)
	}
	return out
}

// CanonicalPlacementValue is the migration primitive: given the stored
// `spec.placement` in any shape, it returns the canonical object and whether
// the stored value differs from it in SHAPE (a legacy string, or an object
// whose `mode` carries a legacy spelling). An absent placement is left
// absent — this never invents a posture. The error is the decode error for a
// malformed value, which callers surface rather than migrate over.
func CanonicalPlacementValue(raw interface{}) (canonical map[string]interface{}, changed bool, err error) {
	spec, err := DecodeApplicationPlacement(raw)
	if err != nil {
		return nil, false, err
	}
	if spec.Absent {
		return nil, false, nil
	}
	if spec.Legacy {
		return spec.Canonical(), true, nil
	}
	// Already an object: the only rewrite is a legacy posture spelling in
	// `mode`. Everything else is carried verbatim, so a second writer's keys
	// (clusters, targets, …) are never dropped by a migration pass.
	obj, _ := raw.(map[string]interface{})
	if m, ok := obj["mode"].(string); ok && m != "" && m != CanonicalMode(m) {
		out := make(map[string]interface{}, len(obj))
		for k, v := range obj {
			out[k] = v
		}
		out["mode"] = CanonicalMode(m)
		return out, true, nil
	}
	return obj, false, nil
}

func decodeStringList(m map[string]interface{}, key string) ([]string, error) {
	raw, exists := m[key]
	if !exists || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("spec.placement.%s is not a list: %T", key, raw)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil, fmt.Errorf("spec.placement.%s[] entry is not a string: %T", key, it)
		}
		out = append(out, s)
	}
	return out, nil
}

func stringAt(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func toInterfaceList(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}
