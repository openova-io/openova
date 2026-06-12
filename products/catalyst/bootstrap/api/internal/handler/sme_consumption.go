// sme_consumption.go — the B3 metering feed (issue #3378 §6 B3).
//
// Per-org consumption aggregation, labeled by org, exposed via ONE GET
// the Organizations billing pages read. The FIRST deliverable is parent
// self-showback (testable with zero sub-orgs): on a Sovereign with no
// sub-orgs, 100% of consumption attributes to the parent organization,
// broken down per application / namespace (#3378 DoD 3 + §5).
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
//   GET /api/v1/sme/consumption  → SovereignConsumptionResponse
//
// This is NOT a marketplace funnel surface — it works day one on the
// operator's own estate, which is the whole point of showback (§5).

package handler

import (
	"net/http"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
)

// orgConsumption is one org's consumption rollup. With zero sub-orgs the
// only row is the parent (org="<parent>", isParent=true) carrying 100%.
type orgConsumption struct {
	Org      string `json:"org"`
	IsParent bool   `json:"isParent"`
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

// HandleSovereignConsumption — GET /api/v1/sme/consumption.
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
			Orgs:    []orgConsumption{{Org: parentOrg, IsParent: true, Apps: []appConsumption{}}},
			Pending: true,
		})
		return
	}

	pods, _, _ := h.k8sCache.List(clusterID, "pod", labels.Everything())
	pvcs, _, _ := h.k8sCache.List(clusterID, "persistentvolumeclaim", labels.Everything())
	podMetrics, _, _ := h.k8sCache.List(clusterID, "podmetrics", labels.Everything())
	namespaces, _, _ := h.k8sCache.List(clusterID, "namespace", labels.Everything())
	nodes, _, _ := h.k8sCache.List(clusterID, "node", labels.Everything())

	rows := buildPodRows(pods, pvcs, podMetrics, namespaces, nodes, clusterID, "")

	resp := aggregateConsumption(rows, parentOrg)
	writeJSON(w, http.StatusOK, resp)
}

// aggregateConsumption rolls podRows up into the per-org / per-app
// showback shape. orgFor maps a namespace to its owning org; today every
// namespace attributes to the parent (sub-org namespaces carry the
// vcluster/org label that a later PR keys on — until then 100% → parent,
// which is exactly the §5 day-one showback contract).
func aggregateConsumption(rows []podRow, parentOrg string) SovereignConsumptionResponse {
	// org → namespace+app key → app rollup.
	type appKey struct{ org, ns, app string }
	appAgg := map[appKey]*appConsumption{}
	orgAgg := map[string]*orgConsumption{}

	ensureOrg := func(org string) *orgConsumption {
		oc, ok := orgAgg[org]
		if !ok {
			oc = &orgConsumption{Org: org, IsParent: org == parentOrg, Apps: []appConsumption{}}
			orgAgg[org] = oc
		}
		return oc
	}
	// Always seed the parent so the estate is never blank (§5).
	ensureOrg(parentOrg)

	var total float64
	for _, row := range rows {
		org := orgForNamespace(row, parentOrg)
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
		ac, ok := appAgg[k]
		if !ok {
			ac = &appConsumption{Application: app, Namespace: row.namespace}
			appAgg[k] = ac
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
	sort.Slice(orgs, func(i, j int) bool {
		if orgs[i].IsParent != orgs[j].IsParent {
			return orgs[i].IsParent // parent first
		}
		return orgs[i].CostUnits > orgs[j].CostUnits
	})

	return SovereignConsumptionResponse{
		TotalCostUnits: round2(total),
		Orgs:           orgs,
	}
}

// orgForNamespace maps a pod's namespace to its owning org. Today every
// namespace attributes to the parent (§5 day-one: 100% → parent). When a
// sub-org's vCluster/namespace carries an org label, a later PR keys on
// it here — the showback labels stay identical, so chargeback "becomes
// meaningful automatically when the first sub-org splits out" (§5) with
// no new mechanism.
func orgForNamespace(_ podRow, parentOrg string) string {
	return parentOrg
}

// consumptionParentOrg resolves the parent org name (the Sovereign FQDN).
func (h *Handler) consumptionParentOrg(depID string) string {
	if val, ok := h.deployments.Load(depID); ok {
		if dep, ok := val.(*Deployment); ok {
			dep.mu.Lock()
			fqdn := dep.Request.SovereignFQDN
			dep.mu.Unlock()
			if strings.TrimSpace(fqdn) != "" {
				return fqdn
			}
		}
	}
	return "sovereign"
}

func roundPct(v float64) float64 { return round2(v) }

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
