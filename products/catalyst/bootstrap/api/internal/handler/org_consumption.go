// org_consumption.go — the B3 metering feed (issue #3378 §6 B3, made
// per-Organization-honest by #3687 fold #3677).
//
// Per-org consumption aggregation, labeled by org, exposed via ONE GET
// the Organizations billing pages read. Attribution keys on the single
// `openova.io/organization` join key (ARCHITECTURE.md:306) stamped on
// every per-Org namespace by the organization-controller
// (gitops/manifests.go) — NOT a hardcoded constant. A pod whose
// namespace carries no Org label (host/control-plane namespaces) and
// every one-shot `Job`-owned pod (cutover / scan / snapshot) rolls up
// into a single, visually-distinct "Platform overhead" line rather than
// being mis-attributed to a tenant. On a Sovereign with zero customer
// Organizations the only tenant-ish row is that Platform-overhead
// rollup under the parent; the moment the first per-Org namespace
// appears with the label, a second org row materializes with no code
// change — the #3687 generality proof (DoD 6 + §7c).
//
// Meter source — the lean kube-metrics aggregation (the §6 B3 alternative,
// "no new component"): this REUSES the existing per-namespace per-pod
// resource-request rows the dashboard treemap already computes
// (buildPodRows in dashboard.go), so no bp-openmeter kit slot, no new
// Blueprint, no placement.yaml row. The cost model is transparent +
// CPU/memory/storage-request weighted (showback = attributed
// consumption, not a billed invoice).
//
// Route (registered in cmd/api/main.go), session-gated like the rest of
// /api/v1/*:
//
//   GET /api/v1/org/consumption  → SovereignConsumptionResponse
//
// This is NOT a marketplace funnel surface — it works day one on the
// operator's own estate, which is the whole point of showback (§5).

package handler

