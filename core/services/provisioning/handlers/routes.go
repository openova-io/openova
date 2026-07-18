package handlers

import "net/http"

// Routes returns an http.Handler with all provisioning endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public — provision status polling.
	mux.HandleFunc("GET /provisioning/status/{id}", h.GetStatus)
	mux.HandleFunc("GET /provisioning/tenant/{tenantId}", h.GetByTenant)
	// Public — #5205 same-origin readiness proxy for the marketplace funnel
	// completion interstitial (LaunchingStep.svelte). Must be public: it is
	// polled before the customer has any session, from the SAME marketplace
	// origin, replacing the old cross-origin no-cors browser probe that could
	// never read the per-Org console's actual readiness.
	mux.HandleFunc("GET /provisioning/console-ready", h.ConsoleReady)

	// Admin — manual provisioning and listing.
	mux.HandleFunc("POST /provisioning/start", h.Start)
	mux.HandleFunc("GET /provisioning/admin/list", h.List)

	// Internal — day-2 apply endpoints. The tenant service calls these after
	// persisting its tenant.Apps change; they mirror the event-bus path so
	// the pipeline works when RedPanda is unavailable.
	mux.HandleFunc("POST /provisioning/apps/install", h.ApplyAppInstall)
	mux.HandleFunc("POST /provisioning/apps/uninstall", h.ApplyAppUninstall)

	// Day-2 jobs — install / uninstall records the console Jobs page renders
	// alongside the initial provision.
	mux.HandleFunc("GET /provisioning/jobs", h.ListJobs)
	// Internal — backing-service pod status used by the tenant service to
	// build the per-tenant "Backing services" view. Safe to keep under the
	// authenticated /provisioning/ prefix at the gateway (no PII in the
	// response, but the kube-API data is non-public).
	mux.HandleFunc("GET /provisioning/backing-services", h.GetTenantBackingServices)

	return mux
}
