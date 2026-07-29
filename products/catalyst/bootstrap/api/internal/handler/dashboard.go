// Package handler — dashboard.go: REST surface for the Sovereign
// Dashboard's resource-utilisation treemap.
//
//	GET /api/v1/dashboard/treemap?group_by=A,B&color_by=C&size_by=D[&deployment_id=X]
//
// The response is a nested tree of TreemapItems matching the TS
// contract in
//   products/catalyst/bootstrap/ui/src/lib/treemap.types.ts
//
// ── Data path ─────────────────────────────────────────────────────────
//
// Per ADR-0001 §5 the kube-apiserver is the system of record. The
// dashboard reads from the in-process k8scache.Factory's Indexer (one
// dynamicinformer.SharedInformerFactory per Sovereign cluster), NOT
// the apiserver directly. Pods, PVCs, and (when metrics-server is
// installed) PodMetrics are all served straight from cache — sub-ms
// per request, event-driven freshness via the same WATCH stream that
// powers the SSE endpoint.
//
// `deployment_id` resolves to the k8scache cluster id — the kubeconfig
// file stem, which by construction is the deployment id (see
// PutKubeconfig handler).
//
// When the cache is not wired (test/CI without a real cluster) or the
// requested deployment_id is not registered, the handler returns a
// well-shaped empty response. The UI renders the "no utilisation data
// yet" empty state.
//
// ── color_by semantics ───────────────────────────────────────────────
//
//   • health      — Σ Ready pods / total ×100. Pure cache data;
//                   ships day-one. Frontend healthColor() flips so
//                   100 → green, 0 → red.
//   • age         — (now − min(creationTimestamp)) normalised to
//                   [0..AGE_NORMALISE_DAYS]. Frontend ageColor() goes
//                   blue → green → red as the value rises.
//   • utilization — Σ pod cpu (or memory, mirroring size_by) / Σ pod
//                   limit ×100. Reads from PodMetrics. When metrics-
//                   server is absent the percentage is JSON null and
//                   the UI greys the cell with a tooltip.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#1 (waterfall) — every group_by × color_by × size_by lands in
//	   one cut. No "for now" stub.
//	#2 (quality)   — fixture data is gone; every cell traces to a
//	   real Pod or PVC in the live cluster.
//	#3 (event-driven) — no apiserver hits. Every byte comes from the
//	   informer's Indexer.
//	#4 (never hardcode) — AGE_NORMALISE_DAYS is the only window
//	   constant and it lives at the top of this file as a named const.
package handler

