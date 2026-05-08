// spec.go — translate an unstructured UserAccess CR into the typed
// shape the reconciler operates on. Mirrors the CRD's openAPI v3 schema
// at platform/crossplane-claims/chart/templates/xrds/useraccess.yaml,
// AND the slice-C5 brief's expanded shape:
//
//	UserAccess.spec:
//	  user:                              # = catalyst-api wire shape
//	    keycloakSubject: <oidc sub>
//	    keycloakGroups: [...]
//	  sovereignRef: <slug>
//	  applications:
//	    - app: <slug>
//	      role: admin|editor|viewer
//	      namespaces: [<ns>, ...]        # ["*"] → ClusterRoleBinding
//	      vClusters: [<name>, ...]       # rendered as vcluster-<name>
//	  scopes:                            # NEW (Manara DNA, §6.3)
//	    - labelKey: <openova.io/...>
//	      labelValue: <v>                # `*` allowed
//	  tier: <viewer|developer|operator|admin|owner>   # NEW (catalog tier)
//
// The CRD currently in main does not yet declare `scopes` and `tier` —
// EPIC-3 (#1098) extends the CRD with these fields. The controller
// reads them today via spec.NestedSlice / spec.NestedString so it is
// ready when the CRD ships. Until then, both arrive empty and the
// reconciler treats the grant as global (matches everything).

package controller

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/labels"
)

// UserAccessSpec is the parsed, typed view of a single UserAccess CR's
// spec. The reconciler reads .Subjects, .Apps, .Scopes and writes to
// the cluster.
type UserAccessSpec struct {
	// Subjects captured from spec.user.{keycloakSubject,keycloakGroups}.
	// Empty Subjects is invalid — the CRD requires at least one — and
	// the reconciler reports a Failed phase rather than panic.
	Subjects []Subject

	// SovereignRef carries the Sovereign identifier — used for the
	// labels-on-output and (eventually) cross-Sovereign scope filtering.
	SovereignRef string

	// Apps is the list of (application × role) grant entries.
	Apps []AppGrant

	// Scopes is the Manara-DNA label-set the reconciler matches against
	// candidate target labels. Empty = global (the controller still
	// materializes the bindings; scopes only filter when EPIC-3's
	// candidate-target evaluator runs).
	Scopes []labels.Scope

	// Tier (catalog tier) auto-injects scope rows per
	// internal/labels.EnforcedScopes. Empty Tier = no auto-injection.
	Tier string
}

// Subject is one RoleBinding subject (User or Group) materialized from
// the UserAccess.spec.user shape. The Name is rendered with the
// Keycloak api-server convention prefix (`oidc:<sub>` / `oidc:<group>`)
// to match --oidc-username-prefix / --oidc-groups-prefix on the
// kube-apiserver — same convention the existing Composition uses.
type Subject struct {
	Kind string // SubjectKindUser | SubjectKindGroup
	Name string // "oidc:<sub>" or "oidc:<group>"
}

// AppGrant is one (application, role, [namespaces|vClusters]) entry
// from spec.applications[].
type AppGrant struct {
	App        string
	Role       string
	Namespaces []string // "*" entry → cluster-wide (ClusterRoleBinding)
	VClusters  []string // "<name>" entry → namespace `vcluster-<name>`
}

// IsClusterWide reports whether the grant should be materialized as a
// ClusterRoleBinding (any namespaces[] entry equals "*") rather than
// per-namespace RoleBindings. The semantics: ANY wildcard wins; mixing
// "*" with explicit namespaces is folded to cluster-wide because a
// ClusterRoleBinding subsumes the per-namespace grant.
func (g AppGrant) IsClusterWide() bool {
	for _, ns := range g.Namespaces {
		if strings.TrimSpace(ns) == NamespaceWildcard {
			return true
		}
	}
	return false
}

