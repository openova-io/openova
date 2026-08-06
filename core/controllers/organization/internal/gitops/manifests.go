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

// BoundaryResourceQuotaName / BoundaryLimitRangeName are the object names the
// boundary templates below render into the `<slug>` host namespace. They are
// exported AND interpolated into the templates through renderView (never
// re-typed as a string literal in either place) so the controller's
// provisioning-postcondition readback (provisioning_postconditions.go, #5395)
// probes for EXACTLY the objects this renderer authored — one source of truth,
// so a rename here can never leave the verifier looking for a name nothing
// writes (a verifier that silently checks the wrong name is worse than none).
const (
	BoundaryResourceQuotaName = "plan-quota"
	BoundaryLimitRangeName    = "plan-limits"
)

// PlanRendersResourceQuota reports whether Render emits
// `vcluster/resourcequota.yaml` for a plan — i.e. whether the Org's boundary
// namespace is EXPECTED to carry a `plan-quota` ResourceQuota once Flux has
// applied the boundary tree.
//
// This is the exported form of the `!quota.Burstable` gate inside Render, and
// it exists so that "this Org has no ResourceQuota" becomes a DECIDED outcome
// instead of an ambiguous one (#5395). Fixed tiers (S/M/L/XL — plus the
// ""/free/unknown slugs planQuota defaults to "s") are hard-capped and MUST
// carry the quota; Flexi is the on-demand soft-cap plan and deliberately
// carries none. Without this predicate an operator looking at a quota-less
// namespace cannot distinguish "Flexi, uncapped by design" from "the boundary
// tree never landed" — the two are byte-identical on the cluster, which is
// exactly how a partially-provisioned Org passed for a healthy one.
//
// The LimitRange (BoundaryLimitRangeName) has no such gate — Render emits it
// for EVERY plan including Flexi — so its absence is ALWAYS a delivery gap.
// That asymmetry is what makes the pair a usable diagnostic; see
// provisioning_postconditions.go.
//
// NOTE (Refs #5393): the SIZE of the cap — in particular whether vCluster
// control-plane overhead should count against the customer's purchased plan —
// is a separate, open product question. This predicate answers only "is a quota
// object expected at all", never "how big should it be".
func PlanRendersResourceQuota(planSlug string) bool {
	return !planQuota(planSlug).Burstable
}

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
  # #5003 (Refs #3376): a FRESH funnel Org's FIRST vcluster install pulls the
  # k8s-distro init image (proxy-ghcr/loft-sh/kubernetes:vX) through a COLD
  # Sovereign-Harbor proxy-ghcr cache — ~3m11s live on hw241 — which blows past
  # Flux's default 5m Helm-operation timeout as "context deadline exceeded".
  # WORSE, with install.remediation.retries UNSET (=0) Flux marks the HR
  # Stalled=True / RetriesExceeded ("terminal error: cannot remediate failed
  # release") and it NEVER self-heals — even though vcluster-0 actually comes up
  # 1/1 Running once the pull finally lands. That strands the org-controller
  # (vcluster_ready:false forever) → the tenant-<slug>-kubeconfig secret is never
  # minted → the per-Org apps Kustomization fails "unable to read KubeConfig
  # secret" → WordPress/the customer app never deploys → the app FQDN 404s (the
  # last terminal blocker for zero-touch marketplace-funnel app-serve).
  #
  # Durable fix, two parts:
  #   (1) timeout: 10m — widen the Helm-operation deadline so the first cold
  #       proxy-ghcr pull of the vcluster images (k8s-distro + vcluster-oss
  #       syncer) fits inside a single install attempt.
  #   (2) install/upgrade.remediation.retries: -1 — retry INDEFINITELY instead of
  #       terminally stalling, so any residual transient cold-pull timeout
  #       self-heals on the next reconcile rather than requiring an operator to
  #       manually kick the wedged HR. Harbor warms after the first pull, so the
  #       retry converges quickly. (-1 is Flux's "infinite retries" sentinel; the
  #       vcluster boundary is load-bearing for the whole Org, so we never want it
  #       to give up — unlike the day-2 app HRs which cap at 3.)
  timeout: 10m
  install:
    remediation:
      retries: -1
  upgrade:
    remediation:
      retries: -1
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
          # #4389/#4297: the k8s-distro is initContainers[0] with an asymmetric
          # 40m/100m + 64Mi/256Mi shape (ratio 2.5/4) — the per-Org LimitRange
          # (#4292, maxLimitRequestRatio 1) rejects it alongside the syncer.
          # Render requests==limits so the whole vcluster-0 pod is admitted.
          resources:
            requests:
              cpu: 200m
              memory: 256Mi
            limits:
              cpu: 200m
              memory: 256Mi
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
        # #4389/#4297: the per-Org LimitRange (#4292) maxLimitRequestRatio
        # {cpu:1,memory:1} ALSO applies to the vcluster control-plane
        # StatefulSet (it runs natively in the Org host ns, NOT inside the
        # vcluster) — the asymmetric 100m/2000m + 192Mi/2Gi shape was 'forbidden:
        # cpu limit to request ratio is 20' → vcluster-0 never scheduled → m-tier
        # Orgs could not provision their vcluster at all (#4297 keystone). Render
        # requests==limits (Guaranteed QoS) so the LimitRange admits the syncer.
        # The platform mgmt/rtz/dmz vclusters escape only because their ns has
        # no LimitRange; the per-Org boundary must instead satisfy it.
        resources:
          requests:
            cpu: 500m
            memory: 1Gi
          limits:
            cpu: 500m
            memory: 1Gi
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
        # #4785: sync gateway-api HTTPRoutes to the host <slug> ns. A customer
        # app's Helm chart (e.g. bp-openclaw, bp-wordpress-tenant) renders an
        # HTTPRoute for its external ingress and is installed INTO the
        # Org-vcluster (apps tree, via kubeConfig). A vanilla vcluster has NO
        # gateway.networking.k8s.io CRD, so the install fails with
        # 'no matches for kind HTTPRoute in gateway.networking.k8s.io' and no
        # customer app ever serves (proven live hw225 uat225wp). Enabling the
        # custom-resource sync imports the HTTPRoute CRD (copied from host) so
        # the app can create it, AND reflects it to the host <slug> ns where the
        # Cilium Gateway routes external traffic to the app pod — the same
        # host-reflection model as services/networkPolicies above.
        customResources:
          httproutes.gateway.networking.k8s.io:
            enabled: true
      fromHost:
        ingressClasses:
          enabled: true
    # #4739/#4803/#4821: mirror the HOST keycloak Service INTO the vcluster so a
    # vcluster-hosted app (bp-openclaw) resolves keycloak.keycloak.svc.cluster.
    # local and fetches the realm JWKS IN-CLUSTER — dodging the kom4dc NAT-EIP
    # hairpin that 503s /readyz when the JWKS is pulled from the public issuer's
    # EXTERNAL jwks_uri (the gateway EIP, unreachable from a Pod on kom4dc).
    # Pairs with openclaw's OIDC_INTERNAL_ISSUER_URL seam (#4803).
    #
    # #4821: loft vcluster 0.33.4 has NO sync.fromHost.services key — its
    # values.schema.json rejects it ("Additional property services is not
    # allowed"), so the vcluster HelmRelease InstallFailed and the ENTIRE
    # customer-Org provision stalled (dns/certs/keycloak/registry never ran;
    # first surfaced on the first real customer-Org vcluster prov, hw228 acme).
    # The correct vcluster config to import a host Service is
    # networking.replicateServices.fromHost (a list of {from,to} — host-ns/svc
    # to virtual-ns/svc) — the same key the platform's bp-mgmt-vcluster chart
    # uses for shared-pg.
    networking:
      replicateServices:
        fromHost:
          - from: keycloak/keycloak
            to: keycloak/keycloak
    # #4785: REGISTER the Gateway-API HTTPRoute CRD inside the per-Org vcluster so
    # a customer app's chart (bp-wordpress etc.) can CREATE its ingress HTTPRoute.
    # A vanilla vcluster has NO gateway.networking.k8s.io CRD, and vcluster 0.33.4
    # sync.toHost.customResources (above) does NOT register the CRD in the vcluster
    # apiserver — it only reflects instances host-ward — so the kubeConfig-targeted
    # tenant-<slug>-apps Kustomization dry-run fails 'no matches for kind HTTPRoute
    # in gateway.networking.k8s.io/v1' and wedges the ENTIRE app tree (no customer
    # app ever serves). VALIDATED live on hw228 acme 2026-07-06: the moment this
    # CRD was registered the apps Kustomization flipped False->Ready/Applied and
    # WordPress+MySQL reached 1/1 Running. A STRUCTURAL stub
    # (x-kubernetes-preserve-unknown-fields) is sufficient — the vcluster only needs
    # to ACCEPT the create; the HOST Cilium Gateway-API operator holds the
    # authoritative full schema + does the real validation when toHost reflects the
    # route. Same mechanism bp-mgmt-vcluster uses (values.yaml experimental.deploy,
    # proven hw130) — see #4785 conclusive diagnosis.
    experimental:
      deploy:
        vcluster:
          manifests: |
            apiVersion: apiextensions.k8s.io/v1
            kind: CustomResourceDefinition
            metadata:
              name: httproutes.gateway.networking.k8s.io
              annotations:
                api-approved.kubernetes.io: "https://github.com/kubernetes-sigs/gateway-api/pull/1111"
                catalyst.openova.io/managed-by: org-controller
            spec:
              group: gateway.networking.k8s.io
              scope: Namespaced
              names:
                kind: HTTPRoute
                listKind: HTTPRouteList
                plural: httproutes
                singular: httproute
              versions:
                - name: v1
                  served: true
                  storage: true
                  schema:
                    openAPIV3Schema:
                      type: object
                      x-kubernetes-preserve-unknown-fields: true
                - name: v1beta1
                  served: true
                  storage: false
                  schema:
                    openAPIV3Schema:
                      type: object
                      x-kubernetes-preserve-unknown-fields: true
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
  name: {{ .ResourceQuotaName }}
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
  name: {{ .LimitRangeName }}
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
{{- /*
  #4758 — NO maxLimitRequestRatio on the vcluster-Org host namespace. This
  template renders the HOST ns of a per-Org vCluster, and the vcluster syncer
  reflects the vcluster's pods INTO this ns — starting with the vcluster's own
  bundled system pods (coredns, ratio 50:1 cpu / 2.66:1 mem) which can NEVER be
  Guaranteed. A maxLimitRequestRatio cpu=1/memory=1 here therefore FORBIDS
  every synced pod at admission → coredns Pending → the vcluster runs nothing →
  the customer's first app (WordPress) 404s. Live-confirmed on hw223 uatwalk223
  (syncer: "forbidden: cpu max limit to request ratio ... is 1, but provided
  50"). The #4292/W-2 Guaranteed-QoS enforcement belongs on the HOST-namespace
  Org path (non-vcluster tiers), NOT here — the vcluster boundary + the
  vcluster's own resource caps provide isolation for vcluster-mode Orgs. The
  defaultRequest/default above stay (they satisfy the ResourceQuota's
  "limited resource must be set" admission rule without imposing a ratio).
*/}}
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
// (note: NO `world` — only the gateway, never direct public ingress).
//
// HOST-SIDE, NOT IN-VCLUSTER (#4475)
// ----------------------------------
// CRITICAL: this CNP renders into the HOST-applied `vcluster/host-apps` tree,
// NOT the `vcluster/apps` tree the syncer reflects from the vcluster apiserver.
// A vanilla vcluster has NO `cilium.io/v2` CRD — a CiliumNetworkPolicy applied
// INTO the vcluster apiserver fails the kustomize-controller dry-run
// (`no matches for kind "CiliumNetworkPolicy" in version "cilium.io/v2"`) and
// WEDGES the entire kubeConfig-targeted apps Kustomization for a vcluster-tier
// Org — taking the K8s NPs AND every day-2 Application install down with it
// (#4475 §1). A CNP is also NOT a syncable workload object (sync.toHost only
// reflects the K8s `networkPolicies` kind), so routing it through the vcluster
// at all is wrong. Instead it applies DIRECTLY to the host `<slug>` namespace,
// where Cilium's CRD lives AND where the syncer reflects the Org's vcluster
// pods — so `endpointSelector: {}` binds the actual reflected endpoints, the
// same de-vcluster host-ns enforcement pattern other host-enforced policies
// follow. For the host tier (free/S) the Org's workloads already run in the
// host `<slug>` ns, so the same host-applied CNP binds them there too.
//
// endpointSelector {} = every endpoint in the namespace (matching the K8s
// default-deny's podSelector {} scope). On a CRD-less cluster (kind CI) the
// host-apps tree is simply not exercised by an enforcing agent — the K8s NP
// path in apps/ still applies; there the identity-aware datapath isn't
// enforcing anyway. On a real Sovereign the org-controller always emits it
// host-side.
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
    # SAME-ORG (same-namespace) intra-Org allow. WITHOUT this the vcluster's own
    # synced coredns (host ns pod) is denied reaching the vcluster apiserver pod
    # (vcluster-0 :8443 — a regular pod, NOT the reserved kube-apiserver entity),
    # so coredns never becomes ready and the vcluster never functions. The #4475
    # host-apps move left the same-Org-allow baseline stranded in the
    # vcluster/apps/ tree, which can ONLY apply AFTER the vcluster is functional →
    # chicken-and-egg deadlock (proven live hw225: coredns 0/1 forever, no app
    # ever deploys). endpointSelector {} default-denies every ns pod, so the
    # same-Org allow MUST be host-side + unconditional here. Same-namespace only
    # ⇒ cross-Org / cross-Environment denial is preserved.
    - fromEndpoints:
        - {}
    # PLATFORM GITOPS reach: the flux kustomize-controller (flux-system) applies
    # the Org's day-2 apps INTO the vcluster + mints the tenant-<slug>-kubeconfig
    # secret — it must reach the vcluster apiserver pod :8443. Cross-namespace, so
    # neither fromEntities nor the same-Org fromEndpoints covers it (proven live:
    # flux 10.42.x → vcluster-0:8443 tcp SYN dropped "Policy denied" → apps
    # Kustomization wedged "kubeConfig secret not found" forever). Scoped to the
    # trusted flux-system ns only.
    - fromEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: flux-system
  egress:
    # CLUSTER DNS — 53 UDP + TCP to kube-system CoreDNS.
    #
    # #5617 ROOT CAUSE. This policy carries an egress section under
    # endpointSelector {}, and in Cilium the moment ANY policy selecting an
    # endpoint declares egress, that endpoint's egress becomes deny-by-default
    # outside the union of every such policy. So this one rule set decides
    # egress for EVERY pod in the Org namespace. It used to name kube-apiserver
    # and same-namespace endpoints and NOTHING else — no DNS — which left every
    # workload that ships no DNS egress of its own unable to resolve a single
    # name. Measured live on hw292 Org uatco: the bp-oidc-gate companion
    # (ingress-only CNP) 500'd every OAuth callback AFTER a successful login —
    # oauthproxy.go:881, lookup keycloak.keycloak.svc.cluster.local on
    # 10.96.0.10:53 i/o timeout — while the sibling agenity pod in the SAME
    # namespace resolved fine because its chart ships an egress CNP with DNS.
    #
    # Fixing it per-chart is a per-instance patch that the NEXT per-Org chart
    # re-breaks (#4437 was the first recurrence, #5617 the second). DNS belongs
    # in the namespace baseline: it is required by every workload without
    # exception, and admitting it weakens no isolation boundary — CoreDNS is a
    # read-only name service, and cross-Org/cross-Environment denial is
    # unchanged because this grants nothing but :53 to kube-system.
    #
    # toEndpoints, NEVER toCIDR/ipBlock: a CIDR rule cannot match an in-cluster
    # pod identity under Cilium (#4360) and never matches a ClusterMesh remote
    # identity (#4656), so a CIDR-shaped DNS allow would be inert here and
    # silently re-break the second region of a 2-region Sovereign.
    #
    # L3/L4 ONLY — deliberately no L7 dns rule here. An L7 DNS rule would put
    # EVERY pod in the Organization namespace behind the cilium-agent DNS proxy,
    # which is a datapath and latency change this policy has no reason to make:
    # the goal is to stop denying DNS, not to filter it. A per-workload chart CNP
    # may still add an L7 DNS rule for its own pods; Cilium unions the two.
    - toEndpoints:
        - matchLabels:
            k8s:io.kubernetes.pod.namespace: kube-system
            k8s-app: kube-dns
      toPorts:
        - ports:
            - port: "53"
              protocol: UDP
            - port: "53"
              protocol: TCP
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
    # SAME-ORG egress: coredns / app pods → the vcluster apiserver pod + each
    # other. Same-namespace only ⇒ no cross-Org weakening. Completes the bootstrap
    # path that the vcluster-gated baseline could never deliver in time.
    - toEndpoints:
        - {}
