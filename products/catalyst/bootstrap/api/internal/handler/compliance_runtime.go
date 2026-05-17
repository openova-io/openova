// Package handler — compliance_runtime.go: Wave-2 Family-E (#1583/Family-E)
// runtime-security + supply-chain compliance aggregators.
//
// Three runtime/supply-chain surfaces the Sovereign Console needs and
// the matrix asserts (C11-008 + C11-010):
//
//	GET /api/v1/sovereigns/{id}/compliance/falco
//	    — runtime alerts emitted by the falco-system DaemonSet. Source
//	      is the Falco gRPC outputs queue (one event per kernel syscall
//	      that matched a Falco rule); we aggregate the last ~500 events
//	      from the per-Pod /var/log/falco/events.txt tail and present
//	      them as a structured JSON feed so the FalcoAlertsPage doesn't
//	      have to scrape container logs.
//
//	GET /api/v1/sovereigns/{id}/compliance/sbom?ns=<ns>&pod=<pod>
//	    — per-Pod vulnerability + SBOM tally derived from Trivy operator
//	      CRs (VulnerabilityReport + SBOMReport). Used by the per-Pod
//	      SBOMTab on the cloud-list ResourceDetailPage. Returns severity
//	      counts (CRITICAL / HIGH / MEDIUM / LOW / UNKNOWN) and a flat
//	      component list extracted from the SBOM.
//
//	GET /api/v1/sovereigns/{id}/compliance/sbom/summary
//	    — cluster-wide rollup of VulnerabilityReports for the
//	      AppDetail Compliance tab + the Security-Lead dashboard.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL +
// page-size + Falco-priority threshold is parameterised; no literal
// hostnames, no embedded sample data.
//
// Per ADR-0001 §5 (event-driven), the SBOM aggregator reads the
// in-memory k8scache (no apiserver round-trip per request).
//
// Per the same architecture: when the relevant CRDs (Trivy operator,
// Falco rules) are not yet installed the handlers return an empty
// envelope rather than a 5xx, so the UI can render a "not installed"
// hint without retry storms.
package handler

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// FalcoEvent is one runtime alert as the UI's FalcoAlertsPage
// consumes it. Mirrors the Falco JSON output envelope but flattened
// for table rendering.
type FalcoEvent struct {
	Time      string            `json:"time"`
	Priority  string            `json:"priority"` // EMERGENCY / ALERT / CRITICAL / ERROR / WARNING / NOTICE / INFO / DEBUG
	Rule      string            `json:"rule"`
	Output    string            `json:"output"`
	Source    string            `json:"source,omitempty"`
	Namespace string            `json:"namespace,omitempty"`
	Pod       string            `json:"pod,omitempty"`
	Container string            `json:"container,omitempty"`
	Tags      []string          `json:"tags,omitempty"`
	Fields    map[string]string `json:"fields,omitempty"`
	Hostname  string            `json:"hostname,omitempty"`
}

// FalcoEventsResponse is the GET /compliance/falco envelope.
type FalcoEventsResponse struct {
	Items     []FalcoEvent `json:"items"`
	Total     int          `json:"total"`
	Installed bool         `json:"installed"` // true when the falco DaemonSet exists in the cluster
	Source    string       `json:"source"`    // "daemonset-logs" | "grpc-stream" | "empty"
	UpdatedAt string       `json:"updatedAt"`
}

// VulnerabilitySeverityCounts — per-severity tally for a Pod/Image.
type VulnerabilitySeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Unknown  int `json:"unknown"`
	Total    int `json:"total"`
}

// SBOMComponent — one OS or application component extracted from the
// SBOMReport `report.components[]` slice. Trimmed to the fields the
// UI table renders to keep the wire payload small.
type SBOMComponent struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Type     string `json:"type,omitempty"` // library / application / operating-system
	PURL     string `json:"purl,omitempty"`
	Licenses string `json:"licenses,omitempty"`
}