import (
	"net/http"
	"sort"
	"strings"
	"time"

	apiresource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// AgeNormaliseDays — the upper bound for the `age` color metric.
// A pod with creationTimestamp 0 days ago maps to percentage 0 (blue);
// a pod older than this many days maps to percentage 100 (red). The
// gradient between is linear.
const AgeNormaliseDays = 30.0

// treemapItem is the wire shape — kept package-private with json tags
// matching the TS interface verbatim. Percentage is a pointer so the
// "no utilisation data" path can encode JSON null without an
// out-of-band sentinel.
type treemapItem struct {
	ID         *string       `json:"id"`
	Name       string        `json:"name"`
	Count      int           `json:"count"`
	Percentage *float64      `json:"percentage"`
	SizeValue  float64       `json:"size_value,omitempty"`
	Children   []treemapItem `json:"children,omitempty"`
}

type treemapResponse struct {
	Items      []treemapItem `json:"items"`
	TotalCount int           `json:"total_count"`
}

// dashboardDimension is the validated set of group_by tokens. Mirror
// of the TreemapDimension union in the UI.
// region and vcluster added 2026-05-16 (DoD D16/D19) so multi-region
// operators can pivot the treemap by their actual topology hierarchy
// (Cloud → Region → Cluster → vCluster → Namespace → Application).
// organization added 2026-06-18 (#3687 fold #3692) so the operator can
// pivot the treemap by the owning Organization — the same
// `openova.io/organization` label join key the per-Org showback uses.
// podRow.org is already populated in buildPodRows; this just exposes it
// as a first-class group_by dimension (the Lane-E UAT FAIL).
var dashboardDimension = map[string]struct{}{
	"sovereign":    {},
	"region":       {},
	"cluster":      {},
	"vcluster":     {},
	"family":       {},
	"namespace":    {},
	"application":  {},
	"organization": {},
}

var dashboardSizeBy = map[string]struct{}{
	"cpu_limit":      {},
	"memory_limit":   {},
	"storage_limit":  {},
	"replica_count":  {},
	"cpu_request":    {},
	"memory_request": {},
	"cpu_usage":      {},
	"memory_usage":   {},
}

var dashboardColorBy = map[string]struct{}{
	"utilization": {},
	"health":      {},
	"age":         {},
}

// GetDashboardTreemap handles GET /api/v1/dashboard/treemap.
//
// Validates the query string, then aggregates Pods + PVCs from the
// k8scache.Factory's Indexer into a nested treemap shaped per the UI
// contract.
func (h *Handler) GetDashboardTreemap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	groupByRaw := strings.TrimSpace(q.Get("group_by"))
	if groupByRaw == "" {
		groupByRaw = "application"
	}
	groupBy := strings.Split(groupByRaw, ",")
	for i, g := range groupBy {
		g = strings.TrimSpace(g)
		groupBy[i] = g
		if _, ok := dashboardDimension[g]; !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "invalid-group-by",
				"detail": "unsupported dimension: " + g,
			})
			return
		}
	}

	colorBy := strings.TrimSpace(q.Get("color_by"))
	if colorBy == "" {
		colorBy = "utilization"
	}
	if _, ok := dashboardColorBy[colorBy]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-color-by",
			"detail": "unsupported color metric: " + colorBy,
		})
		return
	}

	sizeBy := strings.TrimSpace(q.Get("size_by"))
	if sizeBy == "" {
		sizeBy = "cpu_request"
	}
	if _, ok := dashboardSizeBy[sizeBy]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-size-by",
			"detail": "unsupported size metric: " + sizeBy,
		})
		return
	}

	// Resolve cluster id from deployment_id. Empty deployment_id or
	// unregistered cluster → well-shaped empty response (UI shows
	// the empty state).
	//
	// D16 PR H (2026-05-17 t140 regression): the URL carries the mother's
	// deployment_id (e.g. "29b7e14918178f7e") while the chroot's k8sCache
	// self-registers the primary under a SOVEREIGN_FQDN-derived id
	// (e.g. "sovereign-t140.omani.works"). Without resolveChrootClusterID
	// the has-cluster check fails and the dashboard returns empty.
	// Other handlers (k8s.go, networking.go, k8s_search.go, etc.) already
	// call resolveChrootClusterID — dashboard was the missing caller.
	depID := strings.TrimSpace(q.Get("deployment_id"))
	clusterID := depID
	if h.k8sCache != nil {
		clusterID = h.resolveChrootClusterID(clusterID)
	}
	if clusterID == "" || h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, treemapResponse{Items: []treemapItem{}, TotalCount: 0})
		return
	}

	// G77 #2624 (2026-05-31): build clusterID→cloudRegion map from the
	// deployment's declared Regions[]. Used as the region fallback when
	// Node labels (`openova.io/region` / `topology.kubernetes.io/region`)
	// are missing — common on HCS where Huawei CCM doesn't stamp the
	// topology label. Without this fallback, dimensionKey returns the
	// raw clusterID (e.g. `afc8800bc03751c6` / `hw-me-east-215-a-rtz-prod`)
	// as the "region" label, producing meaningless bucket names.
	//
	// Convention: primary clusterID = `<depID>` (or
	// `sovereign-<fqdn>` fallback); secondary clusterIDs follow
	// `<depID>-<cloudRegion>` per sovereign_secondary_kubeconfig.go.
	// First declared region = primary; subsequent = secondaries.
	clusterRegion := map[string]string{}
	var depForRegions *Deployment
	if val, ok := h.deployments.Load(depID); ok {
		depForRegions = val.(*Deployment)
	} else {
		depForRegions = h.chrootEnsureDeployment(depID)
	}
	if depForRegions != nil {
		depForRegions.mu.Lock()
		regs := append([]provisioner.RegionSpec(nil), depForRegions.Request.Regions...)
		fqdn := depForRegions.Request.SovereignFQDN
		depForRegions.mu.Unlock()
		if len(regs) > 0 && regs[0].CloudRegion != "" {
			// Primary cluster identifiers we may see in k8sCache.
			clusterRegion[depID] = regs[0].CloudRegion
			if fqdn != "" {
				clusterRegion["sovereign-"+fqdn] = regs[0].CloudRegion
			}
		}
		for _, rs := range regs {
			if rs.CloudRegion == "" {
				continue
			}
			clusterRegion[depID+"-"+rs.CloudRegion] = rs.CloudRegion
		}
	}

	// D16 multi-cluster fan-out (caught on t132 2026-05-16): when
	// group_by includes "cluster" or "region", enumerate ALL registered
	// clusters (primary + each secondary's kubeconfig synced via PR
	// #1579) so Layer-1=Cluster renders N bubbles on an N-region
	// Sovereign instead of 1. For group_by that ONLY contains
	// {namespace,family,application,vcluster,sovereign} the primary
	// clusterID's pods are sufficient and faster.
	wantFanOut := false
	for _, g := range groupBy {
		if g == "cluster" || g == "region" {
			wantFanOut = true
			break
		}
	}
	clusterIDs := []string{clusterID}
	if wantFanOut {
		// h.k8sCache.Clusters() returns every registered ID. Primary
		// is included; deduplicate via the local map. When PR B ships
		// the mothership handover hook, secondaries land here.
		seen := map[string]struct{}{clusterID: {}}
		for _, cid := range h.k8sCache.Clusters() {
			if _, ok := seen[cid]; ok {
				continue
			}
			seen[cid] = struct{}{}
			clusterIDs = append(clusterIDs, cid)
		}
	}

	var rows []podRow
	for _, cid := range clusterIDs {
		pods, _, _ := h.k8sCache.List(cid, "pod", labels.Everything())
		pvcs, _, _ := h.k8sCache.List(cid, "persistentvolumeclaim", labels.Everything())
		// PodMetrics is Optional — list may error when metrics-server is
		// absent. Treat as nil and the utilization path emits null.
		podMetrics, _, _ := h.k8sCache.List(cid, "podmetrics", labels.Everything())
		// Wave 2 Family D (treemap fan-out): the chart helpers stamp the
		// canonical `catalyst.openova.io/{family,vcluster-role}` labels on
		// the host Namespace, NOT on individual Pods. Likewise
		// `openova.io/region` is a Node label, not a Pod label. Without
		// enrichment, family/vcluster grouping collapses every Pod into
		// the default bucket ("other" / "host") and region falls back to
		// the cluster id. List both kinds from the same cluster's cache
		// so buildPodRows can join them onto each Pod by ns/node name.
		//
		// Both lists are cheap (Namespace + Node informers run anyway for
		// the cloud-list canvas) and the per-pod lookup is a map probe.
		namespaces, _, _ := h.k8sCache.List(cid, "namespace", labels.Everything())
		nodes, _, _ := h.k8sCache.List(cid, "node", labels.Everything())
		// #5485 defect B: a Deployment's Pods are owned by an
		// intermediate ReplicaSet, so the ownerRef fallback in
		// applicationKey yields the hash-suffixed RS name
		// (`admin-865b6dd6c7`) as the "application" for every workload
		// that carries no app.kubernetes.io/{instance,name} label. List
		// the ReplicaSets so buildPodRows can take the second hop
		// RS → Deployment and render the durable workload name.
		replicaSets, _, _ := h.k8sCache.List(cid, "replicaset", labels.Everything())
		rows = append(rows, buildPodRows(pods, pvcs, podMetrics, namespaces, nodes, replicaSets, cid, clusterRegion[cid])...)
	}
	// #3687 (fold #3692): one-shot batch/v1 Job pods (cutover-*,
	// scan-vulnerabilityreport-*, *-snapshot-save-*) are ephemeral
	// activity, not estate — they must NEVER render as a treemap cell /
	// "application". Drop them before aggregation so the Application layer
	// reflects the durable workloads, not the Job/CronJob churn.
	rows = dropEphemeralRows(rows)
	resp := aggregateRows(rows, groupBy, colorBy, sizeBy)
	writeJSON(w, http.StatusOK, resp)
}

