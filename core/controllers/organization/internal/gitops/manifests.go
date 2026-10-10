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

	"k8s.io/apimachinery/pkg/api/resource"

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

	// OverageMode is the package's overage mode, from the Organization CR
	// spec.commerce.overageMode (founder model, 2026-10-10): "capped" — the
	// ResourceQuota limits are the plan headline, exactly the render before
	// the field existed — or "grow" — the limits are raised to the grow
	// ceiling and usage above the headline is billed in arrears by BSS.
	// Empty (no spec.commerce, or a block without the field) and any other
	// value read as "capped". Ignored for Burstable plans (Flexi renders no
	// ResourceQuota in either mode).
	OverageMode string

	// GrowCeilingCPU / GrowCeilingMemory are the grow ceiling as Kubernetes
	// quantities ("8", "16Gi"), from spec.commerce.growCeiling.vcpu /
	// .memoryGB. Read only when OverageMode is "grow". Empty (or
	// unparseable) = the default ceiling, the XL headline. See
	// quotaLimitsFor for the clamps.
	GrowCeilingCPU    string
	GrowCeilingMemory string
}

// Overage modes Inputs.OverageMode carries (spec.commerce.overageMode).
const (
	OverageModeCapped = "capped"
	OverageModeGrow   = "grow"
)

// PlanQuota is the per-plan resource cap the org-controller materializes on
// the Org boundary host namespace (#4292). It REPLACES both the retired
// marketplace-api `SizeResources` (raw req.Size string, dev-tiny, no
// LimitRange) and the provisioning `planLimits` (syncer-pod-only). The cap is
// keyed by the catalog plan SLUG (core/services/catalog/handlers/seed.go
// seedPlanRows), so the resource the customer pays for is exactly the
// resource that materializes.
//
// The overcommit model (#6971, NC-OO-Pricing.xlsx 2026-06-28): a package is
// sold by its HEADLINE shape — S 1 vCPU / 2 GB, M 2 / 4, L 4 / 8, XL 8 / 16 —
// and that headline is the LIMIT: the ceiling the customer's workloads may
// burst to and the cap the customer pays for. What the Sovereign PROVISIONS
// for it — the guaranteed share, reserved on the node — is the headline over
// the workbook's overcommit ratios, CPU 6× and memory 3×: S 167m / 683Mi,
// M 334m / 1366Mi, L 667m / 2731Mi, XL 1334m / 5462Mi (headline ÷ 6 and ÷ 3,
// rounded UP to the millicore / MiB so the ratio is never exceeded). That
// guaranteed share is the REQUEST.
//
//	CPULimit   / MemLimit   — the headline the customer bought; the
//	                          ResourceQuota's limits.cpu / limits.memory term.
//	CPURequest / MemRequest — the guaranteed share the Sovereign reserves;
//	                          the ResourceQuota's requests.cpu / requests.memory
//	                          term. The hard cap on each side is that term PLUS
//	                          the vCluster control-plane overhead
//	                          (vclusterControlPlaneOverhead) PLUS the
//	                          per-Organization platform-stack overhead
//	                          (platformStack), each side with its own figures,
//	                          so neither overhead eats into the plan.
//	Burstable — Flexi alone: on demand, no hard ceiling, no ResourceQuota;
//	            the LimitRange still renders (a small floor) so default-less
//	            pods admit. Fixed tiers (S/M/L/XL) are hard-capped.
type PlanQuota struct {
	Slug       string
	CPULimit   string // headline, e.g. "1", "2", "4", "8"
	MemLimit   string // headline, e.g. "2Gi", "4Gi"
	CPURequest string // guaranteed = headline ÷ 6, e.g. "167m"
	MemRequest string // guaranteed = headline ÷ 3, e.g. "683Mi"
	Burstable  bool
}

// planOvercommitCPU / planOvercommitMemory are the workbook's overcommit
// ratios — the headline limit over the guaranteed request. They are stamped
// on the live LimitRange (openova.io/plan-overcommit-cpu / -memory) and size
// its per-container default request from its default limit.
const (
	planOvercommitCPU    = 6
	planOvercommitMemory = 3
)

