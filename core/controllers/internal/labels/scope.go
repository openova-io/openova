// Package labels — Manara DNA label-based scope matcher.
//
// Per docs/EPICS-1-6-unified-design.md §6.3:
//
//	UserAccess.spec.scopes: [{labelKey, labelValue}]
//	AND within a UserAccess, OR across UserAccess.
//	Wildcard scope `[{*: *}]` = global access.
//
// The matcher is consumed by useraccess-controller (slice C5) when
// deciding whether to materialize a RoleBinding for a given target
// resource. It is the wire-level label algebra; the catalog-tier role
// composition (viewer < developer < operator < admin < owner) is layered
// on top in EPIC-3 (#1098) and consumes ResolveTier() here.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 nothing is hardcoded — wildcards,
// tier names, and the openova.io label namespace are constants only
// because they are part of the documented public contract (EPICS-1-6
// design doc §1.1, §6.2).
package labels

import "strings"

// Wildcard is the literal scope value that means "any" — matches both
// keys and values per the design doc's `{*: *}` shorthand. A scope row
// is global when EITHER its key OR its value is the wildcard; the most
// permissive scope ({*:*}) is the canonical `cluster-wide` form.
const Wildcard = "*"

// Scope is one (key, value) pair from a UserAccess spec.scopes[] entry.
// The shape mirrors the CRD's labelKey / labelValue structural fields
// so a controller can construct a Scope directly from
// unstructured.NestedSlice() output without an intermediate type.
type Scope struct {
	Key   string
	Value string
}

// IsWildcard reports whether the scope row is the global-access shorthand
// — i.e. the most permissive form `{*:*}`. The design contract is:
// either-side wildcard is acceptable for a wildcard scope row, but the
// canonical form (and the one the UI emits) is `{*:*}`.
func (s Scope) IsWildcard() bool {
	return s.Key == Wildcard && s.Value == Wildcard
}

// Match reports whether the given target labels satisfy this single
// scope row. A wildcard key matches any key; a wildcard value matches
// any value at that key. A non-wildcard scope requires the target to
// carry the exact label key with the exact value.
func (s Scope) Match(target map[string]string) bool {
	if s.IsWildcard() {
		return true
	}
	if s.Key == Wildcard {
		// `*=value` matches if ANY label on the target has the value.
		for _, v := range target {
			if v == s.Value {
				return true
			}
		}
		return false
	}
	if s.Value == Wildcard {
		// `key=*` matches if the target carries the key (any value).
		_, ok := target[s.Key]
		return ok
	}
	got, ok := target[s.Key]
	return ok && got == s.Value
}

// AndWithin reports whether the target labels satisfy ALL scope rows in
// `scopes` (AND-within a single UserAccess CR). Empty scopes is treated
// as global (matches everything) — the same semantic the CRD uses when
// spec.scopes is omitted, which the wizard emits for "all applications,
// all environments" grants.
func AndWithin(scopes []Scope, target map[string]string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, s := range scopes {
		if !s.Match(target) {
			return false
		}
	}
	return true
}

// OrAcross reports whether ANY of the supplied per-UserAccess scope sets
// matches the target. Each inner slice represents one UserAccess CR's
// scopes[]; OrAcross is what the controller calls when deciding whether
// to bind a user to a target across the operator's full UserAccess set.
func OrAcross(perUASets [][]Scope, target map[string]string) bool {
	for _, scopes := range perUASets {
		if AndWithin(scopes, target) {
			return true
		}
	}
	return false
}

// EnforcedScopes returns the auto-injected scope rows for a given
// catalog tier per docs/EPICS-1-6-unified-design.md §6.2.
//
// The full tier system (role inheritance: developer composes viewer,
// operator composes developer, etc.) is the EPIC-3 (#1098) concern.
// Slice C5 ships the wire-level enforcement: developer auto-gets
// `env-type=dev`. Other tiers carry no enforced scope today — the
// controller composes the user's declared scopes with whatever this
// returns and matches against the resulting set.
//
// Returning a copy on every call is intentional: callers append to the
// result without disturbing the package-internal table.
func EnforcedScopes(tier string) []Scope {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "developer":
		// Developers can only act in dev environments; the design doc
		// pins this scope as auto-injected so an operator cannot
		// accidentally grant a developer prod access via UI omission.
		return []Scope{{Key: "openova.io/env-type", Value: "dev"}}
	case "viewer", "operator", "admin", "owner":
		// No auto-injected scope at these tiers. The user's declared
		// scopes carry the entire restriction.
		return []Scope{}
	default:
		// Unknown tier — return nil so callers can distinguish
		// "no enforced scope" from "unknown tier" via len() vs nil.
		return nil
	}
}

// CatalogTiers returns the 5 fixed tier names in ascending precedence
// order. EPIC-3 (#1098) consumes this for the role-inheritance logic.
// Returned in slice form so callers can range without re-declaring it.
func CatalogTiers() []string {
	return []string{"viewer", "developer", "operator", "admin", "owner"}
}

// TierLevel returns the numeric precedence per the design doc §6.2.
// `0` for unknown tiers (caller-side fallback for legacy admin/editor/
// viewer role enums that pre-date the catalog tier system).
func TierLevel(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "viewer":
		return 10
	case "developer":
		return 20
	case "operator":
		return 30
	case "admin":
		return 40
	case "owner":
		return 50
	default:
		return 0
	}
}
