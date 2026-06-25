// Package gitops renders the per-Organization manifests that Flux on
// the host cluster reconciles into a vCluster + supporting resources.
//
// Per ADR-0001 §2.1 (GitOps is the only deployment path) and EPICS-1-6
// §3.7 the controller writes a HelmRelease + Namespace + ingress
// manifest into the per-Org Gitea repo. Flux on the host cluster's
// `clusters/<host>/tenants/<org>/` Kustomization picks them up.
//
// Per docs/NAMING-CONVENTION.md §1.5 Org identity lives in the
// vCluster name (= the Organization slug); the namespace on the host
// cluster is also the slug per §4.6. Resource names below the namespace
// don't repeat the slug.
//
// Per EPICS-1-6 §1.1 every Catalyst-managed resource carries the
// canonical label set; rendered manifests include them.

package gitops

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// Inputs is the subset of Organization spec the renderer needs. The
// reconciler builds this from the CR + controller-level config (chart
// version, vcluster repo URL).
type Inputs struct {
	// Slug is the Organization slug (= vCluster name = Gitea Org name).
	Slug string

	// DisplayName is the human-readable Organization name (label-safe
	// or annotation only — display strings don't go in resource names).
	DisplayName string

	// Tier is org | corporate.
	Tier string

	// SovereignFQDN is the Sovereign domain (e.g. omantel.omani.works).
	SovereignFQDN string

	// HostCluster is the canonical name of the host cluster the
	// vCluster lives on (e.g. hz-fsn-rtz-prod). Drives the
	// openova.io/host-cluster label per NAMING §6.2.
	HostCluster string

	// VClusterChartVersion is the SemVer constraint for the upstream
	// vcluster chart. Per Inviolable Principle #4 this is configurable
	// (env var) — never hardcoded in the renderer.
	VClusterChartVersion string

	// VClusterHelmRepoName is the in-cluster Flux HelmRepository to
	// source the vcluster chart from. Default: "loft" in
	// "vcluster-system" — matches clusters/contabo-mkt/tenants/test/.
	VClusterHelmRepoName      string
	VClusterHelmRepoNamespace string

	// VClusterImageRegistry is the Sovereign-local Harbor host every
	// vCluster image (syncer StatefulSet + the k8s-distro init image)
	// pulls through. Default "harbor.openova.io".
	//
	// MIRROR-EVERYTHING (#3760, Refs #3376 #3754): the per-Org vCluster
	// StatefulSet is admission-gated by the `harbor-proxy-pull` Kyverno
	// ClusterPolicy (Enforce), which DENIES any image not matching the
	// `*/proxy-*/*` glob. vcluster 0.33.x renders TWO denied initContainer
	// images off ghcr.io — the k8s distro `loft-sh/kubernetes` AND the
	// `loft-sh/vcluster-oss` syncer — so BOTH must be re-tagged through
	// the Sovereign Harbor proxy-cache (`<registry>/proxy-ghcr/loft-sh/...`),
	// exactly like the platform's own bp-dmz/mgmt/rtz-vcluster charts.
	// Per Inviolable Principle #4 it's read from env, never hardcoded;
	// cutover Step-04 (ADR-0002) flips it to `harbor.<sovereign-fqdn>`
	// post-handover.
	VClusterImageRegistry string

	// PlanSlug is the catalog plan slug (s|m|l|xl|flexi) the customer
	// purchased — the SINGLE truth-source for the resource cap that
	// materializes on the Org boundary namespace (Workstream B, #4292 /
	// EPIC #4293). Resolved from the plan UUID via catalog `resolvePlanSlug`
	// at the two CR-minting emitters (funnel + BSS) and carried on the
	// Organization CR `spec.planSlug`. Drives planQuota(): the ResourceQuota
	// + LimitRange the org-controller co-renders on the `<slug>` host
	// namespace. Empty defaults to "s" (the smallest paid tier) so a legacy
	// Org CR without the field still gets a quota rather than running
	// uncapped. 5-pillar Pillar 1: the cap the customer pays for IS the cap
	// that materializes.
	PlanSlug string
}