`

// networkPolicyDoc is the apps-tree filename for the default-deny +
// same-Org-allow baseline. It lives under vcluster/apps/ so the funnel's
// apps-sync Kustomization reconciles it INTO the Org-vcluster (alongside the
// customer's app manifests); the syncer then reflects it to the host `<slug>`
// ns. It is NOT listed in the boundary kustomization (a different path).
const networkPolicyDoc = "networkpolicy.yaml"

// ciliumNetworkPolicyDoc is the host-apps-tree filename for the reserved-entity
// CNP companion (gateway ingress + apiserver egress). It lives under
// vcluster/host-apps/ — a SEPARATE tree from the syncer-reflected apps/ — because
// a CiliumNetworkPolicy CANNOT be applied into a vanilla vcluster apiserver (no
// cilium.io/v2 CRD → kustomize dry-run rejection wedges the whole tree, #4475 §1).
// The host-apps tree is ALWAYS applied host-side to the `<slug>` ns where Cilium
// enforces and the syncer reflects the Org's vcluster pods.
const ciliumNetworkPolicyDoc = "ciliumnetworkpolicy.yaml"

const kustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
{{- range .KustomizeResources }}
  - {{ . }}
{{- end }}
`

// appsKustomizationTemplate is the kustomize index for the apps/ tree the
// per-Org apps Flux Kustomization reconciles (#4293 MAJOR-2). It lists ONLY the
// default-deny K8s NetworkPolicy baseline — a SYNCABLE object: for the vcluster
// tier the apps Kustomization carries spec.kubeConfig so this lands in the
// vcluster apiserver and the syncer reflects it to the host `<slug>` ns; for the
// host tier it applies straight to the host ns. The CNP is DELIBERATELY NOT here
// (#4475 §1): a CiliumNetworkPolicy cannot apply into the CRD-less vcluster
// apiserver — it lives in the host-apps/ tree instead. The funnel's app-install
// tree (a DIFFERENT repo) carries the customer's purchased Applications. Keeping
// an explicit index here makes `kustomize build ./vcluster/apps` deterministic.
// appsKustomizationTemplate is the kustomize index for the apps/ tree. It ranges
// over renderView.AppsResources so the file list is tier-aware: the vcluster tier
// prepends namespace.yaml (#4991 — the kubeConfig-targeted apps Kustomization
// applies INTO the Org vcluster with targetNamespace=<slug>, but NOTHING creates
// that namespace INSIDE the vcluster, so Flux failed `namespaces "<slug>" not
// found` and the customer's app never deployed; the host tier already has the
// boundary-owned host `<slug>` ns so it must NOT re-declare it here — a second
// Flux Kustomization managing the same host Namespace fights the boundary one).
const appsKustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
{{- range .AppsResources }}
  - {{ . }}
{{- end }}
`

// hostAppsKustomizationTemplate is the kustomize index for the host-apps/ tree —
// the per-Org HOST-applied apps Flux Kustomization (catalyst-tenant-<slug>-host-apps,
// per_org_flux.go) reconciles it ALWAYS host-side (never via kubeConfig). It
// carries the reserved-entity CiliumNetworkPolicy, which cannot apply into a
// vanilla vcluster apiserver (no cilium.io/v2 CRD, #4475 §1) and is not a
// syncable workload object, AND (#4991) the per-Org provisioning-tenant RBAC the
// org-services/provisioning SA needs to create the kubeconfig mirror Secret +
// bounce vcluster-0 during DNS recovery. Both apply to the host `<slug>` ns.
// Ranges over renderView.HostAppsResources so the index stays deterministic.
const hostAppsKustomizationTemplate = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
{{- range .HostAppsResources }}
  - {{ . }}
{{- end }}
`

