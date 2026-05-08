// tier_bindings.go — slice C5-followup: tier-aware RoleBinding /
// ClusterRoleBinding emission honoring `spec.tierRoleRef` +
// `spec.scopes[]`.
//
// Pre-EPIC-3 the controller materialized one binding per
// (UA × app × role × ns) using the `applications[]` shape. Post-EPIC-3
// /rbac/assign authors UserAccess CRs with only `tierRoleRef` +
// `scopes[]` (no `applications[]`) — those CRs would materialize zero
// bindings until this path landed.
//
// Logic:
//
//   1. If spec.tierRoleRef is empty → fall back to legacy
//      applications[] path (caller decides, this file is the tier
//      branch).
//   2. Translate spec.scopes[] to a namespace target set via the
//      Manara matcher (`internal/labels/scope.go`):
//        - openova.io/application=<name>     → match Application's
//                                              Helm-rendered namespaces
//        - openova.io/org=<slug> | openova.io/organization=<slug>
//                                            → all org-labeled namespaces
//        - openova.io/environment=<name>     → Environment's namespaces
//        - openova.io/env-type=<type>        → all namespaces with
//                                              that env-type
//        - wildcard {*: *}                   → cluster-scoped → emit
//                                              ClusterRoleBinding
//        - empty scopes                      → cluster-scoped (per CRD
//                                              §6.3 default semantic)
//        - multiple scopes                   → AND-within (intersection)
//   3. Emit RoleBinding (or ClusterRoleBinding) with
//      roleRef = spec.tierRoleRef, subjects from spec.user.
//   4. Drift detection + cleanup via the existing reconciler upsert /
//      orphan-deletion paths (which key off the
//      access.openova.io/useraccess label).
//
// AND-within is realized by intersecting per-scope namespace lists.
// Empty intersection → no bindings (logged via condition reason).
//
// The namespace lookup uses the controller-runtime cached client
// (`r.Client.List()`) so the cost is amortized — no apiserver round
// trip per scope key, the informer keeps a Namespace cache.

package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/internal/labels"
)

// tierEmissionResult records what the tier path produced and why.
// Surfaces on Status conditions BindingsReconciled + (the Coexistence
// note when both shapes coexist).
type tierEmissionResult struct {
	// Used reports whether the tier path was taken at all.
	// False = caller should fall back to the legacy applications[] path.
	Used bool

	// ClusterScoped reports whether the result is a single
	// ClusterRoleBinding (true, when scopes are wildcard or empty)
	// rather than per-namespace RoleBindings.
	ClusterScoped bool

	// MatchedNamespaces is the namespace target list the controller
	// resolved scopes to (empty when ClusterScoped).
	MatchedNamespaces []string

	// LegacyApplicationsIgnored reports whether the CR carried both
	// tierRoleRef AND legacy applications[] — the tier path won and
	// applications[] was ignored. The reconciler surfaces a Condition
	// note for this so the operator can clean up the CR.
	LegacyApplicationsIgnored bool

	// Reason / Message — surfaced on Condition BindingsReconciled.
	Reason  string
	Message string
}

// computeTierBindings is the tier-aware desired-set builder. Returns
// the desired RoleBindings, ClusterRoleBindings, and a result trace.
// When `spec.TierRoleRef` is empty, the function returns Used=false —
// caller falls back to the legacy desiredRoleBindings/desired-CRBs.
func (r *UserAccessReconciler) computeTierBindings(
	ctx context.Context,
	ua *unstructured.Unstructured,
	spec UserAccessSpec,
) (rbs []rbacv1.RoleBinding, crbs []rbacv1.ClusterRoleBinding, res tierEmissionResult, err error) {
	if strings.TrimSpace(spec.TierRoleRef) == "" {
		return nil, nil, tierEmissionResult{Used: false, Reason: reasonLegacyApplicationsPath}, nil
	}
	res.Used = true
	if len(spec.Apps) > 0 {
		res.LegacyApplicationsIgnored = true
	}

	owner := makeOwnerRef(ua)
	subjects := makeSubjects(spec.Subjects)

	// Determine target namespaces. Wildcard or empty scopes → cluster-
	// scoped. Otherwise compute the intersection of per-scope targets.
	if isClusterScopedScopes(spec.Scopes) {
		res.ClusterScoped = true
		crb := rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            tierBindingName(ua.GetName(), spec.Tier),
				Labels:          makeTierBindingLabels(ua.GetName(), spec.Tier, spec.SovereignRef),
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     spec.TierRoleRef,
			},
			Subjects: subjects,
		}
		crbs = append(crbs, crb)
		res.Reason = reasonBindingsReconciledOK
		res.Message = "wildcard/empty scope → ClusterRoleBinding"
		return
	}

	matched, lookupErr := r.resolveScopesToNamespaces(ctx, spec.Scopes)
	if lookupErr != nil {
		err = lookupErr
		return
	}
	res.MatchedNamespaces = matched
	if len(matched) == 0 {
		res.Reason = reasonBindingsReconciledOK
		res.Message = fmt.Sprintf("scopes resolved to 0 namespaces (no matching targets present on cluster)")
		return
	}

	for _, ns := range matched {
		rb := rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            tierBindingName(ua.GetName(), spec.Tier),
				Namespace:       ns,
				Labels:          makeTierBindingLabels(ua.GetName(), spec.Tier, spec.SovereignRef),
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     spec.TierRoleRef,
			},
			Subjects: subjects,
		}
		rbs = append(rbs, rb)
	}
	res.Reason = reasonBindingsReconciledOK
	res.Message = fmt.Sprintf("emitted %d RoleBinding(s) across resolved namespaces", len(rbs))
	return
}

