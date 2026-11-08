package gitops

import "k8s.io/apimachinery/pkg/api/resource"

// ─── Two admission scopes in one namespace: platform vs customer ────────────
//
// The per-Organization host namespace runs two populations of pods: the ones
// OpenOva delivers with every Organization (the vCluster control plane and the
// platform stack in platformStack, plus their Helm hook Jobs and the surge
// replica of a rolling update) and the ones the customer bought (the BSS
// door's bp-wordpress-tenant / bp-stalwart-tenant and every Application the
// syncer mirrors out of the Org vCluster).
//
// Until this split they shared ONE ResourceQuota sized plan + control plane +
// stack, and the transients rode "the plan's unconsumed slack". Measured on
// hw307 acmewalk307 (plan M, 2026-11-08): the customer's purchase took that
// slack (Stalwart 500m + webmail + WordPress + its database + a synced
// bookstack pod), plan-quota sat at requests.cpu 5205m / 4694m, and the
// post-upgrade hook `bp-keycloak-keycloak-config-cli` (250m / 256Mi) was
// refused "exceeded quota: plan-quota" for 14h+ — bp-keycloak Failed, and
// bp-newapi, bp-openclaw, bp-stalwart-tenant and bp-wordpress-tenant all
// waited on it. Customer usage starved a platform hook and the whole
// Organization wedged permanently: a namespace quota cannot reserve room for a
// pod that does not exist yet, so slack shared with the customer is slack the
// customer takes first.
//
// The structural fix is to stop sharing. Admission is split by SCOPE with the
// ResourceQuota PriorityClass scope selector (GA since Kubernetes 1.17):
//
//   - PriorityClass catalyst-org-platform (cluster-scoped, rendered by every
//     Organization's boundary tree, identical bytes, never pruned).
//   - Kyverno Policy org-platform-scope (namespaced, in the Org namespace)
//     stamps that class onto every platform pod at CREATE — the long-running
//     stack, its hook Jobs, its CronJob runs, its surge replicas and its CNPG
//     pods alike. A mutating admission policy is used, not chart values,
//     because the hook that wedged hw307 is the bitnami subchart's
//     keycloak-config-cli Job, whose template has NO priorityClassName knob
//     (bitnami/keycloak 25.2.0 keycloak-config-cli-job.yaml), and Helm 3 runs
//     no post-renderer over hooks — so no values change and no Flux
//     postRenderers patch can reach that pod. Identification is by the labels
//     the charts already stamp (platformScope below), the same signal the
//     chargeback collector uses to keep these pods off the customer's meters
//     (products/chargeback/internal/adapter/openova/collector.go
//     isPlatformStack / isVClusterControlPlane).
//   - ResourceQuota platform-quota, scoped PriorityClass In
//     [catalyst-org-platform], sized vcluster-0 + platformStack + the
//     transient headroom (the largest hook pod + one surge replica of the
//     largest rolling Deployment). OpenOva's own pods can only ever be
//     refused by OpenOva's own sizing, never by what the customer runs.
//   - ResourceQuota plan-quota, scoped PriorityClass NotIn
//     [catalyst-org-platform] (which is every pod without the class, since a
//     classless pod's empty class name is "not in" the list), carrying the
//     customer's plan — plus the vCluster's coredns, which is the one piece of
//     control plane that cannot carry the class (see below).
//
// What a customer CANNOT do is put its own pod in the platform scope:
//   - every customer pod reaches the host namespace either through the
//     vCluster syncer (which stamps vcluster.loft.sh/managed-by on it — the
//     Policy excludes every such pod from the mutation and REFUSES one that
//     arrives already carrying the class) or through the BSS door's purchase
//     charts, whose labels are not in platformScope;
//   - openclaw's per-user workspace pods carry app.kubernetes.io/name
//     bp-openclaw but are the customer's usage, so they are excluded by their
//     app.kubernetes.io/component=per-user-pod label.
//
// coredns: the vCluster's coredns is created by the syncer from the virtual
// kube-system and so carries vcluster.loft.sh/managed-by like every synced
// pod. It stays in the customer scope rather than opening a hole in the
// managed-by exclusion, and its figure is added to plan-quota as overhead so
// it still never eats the plan.

// OrgPlatformPriorityClassName is the PriorityClass every per-Organization
// platform pod carries and the two ResourceQuotas scope on. Exported so the
// postcondition readback and any other reader name the object the renderer
// authored.
const OrgPlatformPriorityClassName = "catalyst-org-platform"