// SBOMPodResponse — per-Pod SBOM + Vulnerability response shape.
type SBOMPodResponse struct {
	Pod              string                                  `json:"pod"`
	Namespace        string                                  `json:"namespace"`
	Containers       []SBOMContainerEntry                    `json:"containers"`
	CountsByContainer map[string]VulnerabilitySeverityCounts `json:"countsByContainer"`
	TotalCounts      VulnerabilitySeverityCounts             `json:"totalCounts"`
	UpdatedAt        string                                  `json:"updatedAt"`
	Installed        bool                                    `json:"installed"` // true when trivy-operator CRDs are present
}

// SBOMContainerEntry — one container's image + report linkage.
type SBOMContainerEntry struct {
	Container       string                      `json:"container"`
	Image           string                      `json:"image,omitempty"`
	Digest          string                      `json:"digest,omitempty"`
	Severity        VulnerabilitySeverityCounts `json:"severity"`
	Components      []SBOMComponent             `json:"components,omitempty"`
	ReportName      string                      `json:"reportName,omitempty"`
	ScanCompletedAt string                      `json:"scanCompletedAt,omitempty"`
}

// SBOMSummaryResponse — cluster-wide rollup.
type SBOMSummaryResponse struct {
	Total       VulnerabilitySeverityCounts `json:"total"`
	ByNamespace map[string]VulnerabilitySeverityCounts `json:"byNamespace"`
	ByImage     map[string]VulnerabilitySeverityCounts `json:"byImage"`
	Pods        int                         `json:"pods"`
	Containers  int                         `json:"containers"`
	Installed   bool                        `json:"installed"`
	UpdatedAt   string                      `json:"updatedAt"`
}

// ── GVRs ─────────────────────────────────────────────────────────────

// VulnerabilityReportGVR — aquasecurity.github.io/v1alpha1/vulnerabilityreports.
func VulnerabilityReportGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "vulnerabilityreports"}
}

// SBOMReportGVR — aquasecurity.github.io/v1alpha1/sbomreports.
func SBOMReportGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "aquasecurity.github.io", Version: "v1alpha1", Resource: "sbomreports"}
}

// ── Handlers ─────────────────────────────────────────────────────────

