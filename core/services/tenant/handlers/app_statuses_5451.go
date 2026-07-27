package handlers

import (
	"net/http"

	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/openova-io/openova/core/services/tenant/catalog"
)

// AppStatus carries live runtime state for one installed application (#5451).
//
// The per-Organization console derives its INSTALLED badge and its Open button
// from the Organization record's Apps list. That list records what the customer
// *purchased* — it says nothing about whether the workload actually serves. On
// hw290 four applications (listmonk, umami, uptime-kuma, vaultwarden) were
// badged INSTALLED with live Open buttons while every one of them returned 503
// with zero Service endpoints.
//
// That failure mode is worse than an outage: every other defect on that
// environment was recoverable once seen, and this one is what prevented it
// being seen. Pillar 1's terminal acceptance is "a customer's purchased
// application actually serving" — a console that asserts this has happened when
// it has not is asserting the pillar itself.
//
// The platform already knows how to answer the question. The provisioning
// service computes pod readiness by slug for backing services; that endpoint is
// generic (it matches pods by name prefix against whatever `services=` list it
// is handed) and was simply never asked about applications — only about the
// databases and caches the customer never sees.
type AppStatus struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	// PodStatus is "Running" | "Pending" | "Failed" | "not_found" | "unknown".
	// "unknown" means we could not reach the provisioning service — it is NOT
	// a synonym for healthy, and callers must not render it as such.
	PodStatus     string `json:"pod_status"`
	ReadyReplicas int    `json:"ready_replicas"`
	TotalReplicas int    `json:"total_replicas"`
}

// ListAppStatuses handles GET /organizations/{id}/app-statuses.
//
// Returns one row per installed non-service application. Backing services are
// deliberately excluded — they are already covered by the backing-services
// endpoint, and they have no Open button to mislead anyone.
//
// Membership-gated on the same reasoning as ListBackingServices: the payload is
// read-only runtime state, and any team member debugging a dead app needs it.
//
// Failure is never fatal. If provisioning is unreachable, every row reports
// "unknown" and the console degrades to "can't currently confirm" — which is
// the honest answer, and still strictly better than asserting INSTALLED.
func (h *Handler) ListAppStatuses(w http.ResponseWriter, r *http.Request) {
	orgID := r.PathValue("id")
	if _, ok := h.requireMembership(w, r, orgID); !ok {
		return
	}

	org, err := h.Store.GetTenant(r.Context(), orgID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to load organization")
		return
	}
	if org == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}
	if h.Catalog == nil {
		respond.Error(w, http.StatusNotImplemented, "catalog client not configured")
		return
	}

	apps, err := h.Catalog.ListApps(r.Context())
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to reach catalog")
		return
	}
	byID := make(map[string]*catalog.App, len(apps))
	for i := range apps {
		byID[apps[i].ID] = &apps[i]
	}

	installed := selectOpenableApps(org.Apps, byID)
	if len(installed) == 0 {
		respond.OK(w, map[string]any{"apps": []AppStatus{}})
		return
	}

	slugs := make([]string, 0, len(installed))
	for _, a := range installed {
		slugs = append(slugs, a.Slug)
	}
	statuses := h.fetchPodStatuses(r.Context(), org.Subdomain, slugs)

	respond.OK(w, map[string]any{"apps": buildAppStatusRows(installed, statuses)})
}

// selectOpenableApps narrows the Organization's Apps list to the applications
// the console renders an Open button for. Backing services are excluded: they
// are covered by the backing-services endpoint and have no Open button, so a
// wrong badge on one cannot mislead a customer about a purchase.
func selectOpenableApps(appIDs []string, byID map[string]*catalog.App) []*catalog.App {
	seen := make(map[string]bool)
	var out []*catalog.App
	for _, appID := range appIDs {
		a, ok := byID[appID]
		if !ok || isServiceApp(a) || seen[a.Slug] {
			continue
		}
		seen[a.Slug] = true
		out = append(out, a)
	}
	return out
}

// buildAppStatusRows joins the installed applications against live pod state.
//
// An application with no entry in `statuses` reports "unknown" — the runtime
// could not be reached. That is deliberately NOT a healthy-looking value: the
// whole defect in #5451 was a surface that resolved missing information to
// green. Callers must render "unknown" as unconfirmed, never as serving.
func buildAppStatusRows(installed []*catalog.App, statuses map[string]podStatus) []AppStatus {
	out := make([]AppStatus, 0, len(installed))
	for _, a := range installed {
		row := AppStatus{ID: a.ID, Slug: a.Slug, PodStatus: "unknown"}
		if st, ok := statuses[a.Slug]; ok {
			row.PodStatus = st.PodStatus
			row.ReadyReplicas = st.ReadyReplicas
			row.TotalReplicas = st.TotalReplicas
		}
		out = append(out, row)
	}
	return out
}