// orgPlatformPriorityValue is the class value: above every default-0 pod (the
// customer's) so the scheduler queues platform pods first, far below
// catalyst-control-plane (100000000) and the system-* band. Preemption is
// Never — a platform pod of one Organization must never evict another
// Organization's customer pods to schedule.
const orgPlatformPriorityValue = 1000000

// PlatformResourceQuotaName is the platform-scope ResourceQuota.
const PlatformResourceQuotaName = "platform-quota"

// PlatformScopePolicyName is the Kyverno Policy that stamps the class.
const PlatformScopePolicyName = "org-platform-scope"

// Boundary-tree file names of the three new objects.
const (
	priorityClassDoc       = "priorityclass.yaml"
	platformQuotaDoc       = "platformquota.yaml"
	platformScopePolicyDoc = "platform-scope-policy.yaml"
)

// platformScopeSelectors is how a pod is recognised as platform. Each entry is
// ONE match clause (any clause suffices); the pin test checks every platform
// pod template in the stack charts against them and every purchase pod
// template against NONE of them.
type platformScopeSelectors struct {
	// InstanceReleases — app.kubernetes.io/instance values: the HelmRelease
	// names the BSS door renders (bp-keycloak's bitnami keycloak, its
	// postgresql AND its keycloak-config-cli hook all carry instance
	// bp-keycloak; the legacy bare bp-newapi / bp-openclaw / bp-agenity HRs
	// still live on Organizations created before ADR-0015 D5).
	InstanceReleases []string
	// ChartNames — app.kubernetes.io/name values the OpenOva-authored stack
	// charts fix independently of the release name (an Application-rendered
	// HelmRelease is named <app>-<cluster>, so the instance label is not
	// stable across doors; these are).
	ChartNames []string
	// Blueprints — catalyst.openova.io/blueprint values (bp-newapi's
	// external-secrets webhook gate hook carries only this label).
	Blueprints []string
	// CNPGClusterSuffix — the bp-newapi database pods (and its initdb Job
	// pod) carry only cnpg.io/cluster=<fullname>-newapi-pg.
	CNPGClusterSuffix string
	// ControlPlaneApp — the loft-sh vcluster chart labels vcluster-0 app=vcluster.
	ControlPlaneApp string
	// ExcludeComponents — app.kubernetes.io/component values that are the
	// customer's usage even inside a platform chart.
	ExcludeComponents []string
}

var platformScope = platformScopeSelectors{
	InstanceReleases: []string{"bp-keycloak", "bp-newapi", "bp-openclaw", "bp-agenity"},
	ChartNames: []string{
		"bp-newapi", "newapi-admin-seed", "newapi-admin-promote", "newapi-db-dsn-sync", "newapi-channel-seed",
		"bp-openclaw",
		"bp-agenity", "bp-oidc-gate",
	},
	Blueprints:        []string{"bp-newapi", "bp-openclaw", "bp-agenity"},
	CNPGClusterSuffix: "-newapi-pg",
	ControlPlaneApp:   "vcluster",
	ExcludeComponents: []string{"per-user-pod"},
}

// platformTransient is one pod the platform stack creates only while a Helm
// operation (or a CronJob tick) runs: a hook Job's pod, a CronJob's pod.
type platformTransient struct {
	Name    string
	Release string
	Source  string
	// Containers / Inits follow podEffectiveShape, as for platformStackWorkload.
	Containers []containerShape
	Inits      []containerShape
}

// platformTransients are the hook and CronJob pods the BSS door's platform
// releases render today (helm template of each chart with the door's values).
// The pin test re-reads each figure from its Source.
var platformTransients = []platformTransient{
	{
		Name: "bp-keycloak-keycloak-config-cli", Release: "bp-keycloak",
		Source:     "organization_gitops.go orgTenantBPKeycloak spec.values.keycloak.keycloakConfigCli.resources (post-install/post-upgrade hook)",
		Containers: []containerShape{{RequestsCPU: "250m", RequestsMemory: "256Mi", LimitsCPU: "250m", LimitsMemory: "256Mi"}},
	},
	{
		Name: "bp-newapi-admin-seed", Release: "bp-newapi",
		Source:     "platform/newapi/chart/templates/admin-sso-seed-job.yaml admin-seed default resources (post-install/post-upgrade hook)",
		Containers: []containerShape{{RequestsCPU: "10m", RequestsMemory: "64Mi", LimitsCPU: "200m", LimitsMemory: "128Mi"}},
	},
	{
		Name: "bp-newapi-admin-promote", Release: "bp-newapi",
		Source:     "platform/newapi/chart/templates/admin-sso-seed-job.yaml admin-promote (CronJob, inline)",
		Containers: []containerShape{{RequestsCPU: "10m", RequestsMemory: "64Mi", LimitsCPU: "100m", LimitsMemory: "128Mi"}},
	},
	{
		Name: "bp-newapi-db-dsn-sync", Release: "bp-newapi",
		Source:     "platform/newapi/chart/templates/database-secret-sync-job.yaml sync (post-install/post-upgrade hook, inline)",
		Containers: []containerShape{{RequestsCPU: "10m", RequestsMemory: "32Mi", LimitsCPU: "100m", LimitsMemory: "64Mi"}},
	},
}