// MaterializedNamespaces returns the concrete namespace list for this
// grant — explicit Namespaces minus the wildcard token + every
// vcluster-<name> for VClusters[]. Caller filters this further by
// the actual existence of each namespace via a Get() before writing.
func (g AppGrant) MaterializedNamespaces() []string {
	out := make([]string, 0, len(g.Namespaces)+len(g.VClusters))
	seen := map[string]struct{}{}
	add := func(ns string) {
		ns = strings.TrimSpace(ns)
		if ns == "" || ns == NamespaceWildcard {
			return
		}
		if _, ok := seen[ns]; ok {
			return
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	for _, ns := range g.Namespaces {
		add(ns)
	}
	for _, vc := range g.VClusters {
		add(vClusterNamespace(strings.TrimSpace(vc)))
	}
	return out
}

// ParseSpec extracts the typed shape from a UserAccess unstructured
// object. Errors are returned as a string + ok=false so the reconciler
// can post a Failed condition with the message verbatim. The function
// never panics on malformed input — schema validation lives at the
// CRD layer (apiserver), this function tolerates whatever the cluster
// admitted.
func ParseSpec(u *unstructured.Unstructured) (UserAccessSpec, string) {
	var out UserAccessSpec

	user, _, _ := unstructured.NestedMap(u.Object, "spec", "user")
	if sub, ok := user["keycloakSubject"].(string); ok && strings.TrimSpace(sub) != "" {
		out.Subjects = append(out.Subjects, Subject{
			Kind: SubjectKindUser,
			Name: "oidc:" + strings.TrimSpace(sub),
		})
	}
	if rawGroups, ok := user["keycloakGroups"].([]any); ok {
		for _, g := range rawGroups {
			if gs, ok := g.(string); ok && strings.TrimSpace(gs) != "" {
				out.Subjects = append(out.Subjects, Subject{
					Kind: SubjectKindGroup,
					// Group paths from Keycloak typically arrive with a
					// leading slash (`/acme/admins`). The api-server
					// --oidc-groups-prefix convention requires the prefix,
					// then the raw group path verbatim — we preserve any
					// leading slash.
					Name: "oidc:" + strings.TrimSpace(gs),
				})
			}
		}
	}

	if v, ok, _ := unstructured.NestedString(u.Object, "spec", "sovereignRef"); ok {
		out.SovereignRef = v
	}

	if rawApps, ok, _ := unstructured.NestedSlice(u.Object, "spec", "applications"); ok {
		for _, raw := range rawApps {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			ag := AppGrant{}
			if s, ok := m["app"].(string); ok {
				ag.App = strings.TrimSpace(s)
			}
			if s, ok := m["role"].(string); ok {
				ag.Role = strings.TrimSpace(s)
			}
			if rawNs, ok := m["namespaces"].([]any); ok {
				for _, n := range rawNs {
					if ns, ok := n.(string); ok && strings.TrimSpace(ns) != "" {
						ag.Namespaces = append(ag.Namespaces, ns)
					}
				}
			}
			if rawVcs, ok := m["vClusters"].([]any); ok {
				for _, v := range rawVcs {
					if vs, ok := v.(string); ok && strings.TrimSpace(vs) != "" {
						ag.VClusters = append(ag.VClusters, vs)
					}
				}
			}
			out.Apps = append(out.Apps, ag)
		}
	}

	// scopes: [{labelKey, labelValue}] — the EPIC-3 (#1098) extension.
	// Tolerate the field's absence on today's CRD shape.
	if rawScopes, ok, _ := unstructured.NestedSlice(u.Object, "spec", "scopes"); ok {
		for _, raw := range rawScopes {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			s := labels.Scope{}
			if k, ok := m["labelKey"].(string); ok {
				s.Key = strings.TrimSpace(k)
			}
			if v, ok := m["labelValue"].(string); ok {
				s.Value = strings.TrimSpace(v)
			}
			if s.Key == "" && s.Value == "" {
				continue
			}
			out.Scopes = append(out.Scopes, s)
		}
	}

	if t, ok, _ := unstructured.NestedString(u.Object, "spec", "tier"); ok {
		out.Tier = strings.TrimSpace(t)
	}

	// Validation — return error message for caller to surface as
	// Condition `Ready=False, Reason=Invalid`. No panics.
	if len(out.Subjects) == 0 {
		return out, "spec.user must set keycloakSubject and/or keycloakGroups"
	}
	if strings.TrimSpace(out.SovereignRef) == "" {
		return out, "spec.sovereignRef is required"
	}
	if len(out.Apps) == 0 {
		return out, "spec.applications must contain at least one entry"
	}
	for i, a := range out.Apps {
		if a.App == "" {
			return out, "spec.applications[" + itoa(i) + "].app is required"
		}
		if a.Role != RoleAdmin && a.Role != RoleEditor && a.Role != RoleViewer {
			return out, "spec.applications[" + itoa(i) + "].role must be one of admin, editor, viewer"
		}
		if len(a.Namespaces) == 0 && len(a.VClusters) == 0 {
			return out, "spec.applications[" + itoa(i) + "] must set namespaces[] or vClusters[]"
		}
	}
	return out, ""
}

// EffectiveScopes folds spec.scopes[] with the auto-injected scope rows
// for the catalog tier. EPIC-3 (#1098) consumes this for candidate-
// target filtering; today the controller writes the bindings unfiltered
// because the candidate-target labels are not yet wired in (no
// admission webhook reads them at materialization time). The function
// is exported and tested so the EPIC-3 work plugs in cleanly.
func (s UserAccessSpec) EffectiveScopes() []labels.Scope {
	out := make([]labels.Scope, 0, len(s.Scopes)+1)
	out = append(out, s.Scopes...)
	out = append(out, labels.EnforcedScopes(s.Tier)...)
	return out
}

// itoa is a tiny stdlib-free integer-to-string for error messages —
// avoids dragging strconv in for one call. Negative inputs return "0".
func itoa(n int) string {
	if n < 0 {
		return "0"
	}
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
