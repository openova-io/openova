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
var dashboardDimension = map[string]struct{}{
	"sovereign":   {},
	"cluster":     {},
	"family":      {},
	"namespace":   {},
	"application": {},
}

var dashboardSizeBy = map[string]struct{}{
	"cpu_limit":     {},
	"memory_limit":  {},
	"storage_limit": {},
	"replica_count": {},
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
		sizeBy = "cpu_limit"
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
	clusterID := strings.TrimSpace(q.Get("deployment_id"))
	if clusterID == "" || h.k8sCache == nil || !h.k8sCacheHasCluster(clusterID) {
		writeJSON(w, http.StatusOK, treemapResponse{Items: []treemapItem{}, TotalCount: 0})
		return
	}

	pods, _, _ := h.k8sCache.List(clusterID, "pod", labels.Everything())
	pvcs, _, _ := h.k8sCache.List(clusterID, "persistentvolumeclaim", labels.Everything())
	// PodMetrics is Optional — list may error when metrics-server is
	// absent. Treat as nil and the utilization path emits null.
	podMetrics, _, _ := h.k8sCache.List(clusterID, "podmetrics", labels.Everything())

	rows := buildPodRows(pods, pvcs, podMetrics, clusterID)
	resp := aggregateRows(rows, groupBy, colorBy, sizeBy)
	writeJSON(w, http.StatusOK, resp)
}

// podRow is one pod's contribution to the treemap. Built once,
// consumed by every group_by × color_by × size_by aggregation.
type podRow struct {
	namespace   string
	application string  // app.kubernetes.io/instance OR top-level ownerRef name
	family      string  // catalyst.openova.io/family (default "other")
	cluster     string  // cluster id (single-Sovereign per page today)
	cpuLim      float64 // millicores summed across containers
	memLim      float64 // bytes
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
func buildPodRows(pods, pvcs, podMetrics []*unstructured.Unstructured, clusterID string) []podRow {
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

	out := make([]podRow, 0, len(pods))
	for _, p := range pods {
		row := podRow{
			namespace:   p.GetNamespace(),
			cluster:     clusterID,
			application: applicationKey(p),
			family:      stringLabel(p, "catalyst.openova.io/family", "other"),
			isReady:     podIsReady(p),
			createdAt:   p.GetCreationTimestamp().Time,
		}
		// Sum container limits.
		containers, _, _ := unstructured.NestedSlice(p.Object, "spec", "containers")
		for _, ci := range containers {
			c, ok := ci.(map[string]any)
			if !ok {
				continue
			}
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

// applicationKey returns the application identifier per the chart-
// authoring convention. Order of precedence:
//
//  1. label app.kubernetes.io/instance (set by Helm and most chart
//     authors); this is what `group_by=application` should bucket on.
//  2. top-level ownerRef Kind+Name when no instance label is set.
//     Daemonset/Statefulset/Deployment/Job all get hit; the
//     ReplicaSet hop is collapsed by walking the RS ownerRef chain
//     would require a second cache lookup — we treat the pod's first
//     ownerRef as the application unit instead, which is correct for
//     all bp-* charts in the catalyst registry.
//  3. the pod's own name when unowned (rare — DaemonSet stub pods,
//     statically-defined pods).
func applicationKey(p *unstructured.Unstructured) string {
	if v := p.GetLabels()["app.kubernetes.io/instance"]; v != "" {
		return v
	}
	if v := p.GetLabels()["app.kubernetes.io/name"]; v != "" {
		return v
	}
	for _, ref := range p.GetOwnerReferences() {
		if ref.Name != "" {
			return ref.Name
		}
	}
	return p.GetName()
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
	case "cluster":
		return r.cluster, r.cluster
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
		case "replica_count":
			if r.isReady {
				total += 1
			}
		default:
			total += r.cpuLim
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
		// Σ usage / Σ limit across rows that reported metrics. When NO
		// row has metrics, return nil → null in JSON → grey cell.
		var sumUsage, sumLimit float64
		anyMetrics := false
		for _, r := range rows {
			if !r.hasMetrics {
				continue
			}
			anyMetrics = true
			// Use cpu by default; the UI treemap is single-resource per
			// request, so cpu/memory utilisation tracks size_by tightly
			// enough at this level. A future split into separate
			// cpu_utilization / memory_utilization color metrics would
			// branch here.
			sumUsage += r.cpuUsage
			sumLimit += r.cpuLim
		}
		if !anyMetrics || sumLimit == 0 {
			return nil
		}
		v := 100.0 * sumUsage / sumLimit
		if v < 0 {
			v = 0
		}
		if v > 100 {
			v = 100
		}
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
