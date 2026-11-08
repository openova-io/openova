package controller

// platform_scope_gate.go — the #7067 rollout gate (gitops.Inputs.PlatformScopePending).
//
// The scoped plan-quota (PriorityClass NotIn catalyst-org-platform) charges
// every pod WITHOUT the class to the customer's plan. Every platform pod
// admitted before the Org's Kyverno Policy existed has no class, so
// rescoping plan-quota in the same render that introduces the Policy would
// charge the whole running platform stack to the plan (acmewalk307 on hw307:
// ~5.2 CPU against 354m) and refuse every new customer pod in every
// Organization. The gate holds plan-quota UNSCOPED, at the pre-#7067 size,
// until the namespace holds no platform pod without the class; the class, the
// Policy and platform-quota render regardless, so every pod created from then
// on is stamped, and the next reconcile after the last classless platform pod
// is replaced rescopes the quota.
//
// The read is uncached (the manager's APIReader) and namespaced: the
// controller is bound to read pods only in the Organization namespaces, by a
// Role the Org's own boundary tree delivers (gitops platform-scope-rbac.yaml).
// Any failure to read — the Role not applied yet on a fresh Org, an apiserver
// blip — keeps the gate pending: the safe side is today's quota shape, never a
// quota that could charge the platform to the customer.

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
)

// platformScopePending reports whether plan-quota must stay unscoped for the
// Organization namespace slug, and why (for the log).
func (r *Reconciler) platformScopePending(ctx context.Context, slug string) (bool, string) {
	reader := r.PodReader
	if reader == nil {
		reader = r.Client
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(slug)); err != nil {
		return true, fmt.Sprintf("cannot list pods in %s: %v", slug, err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue // a terminating or finished pod is charged to no quota
		}
		if gitops.InPlatformScope(p.Labels) && p.Spec.PriorityClassName != gitops.OrgPlatformPriorityClassName {
			return true, fmt.Sprintf("platform pod %s/%s has no PriorityClass %s yet", slug, p.Name, gitops.OrgPlatformPriorityClassName)
		}
	}
	return false, ""
}