import (
	"net/http"
	"os"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// orgConsumption is one org's consumption rollup. The parent estate row
// (isParent=true) is always present and ordered first; customer
// Organizations follow, attributed via the `openova.io/organization`
// join key; the synthetic platform-overhead row (isPlatform=true) carries
// all control-plane + one-shot-Job consumption and is ordered last so the
// console can render it as a single visually-distinct line (#3687 fold
// #3677 §6).
type orgConsumption struct {
	Org      string `json:"org"`
	IsParent bool   `json:"isParent"`
	// IsPlatform flags the synthetic "Platform overhead" rollup — the
	// control-plane / infra / one-shot-Job consumption that is NOT a
	// tenant. The console renders this as one distinct line, never
	// itemized inside a tenant's app list.
	IsPlatform bool `json:"isPlatform"`
	// IsUnowned flags the synthetic "Unowned namespaces" rollup — real
	// consumption carrying an `openova.io/organization` label whose slug
	// has NO Organization CR. It is neither an Organization nor platform
	// overhead: nothing is billed to an Organization, but the units are
	// still counted into TotalCostUnits and the orphaned slugs are named
	// in UnownedOrgs so the estate total stays honest and the orphan is
	// visible rather than silent (UAT row 25(c), #6114).
	IsUnowned bool `json:"isUnowned"`
	// CostUnits — the showback cost in abstract attribution units (the
	// weighted CPU+memory+storage sum). Showback attributes consumption;
	// it is not a billed currency amount.
	CostUnits  float64 `json:"costUnits"`
	CPUMilli   float64 `json:"cpuMilli"`
	MemoryGiB  float64 `json:"memoryGiB"`
	StorageGiB float64 `json:"storageGiB"`
	// Apps — per-application breakdown within the org (DoD 3: "per-app
	// cost attribution").
	Apps []appConsumption `json:"apps"`
}

// appConsumption is one application's slice within an org.
type appConsumption struct {
	Application string  `json:"application"`
	Namespace   string  `json:"namespace"`
	CostUnits   float64 `json:"costUnits"`
	CPUMilli    float64 `json:"cpuMilli"`
	MemoryGiB   float64 `json:"memoryGiB"`
	StorageGiB  float64 `json:"storageGiB"`
	Percent     float64 `json:"percent"` // share of the org's total cost
}

// SovereignConsumptionResponse is the wire shape the billing pages read.
type SovereignConsumptionResponse struct {
	// TotalCostUnits — the Sovereign-wide showback total.
	TotalCostUnits float64 `json:"totalCostUnits"`
	// Orgs — one rollup per org, parent first. Empty estate ⇒ a single
	// parent row with zeros (never null — the page renders its chrome).
	Orgs []orgConsumption `json:"orgs"`
	// UnownedOrgs — the `openova.io/organization` label values that drew
	// consumption but have no Organization CR, sorted. Empty (never null)
	// when the object model contains everything the estate bills, which
	// is the state UAT row 25 asserts. Naming them here is the whole
	// point: the alternative to billing a phantom Organization is NOT
	// dropping its consumption silently, it is saying which slug is
	// orphaned so the sovereign-admin can reconcile or reap it.
	UnownedOrgs []string `json:"unownedOrgs"`
	// Pending — true when the metrics cache wasn't available so the page
	// can flag "metering warming up" instead of asserting a false zero.
	Pending bool `json:"pending"`
}

// showback cost weights — transparent + stable. CPU is the scarcest
// resource on these clusters, so a millicore weighs more than a MiB; the
// exact constants matter less than that the same weights apply to every
// org so the per-app share (Percent) is meaningful (§2.6 showback).
const (
	weightCPUPerMilli   = 1.0     // per millicore of request
	weightMemPerGiB     = 4.0     // per GiB of memory request
	weightStoragePerGiB = 0.25    // per GiB of storage request
	bytesPerGiB         = 1 << 30 // 1 GiB
)

// platformOrg is the synthetic bucket every control-plane / infra /
// one-shot-Job pod rolls into. It is distinct from any real tenant org
// and from the parent estate so the console can render it as a single
// "Platform overhead" line, never itemized as `scan-vulnerabilityreport-…`
// inside a tenant's app list (#3687 fold #3677 §6). The double-underscore
// guards against collision with a real slug (slugs match
// [a-z][a-z0-9-]{2,30}).
const platformOrg = "__platform__"

// unownedOrg is the synthetic bucket for consumption whose namespace
// carries an `openova.io/organization` label that NO Organization CR
// claims. It exists because the label alone used to be the whole join:
// orgForRow returned any non-empty label value verbatim, so a namespace
// that outlived (or never had) its CR produced a full customer
// Organization row and accrued showback against an entity the control
// plane does not model. Measured on hw293: `g7doora` had no Organization
// CR — `kubectl get organizations` listed hw293vch, hw293walkone,
// hw293walktwo, uat107org, uat107vc and not g7doora — yet it drew the
// LARGEST customer slice on the Sovereign, 4272.25 units across
// bp-keycloak, bp-agenity, bp-stalwart-tenant, bp-newapi, bp-openclaw and
// bp-wordpress, because its namespace kept converging after the Org
// create failed. That is UAT row 25's set-agreement conjunct failing:
// the billable set and the Organization set disagreed.
//
// Why a distinct bucket and not one of the three obvious alternatives:
//
//   - NOT folded into platformOrg. "Platform overhead" is a claim that
//     the consumption is the control plane's own, and a half-torn-down
//     customer namespace is not that. Folding it there re-hides the
//     orphan behind a line the sovereign-admin is trained to ignore —
//     silent again, just in a different column.
//   - NOT dropped. Dropping the rows would make TotalCostUnits understate
//     the estate by whatever the orphan actually runs (4272 units on
//     hw293) and leave no surface on which the orphan is visible. The
//     units are real; only their attribution was wrong.
//   - NOT reaped. Reaping the namespace is destructive and belongs to the
//     organization-controller's finalizer cascade, not to a read-only
//     showback aggregation. A namespace legitimately outlives its CR for
//     the duration of a teardown, so no predicate this handler can
//     evaluate from a cache read distinguishes "orphan" from "mid-delete"
//     safely enough to justify deletion.
//
// The double-underscore guards against collision with a real slug
// (slugs match [a-z][a-z0-9-]{2,30}), same convention as platformOrg.
const unownedOrg = "__unowned__"

// ephemeralApp is the single app row every one-shot Job pod folds into
// inside the platform-overhead org. Before #5485 each Job pod produced
// its own `appConsumption` row, so the Application table itemized
// `cutover-harbor-prewarm-1785340840`, `cutover-gitea-mirror-…`,
// `openbao-snapshot-save-29755690` — the exact "render as a single
// Platform overhead line" contract this file's header states it honors.
// Their cost is NOT dropped (it is real consumption of the estate, and
// the platform row's totals still include it) — only the itemization
// collapses. Ephemeral classification is isEphemeralRow in dashboard.go,
// shared with the treemap so the two surfaces cannot drift.
//
// The parenthesized form matches the existing "(unlabeled)" convention
// for synthetic rows and cannot collide with a real workload name
// (K8s names are DNS labels — no spaces, no parentheses).
const ephemeralApp = "(one-shot jobs)"

// ephemeralMixedNamespace is the namespace shown for the collapsed
// one-shot row when its pods span more than one namespace (cutover Jobs
// in catalyst-system, snapshot Jobs in openbao, scan Jobs in
// trivy-system). A single-namespace collapse keeps the real namespace so
// the operator still sees where the activity ran.
const ephemeralMixedNamespace = "(various)"

// defaultInfraNamespaces is the baked-in default set of host /
// control-plane namespaces whose pods are NEVER tenant consumption. It is
// the floor, not the law: SHOWBACK_INFRA_NAMESPACES (comma-separated)
// extends it per Sovereign (Principle #4 — config, not a frozen literal).
// Pods in these namespaces roll into the platformOrg bucket regardless of
// any Org label, because the join key only becomes meaningful once a real
// per-Org namespace exists.
var defaultInfraNamespaces = []string{
	"kube-system",
	"flux-system",
	"trivy-system",
	"kyverno",
	"cnpg",
	"falco",
	"cert-manager",
	"external-secrets-system",
	"sigstore-system",
	"monitoring",
	"observability",
	"cilium-secrets",
	"gateway-system",
	"catalyst",
	"catalyst-system",
	"shared-data",
	"dmz",
	"rtz",
	"mgmt",
}

// infraNamespaceSet builds the effective infra-namespace exclusion set:
// the baked-in default floor merged with the SHOWBACK_INFRA_NAMESPACES
// env override. Returned as a set for O(1) membership. Exported-to-test
// via the explicit-set overload on aggregateConsumption so the unit test
// drives a deterministic set with no env dependency.
func infraNamespaceSet(envValue string) map[string]struct{} {
	set := make(map[string]struct{}, len(defaultInfraNamespaces)+8)
	for _, ns := range defaultInfraNamespaces {
		set[ns] = struct{}{}
	}
	for _, ns := range strings.Split(envValue, ",") {
		ns = strings.TrimSpace(ns)
		if ns != "" {
			set[ns] = struct{}{}
		}
	}
	return set
}

// HandleSovereignConsumption — GET /api/v1/org/consumption.
func (h *Handler) HandleSovereignConsumption(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	depID := strings.TrimSpace(q.Get("deployment_id"))
	clusterID := depID
	if h.k8sCache != nil {
		clusterID = h.resolveChrootClusterID(clusterID)
	}

	// Resolve the parent org name = the Sovereign FQDN (the parent org IS
	// the Sovereign, §2.2). Falls back to "sovereign" when unresolved.
	parentOrg := h.consumptionParentOrg(depID)

	// No cache ⇒ return the parent-only empty rollup flagged pending so
	// the page renders showback chrome (never a hard error). This is the
	// §5 "never blank" rule applied to the metering feed.
	if clusterID == "" || h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, SovereignConsumptionResponse{
			Orgs:        []orgConsumption{{Org: parentOrg, IsParent: true, Apps: []appConsumption{}}},
			UnownedOrgs: []string{},
			Pending:     true,
		})
		return
	}

	pods, _, _ := h.k8sCache.List(clusterID, "pod", labels.Everything())
	pvcs, _, _ := h.k8sCache.List(clusterID, "persistentvolumeclaim", labels.Everything())
	podMetrics, _, _ := h.k8sCache.List(clusterID, "podmetrics", labels.Everything())
	namespaces, _, _ := h.k8sCache.List(clusterID, "namespace", labels.Everything())
	nodes, _, _ := h.k8sCache.List(clusterID, "node", labels.Everything())
	// ReplicaSets: the Deployment→Pod ownerRef hop. Without them the
	// per-app breakdown lists hash-suffixed ReplicaSet names instead of
	// the Deployment that owns them (#5485 defect B).
	replicaSets, _, _ := h.k8sCache.List(clusterID, "replicaset", labels.Everything())

	rows := buildPodRows(pods, pvcs, podMetrics, namespaces, nodes, replicaSets, clusterID, "")

	resp := aggregateConsumption(rows, parentOrg,
		infraNamespaceSet(os.Getenv("SHOWBACK_INFRA_NAMESPACES")),
		h.knownOrganizationSlugs(clusterID))
	writeJSON(w, http.StatusOK, resp)
}

