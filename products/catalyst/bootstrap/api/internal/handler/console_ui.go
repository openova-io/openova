// console_ui.go — Wave 5.69b (#2375, EPIC #1099 #1097): expose installed
// Blueprints' spec.consoleUI.sidebarEntry registrations so the
// Sovereign-console SovereignSidebar.tsx can subscribe + append nav
// entries between hardcoded BSS (order=60) and Settings (pinned last).
//
// Wave 5.69 (#2370 → PR #2374) shipped the Blueprint CRD field. This
// handler reads installed Blueprint CRs from the k8scache, projects
// the consoleUI sub-tree, filters opt-in entries, and returns the
// JSON list sorted by sidebarOrder ASC.
//
// Wire surface:
//
//	GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries
//
// Response envelope:
//
//	{
//	  "entries": [
//	    { "id": "bp-chepherd", "label": "Chepherd", "route": "/apps/bp-chepherd/dashboard", "order": 50, "icon": "<svg-path>" },
//	    ...
//	  ]
//	}
//
// Empty list when no Blueprint opts in. RBAC: same chi.Group as the
// other read-only k8s/* handlers — RequireSession middleware gates.
package handler

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// SidebarEntry is the wire shape for one entry.
type SidebarEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Route string `json:"route"`
	Order int    `json:"order"`
	Icon  string `json:"icon,omitempty"`
}

// HandleConsoleUISidebarEntries — GET .../console-ui/sidebar-entries.
func (h *Handler) HandleConsoleUISidebarEntries(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID := chi.URLParam(r, "id")
	if clusterID == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	clusterID = h.resolveChrootClusterID(clusterID)

	blueprints, _, err := h.k8sCache.List(clusterID, "blueprint", labels.Everything())
	if err != nil {
		http.Error(w, fmt.Sprintf("list blueprints: %v", err), http.StatusBadGateway)
		return
	}

	entries := make([]SidebarEntry, 0, len(blueprints))
	for _, bp := range blueprints {
		entry, ok := projectSidebarEntry(bp)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Order < entries[j].Order
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"entries": entries,
	})
}

// projectSidebarEntry extracts the SidebarEntry projection from a
// Blueprint unstructured. Returns (entry, true) only when the
// Blueprint opts in via spec.consoleUI.sidebarEntry=true.
func projectSidebarEntry(bp *unstructured.Unstructured) (SidebarEntry, bool) {
	if bp == nil {
		return SidebarEntry{}, false
	}
	consoleUI, found, err := unstructured.NestedMap(bp.Object, "spec", "consoleUI")
	if err != nil || !found || consoleUI == nil {
		return SidebarEntry{}, false
	}
	optIn, _, _ := unstructured.NestedBool(consoleUI, "sidebarEntry")
	if !optIn {
		return SidebarEntry{}, false
	}
	label, _, _ := unstructured.NestedString(consoleUI, "sidebarLabel")
	route, _, _ := unstructured.NestedString(consoleUI, "sidebarRoute")
	icon, _, _ := unstructured.NestedString(consoleUI, "sidebarIcon")
	// Wave 5.69d (#2396 reviewer fix): honor CRD-applied default
	// instead of post-projection coalesce. The CRD declares
	// `sidebarOrder: { default: 50, minimum: 0 }` so the apiserver
	// populates 50 on missing fields. An explicit `0` (used to pin
	// above all hardcoded entries) was being silently rewritten to
	// 50 here — a real bug for authors who want top-pin behaviour.
	orderInt64, _, _ := unstructured.NestedInt64(consoleUI, "sidebarOrder")
	order := int(orderInt64)
	if label == "" {
		label = bp.GetName()
	}
	if route == "" {
		route = fmt.Sprintf("/apps/%s", bp.GetName())
	}
	return SidebarEntry{
		ID:    bp.GetName(),
		Label: label,
		Route: route,
		Order: order,
		Icon:  icon,
	}, true
}