// PlanQuota is the per-plan resource cap the org-controller materializes on
// the Org boundary host namespace (#4292). It REPLACES both the retired
// marketplace-api `SizeResources` (raw req.Size string, dev-tiny, no
// LimitRange) and the provisioning `planLimits` (syncer-pod-only). The cap is
// keyed by the catalog plan SLUG (seed.go:187-198), so the resource the
// customer pays for is exactly the resource that materializes.
//
//	CPU  — the ResourceQuota requests.cpu == limits.cpu ceiling.
//	Mem  — the ResourceQuota requests.memory == limits.memory ceiling.
//	Burstable — Flexi alone; when true the LimitRange omits the
//	            maxLimitRequestRatio so pods may run requests<limits
//	            (Burstable QoS). Fixed tiers (S/M/L/XL) keep it false →
//	            the LimitRange forces requests==limits → Guaranteed QoS.
type PlanQuota struct {
	Slug      string
	CPU       string // e.g. "2", "4", "8", "16"
	Mem       string // e.g. "4Gi", "8Gi"
	Burstable bool
}

// planQuotaTable is the catalog-slug → host-ns cap map (issue #4292 target
// table). It is the ONE source the org-controller drives the ResourceQuota +
// LimitRange off; the marketplace-api SizeResources + provisioning planLimits
// were retired in Workstream A precisely so this table is the only one.
//
// S/M/L/XL are fixed Guaranteed tiers; Flexi is on-demand Burstable (no hard
// quota ceiling — soft, scale-on-demand). The numbers mirror the seeded plan
// rows (seed.go: S=2vCPU/4GB, M=4/8, L=8/16, XL=16/32).
var planQuotaTable = map[string]PlanQuota{
	"s":     {Slug: "s", CPU: "2", Mem: "4Gi", Burstable: false},
	"m":     {Slug: "m", CPU: "4", Mem: "8Gi", Burstable: false},
	"l":     {Slug: "l", CPU: "8", Mem: "16Gi", Burstable: false},
	"xl":    {Slug: "xl", CPU: "16", Mem: "32Gi", Burstable: false},
	"flexi": {Slug: "flexi", CPU: "", Mem: "", Burstable: true},
}

// planQuota resolves the cap for a plan slug, defaulting to "s" for an unknown
// or empty slug (a legacy Org CR without spec.planSlug still gets the smallest
// paid cap rather than running uncapped). The slug is lowercased so "S"/"s"
// resolve identically.
func planQuota(planSlug string) PlanQuota {
	s := strings.ToLower(strings.TrimSpace(planSlug))
	if q, ok := planQuotaTable[s]; ok {
		return q
	}
	return planQuotaTable["s"]
}

// boundaryIsVcluster is the TIER GATE (#4292). It decides whether an Org of a
// given plan slug gets a dedicated vCluster (control-plane-grade isolation) or
// shares the host `<slug>` namespace (namespace-grade isolation). The
// quota/LimitRange/default-deny renderer is IDENTICAL either way — only the
// boundary PRIMITIVE differs by this one-line policy.
//
// Founder default (issue #4292 "TIER GATE"): free/S share the host namespace;
// paid M+ get the dedicated Org-vcluster. Flip allTiersVcluster to true to put
// EVERY tier (incl. free/S) on a vCluster (tierOption A in the spec) — a
// single Sovereign-level switch, no renderer change.
const allTiersVcluster = false

func boundaryIsVcluster(planSlug string) bool {
	if allTiersVcluster {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(planSlug)) {
	case "", "s", "free":
		// free/S → host-ns boundary (same quota+LimitRange+default-deny CNP;
		// the shape already live on demo org-7283eb4a).
		return false
	default:
		// m/l/xl/flexi → dedicated Org-vcluster.
		return true
	}
}

// BoundaryIsVcluster is the EXPORTED tier-gate predicate so the controller
// package (per_org_flux.go, #4293 MAJOR-2) can decide whether the per-Org apps
// Flux Kustomization reconciles the apps/ tree INTO the vcluster (kubeConfig)
// or straight into the host `<slug>` ns. It MUST stay in lockstep with the
// unexported boundaryIsVcluster (and the funnel's gitops.BoundaryIsVcluster) so
// the NetworkPolicy reconciler targets the same boundary the apps land on.
func BoundaryIsVcluster(planSlug string) bool { return boundaryIsVcluster(planSlug) }