// PlatformTransientHeadroom is the room platform-quota keeps on top of the
// long-running stack for the pods that exist only during a Helm operation.
type PlatformTransientHeadroom = PlatformStackOverhead

// transientHeadroomOf is the largest transient pod PLUS one surge replica of
// the largest rolling Deployment, per resource and side. One of each is the
// worst concurrent case the stack produces: Helm runs a release's hooks
// sequentially, a release's Deployments surge by one replica each
// (maxSurge 25% of one replica rounds up to 1), and the BSS door's dependsOn
// chain (bp-newapi / bp-openclaw / bp-agenity wait on bp-keycloak) keeps a
// keycloak hook from overlapping the first rollout of its dependants.
// StatefulSets and the single-instance CNPG cluster replace a pod in place
// and do not surge.
func transientHeadroomOf(stack []platformStackWorkload, transients []platformTransient, def containerShape) PlatformTransientHeadroom {
	sized := func(cs []containerShape) []containerShape {
		out := make([]containerShape, len(cs))
		for i, c := range cs {
			if c == (containerShape{}) {
				c = def
			}
			out[i] = c
		}
		return out
	}
	var hook, surge containerShape
	maxInto := func(dst *containerShape, s containerShape) {
		pick := func(cur, v string) string {
			if cur == "" {
				q := mustQuantity(v)
				return q.String()
			}
			if a, b := mustQuantity(cur), mustQuantity(v); b.Cmp(a) > 0 {
				return b.String()
			}
			return cur
		}
		dst.RequestsCPU = pick(dst.RequestsCPU, s.RequestsCPU)
		dst.RequestsMemory = pick(dst.RequestsMemory, s.RequestsMemory)
		dst.LimitsCPU = pick(dst.LimitsCPU, s.LimitsCPU)
		dst.LimitsMemory = pick(dst.LimitsMemory, s.LimitsMemory)
	}
	for _, tr := range transients {
		maxInto(&hook, podEffectiveShape(sized(tr.Containers), sized(tr.Inits)))
	}
	for _, w := range stack {
		if w.Surge {
			maxInto(&surge, podEffectiveShape(sized(w.Containers), sized(w.Inits)))
		}
	}
	zero := containerShape{RequestsCPU: "0", RequestsMemory: "0", LimitsCPU: "0", LimitsMemory: "0"}
	if hook.RequestsCPU == "" {
		hook = zero
	}
	if surge.RequestsCPU == "" {
		surge = zero
	}
	return PlatformTransientHeadroom{
		RequestsCPU:    sumQuantities(hook.RequestsCPU, surge.RequestsCPU),
		RequestsMemory: sumQuantities(hook.RequestsMemory, surge.RequestsMemory),
		LimitsCPU:      sumQuantities(hook.LimitsCPU, surge.LimitsCPU),
		LimitsMemory:   sumQuantities(hook.LimitsMemory, surge.LimitsMemory),
	}
}

// transientHeadroomFor is the headroom for a plan (a chart-unsized container
// takes that plan's LimitRange defaults, exactly as in platformStackOverheadFor).
func transientHeadroomFor(q PlanQuota) PlatformTransientHeadroom {
	return transientHeadroomOf(platformStack, platformTransients, limitRangeDefaults(q))
}

// vclusterPodOverhead / corednsOverhead split vclusterControlPlaneOverhead by
// scope: vcluster-0 is a host StatefulSet pod (platform scope); coredns is
// created by the syncer (customer scope, see the block comment above). The two
// always sum to vclusterControlPlaneOverhead (pinned by test).
var (
	vclusterPodOverhead = controlPlaneOverheadOf(vclusterControlPlaneShape{
		Syncer: vclusterControlPlane.Syncer, Distro: vclusterControlPlane.Distro,
		CoreDNS:    containerShape{RequestsCPU: "0", RequestsMemory: "0", LimitsCPU: "0", LimitsMemory: "0"},
		VolumeSize: vclusterControlPlane.VolumeSize,
	})
	corednsOverhead = func() ControlPlaneOverhead {
		dns := podEffectiveShape([]containerShape{vclusterControlPlane.CoreDNS}, nil)
		return ControlPlaneOverhead{
			RequestsCPU: mustQuantity(dns.RequestsCPU), RequestsMemory: mustQuantity(dns.RequestsMemory),
			LimitsCPU: mustQuantity(dns.LimitsCPU), LimitsMemory: mustQuantity(dns.LimitsMemory),
			Storage: mustQuantity("0"),
		}
	}()
)