// knownOrganizationSlugs reads the Organization CR set for a cluster —
// the object model showback's billable set must be a function of (#6114,
// UAT row 25(c)).
//
// `organization` is already a registered k8scache kind
// (k8scache/kinds.go: orgs.openova.io/v1 organizations, cluster-scoped)
// and the catalyst-api-cutover-driver ClusterRole already grants read on
// it, so this join adds no informer, no apiserver round-trip and no RBAC
// rule — it reads the same warm indexer the /organizations page does.
//
// Returns nil — meaning "the Organization set is UNKNOWN", never "the
// Organization set is empty" — when the cache cannot vouch for it:
//
//   - no cache / no cluster id (CI, pre-handover mothership),
//   - List error (cluster unregistered, or the Organization CRD is not
//     served on this cluster),
//   - zero Organizations returned.
//
// The zero case is folded into "unknown" on purpose. A cold or
// not-yet-synced informer returns an empty list with no error, and it is
// indistinguishable at this seam from a genuinely Org-less Sovereign — but
// the two states are NOT equally costly to get wrong. Treating an unsynced
// read as authoritative would move every labelled namespace on the estate
// into the unowned rollup at once and bill nobody, which is the
// over-correction this fix must not make. Treating a genuinely Org-less
// Sovereign as unknown costs nothing, because a Sovereign with no
// Organization CRs also has no organization-controller-stamped namespaces
// to mis-attribute: the label and the CR come from the same producer
// (core/controllers/organization/internal/gitops/manifests.go stamps
// `openova.io/organization: {{ .Slug }}`).
func (h *Handler) knownOrganizationSlugs(clusterID string) map[string]struct{} {
	if h == nil || h.k8sCache == nil || clusterID == "" {
		return nil
	}
	orgs, _, err := h.k8sCache.List(clusterID, "organization", labels.Everything())
	if err != nil || len(orgs) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(orgs))
	for _, o := range orgs {
		if o == nil {
			continue
		}
		// spec.slug is the value the org-controller stamps as the
		// namespace label; metadata.name is set to the same slug by every
		// producer (org_tenant_org_cr.go ensureOrganizationCR, the
		// provisioning-service's createOrganizationCR). Accept both so a
		// CR authored by a future producer that diverges on one of the two
		// still counts as owning its namespaces — the failure mode this
		// guards against is a REAL Organization being called unowned.
		if slug, _, _ := unstructured.NestedString(o.Object, "spec", "slug"); strings.TrimSpace(slug) != "" {
			set[strings.TrimSpace(slug)] = struct{}{}
		}
		if name := strings.TrimSpace(o.GetName()); name != "" {
			set[name] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

// aggregateConsumption rolls podRows up into the per-org / per-app
// showback shape (#3687 fold #3677). Attribution keys on the pod's real
// `openova.io/organization` namespace label (resolved in buildPodRows
// into podRow.org). A pod is rolled into the synthetic platformOrg
// bucket — never a tenant — when ANY of these hold:
//   - its namespace is in the infra-exclusion set (host / control-plane),
//   - it is owned by a batch/v1 Job (one-shot cutover / scan / snapshot),
//   - or its namespace carries no Org label at all.
// Real per-Org namespaces (stamped by the organization-controller) yield
// their own org row immediately, with no per-name special-casing — adding
// a third org needs zero code change (the generality proof §7c).
//
// knownOrgs is the Organization CR set (nil = not read; see orgForRow).
// It closes UAT row 25's set-agreement conjunct in BOTH directions:
// a labelled namespace with no CR lands in unownedOrg instead of minting a
// phantom billable Organization, and every CR gets a row even with nothing
// running. The Organization rows the console bills — those with
// !IsParent && !IsPlatform && !IsUnowned — are then exactly the
// Organization set, which is the invariant the row asserts.
//
// Inside the platform bucket the one-shot Job pods further collapse into
// a SINGLE `(one-shot jobs)` app row (#5485 defect A) instead of one row
// per Job pod. Their cost still counts toward the platform row's totals
// and the Sovereign-wide total — only the per-app itemization folds, so
// the table shows durable platform workloads plus one activity line.
func aggregateConsumption(rows []podRow, parentOrg string, infraNamespaces map[string]struct{}, knownOrgs map[string]struct{}) SovereignConsumptionResponse {
	// org → namespace+app key → app rollup.
	type appKey struct{ org, ns, app string }
	appAgg := map[appKey]*appConsumption{}
	orgAgg := map[string]*orgConsumption{}

	ensureOrg := func(org string) *orgConsumption {
		oc, ok := orgAgg[org]
		if !ok {
			oc = &orgConsumption{
				Org:        org,
				IsParent:   org == parentOrg,
				IsPlatform: org == platformOrg,
				IsUnowned:  org == unownedOrg,
				Apps:       []appConsumption{},
			}
			orgAgg[org] = oc
		}
		return oc
	}
	// Always seed the parent so the estate is never blank (§5).
	ensureOrg(parentOrg)
	// #6114, the INVERSE of the phantom-org defect and the other half of
	// row 25's set-agreement conjunct: seed a row for every Organization
	// the CR set holds, so an Org that exists but currently runs nothing
	// renders a zero slice instead of vanishing from the feed entirely.
	//
	// Rows only ever materialised from pod rows before this, so an
	// Organization's presence in showback tracked whether it happened to
	// have a running pod, not whether it existed. hw293 measured exactly
	// that asymmetry: uat107org and its twin uat107vc were created and
	// deleted the same way on the same day, both reading state deleted,
	// and uat107vc carried a 525.5-unit slice purely because its pods had
	// not finished terminating while uat107org drew no slice at all.
	// Same disposition, different treatment — a join defect cutting the
	// opposite way from g7doora's.
	for slug := range knownOrgs {
		ensureOrg(slug)
	}

	var total float64
	unowned := []string{}
	for _, row := range rows {
		org := orgForRow(row, infraNamespaces, knownOrgs, parentOrg)
		if org == unownedOrg {
			// Record the label value that had no CR behind it. This is the
			// slug the sovereign-admin needs in order to act; the bucket
			// name alone would say "something is orphaned" without saying
			// what, which is the silence this fix exists to break.
			if slug := strings.TrimSpace(row.org); slug != "" && !containsString(unowned, slug) {
				unowned = append(unowned, slug)
			}
		}
		cpuMilli := row.cpuReq
		memGiB := row.memReq / bytesPerGiB
		storageGiB := row.storageLim / bytesPerGiB
		cost := cpuMilli*weightCPUPerMilli + memGiB*weightMemPerGiB + storageGiB*weightStoragePerGiB

		oc := ensureOrg(org)
		oc.CostUnits += cost
		oc.CPUMilli += cpuMilli
		oc.MemoryGiB += memGiB
		oc.StorageGiB += storageGiB
		total += cost

		app := row.application
		if app == "" {
			app = "(unlabeled)"
		}
		k := appKey{org: org, ns: row.namespace, app: app}
		// #5485 defect A: one-shot Job pods collapse into ONE row for the
		// whole platform bucket — same ephemeral predicate the treemap
		// filters on (isEphemeralRow). Keyed with an empty namespace so
		// Jobs from different namespaces fold together; the displayed
		// Namespace is resolved below.
		if isEphemeralRow(row) {
			k = appKey{org: org, ns: "", app: ephemeralApp}
		}
		ac, ok := appAgg[k]
		if !ok {
			ac = &appConsumption{Application: app, Namespace: row.namespace}
			if isEphemeralRow(row) {
				ac.Application = ephemeralApp
			}
			appAgg[k] = ac
		} else if ac.Namespace != row.namespace {
			// Only reachable for the collapsed one-shot row (every other
			// key pins the namespace): its pods span >1 namespace.
			ac.Namespace = ephemeralMixedNamespace
		}
		ac.CostUnits += cost
		ac.CPUMilli += cpuMilli
		ac.MemoryGiB += memGiB
		ac.StorageGiB += storageGiB
	}

	// Attach apps to their org + compute per-app percent share.
	for k, ac := range appAgg {
		oc := orgAgg[k.org]
		if oc == nil {
			continue
		}
		if oc.CostUnits > 0 {
			ac.Percent = roundPct(ac.CostUnits / oc.CostUnits * 100)
		}
		oc.Apps = append(oc.Apps, *ac)
	}

	// Stable ordering: parent first, then orgs by descending cost; apps
	// within an org by descending cost.
	orgs := make([]orgConsumption, 0, len(orgAgg))
	sort.Strings(unowned)
	for _, oc := range orgAgg {
		sort.Slice(oc.Apps, func(i, j int) bool {
			return oc.Apps[i].CostUnits > oc.Apps[j].CostUnits
		})
		oc.CostUnits = round2(oc.CostUnits)
		oc.CPUMilli = round2(oc.CPUMilli)
		oc.MemoryGiB = round2(oc.MemoryGiB)
		oc.StorageGiB = round2(oc.StorageGiB)
		orgs = append(orgs, *oc)
	}
	// Ordering: parent estate first, then customer Organizations by
	// descending cost, then the unowned rollup, then the synthetic
	// platform-overhead row last so the console renders it as a trailing
	// distinct line (#3687 §6, extended by #6114).
	sort.Slice(orgs, func(i, j int) bool {
		ri, rj := orgSortRank(orgs[i]), orgSortRank(orgs[j])
		if ri != rj {
			return ri < rj
		}
		return orgs[i].CostUnits > orgs[j].CostUnits
	})

	return SovereignConsumptionResponse{
		TotalCostUnits: round2(total),
		Orgs:           orgs,
		UnownedOrgs:    unowned,
	}
}

// containsString — small linear membership helper. The unowned slug list
// is bounded by the number of orphaned namespaces on a Sovereign (zero on
// a healthy estate, single digits on a broken one), so a linear scan is
// cheaper than building a map.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// orgForRow resolves the owning org for a pod row's showback attribution
// (#3687 fold #3677). It reads the real `openova.io/organization` join key
// carried on podRow.org (resolved in buildPodRows from the namespace
// label the organization-controller stamps). Control-plane / infra
// namespaces, one-shot Job-owned pods, and any pod whose namespace lacks
// an Org label roll into the synthetic platformOrg bucket so they render
// as a single "Platform overhead" line, never inside a tenant's app list.
// No per-org-name or per-namespace-name special-casing: the resolver keys
// purely on the label + the config-driven exclusion set.
//
// #6114 (UAT row 25(c)): the label is now checked AGAINST the Organization
// CR set rather than trusted on its own. A label value with no CR resolves
// to unownedOrg, so the set of billable Organization rows is a strict
// function of the Organization set — which is exactly the conjunct row 25
// asserts and the one hw293 refuted.
//
// knownOrgs is deliberately NILABLE and nil means "not read", not "empty":
//
//   - nil          → the Organization CR set could not be established
//     (no cache, cluster unregistered, CRD not served, informer not yet
//     synced). Every labelled row keeps its pre-#6114 attribution. To move
//     a row into unownedOrg we require POSITIVE evidence that the
//     Organization set was actually read; absence of evidence must never
//     become evidence of orphanhood, or a cold cache would relabel the
//     entire estate as unowned and bill nobody.
//   - non-nil      → authoritative. A slug outside it has no CR.
//
// parentOrg is always owned regardless of knownOrgs: it is the Sovereign
// estate itself (§2.2), seeded unconditionally by aggregateConsumption,
// and not a customer Organization whose CR must exist for it to be real.
func orgForRow(row podRow, infraNamespaces map[string]struct{}, knownOrgs map[string]struct{}, parentOrg string) string {
	if _, infra := infraNamespaces[row.namespace]; infra {
		return platformOrg
	}
	if strings.EqualFold(row.ownerKind, "Job") {
		return platformOrg
	}
	org := strings.TrimSpace(row.org)
	if org == "" {
		return platformOrg
	}
	if org == parentOrg || knownOrgs == nil {
		return org
	}
	if _, owned := knownOrgs[org]; owned {
		return org
	}
	return unownedOrg
}

// orgSortRank orders the response: the parent estate first, then customer
// Organizations (by descending cost), then the unowned rollup, then
// platform overhead last. Expressed as a rank rather than a chain of
// pairwise boolean flips because the pairwise form silently mis-orders
// once a third synthetic bucket exists — comparing an unowned row against
// a platform row would consult the wrong flag and put platform first.
func orgSortRank(oc orgConsumption) int {
	switch {
	case oc.IsParent:
		return 0
	case oc.IsPlatform:
		return 3
	case oc.IsUnowned:
		return 2
	default:
		return 1
	}
}

// consumptionParentOrg resolves the parent Organization's SLUG — the same
// identifier the Organization CR, the directory and the treemap use for the
// Sovereign self-org.
//
// #5819 (UAT row 25). This returned the raw Sovereign FQDN, so showback labelled
// the parent group `hw292.omani.works` while the directory and the CR both said
// `hw292-omani-works`. Row 25's entire assertion is "one consistent model across
// surfaces" — two spellings of one Org is exactly the failure it names, and it
// survived an earlier fix that removed the literal word "sovereign" from this
// path without addressing the mismatch underneath.
//
// Routing through orgNamespace() is not cosmetic normalisation; it is the
// canonical org→identifier seam (namespace_ensure.go), the SAME one
// spineOrganizationSlug uses to mint the self-org CR. Deriving the label a
// second way here is what let the two drift.
//
// It also repairs a join that could never match. The parent bucket is selected
// by `org == parentOrg`, where org comes from the `openova.io/organization`
// namespace label. Label values written by the org-controller are CR slugs —
// DNS-1123, no dots — so an FQDN-shaped parentOrg matched NOTHING, and the
// parent group rendered at 0 cost units on a Sovereign that plainly runs
// workloads. The walk saw exactly that: showback emitting the parent at zero
// while the treemap had no parent node at all.
//
// The "sovereign" fallback is kept for an unresolvable deployment: it is a
// visibly synthetic name, and inventing a plausible-looking slug for an Org we
// cannot identify would be worse than an obviously placeholder one.
func (h *Handler) consumptionParentOrg(depID string) string {
	if val, ok := h.deployments.Load(depID); ok {
		if dep, ok := val.(*Deployment); ok {
			dep.mu.Lock()
			fqdn := dep.Request.SovereignFQDN
			dep.mu.Unlock()
			if strings.TrimSpace(fqdn) != "" {
				return orgNamespace(fqdn)
			}
		}
	}
	return "sovereign"
}

func roundPct(v float64) float64 { return round2(v) }

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
