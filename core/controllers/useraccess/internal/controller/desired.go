// desired.go — render the desired RoleBinding / ClusterRoleBinding set
// from a parsed UserAccessSpec.
//
// Naming:
//
//	RoleBinding (per namespace):
//	    namespace = <materialized ns>
//	    name      = useraccess-<ua-name>-<app>-<role>
//
//	ClusterRoleBinding (cluster-wide grant):
//	    name      = useraccess-<ua-name>-<app>-<role>
//
// Names are deterministic on (UA, app, role) so successive reconciles
// converge on the same set without renaming churn. The 63-character
// label-name budget is respected because:
//
//	useraccess-                  = 11
//	<ua-name (validated ≤ 40)>   = 40   (CRD-side regex caps at this)
//	-<app (≤ 24)>                = 25
//	-<role (≤ 6)>                = 7
//	─────────────────────────────────────
//	total budget                 ≤ 83  → exceeds 63 in pathological cases.
//
// To stay within K8s' DNS-1123 sub-label limit (63 chars), the
// generator truncates the synthesized name with a fnv-32 suffix when
// it would exceed 63. This matches the Kubernetes ecosystem convention
// (e.g. Helm's release-name truncation).

package controller

import (
	"hash/fnv"
	"strconv"
	"strings"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// desiredRoleBindings renders one RoleBinding per (UA × app × role × ns)
// for non-cluster-wide grants. Cluster-wide grants are emitted by
// desiredClusterRoleBindings.
func desiredRoleBindings(ua *unstructured.Unstructured, spec UserAccessSpec) []rbacv1.RoleBinding {
	out := make([]rbacv1.RoleBinding, 0)
	owner := makeOwnerRef(ua)
	subjects := makeSubjects(spec.Subjects)
	for _, app := range spec.Apps {
		if app.IsClusterWide() {
			continue
		}
		for _, ns := range app.MaterializedNamespaces() {
			rb := rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:            bindingName(ua.GetName(), app.App, app.Role),
					Namespace:       ns,
					Labels:          makeBindingLabels(ua.GetName(), app.App, app.Role, spec.SovereignRef),
					OwnerReferences: []metav1.OwnerReference{owner},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: rbacv1.GroupName,
					Kind:     "ClusterRole",
					Name:     clusterRoleForRole(app.Role),
				},
				Subjects: subjects,
			}
			out = append(out, rb)
		}
	}
	return out
}

// desiredClusterRoleBindings renders one ClusterRoleBinding per
// (UA × app × role) for cluster-wide grants (any namespaces[] entry == "*").
func desiredClusterRoleBindings(ua *unstructured.Unstructured, spec UserAccessSpec) []rbacv1.ClusterRoleBinding {
	out := make([]rbacv1.ClusterRoleBinding, 0)
	owner := makeOwnerRef(ua)
	subjects := makeSubjects(spec.Subjects)
	for _, app := range spec.Apps {
		if !app.IsClusterWide() {
			continue
		}
		crb := rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:            bindingName(ua.GetName(), app.App, app.Role),
				Labels:          makeBindingLabels(ua.GetName(), app.App, app.Role, spec.SovereignRef),
				OwnerReferences: []metav1.OwnerReference{owner},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     clusterRoleForRole(app.Role),
			},
			Subjects: subjects,
		}
		out = append(out, crb)
	}
	return out
}

// makeOwnerRef builds the cascade-delete linkage from a binding back
// to the UserAccess CR. controller=true so a `kubectl delete useraccess
// <name>` GC's the bindings without an explicit reconciler call.
func makeOwnerRef(ua *unstructured.Unstructured) metav1.OwnerReference {
	t := true
	return metav1.OwnerReference{
		APIVersion:         UserAccessGroup + "/" + UserAccessVersion,
		Kind:               UserAccessKind,
		Name:               ua.GetName(),
		UID:                ua.GetUID(),
		Controller:         &t,
		BlockOwnerDeletion: &t,
	}
}

// makeSubjects translates parsed Subjects into rbacv1.Subject values
// using the rbac.authorization.k8s.io APIGroup convention (kind = User
// or Group; apiGroup = rbac.authorization.k8s.io for non-SA subjects).
func makeSubjects(in []Subject) []rbacv1.Subject {
	out := make([]rbacv1.Subject, 0, len(in))
	for _, s := range in {
		out = append(out, rbacv1.Subject{
			APIGroup: rbacv1.GroupName,
			Kind:     s.Kind,
			Name:     s.Name,
		})
	}
	return out
}

// makeBindingLabels emits the standard label-set every materialized
// binding carries. The List-by-label call in the reconciler keys off
// LabelUserAccessName.
func makeBindingLabels(uaName, app, role, sovereign string) map[string]string {
	out := map[string]string{
		LabelManagedBy:      LabelManagedByValue,
		LabelUserAccessName: uaName,
		LabelUserAccessApp:  app,
		LabelUserAccessRole: role,
	}
	if strings.TrimSpace(sovereign) != "" {
		out[LabelSovereignKey] = sovereign
	}
	return out
}

// bindingName composes the deterministic name for a binding. If the
// raw name exceeds 63 characters it is truncated with a fnv-32 suffix
// to keep the binding within K8s' DNS-1123 sub-label limit.
func bindingName(uaName, app, role string) string {
	raw := "useraccess-" + uaName + "-" + app + "-" + role
	if len(raw) <= 63 {
		return raw
	}
	return truncatedBindingName(raw)
}

// truncatedBindingName returns a deterministic <=63 char name derived
// from `raw` by appending a fnv-32 base-36 suffix. Shared with the
// tier-aware emission path in tier_bindings.go.
func truncatedBindingName(raw string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(raw))
	suffix := "-" + strconv.FormatUint(uint64(h.Sum32()), 36)
	keep := 63 - len(suffix)
	if keep < 0 {
		keep = 0
	}
	return raw[:keep] + suffix
}