// planQuotaTable is the catalog-slug → host-ns cap map (issue #4292 target
// table). It is the ONE source the org-controller drives the ResourceQuota +
// LimitRange off; the marketplace-api SizeResources + provisioning planLimits
// were retired in Workstream A precisely so this table is the only one.
//
// S/M/L/XL are fixed hard-capped tiers; Flexi is on-demand Burstable (no hard
// quota ceiling — soft, scale-on-demand). The headline numbers mirror the
// seeded plan rows (core/services/catalog/handlers/seed.go seedPlanRows:
// S 1 vCPU / 2 GB, M 2 / 4, L 4 / 8, XL 8 / 16 — the National Cloud workbook,
// NC-OO-Pricing.xlsx 2026-06-28); the requests are those over the 6× / 3×
// overcommit, rounded up (TestPlanQuota_RequestIsHeadlineOverOvercommit).
var planQuotaTable = map[string]PlanQuota{
	"s":     {Slug: "s", CPULimit: "1", MemLimit: "2Gi", CPURequest: "167m", MemRequest: "683Mi", Burstable: false},
	"m":     {Slug: "m", CPULimit: "2", MemLimit: "4Gi", CPURequest: "334m", MemRequest: "1366Mi", Burstable: false},
	"l":     {Slug: "l", CPULimit: "4", MemLimit: "8Gi", CPURequest: "667m", MemRequest: "2731Mi", Burstable: false},
	"xl":    {Slug: "xl", CPULimit: "8", MemLimit: "16Gi", CPURequest: "1334m", MemRequest: "5462Mi", Burstable: false},
	"flexi": {Slug: "flexi", Burstable: true},
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

// ONE boundary primitive (founder, 2026-09-10: "the 1 SME customer must have
// 1 vcluster"). Every Organization — plan s, m, l, xl, flexi, an empty legacy
// slug, "free", or a slug nobody has heard of — is backed by a dedicated Org
// vCluster rendered into its host `<slug>` namespace. The plan slug sizes the
// boundary (planQuota → ResourceQuota + LimitRange, and the QoS shape) and
// never selects it.
//
// Until 2026-09-10 a "tier gate" here (boundaryIsVcluster, Refs #4292 #4297)
// put free/S Organizations onto the bare host namespace and only paid M+ onto
// a vCluster, behind a `const allTiersVcluster = false` whose comment promised
// a one-line flip. The flip was never safe to take: the predicate had been
// copied into three more modules (the funnel's BoundaryIsVcluster, the BSS
// door's isolationForTier, the console's isolationForPlan), each carrying its
// own copy of the constant, and behind it a second file set, a second pod-name
// shape, a second readiness rule and a second status stamp had grown. The gate
// is REMOVED rather than flipped: there is no predicate left to keep in
// lockstep, so nothing can drift, and Render below has exactly one shape.

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
// NOTE (Refs #5393, decided with the #6902 follow-ups): the SIZE of the cap is
// the purchased plan PLUS the vCluster control-plane overhead PLUS the
// per-Organization platform-stack overhead — see vclusterControlPlaneOverhead
// and platformStack below. This predicate answers only "is a quota object
// expected at all", never "how big it is".
func PlanRendersResourceQuota(planSlug string) bool {
	return !planQuota(planSlug).Burstable
}

// ─── The vCluster control plane is overhead, not purchased capacity ─────────
//
// Every Organization's vCluster control plane runs in the SAME host `<slug>`
// namespace the plan ResourceQuota caps (#6902): the `vcluster-0` StatefulSet
// pod (the syncer container behind the k8s-distro init container) and the
// vCluster's own coredns, which the syncer mirrors down from the virtual
// `kube-system` into the host namespace. Both are charged to `plan-quota` at
// admission. With the quota equal to the bare plan an S Organization bought
// 2 vCPU and could schedule ~1.48 of them — the control plane ate the rest.
// Decision (follow-up to #6902, Refs #4292 #6867): the hard cap is the
// purchased plan PLUS the control plane's own requests/limits, so the control
// plane never eats into what the customer bought and the customer's usable
// share equals the plan exactly. Chargeback is unchanged by this: the platform
// collector meters the Organization's workload requests and excludes these
// control-plane pods (products/chargeback/.../collector.go) — they are overhead
// the Sovereign pays, not usage the customer bought.
//
// The figures are derived from THIS renderer's own values: vclusterControlPlane
// is the ONE place the vcluster HelmRelease template below takes its resource
// numbers from, and vclusterControlPlaneOverhead is computed from it — never
// from a chart default read elsewhere or a figure quoted in a PR. The pin test
// (TestVClusterControlPlaneOverhead_PinnedToRenderedValues) parses the rendered
// HelmRelease, walks EVERY `resources` block under spec.values and recomputes
// the overhead; a block the classification there does not know fails the
// build, so a new sidecar cannot land in the quota unaccounted.
//
// Quota arithmetic follows the pod-usage rule the ResourceQuota admission
// plugin applies: a pod's effective request (and limit) per resource is
// max(sum of its app containers, its largest init container). vcluster-0 is
// one app container (syncer) behind one init container (k8s distro), so it
// costs max(syncer, distro); coredns is a pod of its own and adds in full.
// Requests and limits are carried SEPARATELY: coredns runs Burstable (20m
// requests, 1000m limits — the vcluster chart's own shape, which #4758 already
// had to admit by dropping the LimitRange ratio), so a single figure would
// leave one of the two hard caps short.

// containerShape is one container's requests/limits exactly as the vcluster
// HelmRelease template renders them.
type containerShape struct {
	RequestsCPU    string
	RequestsMemory string
	LimitsCPU      string
	LimitsMemory   string
}

// vclusterControlPlaneShape is every resource figure this renderer writes into
// the vCluster HelmRelease values. The template interpolates these fields; the
// overhead is computed from them. Change a figure here and both move together.
type vclusterControlPlaneShape struct {
	// Syncer is controlPlane.statefulSet.resources — vcluster-0's app
	// container (apiserver + controller-manager + syncer processes).
	Syncer containerShape
	// Distro is controlPlane.distro.k8s.resources — vcluster-0's `kubernetes`
	// init container (copies the k8s binaries; #4389/#4297 shape).
	Distro containerShape
	// CoreDNS is controlPlane.coredns.deployment.resources — the vCluster's
	// coredns pod, synced into the host namespace from the virtual kube-system.
	CoreDNS containerShape
	// VolumeSize is controlPlane.statefulSet.persistence.volumeClaim.size —
	// the `data-vcluster-0` PVC (embedded SQLite backing store).
	VolumeSize string
}

// vclusterControlPlane is the shape rendered today. The syncer and distro
// figures are the requests==limits shapes #4389/#4297 introduced; the coredns
// figures are the vcluster 0.33 chart's own defaults, restated here so the
// overhead is derived from this render rather than from a default read off
// the chart (identical to the merged default, so the chart's
// vClusterConfigHash — and vcluster-0 — do not move).
var vclusterControlPlane = vclusterControlPlaneShape{
	Syncer:     containerShape{RequestsCPU: "500m", RequestsMemory: "1Gi", LimitsCPU: "500m", LimitsMemory: "1Gi"},
	Distro:     containerShape{RequestsCPU: "200m", RequestsMemory: "256Mi", LimitsCPU: "200m", LimitsMemory: "256Mi"},
	CoreDNS:    containerShape{RequestsCPU: "20m", RequestsMemory: "64Mi", LimitsCPU: "1000m", LimitsMemory: "170Mi"},
	VolumeSize: "5Gi",
}

// ControlPlaneOverhead is what the per-Org vCluster control plane charges to
// the host-namespace ResourceQuota, per hard-cap resource, plus the storage its
// backing-store volume claims. CPU is exact in millicores
// (Quantity.MilliValue), memory and storage in bytes (Quantity.Value).
type ControlPlaneOverhead struct {
	RequestsCPU    resource.Quantity
	RequestsMemory resource.Quantity
	LimitsCPU      resource.Quantity
	LimitsMemory   resource.Quantity
	Storage        resource.Quantity
}

// vclusterControlPlaneOverhead is THE overhead value the ResourceQuota adds to
// every hard-capped plan: computed once from vclusterControlPlane, pinned to
// the rendered HelmRelease by test.
var vclusterControlPlaneOverhead = controlPlaneOverheadOf(vclusterControlPlane)

// controlPlaneOverheadOf applies the ResourceQuota pod-usage rule to the
// rendered shape: vcluster-0 = max(syncer, distro) per resource (one app
// container behind one init container), coredns = its own pod, summed.
func controlPlaneOverheadOf(s vclusterControlPlaneShape) ControlPlaneOverhead {
	vc0 := podEffectiveShape([]containerShape{s.Syncer}, []containerShape{s.Distro})
	dns := podEffectiveShape([]containerShape{s.CoreDNS}, nil)
	return ControlPlaneOverhead{
		RequestsCPU:    sumQuantities(vc0.RequestsCPU, dns.RequestsCPU),
		RequestsMemory: sumQuantities(vc0.RequestsMemory, dns.RequestsMemory),
		LimitsCPU:      sumQuantities(vc0.LimitsCPU, dns.LimitsCPU),
		LimitsMemory:   sumQuantities(vc0.LimitsMemory, dns.LimitsMemory),
		Storage:        mustQuantity(s.VolumeSize),
	}
}

// podEffectiveShape is the Kubernetes effective pod request/limit per resource:
// max(sum(app containers), max(init containers)) — the figure the ResourceQuota
// admission plugin charges for the pod.
func podEffectiveShape(containers, inits []containerShape) containerShape {
	eff := func(pick func(containerShape) string) string {
		var sum resource.Quantity
		for _, c := range containers {
			sum.Add(mustQuantity(pick(c)))
		}
		for _, c := range inits {
			if q := mustQuantity(pick(c)); q.Cmp(sum) > 0 {
				sum = q
			}
		}
		return sum.String()
	}
	return containerShape{
		RequestsCPU:    eff(func(c containerShape) string { return c.RequestsCPU }),
		RequestsMemory: eff(func(c containerShape) string { return c.RequestsMemory }),
		LimitsCPU:      eff(func(c containerShape) string { return c.LimitsCPU }),
		LimitsMemory:   eff(func(c containerShape) string { return c.LimitsMemory }),
	}
}

func sumQuantities(a, b string) resource.Quantity {
	q := mustQuantity(a)
	q.Add(mustQuantity(b))
	return q
}

// mustQuantity parses a resource literal from this file's own constants. A
// typo is a build-time defect of this package, so it panics at init rather
// than rendering a quota nobody asked for.
func mustQuantity(s string) resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic(fmt.Sprintf("gitops: bad resource literal %q: %v", s, err))
	}
	return q
}