// renderTemplates is the named template set the controller uses.
// Keep these inline (text/template) — the rendered output is YAML
// that Flux applies via Kustomization. Per Inviolable Principle #4
// no values are hardcoded inside; every knob comes from Inputs.
const namespaceTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/host-cluster: {{ .HostCluster }}
    openova.io/sovereign: {{ .SovereignFQDN }}
    openova.io/managed-by: catalyst
    openova.io/tier: {{ .Tier }}
  annotations:
    openova.io/display-name: {{ .DisplayName | quote }}
`

const vclusterTemplate = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: vcluster
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/vcluster: {{ .Slug }}
    openova.io/host-cluster: {{ .HostCluster }}
    openova.io/sovereign: {{ .SovereignFQDN }}
    openova.io/managed-by: flux
    app.kubernetes.io/managed-by: flux
spec:
  interval: 10m
  chart:
    spec:
      chart: vcluster
      version: {{ .VClusterChartVersion | quote }}
      sourceRef:
        kind: HelmRepository
        name: {{ .VClusterHelmRepoName }}
        namespace: {{ .VClusterHelmRepoNamespace }}
  values:
    controlPlane:
      distro:
        k8s:
          enabled: true
          # MIRROR-EVERYTHING (#3760): the k8s-distro image is
          # initContainers[0] of the vcluster StatefulSet (vcluster 0.33.x
          # renders ghcr.io/loft-sh/kubernetes:vX). The harbor-proxy-pull
          # Kyverno ClusterPolicy (Enforce) DENIES it off ghcr.io — re-tag
          # through the Sovereign Harbor proxy-cache so it matches the
          # */proxy-*/* glob, lockstep with bp-dmz/mgmt/rtz-vcluster.
          image:
            registry: {{ .VClusterImageRegistry }}
            repository: proxy-ghcr/loft-sh/kubernetes
      coredns:
        # #3859 (first proven walkorg/hw167): vcluster 0.33.x's baked-in default
        # coredns is coredns/coredns:1.14.1 — a tag that does NOT exist on
        # docker.io (real tags are 1.11.x/1.12.x). The per-Org vCluster's coredns
        # then ImagePullBackOffs ("unexpected media type text/html … not found"),
        # the vCluster never gets cluster DNS, and the customer's purchased app
        # can never resolve/serve → the #3376 funnel terminal is unreachable.
        # The platform vClusters (mgmt/rtz/dmz) only escape because they pin the
        # vcluster subchart at 0.23.0 (baked-in coredns 1.11.3, valid). Pin the
        # same valid 1.11.3 through the Sovereign Harbor proxy-cache (docker.io →
        # proxy-dockerhub, the same project the provisioning alpine/k8s init uses)
        # so it ALSO satisfies the harbor-proxy-pull Kyverno Enforce (*/proxy-*/*).
        deployment:
          image: {{ .VClusterImageRegistry }}/proxy-dockerhub/coredns/coredns:1.11.3
      backingStore:
        database:
          embedded:
            enabled: true
      statefulSet:
        # MIRROR-EVERYTHING (#3760): the syncer image is initContainers[1]
        # (+ the main container). Pull it through the Sovereign Harbor
        # proxy-cache too — ghcr.io is denied by harbor-proxy-pull.
        image:
          registry: {{ .VClusterImageRegistry }}
          repository: proxy-ghcr/loft-sh/vcluster-oss
        resources:
          requests:
            cpu: 100m
            memory: 192Mi
          limits:
            cpu: 2000m
            memory: 2Gi
        persistence:
          volumeClaim:
            size: 5Gi
      service:
        enabled: true
        spec:
          type: ClusterIP
    exportKubeConfig:
      context: vcluster
      server: https://vcluster.{{ .Slug }}:443
      insecure: false
      additionalSecrets:
        - name: vc-vcluster
          server: https://vcluster.{{ .Slug }}:443
          insecure: false
          context: vcluster
    sync:
      toHost:
        services:
          enabled: true
        ingresses:
          enabled: false
        # ENABLE networkPolicy SYNC (#4292, MANDATORY). loft-sh vcluster
        # defaults this to false, so a NetworkPolicy authored INSIDE the
        # Org-vcluster lives only in the vcluster apiserver and is NEVER
        # reflected to Cilium on the host where it is actually enforced —
        # intra-Org isolation between Environments would silently not exist
        # (the wide-open-vcluster bug). With this on, the syncer mirrors the
        # default-deny + same-Org-allow NetworkPolicy this controller co-renders
        # in the apps tree to the host <slug> ns. (Distinct from the
        # bp-mgmt/rtz-vcluster networkPolicy.enabled knob, which is a HOST
        # whole-ns NetworkPolicy -- a different mechanism.)
        networkPolicies:
          enabled: true
      fromHost:
        ingressClasses:
          enabled: true
`