// HandleComplianceFalco — GET /api/v1/sovereigns/{id}/compliance/falco
//
// Returns the most recent Falco runtime alerts as a structured feed.
// When the falco-system DaemonSet is not installed the envelope's
// `installed:false` lets the UI render a one-line "not deployed yet"
// state instead of a generic error.
//
// Source-of-truth order (each falls back to the next when its
// precondition is not met):
//
//  1. Falcosidekick → events.k8s.io v1 Events sink. When bp-falco
//     installs falcosidekick with `--set config.k8saudit.outputs.k8s_events=true`
//     each Falco match is mirrored as a Kubernetes Event with
//     `reportingController=falco`. The compliance handler queries
//     events with that selector and projects them onto FalcoEvent.
//  2. Falco DaemonSet exists but no events have landed yet → return
//     `installed:true, items:[]` so the UI shows the empty-state
//     "Falco running — no alerts yet on this window."
//  3. Falco not installed → `installed:false`.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #2 we never seed synthetic events
// — empty means empty. The richer log-tailing collector ships under
// follow-up issue tracked in the PR body.
//
// Query params:
//   - limit  (int, default 200, max 1000) — page size cap
//   - prio   (csv string, optional) — comma-separated priorities
//     to include (EMERGENCY..DEBUG); defaults to CRITICAL+ERROR+WARNING.
func (h *Handler) HandleComplianceFalco(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	limit := parsePositiveIntQuery(r, "limit", 200, 1000)
	prioFilter := parsePrioritiesQuery(r.URL.Query().Get("prio"))

	resp := FalcoEventsResponse{
		Items:     []FalcoEvent{},
		Total:     0,
		Installed: false,
		Source:    "empty",
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	dep, ok := h.lookupDeploymentForInfra(clusterID)
	if !ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil || dyn == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	_, _, installed := listFalcoPods(r.Context(), dyn)
	resp.Installed = installed
	if !installed {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// When Falco is installed, look for Falcosidekick → k8s Events.
	resp.Source = "k8s-events"
	events := listFalcoK8sEvents(r.Context(), dyn, limit)
	for _, ev := range events {
		if len(prioFilter) > 0 {
			if _, want := prioFilter[strings.ToUpper(ev.Priority)]; !want {
				continue
			}
		}
		resp.Items = append(resp.Items, ev)
	}
	sort.Slice(resp.Items, func(i, j int) bool { return resp.Items[i].Time > resp.Items[j].Time })
	if len(resp.Items) > limit {
		resp.Items = resp.Items[:limit]
	}
	resp.Total = len(resp.Items)
	writeJSON(w, http.StatusOK, resp)
}

// listFalcoK8sEvents reads Kubernetes Events written by falcosidekick.
// Falcosidekick's k8s-events sink writes events with
// `reportingController=falcosidekick` and `type` set to the Falco
// priority. Returns up to `limit` events; on any error returns nil
// so the caller can surface installed:true + empty list.
func listFalcoK8sEvents(ctx context.Context, dyn dynamic.Interface, limit int) []FalcoEvent {
	if dyn == nil {
		return nil
	}
	gvr := schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}
	list, err := dyn.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{
		FieldSelector: "reportingController=falcosidekick",
		Limit:         int64(limit),
	})
	if err != nil || list == nil {
		// FieldSelector may not be indexed; do an unfiltered list capped
		// at limit*4 and filter client-side.
		list, err = dyn.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{
			Limit: int64(limit * 4),
		})
		if err != nil || list == nil {
			return nil
		}
	}
	out := make([]FalcoEvent, 0, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		reporter, _, _ := unstructured.NestedString(it.Object, "reportingController")
		if reporter != "" && !strings.Contains(strings.ToLower(reporter), "falco") {
			continue
		}
		note, _, _ := unstructured.NestedString(it.Object, "note")
		reason, _, _ := unstructured.NestedString(it.Object, "reason")
		typ, _, _ := unstructured.NestedString(it.Object, "type")
		whenStr, _, _ := unstructured.NestedString(it.Object, "eventTime")
		if whenStr == "" {
			whenStr, _, _ = unstructured.NestedString(it.Object, "deprecatedFirstTimestamp")
		}
		ev := FalcoEvent{
			Time:     whenStr,
			Priority: strings.ToUpper(typ),
			Rule:     reason,
			Output:   note,
			Source:   "falcosidekick",
		}
		// Try to surface k8s context from involvedObject.
		ns, _, _ := unstructured.NestedString(it.Object, "regarding", "namespace")
		name, _, _ := unstructured.NestedString(it.Object, "regarding", "name")
		kind, _, _ := unstructured.NestedString(it.Object, "regarding", "kind")
		ev.Namespace = ns
		if strings.EqualFold(kind, "Pod") {
			ev.Pod = name
		}
		if ev.Rule == "" && ev.Output == "" {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// HandleComplianceSBOMPod — GET /api/v1/sovereigns/{id}/compliance/sbom?ns=<ns>&pod=<pod>
//
// Returns Trivy-operator vulnerability + SBOM data for one Pod. When
// trivy-operator is not installed the envelope's `installed:false`
// lets the UI render a one-line "not deployed" state.
func (h *Handler) HandleComplianceSBOMPod(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	ns := strings.TrimSpace(r.URL.Query().Get("ns"))
	pod := strings.TrimSpace(r.URL.Query().Get("pod"))
	if ns == "" || pod == "" {
		http.Error(w, "missing required query params: ns + pod", http.StatusBadRequest)
		return
	}

	resp := SBOMPodResponse{
		Pod:              pod,
		Namespace:        ns,
		Containers:       []SBOMContainerEntry{},
		CountsByContainer: map[string]VulnerabilitySeverityCounts{},
		UpdatedAt:        time.Now().UTC().Format(time.RFC3339),
		Installed:        false,
	}

	dep, ok := h.lookupDeploymentForInfra(clusterID)
	if !ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil || dyn == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// Trivy operator labels reports `trivy-operator.resource.name=<pod>`
	// and `trivy-operator.resource.kind=Pod`. List by labelSelector to
	// avoid a full-namespace scan.
	selector := "trivy-operator.resource.name=" + pod
	vrList, err := dyn.Resource(VulnerabilityReportGVR()).Namespace(ns).List(r.Context(), metav1.ListOptions{
		LabelSelector: selector,
	})
	if err != nil {
		// Not installed or RBAC-blocked; surface the empty shape.
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Installed = true
	sbomList, _ := dyn.Resource(SBOMReportGVR()).Namespace(ns).List(r.Context(), metav1.ListOptions{
		LabelSelector: selector,
	})

	// Build per-container entries from VR + SBOM reports.
	byContainer := map[string]*SBOMContainerEntry{}
	for i := range vrList.Items {
		vr := &vrList.Items[i]
		container := labelOr(vr.GetLabels(), "trivy-operator.container.name")
		if container == "" {
			continue
		}
		entry := byContainer[container]
		if entry == nil {
			entry = &SBOMContainerEntry{Container: container, ReportName: vr.GetName()}
			byContainer[container] = entry
		}
		image, _, _ := unstructured.NestedString(vr.Object, "report", "artifact", "repository")
		tag, _, _ := unstructured.NestedString(vr.Object, "report", "artifact", "tag")
		digest, _, _ := unstructured.NestedString(vr.Object, "report", "artifact", "digest")
		if image != "" {
			if tag != "" {
				entry.Image = image + ":" + tag
			} else {
				entry.Image = image
			}
		}
		entry.Digest = digest
		when, _, _ := unstructured.NestedString(vr.Object, "report", "updateTimestamp")
		entry.ScanCompletedAt = when

		// `report.summary.criticalCount` etc.
		critCount, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "criticalCount")
		highCount, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "highCount")
		mediumCount, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "mediumCount")
		lowCount, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "lowCount")
		unkCount, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "unknownCount")
		entry.Severity = VulnerabilitySeverityCounts{
			Critical: int(critCount),
			High:     int(highCount),
			Medium:   int(mediumCount),
			Low:      int(lowCount),
			Unknown:  int(unkCount),
		}
		entry.Severity.Total = entry.Severity.Critical + entry.Severity.High + entry.Severity.Medium + entry.Severity.Low + entry.Severity.Unknown
	}
	// Merge SBOM components into the per-container entries.
	for i := range sbomList.Items {
		sb := &sbomList.Items[i]
		container := labelOr(sb.GetLabels(), "trivy-operator.container.name")
		if container == "" {
			continue
		}
		entry := byContainer[container]
		if entry == nil {
			entry = &SBOMContainerEntry{Container: container}
			byContainer[container] = entry
		}
		comps, _, _ := unstructured.NestedSlice(sb.Object, "report", "components", "components")
		for _, c := range comps {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			comp := SBOMComponent{
				Name:    strString(cm["name"]),
				Version: strString(cm["version"]),
				Type:    strString(cm["type"]),
				PURL:    strString(cm["bom-ref"]),
			}
			if lics, ok := cm["licenses"].([]any); ok && len(lics) > 0 {
				var names []string
				for _, l := range lics {
					if lm, ok := l.(map[string]any); ok {
						if name := strString(lm["name"]); name != "" {
							names = append(names, name)
						} else if id := strString(lm["id"]); id != "" {
							names = append(names, id)
						}
					}
				}
				comp.Licenses = strings.Join(names, ",")
			}
			entry.Components = append(entry.Components, comp)
		}
	}

	// Flatten + tally totals.
	containerNames := make([]string, 0, len(byContainer))
	for n := range byContainer {
		containerNames = append(containerNames, n)
	}
	sort.Strings(containerNames)
	for _, n := range containerNames {
		entry := byContainer[n]
		resp.Containers = append(resp.Containers, *entry)
		resp.CountsByContainer[n] = entry.Severity
		resp.TotalCounts.Critical += entry.Severity.Critical
		resp.TotalCounts.High += entry.Severity.High
		resp.TotalCounts.Medium += entry.Severity.Medium
		resp.TotalCounts.Low += entry.Severity.Low
		resp.TotalCounts.Unknown += entry.Severity.Unknown
	}
	resp.TotalCounts.Total = resp.TotalCounts.Critical + resp.TotalCounts.High + resp.TotalCounts.Medium + resp.TotalCounts.Low + resp.TotalCounts.Unknown
	writeJSON(w, http.StatusOK, resp)
}