// quotaHard is the four hard-cap strings the ResourceQuota template renders:
// the plan term plus the control-plane overhead plus the platform-stack
// overhead, per resource, in the canonical Quantity spelling. The plan term
// differs per side (#6971): the guaranteed request on requests.* (plan M
// "334m" + 520m + 3840m → "4694m"), the headline limit on limits.* (plan M
// "2" + 1500m + 4550m → "8050m").
type quotaHard struct {
	RequestsCPU    string
	RequestsMemory string
	LimitsCPU      string
	LimitsMemory   string
}

// planPlusOverhead sizes the hard cap for a fixed-tier plan: the plan term
// plus the vCluster control plane plus the per-Organization platform stack,
// requests (guaranteed share + the overheads' requests) and limits (headline +
// the overheads' limits) each with their own figures. Flexi never reaches
// here (PlanRendersResourceQuota gates the file), and its empty figures would
// not parse.
func planPlusOverhead(q PlanQuota, cp ControlPlaneOverhead, ps PlatformStackOverhead) quotaHard {
	add := func(plan string, overheads ...resource.Quantity) string {
		sum := mustQuantity(plan)
		for _, o := range overheads {
			sum.Add(o)
		}
		return sum.String()
	}
	return quotaHard{
		RequestsCPU:    add(q.CPURequest, cp.RequestsCPU, ps.RequestsCPU),
		RequestsMemory: add(q.MemRequest, cp.RequestsMemory, ps.RequestsMemory),
		LimitsCPU:      add(q.CPULimit, cp.LimitsCPU, ps.LimitsCPU),
		LimitsMemory:   add(q.MemLimit, cp.LimitsMemory, ps.LimitsMemory),
	}
}

// growCeilingPlan is the plan whose headline is the DEFAULT grow ceiling and
// the HIGHEST ceiling a grow package may choose: the XL shape. It is read from
// planQuotaTable, never restated as a literal, so a change to the XL headline
// moves the grow ceiling with it.
const growCeilingPlan = "xl"

// quotaLimitsFor is the plan term on the ResourceQuota LIMITS side for a
// fixed-tier plan, per overage mode (founder model, 2026-10-10):
//
//   - capped (also empty or any unknown mode): the headline, q.CPULimit /
//     q.MemLimit, returned as the table's own strings so the render is
//     byte-for-byte the pre-overage one.
//   - grow: the grow ceiling. An empty, unparseable or non-positive value is
//     the default ceiling, the XL headline. Two clamps, per resource:
//     a ceiling BELOW the plan headline is raised to the headline (grow can
//     never cap tighter than the package the customer paid for); a ceiling
//     ABOVE the XL headline is lowered to it (XL is the largest shape the
//     Sovereign sells, and a grow package may not outgrow it). The order
//     path validates the same bounds upstream; these clamps keep a CR that
//     reached the controller another way inside them.
//
// The REQUESTS side is not this function's: it stays the guaranteed share
// (headline ÷ overcommit) in both modes. grow reports whether the ceiling was
// applied. Burstable plans have no limits term (they render no quota).
func quotaLimitsFor(q PlanQuota, mode, ceilCPU, ceilMem string) (cpu, mem string, grow bool) {
	if q.Burstable {
		return "", "", false
	}
	if mode != OverageModeGrow {
		return q.CPULimit, q.MemLimit, false
	}
	top := planQuotaTable[growCeilingPlan]
	return clampCeiling(ceilCPU, q.CPULimit, top.CPULimit),
		clampCeiling(ceilMem, q.MemLimit, top.MemLimit), true
}

// clampCeiling resolves one grow-ceiling value into [floor, top] (both this
// file's own literals), defaulting to top when v is empty, unparseable or not
// positive. It returns the canonical Quantity spelling.
func clampCeiling(v, floor, top string) string {
	t := mustQuantity(top)
	c, err := resource.ParseQuantity(strings.TrimSpace(v))
	if err != nil || c.Sign() <= 0 {
		return t.String()
	}
	if f := mustQuantity(floor); c.Cmp(f) < 0 {
		return f.String()
	}
	if c.Cmp(t) > 0 {
		return t.String()
	}
	return c.String()
}

// QuotaLimitsFor is the exported form of quotaLimitsFor keyed by plan slug:
// the cpu / memory plan term the ResourceQuota renders on its limits side
// (before the control-plane and platform-stack overheads are added), and
// whether the grow ceiling replaced the headline. Burstable plans return
// empty strings. PlanRendersResourceQuota still answers whether a quota
// renders at all — the overage mode never changes that.
func QuotaLimitsFor(planSlug, overageMode, growCeilingCPU, growCeilingMemory string) (cpu, memory string, grow bool) {
	return quotaLimitsFor(planQuota(planSlug), overageMode, growCeilingCPU, growCeilingMemory)
}

// String renders the overhead the way the ResourceQuota annotation carries it,
// so an operator reading the live object sees the split without the source.
func (o ControlPlaneOverhead) String() string {
	return fmt.Sprintf("requests cpu=%s memory=%s; limits cpu=%s memory=%s; storage=%s",
		o.RequestsCPU.String(), o.RequestsMemory.String(),
		o.LimitsCPU.String(), o.LimitsMemory.String(), o.Storage.String())
}