// provisioningRBACDoc is the host-apps-tree filename for the per-Org
// provisioning-tenant Role + RoleBinding (#4991).
const provisioningRBACDoc = "provisioning-rbac.yaml"

// appsNamespaceDoc is the apps-tree filename for the vcluster-internal target
// Namespace (#4991, vcluster tier only).
const appsNamespaceDoc = "namespace.yaml"

// provisioningRBACTemplate grants the org-services/provisioning ServiceAccount
// the minimum namespaced permissions it needs inside the Org `<slug>` ns during
// provisioning + teardown (#4991). Root cause it fixes: on a Sovereign the
// funnel's GeneratePerOrgAppsTree DELIBERATELY drops the provisioning-rbac.yaml
// host-scaffolding file (it re-roots only the app workloads into vcluster/apps/),
// and the org-controller emitted NO provisioning Role either — so
// `provisioning` had NO create-secrets / delete-pods rights in `<slug>`. The
// mirrorVClusterKubeconfig step POSTs the kubeconfig mirror into `<slug>` and
// 403'd (`secrets is forbidden`), which is FATAL (failProvision) → the whole
// provision aborted at the vcluster step → WordPress/customer apps never served
// (#4964 added these verbs to the funnel's own generateProvisioningTenantRBAC,
// but on a Sovereign that file is never committed — the Role must be delivered by
// the boundary owner instead). Emitted into vcluster/host-apps/ so it is ALWAYS
// applied host-side onto `<slug>` (never via kubeConfig) at Org-create — well
// before the funnel's mirror step runs. Namespaced (Role, not ClusterRole) to
// preserve the deliberate per-Org write scoping (#75).
const provisioningRBACTemplate = `apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: provisioning-tenant
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
rules:
  - apiGroups: ["helm.toolkit.fluxcd.io"]
    resources: ["helmreleases"]
    verbs: ["get", "list", "watch", "patch", "delete"]
  - apiGroups: ["kustomize.toolkit.fluxcd.io"]
    resources: ["kustomizations"]
    verbs: ["get", "list", "watch", "patch", "delete"]
  - apiGroups: [""]
    # create/update/patch: the #4785 dual-namespace kubeconfig mirror
    # (mirrorVClusterKubeconfig) upserts tenant-<slug>-kubeconfig into this ns.
    resources: ["secrets"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
  - apiGroups: [""]
    # delete: waitForVclusterDNSOrKick bounces vcluster-0 when the syncer's
    # initial DNS reconciliation doesn't publish kube-dns-x-kube-system-x-vcluster.
    resources: ["pods"]
    verbs: ["get", "list", "watch", "delete"]
  - apiGroups: [""]
    resources: ["services"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["cert-manager.io"]
    resources: ["certificates", "certificaterequests"]
    verbs: ["get", "list", "watch", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: provisioning-tenant
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: provisioning-tenant
subjects:
  - kind: ServiceAccount
    name: provisioning
    namespace: org-services
`