// HandleComplianceSBOMSummary — GET /api/v1/sovereigns/{id}/compliance/sbom/summary
//
// Cluster-wide CVE rollup for the AppDetail Compliance tab + the
// Security-Lead dashboard. Aggregates VulnerabilityReport CRs by
// namespace + image.
func (h *Handler) HandleComplianceSBOMSummary(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	resp := SBOMSummaryResponse{
		ByNamespace: map[string]VulnerabilitySeverityCounts{},
		ByImage:     map[string]VulnerabilitySeverityCounts{},
		Installed:   false,
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	dep, ok := h.lookupDeploymentForInfra(clusterID)
	if !ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil || dyn == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	vrList, err := dyn.Resource(VulnerabilityReportGVR()).List(r.Context(), metav1.ListOptions{})
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Installed = true
	podSet := map[string]struct{}{}
	for i := range vrList.Items {
		vr := &vrList.Items[i]
		ns := vr.GetNamespace()
		lbls := vr.GetLabels()
		podName := labelOr(lbls, "trivy-operator.resource.name")
		container := labelOr(lbls, "trivy-operator.container.name")
		if podName != "" {
			podSet[ns+"/"+podName] = struct{}{}
		}
		image, _, _ := unstructured.NestedString(vr.Object, "report", "artifact", "repository")
		tag, _, _ := unstructured.NestedString(vr.Object, "report", "artifact", "tag")
		if tag != "" {
			image = image + ":" + tag
		}
		crit, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "criticalCount")
		high, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "highCount")
		med, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "mediumCount")
		low, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "lowCount")
		unk, _, _ := unstructured.NestedInt64(vr.Object, "report", "summary", "unknownCount")
		_ = container
		bump := func(c *VulnerabilitySeverityCounts) {
			c.Critical += int(crit)
			c.High += int(high)
			c.Medium += int(med)
			c.Low += int(low)
			c.Unknown += int(unk)
			c.Total += int(crit + high + med + low + unk)
		}
		t := resp.ByNamespace[ns]
		bump(&t)
		resp.ByNamespace[ns] = t
		if image != "" {
			t := resp.ByImage[image]
			bump(&t)
			resp.ByImage[image] = t
		}
		bump(&resp.Total)
		resp.Containers++
	}
	resp.Pods = len(podSet)
	writeJSON(w, http.StatusOK, resp)
}