// ─── The per-Organization platform stack is overhead, not purchased capacity ─
//
// Every Organization is delivered with the SAME set of platform HelmReleases
// in its host `<slug>` namespace, none of which the customer chose from the
// catalog: bp-keycloak (the Organization's own Keycloak plus its bundled
// PostgreSQL), bp-newapi (the LLM gateway plus its CNPG PostgreSQL),
// bp-openclaw (the workspace controller) and bp-agenity (the agentic dashboard
// plus the oidc-gate in front of it). The BSS door renders all four for every
// Organization unconditionally (products/catalyst/bootstrap/api/internal/
// handler/organization_gitops.go orgTenantTemplates; bp-wordpress-tenant and
// bp-stalwart-tenant in that same map are the customer's purchase and are NOT
// in this list). Every one of their pods is charged to `plan-quota` at
// admission.
//
// Measured on hw307 (Acme Walk, plan S, 2026-09-10 17:10Z): with the quota
// equal to plan + control plane, vcluster-0 was admitted and bp-keycloak-0 was
// refused — `requested: limits.cpu=1,limits.memory=2Gi | used:
// limits.cpu=3450m,limits.memory=4394Mi | limited: limits.cpu=3500m,
// limits.memory=5290Mi` — so the Helm install timed out and newapi, openclaw
// and the purchased WordPress and Stalwart waited on the keycloak dependency
// forever. The stack alone is ~4.5 CPU / 7 GiB of limits, larger than the S
// plan by itself.
//
// Decision (Refs #6902 #6867; same principle as the control plane): the stack
// is OpenOva's overhead delivered with every Organization, not the customer's
// purchase. The hard cap is purchased plan + vCluster control plane + this
// platform stack, for requests and for limits, and the customer's catalog
// applications consume the plan. Chargeback draws the same line
// (products/chargeback/internal/adapter/openova/collector.go isPlatformStack):
// these pods stay off the customer's k8s.* meters.
//
// WHAT IS COUNTED: the long-running pods each release keeps in the namespace,
// with every container the pod carries — the app container, native sidecars
// (which count like app containers) and plain init containers under the
// pod-usage max rule (podEffectiveShape). The figures come from the ONE place
// each is set: the BSS door pins keycloak, its postgresql and the newapi
// container; the funnel (core/services/provisioning/gitops/helmrelease_apps.go)
// pins the newapi container to the same values; every other figure is the
// chart's own default, because neither door overrides it. The pin test
// (plan_quota_plus_platform_stack_test.go) re-reads each of those sources,
// fails on drift, and checks the release list against orgTenantTemplates.
//
// A container the chart leaves UNSIZED (bp-agenity's `seed-claude-creds` init
// container) is admitted with the per-Org LimitRange defaults of the plan
// (limitRangeDefaults: default limit = headline/8, default request = that over
// the overcommit ratio) — so its cost, and with it the stack overhead, depends
// on the plan in principle. With the #6971 headlines (XL 8 vCPU / 16 GiB →
// defaults 1 CPU / 2Gi limit, 167m / 683Mi request) the agenity app
// containers dominate the pod on every plan, so the four figures coincide
// today; the rule is still applied per plan, and a larger headline would
// change XL first. A zero-value containerShape below means exactly that, and
// platformStackOverheadOf resolves it per plan.
//
// NOT COUNTED, deliberately: transient pods — Helm hook Jobs (keycloak-config-
// cli 250m/256Mi, the newapi seed and secret-sync Jobs) and a Deployment's
// surge replica during a rollout. The quota charges them only while they run,
// and a namespace quota cannot reserve room for a pod that is not there: any
// headroom added for them is headroom the customer's own pods may take first.
// They ride the plan's unconsumed slack, exactly as the control plane's own
// transients do. openclaw's per-user workspace pods are not here either: they
// exist per signed-in User of the customer, so they are the customer's usage,
// not a workload delivered with the Organization. Storage is not counted: the
// quota renders no storage cap.

// platformStackWorkload is one long-running pod the per-Organization platform
// stack keeps in the host namespace, with the requests/limits of every
// container it carries exactly as the source named in Source sets them.
type platformStackWorkload struct {
	// Name is the pod (or pod-name prefix) as it appears in the host namespace.
	Name string
	// Release is the HelmRelease that installs it — one of the four the BSS
	// door renders for every Organization.
	Release string
	// Source names where each figure is pinned, for the pin test and the reader.
	Source string
	// Containers are the app containers plus native sidecars; their shapes sum.
	Containers []containerShape
	// Inits are plain init containers; the largest competes with the sum above
	// under the pod-usage rule. A zero-value shape is a container the chart
	// leaves unsized, which the LimitRange defaults size per plan.
	Inits []containerShape
}

// platformStack is the stack rendered today. Change a figure at its Source
// and the pin test names this table; change it here and the pin test names
// the Source — the two cannot drift apart silently.
var platformStack = []platformStackWorkload{
	{
		Name: "bp-keycloak-0", Release: "bp-keycloak",
		Source: "organization_gitops.go orgTenantBPKeycloak spec.values.keycloak.resources",
		// The bitnami subchart's own init containers are preset-sized below the
		// keycloak container on every plan; the hw307 admission message quoted
		// above charges this pod at exactly 1 CPU / 2Gi.
		Containers: []containerShape{{RequestsCPU: "1", RequestsMemory: "2Gi", LimitsCPU: "1", LimitsMemory: "2Gi"}},
	},
	{
		Name: "bp-keycloak-postgresql-0", Release: "bp-keycloak",
		Source:     "organization_gitops.go orgTenantBPKeycloak spec.values.keycloak.postgresql.primary.resources",
		Containers: []containerShape{{RequestsCPU: "500m", RequestsMemory: "512Mi", LimitsCPU: "500m", LimitsMemory: "512Mi"}},
	},
	{
		Name: "bp-newapi-<hash>", Release: "bp-newapi",
		Source: "newapi: organization_gitops.go orgTenantBPNewAPI and helmrelease_apps.go generateNewAPIHR spec.values.newapi.resources; " +
			"sandbox-bridge (native sidecar) / metering-sidecar: platform/newapi/chart/values.yaml sandboxBridge.resources / meteringSidecar.resources; " +
			"wait-for-sql-dsn: platform/newapi/chart/templates/deployment.yaml (inline)",
		Containers: []containerShape{
			{RequestsCPU: "500m", RequestsMemory: "256Mi", LimitsCPU: "500m", LimitsMemory: "1Gi"}, // newapi
			{RequestsCPU: "10m", RequestsMemory: "32Mi", LimitsCPU: "200m", LimitsMemory: "128Mi"}, // sandbox-bridge (native sidecar, #3374)
			{RequestsCPU: "25m", RequestsMemory: "64Mi", LimitsCPU: "500m", LimitsMemory: "256Mi"}, // metering-sidecar
		},
		// The plain init runs with the sandbox-bridge sidecar already held, so
		// its true branch is init + bridge; both are far below the container
		// sum, so the standalone max rule below gives the same pod shape.
		Inits: []containerShape{{RequestsCPU: "10m", RequestsMemory: "16Mi", LimitsCPU: "100m", LimitsMemory: "32Mi"}}, // wait-for-sql-dsn
	},
	{
		Name: "bp-newapi-newapi-pg-1", Release: "bp-newapi",
		Source:     "platform/newapi/chart/values.yaml cnpg.cluster.resources × cnpg.cluster.instances (1); the CNPG bootstrap-controller init container inherits the same block",
		Containers: []containerShape{{RequestsCPU: "500m", RequestsMemory: "512Mi", LimitsCPU: "500m", LimitsMemory: "512Mi"}},
		Inits:      []containerShape{{RequestsCPU: "500m", RequestsMemory: "512Mi", LimitsCPU: "500m", LimitsMemory: "512Mi"}},
	},
	{
		Name: "bp-openclaw-<hash>", Release: "bp-openclaw",
		Source:     "platform/openclaw/chart/values.yaml controller.resources",
		Containers: []containerShape{{RequestsCPU: "250m", RequestsMemory: "512Mi", LimitsCPU: "250m", LimitsMemory: "512Mi"}},
	},
	{
		Name: "bp-agenity-0", Release: "bp-agenity",
		Source: "products/agenity/chart/values.yaml resources (chepherd) and anthropic.credentialResync.resources (creds-resync); " +
			"seed-claude-creds is unsized in templates/statefulset.yaml and takes the LimitRange defaults",
		Containers: []containerShape{
			{RequestsCPU: "1", RequestsMemory: "2Gi", LimitsCPU: "1", LimitsMemory: "2Gi"},      // chepherd
			{RequestsCPU: "5m", RequestsMemory: "16Mi", LimitsCPU: "50m", LimitsMemory: "64Mi"}, // creds-resync (#6317)
		},
		Inits: []containerShape{{}}, // seed-claude-creds → LimitRange defaults of the plan
	},
	{
		Name: "oidc-gate-agenity-<slug>", Release: "bp-agenity",
		Source:     "products/agenity/chart/values.yaml oidcGate.resources",
		Containers: []containerShape{{RequestsCPU: "50m", RequestsMemory: "64Mi", LimitsCPU: "50m", LimitsMemory: "64Mi"}},
	},
}