// isClusterScopedScopes reports whether the scope set means
// "everywhere" — empty list (per CRD §6.3 default) or any wildcard
// `{*:*}` row. If ANY scope is restrictive, the result is namespace-
// scoped.
func isClusterScopedScopes(scopes []labels.Scope) bool {
	if len(scopes) == 0 {
		return true
	}
	// A `{*:*}` row alone is the canonical wildcard scope. If a CR has
	// `[{*:*}, {key=v}]`, the restrictive scope wins (AND-within), so
	// the result is namespaced. We only treat the slice as cluster-
	// scoped when ALL rows are wildcard (or, equivalently, the slice
	// is exactly one wildcard row).
	for _, s := range scopes {
		if !s.IsWildcard() {
			return false
		}
	}
	return true
}

// resolveScopesToNamespaces translates spec.scopes[] to a concrete
// namespace target list. Performs the AND-within intersection across
// scope rows.
//
// The function lists Namespaces with the cached client and filters
// in-memory (the cache holds them already; informer cost is paid once
// at startup). Scaling: ~1k namespaces is the design ceiling for a
// Sovereign and the linear scan is well below the controller's
// reconcile budget.
func (r *UserAccessReconciler) resolveScopesToNamespaces(
	ctx context.Context,
	scopes []labels.Scope,
) ([]string, error) {
	// Drop wildcard rows — they're vacuous in an intersection.
	restrictive := make([]labels.Scope, 0, len(scopes))
	for _, s := range scopes {
		if s.IsWildcard() {
			continue
		}
		restrictive = append(restrictive, s)
	}
	if len(restrictive) == 0 {
		// All-wildcard → caller treats this as cluster-scoped, but be
		// defensive: return the entire namespace list.
		return r.listAllNamespaces(ctx)
	}

	// Single label-selector List would only support AND, but
	// internal/labels.Match has wildcard semantics our LabelSelector
	// can't express. Materialize candidates in-memory.
	var nsList corev1.NamespaceList
	if err := r.Client.List(ctx, &nsList); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	out := make([]string, 0)
	for i := range nsList.Items {
		ns := &nsList.Items[i]
		if labels.AndWithin(restrictive, ns.GetLabels()) {
			out = append(out, ns.GetName())
		}
	}
	sort.Strings(out)
	return out, nil
}

// listAllNamespaces returns every namespace name on the cluster — used
// only by the defensive fallback in resolveScopesToNamespaces. The
// happy path for cluster-scoped grants emits a ClusterRoleBinding and
// never reaches here.
func (r *UserAccessReconciler) listAllNamespaces(ctx context.Context) ([]string, error) {
	var nsList corev1.NamespaceList
	if err := r.Client.List(ctx, &nsList); err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	out := make([]string, 0, len(nsList.Items))
	for i := range nsList.Items {
		out = append(out, nsList.Items[i].GetName())
	}
	sort.Strings(out)
	return out, nil
}

// tierBindingName composes the deterministic name for a tier-path
// binding. Format: `useraccess-<ua>-<tier>`. Truncated with a fnv-32
// suffix when raw exceeds 63 chars (same pattern desired.go uses).
func tierBindingName(uaName, tier string) string {
	tierSeg := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tier)), "tier-")
	if tierSeg == "" {
		tierSeg = "tier"
	}
	raw := "useraccess-" + uaName + "-" + tierSeg
	if len(raw) <= 63 {
		return raw
	}
	return truncatedBindingName(raw)
}

// makeTierBindingLabels emits the standard label-set every tier-path
// binding carries — same shape as the legacy bindings (so the
// existing list-by-label query in the reconciler picks them up) plus
// an explicit tier label so operators can `kubectl get rolebindings
// -l access.openova.io/tier=developer` for governance audits.
func makeTierBindingLabels(uaName, tier, sovereign string) map[string]string {
	out := map[string]string{
		LabelManagedBy:      LabelManagedByValue,
		LabelUserAccessName: uaName,
	}
	if tier := strings.TrimSpace(tier); tier != "" {
		out[LabelUserAccessTier] = strings.ToLower(tier)
	}
	if strings.TrimSpace(sovereign) != "" {
		out[LabelSovereignKey] = sovereign
	}
	return out
}
