package controller

// platform_scope_gate.go — the classless platform credit (Refs #7067,
// gitops.Inputs.ClasslessPlatformCredit).
//
// plan-quota is ALWAYS scoped PriorityClass NotIn [catalyst-org-platform]. A
// platform pod admitted before the Org's Kyverno Policy existed carries no
// class, so that quota charges it as if it were the customer's. The #7078
// answer — hold plan-quota UNSCOPED until every platform pod is stamped —
// wedged the Organization it was written for (hw307 acmewalk307, live after
// #7078: unscoped plan-quota requests.memory 9808Mi used of 9286Mi, the
// keycloak-config-cli hook refused, and a platform pod restarted to obtain the
// class would itself be charged to that unscoped quota and refused — a
// deadlock).
//
// Instead, every reconcile lists the namespace's pods and adds the effective
// requests/limits of the RUNNING (non-terminal) platform pods that lack the
// class to plan-quota's hard cap. The quota then charges those pods exactly
// what it is given for them, so they never eat the plan; every pod created
// from now on — hooks, surge replicas, a restarted keycloak — is stamped at
// admission and charged to platform-quota; and as the classless pods are
// replaced the credit shrinks to zero and plan-quota converges to the plan.
//
// The read is uncached (the manager's APIReader) and namespaced: the
// controller may read pods only in Organization namespaces, through a Role the
// Org's own boundary tree renders (gitops platform-scope-rbac.yaml) in the
// SAME Flux apply as the quota. A namespaced Role delivered with the quota is
// used rather than a chart ClusterRole because RBAC cannot restrict list by
// label — a ClusterRole would grant pod read in every namespace on the
// Sovereign — and because the Role ships in the org-controller's own tree
// with no umbrella chart move. The first render after an upgrade therefore
// precedes the Role: an unlistable namespace sets CreditUnknown, and the
// renderer then never shrinks the hard cap (the larger of the live hard and
// plan + coredns + the whole modelled stack).

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openova-io/openova/core/controllers/organization/internal/gitops"
)

// classlessPlatformCredit returns the credit for the Organization namespace
// slug, or ok=false (with the reason) when the pods cannot be listed.
func (r *Reconciler) classlessPlatformCredit(ctx context.Context, slug string) (credit gitops.PlatformCredit, ok bool, why string) {
	reader := r.PodReader
	if reader == nil {
		reader = r.Client
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(slug)); err != nil {
		return credit, false, fmt.Sprintf("cannot list pods in %s: %v", slug, err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		// Terminal pods are charged to no quota. A terminating pod still is
		// until it is gone, so it keeps its credit (over-crediting for the
		// few seconds of its grace period is the safe direction).
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			continue
		}
		if !gitops.InPlatformScope(p.Labels) || p.Spec.PriorityClassName == gitops.OrgPlatformPriorityClassName {
			continue
		}
		req, lim := podQuotaUsage(p)
		credit.RequestsCPU.Add(req[corev1.ResourceCPU])
		credit.RequestsMemory.Add(req[corev1.ResourceMemory])
		credit.LimitsCPU.Add(lim[corev1.ResourceCPU])
		credit.LimitsMemory.Add(lim[corev1.ResourceMemory])
		credit.Pods++
	}
	return credit, true, ""
}

// livePlanQuotaHard reads the plan-quota hard cap currently on the cluster
// (the floor of a render whose credit is unknown). Empty when absent.
func (r *Reconciler) livePlanQuotaHard(ctx context.Context, slug string) map[string]string {
	rq := &corev1.ResourceQuota{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: slug, Name: gitops.BoundaryResourceQuotaName}, rq); err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range rq.Spec.Hard {
		out[string(k)] = v.String()
	}
	return out
}

// podQuotaUsage is what the ResourceQuota admission plugin charges for a pod,
// per resource (the kubelet/scheduler effective-request rule): the larger of
// (every app container + every native sidecar) and (each plain init container
// + the sidecars started before it), plus the pod overhead.
func podQuotaUsage(p *corev1.Pod) (requests, limits corev1.ResourceList) {
	side := func(pick func(corev1.ResourceRequirements) corev1.ResourceList) corev1.ResourceList {
		total := corev1.ResourceList{}
		add := func(dst corev1.ResourceList, src corev1.ResourceList) {
			for k, v := range src {
				q := dst[k]
				q.Add(v)
				dst[k] = q
			}
		}
		for _, c := range p.Spec.Containers {
			add(total, pick(c.Resources))
		}
		sidecars := corev1.ResourceList{}
		initMax := corev1.ResourceList{}
		for _, c := range p.Spec.InitContainers {
			if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
				add(total, pick(c.Resources))
				add(sidecars, pick(c.Resources))
				continue
			}
			step := corev1.ResourceList{}
			add(step, sidecars)
			add(step, pick(c.Resources))
			for k, v := range step {
				if cur, ok := initMax[k]; !ok || v.Cmp(cur) > 0 {
					initMax[k] = v
				}
			}
		}
		for k, v := range initMax {
			if cur := total[k]; v.Cmp(cur) > 0 {
				total[k] = v
			}
		}
		for k, v := range p.Spec.Overhead {
			q := total[k]
			q.Add(v)
			total[k] = q
		}
		return total
	}
	requests = side(func(r corev1.ResourceRequirements) corev1.ResourceList { return r.Requests })
	limits = side(func(r corev1.ResourceRequirements) corev1.ResourceList { return r.Limits })
	return requests, limits
}