// ── Helpers ──────────────────────────────────────────────────────────

func parsePositiveIntQuery(r *http.Request, key string, def, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func parsePrioritiesQuery(raw string) map[string]struct{} {
	if raw == "" {
		return map[string]struct{}{
			"EMERGENCY": {}, "ALERT": {}, "CRITICAL": {}, "ERROR": {}, "WARNING": {},
		}
	}
	out := map[string]struct{}{}
	for _, s := range strings.Split(raw, ",") {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		out[s] = struct{}{}
	}
	return out
}

func strString(v any) string {
	s, _ := v.(string)
	return s
}

// HandleCompliancePolicyByName — GET /api/v1/sovereigns/{id}/compliance/policies/{name}
//
// Returns one PolicyView for a single ClusterPolicy by name. Looks up
// the live ClusterPolicy in the Sovereign cluster (bypassing the
// cached aggregator so the response always reflects what's actually
// installed). Used by PolicyDrilldownPage's per-name fallback when
// the cached bulk list is missing the requested policy (C11-003 fix).
//
// Returns 404 + `{"error": "not found"}` when the named ClusterPolicy
// does not exist in the cluster. Returns 200 + full PolicyView when it
// does.
func (h *Handler) HandleCompliancePolicyByName(w http.ResponseWriter, r *http.Request) {
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing sovereign id", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		http.Error(w, "missing policy name", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	dep, ok := h.lookupDeploymentForInfra(clusterID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		return
	}
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil || dyn == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cluster client unavailable"})
		return
	}

	// First try the live ClusterPolicy registry (Kyverno).
	item, err := dyn.Resource(ClusterPolicyGVR()).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil || item == nil {
		// Not found in live cluster — surface 404 so UI renders the
		// canonical "not found" empty state.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "policy not found"})
		return
	}
	view := projectClusterPolicyToView(item)
	if view.Name == "" {
		view.Name = name
	}
	writeJSON(w, http.StatusOK, view)
}