// resourceQuotaTemplate caps the Org boundary host namespace at the plan the
// customer purchased (#4292). Driven by planQuota(.PlanSlug). For the fixed
// tiers (S/M/L/XL) requests.cpu==limits.cpu and requests.memory==limits.memory
// — paired with the LimitRange's maxLimitRequestRatio {cpu:1,memory:1} this
// forces Guaranteed QoS. Flexi renders NO ResourceQuota (on-demand, soft cap)
// — the controller skips this file for Burstable plans.
//
// 5-pillar Pillar 1: the cap the customer pays for IS the cap that
// materializes. This replaces the dev-tiny marketplace-api SizeResources +
// the syncer-only provisioning planLimits, both retired in Workstream A.
const resourceQuotaTemplate = `apiVersion: v1
kind: ResourceQuota
metadata:
  name: plan-quota
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/plan: {{ .PlanSlug }}
    openova.io/managed-by: catalyst
spec:
  hard:
    requests.cpu: "{{ .Quota.CPU }}"
    requests.memory: "{{ .Quota.Mem }}"
    limits.cpu: "{{ .Quota.CPU }}"
    limits.memory: "{{ .Quota.Mem }}"
`

// limitRangeTemplate seeds defaultRequest/default so pods authored without
// explicit requests/limits still ADMIT once a ResourceQuota exists (a quota
// rejects any pod missing the limited resources). For the fixed tiers it also
// pins maxLimitRequestRatio {cpu:1,memory:1} + defaultRequest==default →
// requests==limits → Guaranteed QoS (#4292). Flexi omits the ratio (asymmetric
// requests<limits allowed → Burstable). The default container request is the
// plan ceiling / 8 so a handful of small Pods fit under the quota out of the
// box; an Application that needs more sets its own explicit requests.
const limitRangeTemplate = `apiVersion: v1
kind: LimitRange
metadata:
  name: plan-limits
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/plan: {{ .PlanSlug }}
    openova.io/managed-by: catalyst
spec:
  limits:
    - type: Container
      defaultRequest:
        cpu: "{{ .DefaultCPU }}"
        memory: "{{ .DefaultMem }}"
      default:
        cpu: "{{ .DefaultCPU }}"
        memory: "{{ .DefaultMem }}"
{{- if not .Quota.Burstable }}
      maxLimitRequestRatio:
        cpu: "1"
        memory: "1"
{{- end }}
`

// networkPolicyTemplate is the default-deny + same-Org-allow baseline rendered
// INSIDE the Org-vcluster apps tree (#4292). With sync.toHost.networkPolicies
// enabled (above) the syncer reflects it to the host `<slug>` ns where Cilium
// enforces it. Without the sync flag it is inert; without this policy the
// vcluster is wide open. Together they make intra-Org isolation REAL:
//
//   - default-deny: an empty podSelector selects all pods; absent Ingress
//     rules deny all ingress. The companion allow re-opens same-namespace.
//   - same-Org-allow: pods may talk to pods in the same namespace (the Org's
//     own Environments/Applications) + DNS egress. Cross-Org traffic — which
//     would land in a DIFFERENT host ns after sync — is denied.
const networkPolicyTemplate = `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-all
  namespace: {{ .AppNamespace }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
spec:
  podSelector: {}
  policyTypes:
    - Ingress
    - Egress
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-same-org
  namespace: {{ .AppNamespace }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
spec:
  podSelector: {}
  policyTypes:
    - Ingress
    - Egress
  ingress:
    - from:
        - podSelector: {}
  egress:
    # Same-Org pod-to-pod.
    - to:
        - podSelector: {}
    # DNS resolution (cluster CoreDNS).
    - ports:
        - protocol: UDP
          port: 53
        - protocol: TCP
          port: 53
`

