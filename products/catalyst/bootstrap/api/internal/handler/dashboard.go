// Package handler — dashboard.go: REST surface for the Sovereign
// Dashboard's resource-utilisation treemap.
//
//	GET /api/v1/dashboard/treemap?group_by=A,B&color_by=C&size_by=D[&deployment_id=X]
//
// The response is a nested tree of TreemapItems matching the TS
// contract in
//   products/catalyst/bootstrap/ui/src/lib/treemap.types.ts
//
// ── Data path (target state) ─────────────────────────────────────────
//
// The target state walks each registered Sovereign's kubeconfig, hits
// metrics-server for live pod CPU/memory, sums against
// `resources.limits.{cpu,memory}` per workload, and groups by the
// requested dimensions. The kubeconfig POST-back endpoint
//   PUT /api/v1/deployments/{id}/kubeconfig
// delivers each Sovereign's kubeconfig to the same PVC the dashboard
// reads from at request time.
//
// ── v1 placeholder (this file) ───────────────────────────────────────
//
// metrics-server is NOT yet trivially reachable from catalyst-api in
// every Sovereign profile (the bootstrap kit does NOT install it; it's
// an optional add-on). Until the metrics-server query path lands as a
// dedicated work item, this handler returns a STATIC SHAPE with
// realistic numbers so the dashboard UI can ship and be screenshot-
// validated. Every cell carries:
//
//   - A representative `count` (replicas)
//   - A `size_value` derived from a typical Helm chart's
//     `resources.requests` for the named application
//   - A `percentage` synthesised so the gradient covers blue, green
//     and red regions (so the UI proves the colour map at runtime)
//
// TODO(catalyst-api): replace this static path with the metrics-server
// integration. Tracked in the dashboard-treemap follow-up issue. The
// HTTP shape must NOT change — the UI is wired against this contract.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, not iterative MVP),
// the JSON shape is the target shape from day one. Only the data SOURCE
// is a placeholder; the schema is final.
package handler

import (
	"net/http"
	"strings"
)