// appsNamespaceTemplate creates the vcluster-INTERNAL target namespace for the
// vcluster tier (#4991). The apps Flux Kustomization applies via kubeConfig into
// the Org vcluster with targetNamespace=<slug>; Flux does NOT auto-create the
// target namespace, and nothing else did → `namespaces "<slug>" not found`.
// A Namespace object is cluster-scoped so Flux's targetNamespace rewrite leaves
// its name untouched, creating the virtual `<slug>` ns the workloads land in.
const appsNamespaceTemplate = `apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/managed-by: catalyst
    catalyst.openova.io/component: per-org-apps-target-ns
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
	// AppsResources is the file list vcluster/apps/kustomization.yaml references
	// (#4991) — networkpolicy.yaml always; namespace.yaml only for the vcluster
	// tier (the kubeConfig-targeted apps Kustomization needs the target ns
	// created INSIDE the vcluster; the host tier reuses the boundary-owned ns).
	AppsResources []string
	// HostAppsResources is the file list vcluster/host-apps/kustomization.yaml
	// references (#4991) — ciliumnetworkpolicy.yaml + provisioning-rbac.yaml,
	// both always applied host-side onto `<slug>`.
	HostAppsResources []string
	// ResourceQuotaName / LimitRangeName carry BoundaryResourceQuotaName /
	// BoundaryLimitRangeName into the boundary templates so the rendered object
	// names and the controller's postcondition readback (#5395) cannot drift
	// apart — the constants are the single definition of both.
	ResourceQuotaName string
	LimitRangeName    string
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
//   - host-apps/ciliumnetworkpolicy.yaml is the MANDATORY companion that admits
//     the Cilium Gateway `ingress` entity (else the Org's app behind its
//     HTTPRoute 503s) + egress to the `kube-apiserver` entity (else in-vcluster
//     pods can't reach the cluster API) — reserved entities no K8s NetworkPolicy
//     can match. It lives in a SEPARATE host-applied tree (#4475 §1): a
//     CiliumNetworkPolicy cannot be applied into the CRD-less vcluster apiserver.
func Render(in Inputs) (map[string][]byte, error) {
	if in.VClusterHelmRepoName == "" {
		in.VClusterHelmRepoName = "loft"
	}
	if in.VClusterHelmRepoNamespace == "" {
		in.VClusterHelmRepoNamespace = "vcluster-system"
	}
	// #5439: cutover-aware image-registry host. The chart ALWAYS stamps
	// CATALYST_VCLUSTER_IMAGE_REGISTRY, so the zero-value branch below is
	// only reached by direct callers/tests — the live value arriving here is
	// the mothership literal on every Sovereign from birth. Post-cutover that
	// literal, re-rendered into vcluster/vcluster.yaml on every reconcile,
	// re-tethers a cut-over Sovereign (see cutover_aware_vcluster_5439.go).
	// Pre-cutover (no pivot fact) the resolver returns the input unchanged, so
	// the rendered bytes stay identical and Flux sees zero drift.
	in.VClusterImageRegistry = vclusterImageRegistryFor(
		in.VClusterImageRegistry, in.SovereignFQDN, in.HostCluster)
	if in.Tier == "" {
		in.Tier = "org"
	}

	quota := planQuota(in.PlanSlug)
	defCPU, defMem := limitRangeDefaults(quota)
	view := renderView{
		Inputs:            in,
		Quota:             quota,
		DefaultCPU:        defCPU,
		DefaultMem:        defMem,
		AppNamespace:      "apps",
		ResourceQuotaName: BoundaryResourceQuotaName,
		LimitRangeName:    BoundaryLimitRangeName,
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
	// PlanRendersResourceQuota is the exported form of this same gate — the
	// controller's postcondition verifier (#5395) calls it to decide whether a
	// quota-less boundary namespace is Flexi-by-design or a delivery gap, so the
	// decision MUST be made here through that one predicate.
	if PlanRendersResourceQuota(in.PlanSlug) {
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
	// The reserved-entity CNP companion (gateway ingress + apiserver egress) lives
	// in a SEPARATE host-applied tree (#4475 §1). A CiliumNetworkPolicy CANNOT be
	// applied into the CRD-less vcluster apiserver — the kustomize-controller
	// dry-run rejects `cilium.io/v2` ("no matches for kind CiliumNetworkPolicy")
	// and WEDGES the whole kubeConfig-targeted apps Kustomization (taking the K8s
	// NPs and every day-2 Application install down) for a vcluster-tier Org. It is
	// also NOT a syncable workload object. So the org-controller's host-apps Flux
	// Kustomization (catalyst-tenant-<slug>-host-apps, per_org_flux.go) reconciles
	// this tree ALWAYS host-side onto the `<slug>` ns — where Cilium's CRD lives,
	// where the syncer reflects the Org's vcluster pods, and where the host-tier
	// Org's own pods already run. Without the CNP the K8s default-deny silently
	// 503s the Org's Application behind the Cilium Gateway and blocks egress to the
	// cluster API (neither reachable via any K8s NP selector).
	files["vcluster/host-apps/"+ciliumNetworkPolicyDoc] = ciliumNetworkPolicyTemplate
	// #4991 — the per-Org provisioning-tenant Role+RoleBinding, delivered by the
	// boundary owner into the ALWAYS-host-applied host-apps tree so the
	// org-services/provisioning SA can create the kubeconfig mirror Secret +
	// bounce vcluster-0 in `<slug>` BEFORE the funnel's mirror step 403s and
	// aborts the whole provision.
	files["vcluster/host-apps/"+provisioningRBACDoc] = provisioningRBACTemplate
	appsResources := []string{networkPolicyDoc}
	if boundaryIsVcluster(in.PlanSlug) {
		// #4991 — the kubeConfig-targeted apps Kustomization applies INTO the Org
		// vcluster with targetNamespace=<slug>; create that namespace INSIDE the
		// vcluster (nothing else does → `namespaces "<slug>" not found`). Host
		// tier reuses the boundary-owned host ns, so it is NOT re-declared there.
		files["vcluster/apps/"+appsNamespaceDoc] = appsNamespaceTemplate
		appsResources = append(appsResources, appsNamespaceDoc)
	}
	// Explicit apps/kustomization.yaml so the per-Org apps Flux Kustomization's
	// `kustomize build ./vcluster/apps` enumerates the K8s NP (+ vcluster-tier
	// target namespace) deterministically.
	view.AppsResources = sortedResources(appsResources)
	files["vcluster/apps/kustomization.yaml"] = appsKustomizationTemplate
	// Explicit host-apps/kustomization.yaml so the per-Org host-apps Flux
	// Kustomization's `kustomize build ./vcluster/host-apps` enumerates the CNP
	// + provisioning RBAC deterministically.
	view.HostAppsResources = sortedResources([]string{ciliumNetworkPolicyDoc, provisioningRBACDoc})
	files["vcluster/host-apps/kustomization.yaml"] = hostAppsKustomizationTemplate

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