// ciliumNetworkPolicyTemplate is the MANDATORY companion to the default-deny
// K8s NetworkPolicy above (#4292). A vanilla `networking.k8s.io/v1`
// NetworkPolicy expresses its allow-set PURELY as podSelector / namespaceSelector
// / ipBlock rules — none of which can match the Cilium RESERVED ENTITIES. On a
// Sovereign (every cluster runs the Cilium Gateway API) two reserved entities
// MUST be admitted or the default-deny silently breaks the Org's real workloads:
//
//   - `ingress` (+ `host`, `remote-node`): the Cilium Gateway / Envoy proxies
//     external traffic to the backend pod under the reserved `ingress` security
//     identity (Envoy runs in the host netns; the backend may sit on a peer
//     node). NO K8s NetworkPolicy selector matches it, so the customer's
//     purchased Application behind its HTTPRoute returns "upstream connect error
//     … connection timeout" / 503 (the #2940/#3374 app-plane wedge, fixed live
//     on hw165). fromEntities is the canonical expression.
//   - `kube-apiserver`: an in-vcluster Org app pod (and any in-cluster client)
//     reaches the cluster API as the special `kube-apiserver` entity, which no
//     podSelector/namespaceSelector/ipBlock (not even 0.0.0.0/0) covers (the
//     sso-bridge 0.2.14 saga). toEntities is the canonical expression.
//
// Cilium computes the UNION of every policy selecting an endpoint, so this CNP
// is PURELY ADDITIVE to the K8s default-deny: it grants gateway reachability +
// apiserver egress without weakening the cross-Org/cross-Environment denial
// (note: NO `world` — only the gateway, never direct public ingress). It is
// co-located with the K8s NP in the SAME apps tree, so it lands wherever the
// real Org workloads do — INSIDE the vcluster for the paid tier (the keystone
// #4299 redirects app HelmReleases there via spec.kubeConfig), on the host
// `<slug>` ns for free/S — binding the actual pods, never an empty host shell.
//
// endpointSelector {} = every endpoint in the namespace (matching the K8s
// default-deny's podSelector {} scope). Gated by the consumer on the cilium.io/v2
// Capabilities check (the #2988/#3102 idiom) so kind CI (CRD-less) still applies
// the K8s NP without this file — there the identity-aware agent isn't enforcing
// anyway. On a real Sovereign the org-controller always emits it.
const ciliumNetworkPolicyTemplate = `apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: allow-gateway-and-apiserver
  namespace: {{ .AppNamespace }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
    catalyst.openova.io/component: org-vcluster-gateway-netpol
spec:
  endpointSelector: {}
  ingress:
    # Admit the Cilium Gateway / Envoy datapath (reserved entities no K8s
    # NetworkPolicy selector can match) so the Org's Application behind its
    # HTTPRoute is reachable — without this the gateway→pod hop is dropped → 503.
    - fromEntities:
        - ingress
        - host
        - remote-node
  egress:
    # Admit egress to the cluster API (the reserved kube-apiserver entity) so an
    # in-vcluster Org app pod / in-cluster client can reach :443/:6443.
    - toEntities:
        - kube-apiserver
      toPorts:
        - ports:
            - port: "443"
              protocol: TCP
            - port: "6443"
              protocol: TCP
`

// networkPolicyDoc is the apps-tree filename for the default-deny +
// same-Org-allow baseline. It lives under vcluster/apps/ so the funnel's
// apps-sync Kustomization reconciles it INTO the Org-vcluster (alongside the
// customer's app manifests); the syncer then reflects it to the host `<slug>`
// ns. It is NOT listed in the boundary kustomization (a different path).
const networkPolicyDoc = "networkpolicy.yaml"

// ciliumNetworkPolicyDoc is the apps-tree filename for the reserved-entity CNP
// companion (gateway ingress + apiserver egress). Co-located with
// networkPolicyDoc under vcluster/apps/ so it lands on the SAME boundary the
// real Org workloads run on (inside the vcluster for paid tiers; host `<slug>`
// ns for free/S).
const ciliumNetworkPolicyDoc = "ciliumnetworkpolicy.yaml"

const kustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
{{- range .KustomizeResources }}
  - {{ . }}
{{- end }}
`

// appsKustomizationTemplate is the kustomize index for the apps/ tree the
// per-Org apps Flux Kustomization reconciles (#4293 MAJOR-2). Today it lists
// only the default-deny NetworkPolicy baseline; the funnel's app-install tree
// (a DIFFERENT repo) carries the customer's purchased Applications. Keeping an
// explicit index here makes `kustomize build ./vcluster/apps` deterministic.
const appsKustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - ` + networkPolicyDoc + `
  - ` + ciliumNetworkPolicyDoc + `
`