// platformQuotaHard sizes platform-quota: vcluster-0 + the platform stack +
// the transient headroom, requests and limits each with their own figures.
// It does not depend on the overage mode (grow moves only the customer's
// limits).
func platformQuotaHard(q PlanQuota) quotaHard {
	ps := platformStackOverheadFor(q)
	th := transientHeadroomFor(q)
	add := func(qs ...resource.Quantity) string {
		var sum resource.Quantity
		for _, x := range qs {
			sum.Add(x)
		}
		return sum.String()
	}
	return quotaHard{
		RequestsCPU:    add(vclusterPodOverhead.RequestsCPU, ps.RequestsCPU, th.RequestsCPU),
		RequestsMemory: add(vclusterPodOverhead.RequestsMemory, ps.RequestsMemory, th.RequestsMemory),
		LimitsCPU:      add(vclusterPodOverhead.LimitsCPU, ps.LimitsCPU, th.LimitsCPU),
		LimitsMemory:   add(vclusterPodOverhead.LimitsMemory, ps.LimitsMemory, th.LimitsMemory),
	}
}

// planQuotaHard sizes the customer-scope plan-quota: the plan term (the
// guaranteed share on requests, the headline — or the grow ceiling — on
// limits) plus the synced coredns.
func planQuotaHard(limits PlanQuota) quotaHard {
	add := func(plan string, o resource.Quantity) string {
		s := mustQuantity(plan)
		s.Add(o)
		return s.String()
	}
	return quotaHard{
		RequestsCPU:    add(limits.CPURequest, corednsOverhead.RequestsCPU),
		RequestsMemory: add(limits.MemRequest, corednsOverhead.RequestsMemory),
		LimitsCPU:      add(limits.CPULimit, corednsOverhead.LimitsCPU),
		LimitsMemory:   add(limits.MemLimit, corednsOverhead.LimitsMemory),
	}
}

// overheadText renders a four-figure overhead for an annotation.
func overheadText(o PlatformStackOverhead) string { return o.String() }

// podOverheadText renders a control-plane part without its storage term.
func podOverheadText(o ControlPlaneOverhead) string {
	return PlatformStackOverhead{
		RequestsCPU: o.RequestsCPU, RequestsMemory: o.RequestsMemory,
		LimitsCPU: o.LimitsCPU, LimitsMemory: o.LimitsMemory,
	}.String()
}

const priorityClassTemplate = `# The admission scope every per-Organization platform pod carries (Kyverno
# Policy org-platform-scope stamps it). Cluster-scoped and rendered with the
# SAME bytes by every Organization's boundary tree; never pruned, so tearing
# one Organization down cannot remove it from under the others.
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: {{ .PlatformPriorityClass }}
  labels:
    openova.io/managed-by: catalyst
    catalyst.openova.io/component: org-platform-scope
  annotations:
    kustomize.toolkit.fluxcd.io/prune: disabled
value: {{ .PlatformPriorityValue }}
globalDefault: false
preemptionPolicy: Never
description: >-
  Per-Organization platform pods (vcluster-0, bp-keycloak, bp-newapi,
  bp-openclaw, bp-agenity, oidc-gate and their Helm hook Jobs). Scopes the
  Organization namespace's platform-quota; plan-quota carries every other pod.
  Never preempts.
`