// projectClusterPolicyToView maps an unstructured Kyverno ClusterPolicy
// onto a PolicyView (mirrors listLivePoliciesFromCluster's projection).
// Extracted into its own function so HandleCompliancePolicyByName can
// reuse it without dragging in the full bulk-list code path.
func projectClusterPolicyToView(item *unstructured.Unstructured) PolicyView {
	v := PolicyView{Source: "kyverno", Weight: 1, Scope: "all"}
	v.Name = item.GetName()
	annotations := item.GetAnnotations()
	policyLabels := item.GetLabels()
	mode := "permissive"
	if vfa, found, _ := unstructured.NestedString(item.Object, "spec", "validationFailureAction"); found {
		switch strings.ToLower(strings.TrimSpace(vfa)) {
		case "enforce":
			mode = "enforcing"
		case "audit":
			mode = "permissive"
		}
	}
	v.Mode = mode
	var rules []string
	if specRules, found, _ := unstructured.NestedSlice(item.Object, "spec", "rules"); found {
		for _, r := range specRules {
			if rm, ok := r.(map[string]any); ok {
				if name, ok := rm["name"].(string); ok && name != "" {
					rules = append(rules, name)
				}
			}
		}
	}
	v.Rules = rules
	if title, ok := annotations["policies.kyverno.io/title"]; ok && title != "" {
		v.Title = title
	}
	if category, ok := annotations["policies.kyverno.io/category"]; ok && category != "" {
		v.Category = category
	}
	if severity, ok := annotations["policies.kyverno.io/severity"]; ok && severity != "" {
		v.Severity = severity
	}
	if desc, ok := annotations["policies.kyverno.io/description"]; ok && desc != "" {
		v.Description = desc
	}
	if tier, ok := policyLabels["compliance-tier"]; ok && tier != "" {
		v.Scope = tier
	}
	return v
}

// listFalcoPods returns the names + namespace of the Falco DaemonSet
// Pods. Returns (nil, "", false) when the falco-system namespace
// doesn't exist (Falco not installed).
func listFalcoPods(ctx context.Context, dyn dynamic.Interface) ([]string, string, bool) {
	// Look for the DaemonSet first in canonical `falco-system` then
	// `falco` then `security` (chart author's choice may vary).
	candidates := []string{"falco-system", "falco", "security"}
	for _, ns := range candidates {
		podGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
		list, err := dyn.Resource(podGVR).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: "app.kubernetes.io/name=falco",
		})
		if err != nil || len(list.Items) == 0 {
			continue
		}
		names := make([]string, 0, len(list.Items))
		for i := range list.Items {
			names = append(names, list.Items[i].GetName())
		}
		return names, ns, true
	}
	return nil, "", false
}