// renderView is the data the templates execute against: the flat Inputs plus
// the plan-derived fields (#4292). Embedding Inputs keeps every existing field
// reachable as `.Slug`, `.Tier`, … while the added fields surface the resolved
// quota + the apps-tree namespace the NetworkPolicy targets.
type renderView struct {
	Inputs
	// Quota is the resolved plan cap (planQuota(.PlanSlug)).
	Quota PlanQuota
	// DefaultCPU/DefaultMem are the LimitRange per-container default
	// request==limit (plan ceiling / 8 for fixed tiers; a small fixed
	// floor for Flexi which has no ceiling).
	DefaultCPU string
	DefaultMem string
	// AppNamespace is the in-vcluster namespace the funnel installs the
	// customer's Applications into (= "apps", matching the provisioning
	// funnel's appNS). The default-deny + same-Org NetworkPolicy targets it
	// so the syncer reflects it to the host `<slug>` ns.
	AppNamespace string
	// KustomizeResources is the file list the boundary kustomization.yaml
	// references — gated by the tier (vcluster.yaml only for paid tiers) and
	// the plan (resourcequota.yaml skipped for soft-cap Flexi).
	KustomizeResources []string
}

// limitRangeDefaults derives the per-container default request==limit for a
// plan. For a fixed tier it is the plan ceiling / 8 (so ~8 unspecified small
// Pods fit under the quota before any Application sets explicit requests). For
// Flexi (no ceiling) it is a small fixed floor.
func limitRangeDefaults(q PlanQuota) (cpu, mem string) {
	switch q.Slug {
	case "s":
		return "250m", "512Mi"
	case "m":
		return "500m", "1Gi"
	case "l":
		return "1", "2Gi"
	case "xl":
		return "2", "4Gi"
	default: // flexi / unknown — small Burstable floor.
		return "100m", "128Mi"
	}
}