const platformQuotaTemplate = `# OpenOva's own pods in this Organization namespace, and nothing else: the
# per-Org vCluster control plane (vcluster-0), the per-Organization platform
# stack, and room for the pods that exist only during a Helm operation (the
# largest hook pod + one surge replica of the largest rolling Deployment).
# Scoped to PriorityClass {{ .PlatformPriorityClass }}, which Kyverno Policy
# {{ .PlatformScopePolicy }} stamps on those pods at admission. The customer's
# pods are admitted against {{ .ResourceQuotaName }} and can never consume this.
#   vcluster-0: {{ .VClusterPodText }}
#   per-Organization platform stack: {{ .PlatformStackText }}
#   transient headroom: {{ .TransientText }}
apiVersion: v1
kind: ResourceQuota
metadata:
  name: {{ .PlatformQuotaName }}
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/plan: {{ .PlanSlug }}
    openova.io/managed-by: catalyst
    openova.io/quota-scope: platform
  annotations:
    openova.io/quota-formula: "vcluster-0 + per-Organization platform stack + transient headroom (largest hook pod + one surge replica)"
    openova.io/vcluster-control-plane-overhead: {{ .VClusterPodText | quote }}
    openova.io/platform-stack-overhead: {{ .PlatformStackText | quote }}
    openova.io/platform-transient-headroom: {{ .TransientText | quote }}
    kustomize.toolkit.fluxcd.io/force: enabled
spec:
  scopeSelector:
    matchExpressions:
      - scopeName: PriorityClass
        operator: In
        values:
          - {{ .PlatformPriorityClass }}
  hard:
    requests.cpu: "{{ .PlatformHard.RequestsCPU }}"
    requests.memory: "{{ .PlatformHard.RequestsMemory }}"
    limits.cpu: "{{ .PlatformHard.LimitsCPU }}"
    limits.memory: "{{ .PlatformHard.LimitsMemory }}"
`

// platformScopePolicyTemplate — see the block comment at the top of this
// file. autogen is off so the policy touches Pods only (never a controller's
// pod template, which for a Job is immutable). priority and preemptionPolicy
// are patched together with the class name so the Priority admission plugin
// computes the same figures if it is re-invoked after the webhook.
const platformScopePolicyTemplate = `apiVersion: kyverno.io/v1
kind: Policy
metadata:
  name: {{ .PlatformScopePolicy }}
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
  annotations:
    pod-policies.kyverno.io/autogen-controllers: none
    policies.kyverno.io/title: Per-Organization platform admission scope
    policies.kyverno.io/description: >-
      Stamps PriorityClass {{ .PlatformPriorityClass }} on the Organization's
      platform pods so they are admitted against {{ .PlatformQuotaName }}, never
      against the customer's {{ .ResourceQuotaName }}; refuses a pod synced
      out of the vCluster that claims the class.
spec:
  background: false
  rules:
    - name: stamp-platform-scope
      match:
        any:
          - resources:
              kinds: [Pod]
              operations: [CREATE]
              selector:
                matchExpressions:
                  - key: app.kubernetes.io/instance
                    operator: In
                    values: [{{ join .Scope.InstanceReleases }}]
          - resources:
              kinds: [Pod]
              operations: [CREATE]
              selector:
                matchExpressions:
                  - key: app.kubernetes.io/name
                    operator: In
                    values: [{{ join .Scope.ChartNames }}]
          - resources:
              kinds: [Pod]
              operations: [CREATE]
              selector:
                matchExpressions:
                  - key: catalyst.openova.io/blueprint
                    operator: In
                    values: [{{ join .Scope.Blueprints }}]
          - resources:
              kinds: [Pod]
              operations: [CREATE]
              selector:
                matchLabels:
                  cnpg.io/cluster: "*{{ .Scope.CNPGClusterSuffix }}"
          - resources:
              kinds: [Pod]
              operations: [CREATE]
              selector:
                matchLabels:
                  app: {{ .Scope.ControlPlaneApp }}
      exclude:
        any:
          - resources:
              selector:
                matchExpressions:
                  - key: vcluster.loft.sh/managed-by
                    operator: Exists
          - resources:
              selector:
                matchExpressions:
                  - key: app.kubernetes.io/component
                    operator: In
                    values: [{{ join .Scope.ExcludeComponents }}]
      mutate:
        patchStrategicMerge:
          spec:
            priorityClassName: {{ .PlatformPriorityClass }}
            priority: {{ .PlatformPriorityValue }}
            preemptionPolicy: Never
    - name: synced-pods-stay-customer-scope
      match:
        any:
          - resources:
              kinds: [Pod]
              operations: [CREATE]
              selector:
                matchExpressions:
                  - key: vcluster.loft.sh/managed-by
                    operator: Exists
      validate:
        failureAction: Enforce
        message: >-
          A pod synced out of the Organization vCluster is the customer's and is
          admitted against {{ .ResourceQuotaName }}; it may not carry PriorityClass
          {{ .PlatformPriorityClass }}.
        deny:
          conditions:
            any:
              - key: "{{"{{"}} request.object.spec.priorityClassName || '' {{"}}"}}"
                operator: Equals
                value: {{ .PlatformPriorityClass }}
`