// isEphemeralRow reports whether a pod row is one-shot batch activity
// rather than a durable application: the cutover / scan / snapshot Job
// pods (`cutover-harbor-prewarm-1785340840`, `scan-vulnerabilityreport-…`,
// `openbao-snapshot-save-29755690`). Keys on the pod's top-level ownerRef
// Kind (podRow.ownerKind), resolved in buildPodRows — no name-pattern
// matching (#3687 §7c). CronJob-spawned pods are Job-owned too, so they
// classify here as well.
//
// THE single definition of "ephemeral" for every display layer. The
// treemap drops these rows outright (dropEphemeralRows); showback keeps
// their cost but collapses them into one Platform-overhead line
// (aggregateConsumption). Both call this predicate so the two surfaces
// can never disagree about what counts as one-shot (#5485).
func isEphemeralRow(r podRow) bool {
	return strings.EqualFold(r.ownerKind, "Job")
}

// dropEphemeralRows filters out pods owned by a batch/v1 Job — the
// one-shot cutover / scan / snapshot workloads that are activity, not a
// running application. Pods owned by anything else (Deployment,
// StatefulSet, DaemonSet, ReplicaSet) are retained.
func dropEphemeralRows(rows []podRow) []podRow {
	out := rows[:0]
	for _, r := range rows {
		if isEphemeralRow(r) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// podRow is one pod's contribution to the treemap. Built once,
// consumed by every group_by × color_by × size_by aggregation.
type podRow struct {
	namespace   string
	application string  // app.kubernetes.io/instance OR top-level ownerRef name
	family      string  // catalyst.openova.io/family (default "other")
	cluster     string  // cluster id (single-Sovereign per page today)
	region      string  // openova.io/region label value (e.g. hz-hel-rtz-prod); empty on single-region
	vcluster    string  // catalyst.openova.io/vcluster-role label value on the pod's host namespace (mgmt/dmz/rtz); empty for host pods
	// org is the owning Organization, resolved from the pod's namespace
	// `openova.io/organization` (or `catalyst.openova.io/organization`)
	// label — the single join key across compliance/RBAC/billing
	// (ARCHITECTURE.md:306). The organization-controller stamps it onto
	// every per-Org namespace (gitops/manifests.go). Empty when the
	// namespace carries no Org label (host/control-plane namespaces);
	// showback attribution (#3687 fold #3677) buckets those under the
	// platform-overhead line rather than mis-attributing them to a tenant.
	org string
	// ownerKind is the Kind of the pod's top-level owner (Deployment,
	// StatefulSet, DaemonSet, Job, ...). showback attribution excludes
	// `Job`-owned pods (one-shot cutover / scan / snapshot workloads) from
	// tenant `apps[]` so ephemeral activity never renders as consumption
	// (#3687 fold #3677).
	ownerKind   string
	cpuReq      float64 // millicores summed across containers (resources.requests.cpu)
	memReq      float64 // bytes (resources.requests.memory)
	cpuLim      float64 // millicores summed across containers (resources.limits.cpu)
	memLim      float64 // bytes (resources.limits.memory)
	storageLim  float64 // bytes — sum of attached PVC requests
	cpuUsage    float64 // from PodMetrics; 0 when absent
	memUsage    float64 // from PodMetrics; 0 when absent
	hasMetrics  bool    // true when PodMetrics observed for this pod
	isReady     bool
	createdAt   time.Time
}

// buildPodRows projects raw cache objects into the row shape. Pods
// without a Ready condition are still counted (they contribute 0 to
// the health numerator). PVCs are matched by namespace + claim name
// from each pod's spec.volumes[].
//
// Wave 2 Family D enrichment: the canonical `catalyst.openova.io/{family,
// vcluster-role}` labels live on the host Namespace (set by bp-{mgmt,
// dmz,rtz}-vcluster + chart helpers in platform/_template) and
// `openova.io/region` is a Node label (stamped by Hetzner cloud-init).
// When the caller passes the cluster's Namespaces + Nodes we join them
// onto each Pod so family/vcluster/region grouping fans out beyond the
// single "other"/"host"/cluster-id default buckets. Callers that have
// no namespace/node lists (older tests, the "cache absent" path) may
// pass nil — every enrichment is a best-effort map probe with a
// well-defined fallback.
// G77 #2624 (2026-05-31): clusterCloudRegion is the cloudRegion code
// for this cluster, joined from deployment.Request.Regions[] in
// HandleTreemap. Used as a region fallback when Pods/Nodes lack the
// `openova.io/region`/`topology.kubernetes.io/region` label — common
// on HCS where Huawei CCM doesn't stamp the topology label.
func buildPodRows(pods, pvcs, podMetrics, namespaces, nodes, replicaSets []*unstructured.Unstructured, clusterID string, clusterCloudRegion string) []podRow {
	pvcByKey := map[string]*unstructured.Unstructured{}
	for _, p := range pvcs {
		key := p.GetNamespace() + "/" + p.GetName()
		pvcByKey[key] = p
	}
	metricsByKey := map[string]*unstructured.Unstructured{}
	for _, m := range podMetrics {
		key := m.GetNamespace() + "/" + m.GetName()
		metricsByKey[key] = m
	}
	// Namespace-label join keys: bp-{mgmt,dmz,rtz}-vcluster stamp
	// `catalyst.openova.io/vcluster-role` and chart helpers stamp
	// `catalyst.openova.io/family` on the host Namespace.
	nsByName := map[string]*unstructured.Unstructured{}
	for _, ns := range namespaces {
		nsByName[ns.GetName()] = ns
	}
	// Node-label join keys: `openova.io/region` (canonical OpenOva) or
	// `topology.kubernetes.io/region` (K8s standard, set by hcloud-ccm
	// on every Hetzner node). Pods inherit via spec.nodeName.
	nodeByName := map[string]*unstructured.Unstructured{}
	for _, n := range nodes {
		nodeByName[n.GetName()] = n
	}
	// ReplicaSet join key: the intermediate hop on the Deployment→Pod
	// ownerRef chain. applicationKey uses it to resolve a Pod owned by
	// `admin-865b6dd6c7` to the Deployment `admin` (#5485). Nil/empty
	// when the caller has no ReplicaSet list — applicationKey then keeps
	// the ownerRef name rather than inventing one.
	rsByKey := indexReplicaSets(replicaSets)

	out := make([]podRow, 0, len(pods))
	for _, p := range pods {
		// Derive family + vcluster-role from the pod's Namespace, then
		// fall back to pod-level labels (which a handful of charts like
		// mimir _do_ set in their _helpers.tpl). When both are absent
		// dimensionKey produces "other" / "host" buckets so the cell is
		// still visible (never silently dropped).
		nsLabels := map[string]string{}
		if ns, ok := nsByName[p.GetNamespace()]; ok {
			nsLabels = ns.GetLabels()
		}
		family := stringLabel(p, "catalyst.openova.io/family", "")
		if family == "" {
			family = nsLabels["catalyst.openova.io/family"]
		}
		if family == "" {
			family = "other"
		}
		vcluster := stringLabel(p, "catalyst.openova.io/vcluster-role", "")
		if vcluster == "" {
			vcluster = nsLabels["catalyst.openova.io/vcluster-role"]
		}
		// Derive region: pod-level label wins, then Namespace label,
		// then the pod's host Node's region labels. Empty falls back
		// to cluster-id in dimensionKey so single-region/single-cluster
		// renders correctly while multi-region pods (when nodes carry
		// the label) bucket per region.
		region := stringLabel(p, "openova.io/region", "")
		if region == "" {
			region = nsLabels["openova.io/region"]
		}
		if region == "" {
			nodeName, _, _ := unstructured.NestedString(p.Object, "spec", "nodeName")
			if nodeName != "" {
				if n, ok := nodeByName[nodeName]; ok {
					nl := n.GetLabels()
					if v := nl["openova.io/region"]; v != "" {
						region = v
					} else if v := nl["topology.kubernetes.io/region"]; v != "" {
						region = v
					} else if v := nl["failure-domain.beta.kubernetes.io/region"]; v != "" {
						region = v
					}
				}
			}
		}
		// G77 #2624: when every label lookup misses (HCS doesn't stamp
		// topology labels by default), fall back to the cluster's
		// declared cloudRegion. dimensionKey then groups by real region
		// codes (`me-east-215-a`, `me-east-215-b`) instead of the raw
		// clusterID hex.
		if region == "" && clusterCloudRegion != "" {
			region = clusterCloudRegion
		}
		// org: the single billing/RBAC join key. The
		// organization-controller stamps `openova.io/organization=<slug>`
		// onto every per-Org namespace (gitops/manifests.go); a few
		// surfaces also use the `catalyst.openova.io/organization` form.
		// Host/control-plane namespaces carry neither → org stays "" and
		// showback rolls the pod into the platform-overhead bucket
		// instead of mis-attributing it to a tenant (#3687 fold #3677).
		org := labelOr(nsLabels, "openova.io/organization", "catalyst.openova.io/organization")
		if org == "" {
			org = labelOr(p.GetLabels(), "openova.io/organization", "catalyst.openova.io/organization")
		}
		// ownerKind: the Kind of the pod's first (top-level) owner. Used
		// to filter `Job`-owned one-shot pods out of tenant attribution.
		ownerKind := ""
		if refs := p.GetOwnerReferences(); len(refs) > 0 {
			ownerKind = refs[0].Kind
		}
		row := podRow{
			namespace:   p.GetNamespace(),
			cluster:     clusterID,
			application: applicationKey(p, rsByKey),
			family:      family,
			region:      region,
			vcluster:    vcluster,
			org:         org,
			ownerKind:   ownerKind,
			isReady:     podIsReady(p),
			createdAt:   p.GetCreationTimestamp().Time,
		}
		// Sum container requests + limits.
		containers, _, _ := unstructured.NestedSlice(p.Object, "spec", "containers")
		for _, ci := range containers {
			c, ok := ci.(map[string]any)
			if !ok {
				continue
			}
			requests, _, _ := unstructured.NestedStringMap(c, "resources", "requests")
			row.cpuReq += parseQuantityMillicores(requests["cpu"])
			row.memReq += parseQuantityBytes(requests["memory"])
			limits, _, _ := unstructured.NestedStringMap(c, "resources", "limits")
			row.cpuLim += parseQuantityMillicores(limits["cpu"])
			row.memLim += parseQuantityBytes(limits["memory"])
		}
		// Sum attached PVC storage.
		volumes, _, _ := unstructured.NestedSlice(p.Object, "spec", "volumes")
		for _, vi := range volumes {
			v, ok := vi.(map[string]any)
			if !ok {
				continue
			}
			pvcName, _, _ := unstructured.NestedString(v, "persistentVolumeClaim", "claimName")
			if pvcName == "" {
				continue
			}
			pvc, ok := pvcByKey[p.GetNamespace()+"/"+pvcName]
			if !ok {
				continue
			}
			storage, _, _ := unstructured.NestedString(pvc.Object, "spec", "resources", "requests", "storage")
			row.storageLim += parseQuantityBytes(storage)
		}
		// Pod metrics — when metrics-server is installed.
		if mm, ok := metricsByKey[p.GetNamespace()+"/"+p.GetName()]; ok {
			row.hasMetrics = true
			mContainers, _, _ := unstructured.NestedSlice(mm.Object, "containers")
			for _, ci := range mContainers {
				c, ok := ci.(map[string]any)
				if !ok {
					continue
				}
				usage, _, _ := unstructured.NestedStringMap(c, "usage")
				row.cpuUsage += parseQuantityMillicores(usage["cpu"])
				row.memUsage += parseQuantityBytes(usage["memory"])
			}
		}
		out = append(out, row)
	}
	return out
}

// indexReplicaSets keys ReplicaSets by "<namespace>/<name>" for the
// Pod → ReplicaSet → Deployment hop in applicationKey. Returns an empty
// (non-nil) map for an empty list so callers can probe unconditionally.
func indexReplicaSets(replicaSets []*unstructured.Unstructured) map[string]*unstructured.Unstructured {
	byKey := make(map[string]*unstructured.Unstructured, len(replicaSets))
	for _, rs := range replicaSets {
		if rs == nil {
			continue
		}
		byKey[rs.GetNamespace()+"/"+rs.GetName()] = rs
	}
	return byKey
}

// applicationKey returns the application identifier per the chart-
// authoring convention. Order of precedence:
//
//  1. label app.kubernetes.io/instance (set by Helm and most chart
//     authors); this is what `group_by=application` should bucket on.
//  2. label app.kubernetes.io/name.
//  3. the owning workload's name, resolved from the pod's ownerRefs.
//     StatefulSet / DaemonSet / Job pods are owned by their workload
//     directly, so the ownerRef name IS the application. A Deployment's
//     pods are owned by an intermediate ReplicaSet, so stopping at the
//     first ownerRef would surface the hash-suffixed RS name
//     (`admin-865b6dd6c7`, `coredns-57fc96f748`) as an "application" —
//     #5485 defect B, 24 of 101 live treemap leaves. When `rsByKey`
//     carries the pod's ReplicaSet we take one more hop to the RS's own
//     controller (the Deployment) and use THAT name.
//  4. the pod's own name when unowned (rare — statically-defined pods).
//
// No-further-owner cases resolve without inventing a name: a ReplicaSet
// absent from rsByKey (nil index / cache miss), or a bare ReplicaSet with
// no controller of its own, both keep the ReplicaSet name — the same
// value this returned before #5485, never "" and never a guess.
func applicationKey(p *unstructured.Unstructured, rsByKey map[string]*unstructured.Unstructured) string {
	if v := p.GetLabels()["app.kubernetes.io/instance"]; v != "" {
		return v
	}
	if v := p.GetLabels()["app.kubernetes.io/name"]; v != "" {
		return v
	}
	for _, ref := range p.GetOwnerReferences() {
		if ref.Name == "" {
			continue
		}
		if ref.Kind != "ReplicaSet" {
			return ref.Name
		}
		if owner := replicaSetOwnerName(rsByKey[p.GetNamespace()+"/"+ref.Name]); owner != "" {
			return owner
		}
		return ref.Name
	}
	return p.GetName()
}

// replicaSetOwnerName returns the name of the workload that controls this
// ReplicaSet — the Deployment in every Helm/Flux-managed case, an Argo
// Rollout or another custom controller elsewhere. Returns "" when the RS
// is nil (not in the index) or genuinely has no owner (a hand-applied,
// bare ReplicaSet), which keeps applicationKey on the ReplicaSet name
// instead of fabricating one.
func replicaSetOwnerName(rs *unstructured.Unstructured) string {
	if rs == nil {
		return ""
	}
	for _, ref := range rs.GetOwnerReferences() {
		if ref.Name != "" {
			return ref.Name
		}
	}
	return ""
}

func stringLabel(p *unstructured.Unstructured, key, fallback string) string {
	if v, ok := p.GetLabels()[key]; ok && v != "" {
		return v
	}
	return fallback
}

func podIsReady(p *unstructured.Unstructured) bool {
	conds, _, _ := unstructured.NestedSlice(p.Object, "status", "conditions")
	for _, ci := range conds {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		t, _, _ := unstructured.NestedString(c, "type")
		s, _, _ := unstructured.NestedString(c, "status")
		if t == "Ready" {
			return s == "True"
		}
	}
	return false
}

// parseQuantityMillicores converts a K8s quantity string ("100m",
// "1", "2.5") to millicores. Empty / unparseable → 0.
func parseQuantityMillicores(s string) float64 {
	if s == "" {
		return 0
	}
	q, err := apiresource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	return float64(q.MilliValue())
}

// parseQuantityBytes converts a K8s quantity string ("256Mi", "1Gi")
// to bytes. Empty / unparseable → 0.
func parseQuantityBytes(s string) float64 {
	if s == "" {
		return 0
	}
	q, err := apiresource.ParseQuantity(s)
	if err != nil {
		return 0
	}
	v, ok := q.AsInt64()
	if !ok {
		return q.AsApproximateFloat64()
	}
	return float64(v)
}

/* ── Aggregation ─────────────────────────────────────────────────── */

// aggregateRows groups rows by the requested group_by chain and
// computes size + percentage per bucket.
func aggregateRows(rows []podRow, groupBy []string, colorBy, sizeBy string) treemapResponse {
	if len(groupBy) == 0 {
		groupBy = []string{"application"}
	}
	items := groupAtLevel(rows, groupBy, 0, colorBy, sizeBy)
	return treemapResponse{Items: items, TotalCount: leafCount(items)}
}

type bucket struct {
	id   string
	name string
	rows []podRow
}

// groupAtLevel walks the group_by chain. At each depth it buckets the
// rows by the dimension at `level`, computes size+percentage, and
// recurses for the next level.
func groupAtLevel(rows []podRow, groupBy []string, level int, colorBy, sizeBy string) []treemapItem {
	if level >= len(groupBy) || len(rows) == 0 {
		return nil
	}
	dim := groupBy[level]
	buckets := bucketRows(rows, dim)
	out := make([]treemapItem, 0, len(buckets))
	for _, b := range buckets {
		size := sumSize(b.rows, sizeBy)
		pct := computePercentage(b.rows, colorBy)
		idCopy := b.id
		item := treemapItem{
			ID:         &idCopy,
			Name:       b.name,
			Count:      countContribution(b.rows, sizeBy),
			SizeValue:  size,
			Percentage: pct,
		}
		if level+1 < len(groupBy) {
			item.Children = groupAtLevel(b.rows, groupBy, level+1, colorBy, sizeBy)
		}
		out = append(out, item)
	}
	return out
}

func bucketRows(rows []podRow, dim string) []bucket {
	idx := map[string]*bucket{}
	order := []string{}
	for _, r := range rows {
		id, name := dimensionKey(r, dim)
		if _, ok := idx[id]; !ok {
			idx[id] = &bucket{id: id, name: name}
			order = append(order, id)
		}
		idx[id].rows = append(idx[id].rows, r)
	}
	out := make([]bucket, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	// Stable order for deterministic responses (tests + cache headers).
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func dimensionKey(r podRow, dim string) (string, string) {
	switch dim {
	case "sovereign":
		return r.cluster, r.cluster
	case "region":
		// Region key derives from the host node's
		// `openova.io/region` label, populated at buildPodRows time
		// from the pod's node + per-cluster node-label cache. Fall
		// back to cluster id so single-region Sovereigns still
		// render one bucket.
		if r.region != "" {
			return r.region, r.region
		}
		return r.cluster, r.cluster
	case "cluster":
		// TBD-E4b (#1756): operators see the bare deployment-id hex on the
		// primary cluster cell (e.g. `30dbef8b238c2d84`) and cannot tell
		// which region it represents. When the row carries a region label
		// (populated by buildPodRows from the host node's
		// `openova.io/region` label), postfix it onto the name so the
		// label reads `30dbef8b238c2d84 (hz-hel-rtz-prod)`. The bucket id
		// stays the cluster id so all rows for the same cluster still
		// merge into one cell.
		if r.region != "" {
			return r.cluster, r.cluster + " (" + r.region + ")"
		}
		return r.cluster, r.cluster
	case "vcluster":
		// vCluster name derives from the pod's host-namespace
		// `catalyst.openova.io/vcluster-role` label. Pods OUTSIDE
		// any vCluster (the bootstrap-kit host workloads) bucket
		// under "host" so they're visible, not silently dropped.
		if r.vcluster != "" {
			return r.vcluster, r.vcluster
		}
		return "host", "host"
	case "organization":
		// Organization name derives from the pod's namespace
		// `openova.io/organization` (or `catalyst.openova.io/organization`)
		// label — the SAME single join key the per-Org showback
		// (#3687 fold #3677) attributes consumption by, resolved into
		// podRow.org at buildPodRows time. Pods in host/control-plane
		// namespaces carry no Org label (org == "") and roll into the
		// synthetic platform-overhead bucket — visible + labelled, never
		// silently dropped and never mis-attributed to a tenant. The
		// bucket id is the same `platformOrg` sentinel the showback uses
		// so the two surfaces agree on the unattributed estate.
		if r.org != "" {
			return r.org, r.org
		}
		return platformOrg, "Platform overhead"
	case "family":
		return r.family, titleCase(r.family)
	case "namespace":
		return r.namespace, r.namespace
	case "application":
		return r.application, r.application
	default:
		return r.application, r.application
	}
}

// titleCase upper-cases the first letter without using the deprecated
// strings.Title helper. ASCII-only — every family slug in the catalyst
// registry is ASCII (spine/pilot/fabric/cortex/observability/security).
func titleCase(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-('a'-'A')) + s[1:]
	}
	return s
}

func sumSize(rows []podRow, sizeBy string) float64 {
	total := 0.0
	for _, r := range rows {
		switch sizeBy {
		case "cpu_limit":
			total += r.cpuLim
		case "memory_limit":
			total += r.memLim
		case "storage_limit":
			total += r.storageLim
		case "cpu_request":
			total += r.cpuReq
		case "memory_request":
			total += r.memReq
		case "cpu_usage":
			total += r.cpuUsage
		case "memory_usage":
			total += r.memUsage
		case "replica_count":
			if r.isReady {
				total += 1
			}
		default:
			total += r.cpuReq
		}
	}
	return total
}

// countContribution mirrors `replica_count` semantics for the cell's
// `count` field — but every other size_by uses pod count so the
// tooltip's "Items: N" reads naturally regardless of size selector.
func countContribution(rows []podRow, sizeBy string) int {
	if sizeBy == "replica_count" {
		n := 0
		for _, r := range rows {
			if r.isReady {
				n++
			}
		}
		return n
	}
	return len(rows)
}

// computePercentage returns the cell's color-driving percentage for
// the requested colorBy. Returns nil when the data source is not
// available (only utilization without metrics-server today). The UI
// renders nil-percentage cells as grey.
func computePercentage(rows []podRow, colorBy string) *float64 {
	switch colorBy {
	case "health":
		if len(rows) == 0 {
			return nil
		}
		ready := 0
		for _, r := range rows {
			if r.isReady {
				ready++
			}
		}
		v := 100.0 * float64(ready) / float64(len(rows))
		return &v
	case "age":
		if len(rows) == 0 {
			return nil
		}
		var minCreated time.Time
		for _, r := range rows {
			if r.createdAt.IsZero() {
				continue
			}
			if minCreated.IsZero() || r.createdAt.Before(minCreated) {
				minCreated = r.createdAt
			}
		}
		if minCreated.IsZero() {
			return nil
		}
		days := time.Since(minCreated).Hours() / 24.0
		v := (days / AgeNormaliseDays) * 100.0
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
		return &v
	case "utilization":
		// Σ usage / Σ request across rows that reported metrics.
		// Requests are the denominator because operators size their
		// workloads to a request and treat over-request as the
		// over-utilization signal — limits are usually unset on bp-*
		// charts (limits cause throttling). Falls back to limits when
		// requests are unset (legacy charts), and finally returns nil
		// when neither metrics nor a budget exist.
		var sumUsage, sumReq, sumLim float64
		anyMetrics := false
		for _, r := range rows {
			if !r.hasMetrics {
				continue
			}
			anyMetrics = true
			sumUsage += r.cpuUsage
			sumReq += r.cpuReq
			sumLim += r.cpuLim
		}
		if !anyMetrics {
			return nil
		}
		denom := sumReq
		if denom == 0 {
			denom = sumLim
		}
		if denom == 0 {
			return nil
		}
		v := 100.0 * sumUsage / denom
		if v < 0 {
			v = 0
		}
		// >100% is a real signal (over-request) — keep it. Renderer
		// clamps for color but tooltip surfaces the true value so
		// operators can scale up.
		return &v
	default:
		return nil
	}
}

func leafCount(items []treemapItem) int {
	n := 0
	for _, it := range items {
		if len(it.Children) > 0 {
			n += leafCount(it.Children)
			continue
		}
		n += 1
	}
	return n
}

// k8sCacheHasCluster — k8s.go owns the full method on Handler; this
// dashboard-side wrapper ensures we never crash on a nil k8sCache when
// the catalyst-api boots without a watcher (test/CI).
//
// (The real implementation lives in k8s.go to avoid duplicate-symbol
// errors during test linking — this comment documents the contract.)