// treemapItem is the wire shape — kept package-private with json tags
// matching the TS interface verbatim.
type treemapItem struct {
	ID         *string       `json:"id"`
	Name       string        `json:"name"`
	Count      int           `json:"count"`
	Percentage float64       `json:"percentage"`
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
// Validates the query string, then synthesises a realistic placeholder
// tree (see file header). Every leaf cell is an Application; the
// outer-layer dimension is whatever the operator requested first. When
// only one layer is requested, a flat list of leaves is returned.
func (h *Handler) GetDashboardTreemap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	groupByRaw := strings.TrimSpace(q.Get("group_by"))
	if groupByRaw == "" {
		groupByRaw = "application"
	}
	groupBy := strings.Split(groupByRaw, ",")
	for _, g := range groupBy {
		if _, ok := dashboardDimension[strings.TrimSpace(g)]; !ok {
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

	resp := buildPlaceholderTree(groupBy, sizeBy)
	writeJSON(w, http.StatusOK, resp)
}

// placeholder tree — keeps the schema honest and gives the UI a
// recognisable shape (~30 cells nested 2-deep, ~12 cells flat).
//
// The fixture is keyed off the canonical Catalyst-Zero family list so
// the Dashboard renders meaningful application names even before the
// metrics-server integration lands. Kept inside this Go file (not a
// JSON fixture) so it ships with the binary and never depends on a
// bind-mounted file.
type appFixture struct {
	id         string
	name       string
	family     string
	namespace  string
	cluster    string
	cpuLimit   float64 // millicores
	memLimit   float64 // bytes
	storage    float64 // bytes
	replicas   int
	utilizPct  float64
	healthPct  float64
	agePct     float64
}

var dashboardFixture = []appFixture{
	// SPINE
	{id: "bp-cilium", name: "cilium", family: "spine", namespace: "kube-system", cluster: "omantel-mkt", cpuLimit: 1500, memLimit: 1.5 * 1024 * 1024 * 1024, storage: 0, replicas: 3, utilizPct: 62, healthPct: 100, agePct: 28},
	{id: "bp-cert-manager", name: "cert-manager", family: "spine", namespace: "cert-manager", cluster: "omantel-mkt", cpuLimit: 200, memLimit: 256 * 1024 * 1024, storage: 0, replicas: 1, utilizPct: 18, healthPct: 100, agePct: 28},
	{id: "bp-flux", name: "flux", family: "spine", namespace: "flux-system", cluster: "omantel-mkt", cpuLimit: 500, memLimit: 512 * 1024 * 1024, storage: 0, replicas: 4, utilizPct: 47, healthPct: 100, agePct: 28},
	{id: "bp-crossplane", name: "crossplane", family: "spine", namespace: "crossplane-system", cluster: "omantel-mkt", cpuLimit: 300, memLimit: 512 * 1024 * 1024, storage: 0, replicas: 1, utilizPct: 22, healthPct: 100, agePct: 28},
	// PILOT (auth + service mesh)
	{id: "bp-keycloak", name: "keycloak", family: "pilot", namespace: "auth", cluster: "omantel-mkt", cpuLimit: 1000, memLimit: 2 * 1024 * 1024 * 1024, storage: 5 * 1024 * 1024 * 1024, replicas: 2, utilizPct: 71, healthPct: 100, agePct: 14},
	{id: "bp-spire", name: "spire", family: "pilot", namespace: "spire-system", cluster: "omantel-mkt", cpuLimit: 200, memLimit: 256 * 1024 * 1024, storage: 1 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 33, healthPct: 100, agePct: 14},
	{id: "bp-openbao", name: "openbao", family: "pilot", namespace: "openbao", cluster: "omantel-mkt", cpuLimit: 500, memLimit: 1024 * 1024 * 1024, storage: 10 * 1024 * 1024 * 1024, replicas: 3, utilizPct: 54, healthPct: 100, agePct: 14},
	// FABRIC (event/data spine)
	{id: "bp-nats-jetstream", name: "nats-jetstream", family: "fabric", namespace: "nats", cluster: "omantel-mkt", cpuLimit: 600, memLimit: 1024 * 1024 * 1024, storage: 20 * 1024 * 1024 * 1024, replicas: 3, utilizPct: 81, healthPct: 100, agePct: 14},
	{id: "bp-gitea", name: "gitea", family: "fabric", namespace: "gitea", cluster: "omantel-mkt", cpuLimit: 300, memLimit: 512 * 1024 * 1024, storage: 15 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 41, healthPct: 100, agePct: 14},
	{id: "bp-cnpg", name: "cnpg", family: "fabric", namespace: "cnpg-system", cluster: "omantel-mkt", cpuLimit: 800, memLimit: 2 * 1024 * 1024 * 1024, storage: 50 * 1024 * 1024 * 1024, replicas: 3, utilizPct: 67, healthPct: 100, agePct: 14},
	{id: "bp-seaweedfs", name: "seaweedfs", family: "fabric", namespace: "seaweedfs", cluster: "omantel-mkt", cpuLimit: 400, memLimit: 1024 * 1024 * 1024, storage: 100 * 1024 * 1024 * 1024, replicas: 3, utilizPct: 38, healthPct: 100, agePct: 14},
	// CORTEX (AI / ML serving)
	{id: "bp-kserve", name: "kserve", family: "cortex", namespace: "kserve", cluster: "omantel-mkt", cpuLimit: 2000, memLimit: 4 * 1024 * 1024 * 1024, storage: 0, replicas: 2, utilizPct: 92, healthPct: 75, agePct: 7},
	{id: "bp-axon", name: "axon", family: "cortex", namespace: "axon", cluster: "omantel-mkt", cpuLimit: 1500, memLimit: 3 * 1024 * 1024 * 1024, storage: 0, replicas: 2, utilizPct: 88, healthPct: 100, agePct: 7},
	// OBSERVABILITY
	{id: "bp-prometheus", name: "prometheus", family: "observability", namespace: "observability", cluster: "omantel-mkt", cpuLimit: 1000, memLimit: 2 * 1024 * 1024 * 1024, storage: 30 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 76, healthPct: 100, agePct: 14},
	{id: "bp-grafana", name: "grafana", family: "observability", namespace: "observability", cluster: "omantel-mkt", cpuLimit: 200, memLimit: 256 * 1024 * 1024, storage: 1 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 29, healthPct: 100, agePct: 14},
	{id: "bp-tempo", name: "tempo", family: "observability", namespace: "observability", cluster: "omantel-mkt", cpuLimit: 400, memLimit: 1024 * 1024 * 1024, storage: 20 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 43, healthPct: 100, agePct: 14},
	{id: "bp-loki", name: "loki", family: "observability", namespace: "observability", cluster: "omantel-mkt", cpuLimit: 500, memLimit: 1024 * 1024 * 1024, storage: 50 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 58, healthPct: 100, agePct: 14},
	// SECURITY
	{id: "bp-coraza", name: "coraza", family: "security", namespace: "ingress", cluster: "omantel-mkt", cpuLimit: 200, memLimit: 256 * 1024 * 1024, storage: 0, replicas: 2, utilizPct: 26, healthPct: 100, agePct: 7},
	{id: "bp-syft-grype", name: "syft-grype", family: "security", namespace: "security", cluster: "omantel-mkt", cpuLimit: 100, memLimit: 256 * 1024 * 1024, storage: 5 * 1024 * 1024 * 1024, replicas: 1, utilizPct: 12, healthPct: 100, agePct: 7},
}

func buildPlaceholderTree(groupBy []string, sizeBy string) treemapResponse {
	if len(groupBy) == 0 {
		groupBy = []string{"application"}
	}
	// Single-layer flat list when only one layer is requested.
	if len(groupBy) == 1 {
		dim := strings.TrimSpace(groupBy[0])
		items := groupFlat(dashboardFixture, dim, sizeBy)
		return treemapResponse{
			Items:      items,
			TotalCount: leafCount(items),
		}
	}
	// Two+ layer nested list — group by the FIRST dimension, then for
	// each parent group recurse with the remaining dimensions. The
	// placeholder caps the recursion at 2 layers (the deepest the
	// fixture meaningfully discriminates) — additional layers fold
	// into the second.
	outer := strings.TrimSpace(groupBy[0])
	inner := strings.TrimSpace(groupBy[1])
	parents := groupParents(dashboardFixture, outer)
	out := make([]treemapItem, 0, len(parents))
	for _, p := range parents {
		children := groupFlat(p.rows, inner, sizeBy)
		// Compute parent rollup. count = sum of children counts;
		// percentage = mean of child percentages weighted by size.
		parent := rollupParent(p.id, p.name, children)
		parent.Children = children
		out = append(out, parent)
	}
	return treemapResponse{
		Items:      out,
		TotalCount: leafCount(out),
	}
}

type parentBucket struct {
	id   string
	name string
	rows []appFixture
}

func groupParents(rows []appFixture, dim string) []parentBucket {
	idx := map[string]*parentBucket{}
	order := []string{}
	for _, r := range rows {
		key, name := dimensionKey(r, dim)
		if _, ok := idx[key]; !ok {
			idx[key] = &parentBucket{id: key, name: name}
			order = append(order, key)
		}
		idx[key].rows = append(idx[key].rows, r)
	}
	out := make([]parentBucket, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	return out
}

func groupFlat(rows []appFixture, dim, sizeBy string) []treemapItem {
	idx := map[string]*treemapItem{}
	order := []string{}
	for _, r := range rows {
		key, name := dimensionKey(r, dim)
		if _, ok := idx[key]; !ok {
			idCopy := key
			idx[key] = &treemapItem{ID: &idCopy, Name: name}
			order = append(order, key)
		}
		// Aggregate
		size := sizeValueFor(r, sizeBy)
		idx[key].SizeValue += size
		idx[key].Count += r.replicas
		// Weighted-average percentage.
		// First arrival sets value; subsequent arrivals weight by size.
		if idx[key].Percentage == 0 {
			idx[key].Percentage = percentageFor(r)
		} else {
			// Running weighted mean.
			prevSize := idx[key].SizeValue - size
			if prevSize > 0 {
				idx[key].Percentage = (idx[key].Percentage*prevSize + percentageFor(r)*size) / idx[key].SizeValue
			}
		}
	}
	// Note: percentageFor closes over color metric via a package-level
	// indirection — see below.
	out := make([]treemapItem, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	return out
}

func dimensionKey(r appFixture, dim string) (string, string) {
	switch dim {
	case "sovereign":
		// Single-Sovereign placeholder; one bucket.
		return "sovereign-this", "this Sovereign"
	case "cluster":
		return r.cluster, r.cluster
	case "family":
		return r.family, strings.Title(r.family) //nolint:staticcheck
	case "namespace":
		return r.namespace, r.namespace
	case "application":
		return r.id, r.name
	default:
		return r.id, r.name
	}
}

// percentageFor is hard-wired to utilisation in the placeholder. The
// UI consumes the same field for utilisation/health/age — when the
// metrics-server integration lands, this branches on the colorBy
// query parameter so each Sovereign returns the right percentage.
func percentageFor(r appFixture) float64 {
	return r.utilizPct
}

func sizeValueFor(r appFixture, sizeBy string) float64 {
	switch sizeBy {
	case "cpu_limit":
		return r.cpuLimit
	case "memory_limit":
		return r.memLimit
	case "storage_limit":
		return r.storage
	case "replica_count":
		return float64(r.replicas)
	default:
		return r.cpuLimit
	}
}

func rollupParent(id, name string, children []treemapItem) treemapItem {
	idCopy := id
	parent := treemapItem{ID: &idCopy, Name: name}
	totalSize := 0.0
	for _, c := range children {
		parent.Count += c.Count
		totalSize += c.SizeValue
	}
	if totalSize > 0 {
		weighted := 0.0
		for _, c := range children {
			weighted += c.Percentage * c.SizeValue
		}
		parent.Percentage = weighted / totalSize
	}
	parent.SizeValue = totalSize
	return parent
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