// PlatformStackOverhead is what the per-Organization platform stack charges to
// the host-namespace ResourceQuota, per hard-cap resource, for one plan. CPU is
// exact in millicores (Quantity.MilliValue), memory in bytes (Quantity.Value).
type PlatformStackOverhead struct {
	RequestsCPU    resource.Quantity
	RequestsMemory resource.Quantity
	LimitsCPU      resource.Quantity
	LimitsMemory   resource.Quantity
}

// platformStackOverheadOf applies the ResourceQuota pod-usage rule to every
// workload in the stack and sums the pods. def is the plan's LimitRange
// per-container defaults (limitRangeDefaults: defaultRequest on the request
// side, default on the limit side), which size any container the chart
// leaves unsized — so the result is per plan.
func platformStackOverheadOf(stack []platformStackWorkload, def containerShape) PlatformStackOverhead {
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
	var o PlatformStackOverhead
	for _, w := range stack {
		eff := podEffectiveShape(sized(w.Containers), sized(w.Inits))
		o.RequestsCPU.Add(mustQuantity(eff.RequestsCPU))
		o.RequestsMemory.Add(mustQuantity(eff.RequestsMemory))
		o.LimitsCPU.Add(mustQuantity(eff.LimitsCPU))
		o.LimitsMemory.Add(mustQuantity(eff.LimitsMemory))
	}
	return o
}

// platformStackOverheadFor is the stack overhead for a plan: the shared table
// resolved against that plan's LimitRange defaults.
func platformStackOverheadFor(q PlanQuota) PlatformStackOverhead {
	return platformStackOverheadOf(platformStack, limitRangeDefaults(q))
}