// Render returns the rendered (path, bytes) tuples the controller
// writes into the per-Org Gitea repo.
//
// Workstream B (#4292): the rendered set is now plan- and tier-aware:
//   - vcluster.yaml is emitted ONLY for tiers whose boundary is a vCluster
//     (boundaryIsVcluster) — free/S share the host `<slug>` ns by default.
//   - resourcequota.yaml + limitrange.yaml cap the host ns at the purchased
//     plan (skipped ResourceQuota for soft-cap Flexi; LimitRange always).
//   - apps/networkpolicy.yaml seeds the default-deny + same-Org-allow baseline
//     the syncer reflects to the host (sync.toHost.networkPolicies.enabled).
//   - apps/ciliumnetworkpolicy.yaml is the MANDATORY companion that admits the
//     Cilium Gateway `ingress` entity (else the Org's app behind its HTTPRoute
//     503s) + egress to the `kube-apiserver` entity (else in-vcluster pods can't
//     reach the cluster API) — reserved entities no K8s NetworkPolicy can match.
func Render(in Inputs) (map[string][]byte, error) {
	if in.VClusterHelmRepoName == "" {
		in.VClusterHelmRepoName = "loft"
	}
	if in.VClusterHelmRepoNamespace == "" {
		in.VClusterHelmRepoNamespace = "vcluster-system"
	}
	if in.VClusterImageRegistry == "" {
		// Bootstrap default — the Sovereign-local Harbor. Matches the
		// `global.registryMirror` default in bp-dmz/mgmt/rtz-vcluster.
		// Cutover Step-04 flips it to harbor.<sovereign-fqdn> per ADR-0002.
		in.VClusterImageRegistry = "harbor.openova.io"
	}
	if in.Tier == "" {
		in.Tier = "org"
	}

	quota := planQuota(in.PlanSlug)
	defCPU, defMem := limitRangeDefaults(quota)
	view := renderView{
		Inputs:       in,
		Quota:        quota,
		DefaultCPU:   defCPU,
		DefaultMem:   defMem,
		AppNamespace: "apps",
	}

	// Assemble the file set as a function of the tier-gate + plan. The
	// boundary host namespace + its plan-templated quota/LimitRange always
	// render; the vCluster HelmRelease only for paid tiers; the ResourceQuota
	// only for hard-capped plans (Flexi is soft/on-demand).
	files := map[string]string{
		"vcluster/namespace.yaml":  namespaceTemplate,
		"vcluster/limitrange.yaml": limitRangeTemplate,
	}
	res := []string{"namespace.yaml", "limitrange.yaml"}
	if !quota.Burstable {
		files["vcluster/resourcequota.yaml"] = resourceQuotaTemplate
		res = append(res, "resourcequota.yaml")
	}
	if boundaryIsVcluster(in.PlanSlug) {
		files["vcluster/vcluster.yaml"] = vclusterTemplate
		res = append(res, "vcluster.yaml")
	}
	// The default-deny + same-Org-allow baseline lives in the apps tree so
	// the syncer (sync.toHost.networkPolicies.enabled) reflects it to the
	// host `<slug>` ns (vcluster tier) / it applies directly (host tier). It is
	// NOT listed in the boundary kustomization `res` — it is a SEPARATE path
	// under apps/ reconciled by its OWN per-Org Flux Kustomization
	// (catalyst-tenant-<slug>-apps, per_org_flux.go), tier-aware: the vcluster
	// tier carries spec.kubeConfig so the NP lands in the vcluster apiserver;
	// the host tier applies it to the host `<slug>` ns. #4293 MAJOR-2 closed the
	// gap where this NP was committed to a path NO Kustomization referenced
	// (the boundary kustomization omits apps/, and the funnel apps-sync reads a
	// DIFFERENT repo) → intra-Org isolation stayed inert.
	files["vcluster/apps/"+networkPolicyDoc] = networkPolicyTemplate
	// The reserved-entity CNP companion (gateway ingress + apiserver egress).
	// Co-located with the K8s NP so it lands on the SAME boundary the keystone
	// (#4299) installs the Org's real app workloads onto — inside the vcluster for
	// the paid tier, the host `<slug>` ns for free/S. Without it the K8s
	// default-deny silently 503s the Org's Application behind the Cilium Gateway
	// and blocks egress to the cluster API (neither reachable via any K8s NP
	// selector). Kustomize applies a CRD-less cluster's CNP as a no-op object;
	// it has no effect there (kind CI isn't identity-enforcing), so it is safe to
	// always render — on a real Sovereign cilium.io/v2 is present and enforces it.
	files["vcluster/apps/"+ciliumNetworkPolicyDoc] = ciliumNetworkPolicyTemplate
	// Explicit apps/kustomization.yaml so the per-Org apps Flux Kustomization's
	// `kustomize build ./vcluster/apps` enumerates the NP + CNP deterministically
	// (mirrors the funnel apps-tree, which also ships its own kustomization.yaml).
	files["vcluster/apps/kustomization.yaml"] = appsKustomizationTemplate

	// Stable order for the kustomization resource list (map iteration above
	// is randomized — keep the rendered kustomization byte-stable).
	view.KustomizeResources = sortedResources(res)
	files["vcluster/kustomization.yaml"] = kustomizationTemplate

	out := make(map[string][]byte, len(files))
	for path, raw := range files {
		t, err := template.New(path).Funcs(funcs()).Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("template parse %s: %w", path, err)
		}
		var buf bytes.Buffer
		if err := t.Execute(&buf, view); err != nil {
			return nil, fmt.Errorf("template execute %s: %w", path, err)
		}
		out[path] = buf.Bytes()
	}
	return out, nil
}

// sortedResources returns a copy of res in a stable canonical order
// (namespace first, then alphabetical) so the kustomization.yaml is
// byte-stable across reconciles regardless of map iteration order.
func sortedResources(res []string) []string {
	out := make([]string, len(res))
	copy(out, res)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && resourceLess(out[j], out[j-1]); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// resourceLess keeps namespace.yaml first (it must apply before the
// namespaced quota/LimitRange/vcluster), then alphabetical.
func resourceLess(a, b string) bool {
	if a == "namespace.yaml" {
		return b != "namespace.yaml"
	}
	if b == "namespace.yaml" {
		return false
	}
	return a < b
}

func funcs() template.FuncMap {
	return template.FuncMap{
		"quote": func(s string) string { return fmt.Sprintf("%q", s) },
	}
}

// OwnersFromOrg extracts spec.owners[] for re-use by useraccess writers.
func OwnersFromOrg(o *orgapi.Organization) []orgapi.OrganizationOwner {
	if o == nil {
		return nil
	}
	out := make([]orgapi.OrganizationOwner, len(o.Spec.Owners))
	copy(out, o.Spec.Owners)
	return out
}