// String renders the overhead the way the ResourceQuota annotation carries it.
func (o PlatformStackOverhead) String() string {
	return fmt.Sprintf("requests cpu=%s memory=%s; limits cpu=%s memory=%s",
		o.RequestsCPU.String(), o.RequestsMemory.String(),
		o.LimitsCPU.String(), o.LimitsMemory.String())
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
          # of the time (#4292, maxLimitRequestRatio 1; the ratio is gone since
          # #4758) rejected it alongside the syncer. Rendered requests==limits
          # since then so the whole vcluster-0 pod is admitted; kept as is.
          # Figures come from vclusterControlPlane.Distro — the same values the
          # plan ResourceQuota's control-plane overhead is computed from.
          resources:
            requests:
              cpu: {{ .ControlPlane.Distro.RequestsCPU }}
              memory: {{ .ControlPlane.Distro.RequestsMemory }}
            limits:
              cpu: {{ .ControlPlane.Distro.LimitsCPU }}
              memory: {{ .ControlPlane.Distro.LimitsMemory }}
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
          # The vcluster 0.33 chart's own default coredns shape (requests
          # 20m/64Mi, limits 1000m/170Mi), restated from vclusterControlPlane.CoreDNS
          # so the control-plane overhead the plan ResourceQuota carries is
          # derived from THIS render, not from a default read off the chart. The
          # syncer mirrors this pod into the host <slug> ns where the quota
          # charges it. Byte-identical to the merged chart default, so the
          # chart's vClusterConfigHash — and vcluster-0 — do not move. #4758 is
          # why this pod can never be Guaranteed (and why requests and limits
          # carry separate overhead figures).
          resources:
            requests:
              cpu: {{ .ControlPlane.CoreDNS.RequestsCPU }}
              memory: {{ .ControlPlane.CoreDNS.RequestsMemory }}
            limits:
              cpu: {{ .ControlPlane.CoreDNS.LimitsCPU }}
              memory: {{ .ControlPlane.CoreDNS.LimitsMemory }}
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
        # #4389/#4297: the per-Org LimitRange of the time (#4292,
        # maxLimitRequestRatio {cpu:1,memory:1}; the ratio is gone since #4758)
        # ALSO applied to the vcluster control-plane StatefulSet (it runs
        # natively in the Org host ns, NOT inside the vcluster) — the asymmetric
        # 100m/2000m + 192Mi/2Gi shape was 'forbidden: cpu limit to request
        # ratio is 20' → vcluster-0 never scheduled → m-tier Orgs could not
        # provision their vcluster at all (#4297 keystone). Rendered
        # requests==limits (Guaranteed QoS) since then and kept as is: the
        # LimitRange carries no ratio today, but the shape is what the plan
        # ResourceQuota's control-plane overhead is derived from.
        # Figures come from vclusterControlPlane.Syncer — the same values the
        # plan ResourceQuota's control-plane overhead is computed from.
        resources:
          requests:
            cpu: {{ .ControlPlane.Syncer.RequestsCPU }}
            memory: {{ .ControlPlane.Syncer.RequestsMemory }}
          limits:
            cpu: {{ .ControlPlane.Syncer.LimitsCPU }}
            memory: {{ .ControlPlane.Syncer.LimitsMemory }}
        persistence:
          volumeClaim:
            size: {{ .ControlPlane.VolumeSize }}
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
// customer purchased (#4292) PLUS the vCluster control-plane overhead PLUS the
// per-Organization platform-stack overhead, both of which run in the same
// namespace (#6902 follow-ups; see vclusterControlPlaneOverhead and
// platformStack). Driven by planQuota(.PlanSlug) → planPlusOverhead.
//
// The plan term is NOT the same on both sides (#6971 overcommit model): the
// limits.* cap carries the HEADLINE the customer bought (M: 2 CPU / 4Gi), the
// requests.* cap carries the GUARANTEED share the Sovereign provisions for it
// (M: 334m / 1366Mi — headline ÷ 6 and ÷ 3). The customer's workloads may
// request up to the guaranteed share and burst up to the headline; what they
// pay for is the headline, what is reserved for them is the guarantee. The
// overheads are added on their own sides likewise (coredns and the newapi pod
// are Burstable), so the two hard caps differ by the plan's overcommit gap
// plus the overheads' request/limit gap. Flexi renders NO ResourceQuota
// (on-demand, soft cap) — the controller skips this file for Burstable plans.
//
// Overage mode (spec.commerce.overageMode, founder model 2026-10-10): in
// "capped" the limits plan term is the headline as above; in "grow" it is the
// grow ceiling (quotaLimitsFor) and the requests side does not move. The mode
// and, in grow, the ceiling are stamped as openova.io/overage-mode and
// openova.io/grow-ceiling.
//
// The split is stamped as annotations so an operator reading the LIVE object
// (`kubectl get resourcequota plan-quota -o yaml`) can reconcile the number to
// the plan without this source: annotations survive apply, the YAML comment
// block does not.
//
// 5-pillar Pillar 1: the cap the customer pays for IS the cap that
// materializes — and is usable in full, because the control plane and the
// platform stack are on top of it. This replaces the dev-tiny marketplace-api
// SizeResources + the syncer-only provisioning planLimits, both retired in
// Workstream A.
const resourceQuotaTemplate = `# The hard cap below is NOT the plan alone. It is the purchased plan PLUS the
# per-Org vCluster control plane (vcluster-0 syncer + the synced coredns) PLUS
# the per-Organization platform stack (bp-keycloak + its postgresql, bp-newapi
# + its postgresql, bp-openclaw, bp-agenity + its oidc-gate), all of which run
# in this same namespace and are charged to this quota at admission, so neither
# eats into what the customer bought. The plan term is the headline on limits
# and the guaranteed share (headline over the CPU 6x / memory 3x overcommit)
# on requests.
#   plan {{ .PlanSlug }} headline (limits): {{ .PlanCapText }}
#   plan {{ .PlanSlug }} guaranteed (requests): {{ .PlanGuaranteedText }}
#   vcluster control plane: {{ .OverheadText }}
#   per-Organization platform stack: {{ .PlatformStackText }}
#   overage mode: {{ .EffectiveOverageMode }}
{{- if .GrowCeilingText }}
#   grow ceiling (limits, in place of the headline): {{ .GrowCeilingText }}
#   usage above the headline is billed in arrears by BSS; no storage cap in
#   either mode (the platform stack's own volumes share this namespace)
{{- end }}
apiVersion: v1
kind: ResourceQuota
metadata:
  name: {{ .ResourceQuotaName }}
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/plan: {{ .PlanSlug }}
    openova.io/managed-by: catalyst
  annotations:
    openova.io/quota-formula: "purchased plan + vcluster control plane + per-Organization platform stack"
    openova.io/plan-cap: {{ .PlanCapText | quote }}
    openova.io/plan-guaranteed-cpu: {{ .Quota.CPURequest | quote }}
    openova.io/plan-guaranteed-memory: {{ .Quota.MemRequest | quote }}
    openova.io/plan-overcommit-cpu: "{{ .OvercommitCPU }}"
    openova.io/plan-overcommit-memory: "{{ .OvercommitMemory }}"
    openova.io/vcluster-control-plane-overhead: {{ .OverheadText | quote }}
    openova.io/platform-stack-overhead: {{ .PlatformStackText | quote }}
    openova.io/overage-mode: {{ .EffectiveOverageMode | quote }}
{{- if .GrowCeilingText }}
    openova.io/grow-ceiling: {{ .GrowCeilingText | quote }}
{{- end }}
spec:
  hard:
    requests.cpu: "{{ .Hard.RequestsCPU }}"
    requests.memory: "{{ .Hard.RequestsMemory }}"
    limits.cpu: "{{ .Hard.LimitsCPU }}"
    limits.memory: "{{ .Hard.LimitsMemory }}"
`

// limitRangeTemplate seeds defaultRequest/default so pods authored without
// explicit requests/limits still ADMIT once a ResourceQuota exists (a quota
// rejects any pod missing the limited resources).
//
// The defaults carry the plan's overcommit shape (#6971): the default LIMIT
// is the headline / 8, the default REQUEST is that over the workbook's ratio
// (CPU ÷ 6, memory ÷ 3) — limitRangeDefaults. A container authored without
// resources is therefore admitted requests<limits, Burstable, at the same
// guaranteed:headline proportion the plan itself has; an Application that
// needs more sets its own explicit requests, up to the ResourceQuota's caps.
// Until #6971 the two defaults were EQUAL (plan / 8 on both), which made every
// defaulted container Guaranteed and reserved the whole headline on the node;
// the headline is the cap the customer pays for, the guarantee is what the
// Sovereign provisions, and the defaults now say so.
//
// The overcommit ratios are stamped as annotations (openova.io/plan-
// overcommit-cpu / -memory) with the headline and the guarantee, so the live
// object states the model the defaults were derived from. They are NOT
// enforced as maxLimitRequestRatio — see the note inside the template.
const limitRangeTemplate = `apiVersion: v1
kind: LimitRange
metadata:
  name: {{ .LimitRangeName }}
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    openova.io/plan: {{ .PlanSlug }}
    openova.io/managed-by: catalyst
  annotations:
    openova.io/plan-cap: {{ .PlanCapText | quote }}
    openova.io/plan-guaranteed-cpu: {{ .Quota.CPURequest | quote }}
    openova.io/plan-guaranteed-memory: {{ .Quota.MemRequest | quote }}
    openova.io/plan-overcommit-cpu: "{{ .OvercommitCPU }}"
    openova.io/plan-overcommit-memory: "{{ .OvercommitMemory }}"
{{- if .EffectiveOverageMode }}
    openova.io/overage-mode: {{ .EffectiveOverageMode | quote }}
{{- end }}
{{- if .GrowCeilingText }}
    openova.io/grow-ceiling: {{ .GrowCeilingText | quote }}
{{- end }}
spec:
  limits:
    - type: Container
      defaultRequest:
        cpu: "{{ .Defaults.RequestsCPU }}"
        memory: "{{ .Defaults.RequestsMemory }}"
      default:
        cpu: "{{ .Defaults.LimitsCPU }}"
        memory: "{{ .Defaults.LimitsMemory }}"
{{- /*
  NO maxLimitRequestRatio here — not 1 (#4758) and not the plan's 6 / 3
  (#6971) either. This template renders the HOST ns of a per-Org vCluster, and
  that namespace holds pods this plan does not size: the vcluster syncer
  reflects the vcluster's own bundled system pods into it (coredns: 20m
  requests / 1000m limits, ratio 50:1 cpu, 2.66:1 mem) and the per-Organization
  platform stack runs here too (the newapi sandbox-bridge and metering
  sidecars at 20:1, its wait-for-sql-dsn init at 10:1, agenity's creds-resync
  at 10:1, newapi memory at 4:1 — platformStack). A LimitRange ratio applies to
  EVERY container admitted into the namespace, so any ratio tighter than 50:1
  forbids those pods at admission → coredns Pending → the vcluster runs
  nothing → the customer's first app (WordPress) 404s. Live-confirmed on hw223
  uatwalk223 with ratio 1 (syncer: "forbidden: cpu max limit to request ratio
  ... is 1, but provided 50"); the same admission rule would reject the same
  pods at 6. The plan's overcommit is therefore carried where it can be
  enforced without touching those pods — the ResourceQuota (guaranteed share on
  requests.*, headline on limits.*) and the defaults above — and stated on this
  object as annotations. The defaultRequest/default stay (they satisfy the
  ResourceQuota's "limited resource must be set" admission rule without
  imposing a ratio).
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
// follow.
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
// default-deny K8s NetworkPolicy baseline — a SYNCABLE object: the apps
// Kustomization carries spec.kubeConfig so this lands in the vcluster apiserver
// and the syncer reflects it to the host `<slug>` ns. The CNP is DELIBERATELY NOT here
// (#4475 §1): a CiliumNetworkPolicy cannot apply into the CRD-less vcluster
// apiserver — it lives in the host-apps/ tree instead. The funnel's app-install
// tree (a DIFFERENT repo) carries the customer's purchased Applications. Keeping
// an explicit index here makes `kustomize build ./vcluster/apps` deterministic.
// It ranges over renderView.AppsResources: networkpolicy.yaml plus
// namespace.yaml (#4991 — the kubeConfig-targeted apps Kustomization applies
// INTO the Org vcluster with targetNamespace=<slug>, but NOTHING else creates
// that namespace INSIDE the vcluster, so Flux failed `namespaces "<slug>" not
// found` and the customer's app never deployed).
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
	// Quota is the resolved plan cap (planQuota(.PlanSlug)) — what the
	// customer purchased.
	Quota PlanQuota
	// Hard is the ResourceQuota hard cap: Quota plus the vCluster
	// control-plane overhead plus the per-Organization platform-stack
	// overhead, per resource (planPlusOverhead).
	Hard quotaHard
	// ControlPlane is the vCluster control-plane shape the HelmRelease
	// template interpolates (vclusterControlPlane) — the same values Hard's
	// overhead was computed from.
	ControlPlane vclusterControlPlaneShape
	// PlanCapText / PlanGuaranteedText / OverheadText / PlatformStackText are
	// the human-readable terms of the split, stamped as annotations on the
	// ResourceQuota: the headline (limits), the guaranteed share (requests),
	// and the two overheads.
	PlanCapText        string
	PlanGuaranteedText string
	OverheadText       string
	PlatformStackText  string
	// OvercommitCPU / OvercommitMemory are the workbook ratios (headline over
	// guaranteed), stamped on the ResourceQuota and the LimitRange.
	OvercommitCPU    int
	OvercommitMemory int
	// Defaults are the LimitRange per-container defaults (limitRangeDefaults):
	// default limit = headline / 8, default request = that over the overcommit
	// ratio for fixed tiers; a small fixed floor for Flexi which has no
	// ceiling. Derived from the HEADLINE in both overage modes.
	Defaults containerShape
	// EffectiveOverageMode is the overage mode the quota was sized with —
	// "capped" or "grow" for a fixed tier, stamped as openova.io/overage-mode
	// on the ResourceQuota and the LimitRange; empty for Burstable plans,
	// which carry no quota and so no mode annotation.
	EffectiveOverageMode string
	// GrowCeilingText is the clamped grow ceiling ("cpu=8 memory=16Gi"),
	// stamped as openova.io/grow-ceiling; empty unless the mode is grow.
	GrowCeilingText string
	// AppNamespace is the in-vcluster namespace the funnel installs the
	// customer's Applications into (= "apps", matching the provisioning
	// funnel's appNS). The default-deny + same-Org NetworkPolicy targets it
	// so the syncer reflects it to the host `<slug>` ns.
	AppNamespace string
	// KustomizeResources is the file list the boundary kustomization.yaml
	// references — namespace.yaml, limitrange.yaml and vcluster.yaml always;
	// resourcequota.yaml skipped for soft-cap Flexi.
	KustomizeResources []string
	// AppsResources is the file list vcluster/apps/kustomization.yaml references
	// (#4991) — networkpolicy.yaml plus namespace.yaml (the kubeConfig-targeted
	// apps Kustomization needs the target ns created INSIDE the vcluster).
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

// limitRangeDefaults derives the per-container defaults the LimitRange seeds
// into a container authored without resources, as one containerShape:
//
//   - default LIMIT   = the plan headline / 8 (so ~8 unspecified small
//     containers fit under the headline before any Application sets explicit
//     resources) — S 125m / 256Mi, M 250m / 512Mi, L 500m / 1Gi, XL 1 / 2Gi;
//   - default REQUEST = that limit over the overcommit ratio (CPU ÷ 6,
//     memory ÷ 3), rounded UP so limit/request never exceeds the ratio —
//     S 21m / 86Mi, M 42m / 171Mi, L 84m / 342Mi, XL 167m / 683Mi.
//
// The request:limit shape of a defaulted container therefore mirrors the
// plan's own guaranteed:headline shape. For Flexi (no ceiling) both are a
// small fixed floor; the same numbers are used for an unknown slug, which
// planQuota already resolved to "s".
func limitRangeDefaults(q PlanQuota) containerShape {
	if q.Burstable {
		return containerShape{RequestsCPU: "100m", RequestsMemory: "128Mi", LimitsCPU: "100m", LimitsMemory: "128Mi"}
	}
	limCPU := mustQuantity(q.CPULimit)
	limMem := mustQuantity(q.MemLimit)
	// Headline / 8, in exact millicores and MiB (every headline is a whole
	// number of CPUs and a power-of-two GiB, so neither division truncates).
	defCPU := resource.NewMilliQuantity(limCPU.MilliValue()/8, resource.DecimalSI)
	defMem := resource.NewQuantity(limMem.Value()/8, resource.BinarySI)
	return containerShape{
		RequestsCPU:    ceilDiv(defCPU.MilliValue(), planOvercommitCPU, "m"),
		RequestsMemory: ceilDiv(defMem.Value()/mi, planOvercommitMemory, "Mi"),
		LimitsCPU:      defCPU.String(),
		LimitsMemory:   defMem.String(),
	}
}

// mi is one MiB in bytes, for memory arithmetic in whole MiB.
const mi = 1024 * 1024

// ceilDiv divides n by d rounding UP and renders it with the unit suffix the
// LimitRange carries ("m" millicores, "Mi" MiB). Rounding up is load-bearing:
// a request rounded down would put the container's limit/request ratio a
// hair ABOVE the overcommit ratio the LimitRange annotations state.
func ceilDiv(n, d int64, unit string) string {
	return fmt.Sprintf("%d%s", (n+d-1)/d, unit)
}

// Render returns the rendered (path, bytes) tuples the controller
// writes into the per-Org Gitea repo.
//
// The rendered set is the SAME for every Organization; only the plan-sized
// numbers differ (Workstream B #4292 sizing; one boundary primitive per the
// founder's 2026-09-10 direction — see the note above planQuota):
//   - vcluster.yaml is the dedicated Org vCluster HelmRelease, emitted for
//     EVERY plan slug. It deploys into the host `<slug>` ns, so the plan cap
//     below applies to it exactly as it applies to the Org's own pods.
//   - resourcequota.yaml + limitrange.yaml cap the host ns at the purchased
//     plan PLUS the vCluster control-plane overhead PLUS the per-Organization
//     platform-stack overhead that share the namespace
//     (vclusterControlPlaneOverhead, platformStack; skipped ResourceQuota for
//     soft-cap Flexi; LimitRange always, its per-container defaults plan-only).
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
	view := renderView{
		Inputs:            in,
		Quota:             quota,
		ControlPlane:      vclusterControlPlane,
		OverheadText:      vclusterControlPlaneOverhead.String(),
		OvercommitCPU:     planOvercommitCPU,
		OvercommitMemory:  planOvercommitMemory,
		Defaults:          limitRangeDefaults(quota),
		AppNamespace:      "apps",
		ResourceQuotaName: BoundaryResourceQuotaName,
		LimitRangeName:    BoundaryLimitRangeName,
	}
	// The hard cap = plan term + control-plane overhead + platform-stack
	// overhead, the plan term being the guaranteed share on requests and the
	// headline on limits. Only for hard-capped plans: Flexi has no plan figure
	// to add to (and renders no quota). The stack term is per plan because a
	// chart-unsized container takes this plan's LimitRange defaults.
	if PlanRendersResourceQuota(in.PlanSlug) {
		ps := platformStackOverheadFor(quota)
		// Overage mode. capped: the limits plan term is the headline — the
		// table's own strings, so the hard cap is byte-identical to the
		// render before the mode existed. grow: the limits plan term is the
		// grow ceiling (default the XL headline; clamped to [plan headline,
		// XL headline] — see quotaLimitsFor). Either way the requests plan
		// term stays the guaranteed share (headline ÷ overcommit), and both
		// overheads are added exactly as before; the platform-stack term is
		// still sized from the HEADLINE (its chart-unsized containers take
		// the LimitRange defaults, which stay headline-derived in both
		// modes).
		//
		// Storage: NO requests.storage in either mode, deliberately. The
		// per-Organization platform stack's PVCs (the keycloak and newapi
		// postgresql volumes) share this namespace with the customer's own
		// and are not sized in this controller, so a storage cap
		// would refuse the Organization's own databases (see "Storage is
		// not counted" above platformStackWorkload). Disk above the package
		// is metered by BSS from the PVC meter and billed in arrears; the
		// growCeiling.diskGB / bandwidthMbps values are BSS's, not this
		// quota's.
		limCPU, limMem, grow := quotaLimitsFor(quota, in.OverageMode, in.GrowCeilingCPU, in.GrowCeilingMemory)
		limitsQuota := quota
		limitsQuota.CPULimit, limitsQuota.MemLimit = limCPU, limMem
		view.Hard = planPlusOverhead(limitsQuota, vclusterControlPlaneOverhead, ps)
		view.EffectiveOverageMode = OverageModeCapped
		if grow {
			view.EffectiveOverageMode = OverageModeGrow
			view.GrowCeilingText = fmt.Sprintf("cpu=%s memory=%s", limCPU, limMem)
		}
		view.PlanCapText = fmt.Sprintf("cpu=%s memory=%s", quota.CPULimit, quota.MemLimit)
		view.PlanGuaranteedText = fmt.Sprintf("cpu=%s memory=%s", quota.CPURequest, quota.MemRequest)
		view.PlatformStackText = ps.String()
	}

	// Assemble the file set. The boundary host namespace, its plan-templated
	// quota/LimitRange and the vCluster HelmRelease always render; the
	// ResourceQuota only for hard-capped plans (Flexi is soft/on-demand).
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
	files["vcluster/vcluster.yaml"] = vclusterTemplate
	res = append(res, "vcluster.yaml")
	// The default-deny + same-Org-allow baseline lives in the apps tree so
	// the syncer (sync.toHost.networkPolicies.enabled) reflects it to the
	// host `<slug>` ns. It is NOT listed in the boundary kustomization `res`
	// — it is a SEPARATE path under apps/ reconciled by its OWN per-Org Flux
	// Kustomization (catalyst-tenant-<slug>-apps, per_org_flux.go), which
	// carries spec.kubeConfig so the NP lands in the vcluster apiserver. #4293
	// MAJOR-2 closed the gap where this NP was committed to a path NO
	// Kustomization referenced (the boundary kustomization omits apps/, and
	// the funnel apps-sync reads a DIFFERENT repo) → intra-Org isolation
	// stayed inert.
	files["vcluster/apps/"+networkPolicyDoc] = networkPolicyTemplate
	// The reserved-entity CNP companion (gateway ingress + apiserver egress) lives
	// in a SEPARATE host-applied tree (#4475 §1). A CiliumNetworkPolicy CANNOT be
	// applied into the CRD-less vcluster apiserver — the kustomize-controller
	// dry-run rejects `cilium.io/v2` ("no matches for kind CiliumNetworkPolicy")
	// and WEDGES the whole kubeConfig-targeted apps Kustomization (taking the K8s
	// NPs and every day-2 Application install down). It is also NOT a syncable
	// workload object. So the org-controller's host-apps Flux Kustomization
	// (catalyst-tenant-<slug>-host-apps, per_org_flux.go) reconciles this tree
	// ALWAYS host-side onto the `<slug>` ns — where Cilium's CRD lives and where
	// the syncer reflects the Org's vcluster pods. Without the CNP the K8s
	// default-deny silently
	// 503s the Org's Application behind the Cilium Gateway and blocks egress to the
	// cluster API (neither reachable via any K8s NP selector).
	files["vcluster/host-apps/"+ciliumNetworkPolicyDoc] = ciliumNetworkPolicyTemplate
	// #4991 — the per-Org provisioning-tenant Role+RoleBinding, delivered by the
	// boundary owner into the ALWAYS-host-applied host-apps tree so the
	// org-services/provisioning SA can create the kubeconfig mirror Secret +
	// bounce vcluster-0 in `<slug>` BEFORE the funnel's mirror step 403s and
	// aborts the whole provision.
	files["vcluster/host-apps/"+provisioningRBACDoc] = provisioningRBACTemplate
	// #4991 — the kubeConfig-targeted apps Kustomization applies INTO the Org
	// vcluster with targetNamespace=<slug>; create that namespace INSIDE the
	// vcluster (nothing else does → `namespaces "<slug>" not found`).
	files["vcluster/apps/"+appsNamespaceDoc] = appsNamespaceTemplate
	appsResources := []string{networkPolicyDoc, appsNamespaceDoc}
	// Explicit apps/kustomization.yaml so the per-Org apps Flux Kustomization's
	// `kustomize build ./vcluster/apps` enumerates the K8s NP + the in-vcluster
	// target namespace deterministically.
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
