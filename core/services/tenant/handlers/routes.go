package handlers

import (
	"fmt"
	"net/http"
)

// deprecatedAlias wraps a handler registered on a LEGACY route path so the
// response carries RFC-8594 deprecation signalling. #3383 renamed the
// marketplace org-create/list path `POST|GET /tenant/orgs` (exposed by the
// gateway as `/api/tenant/orgs`) to the canonical `/organizations`. The old
// path stays for exactly ONE release so the marketplace SPA keeps working
// across the rename; the removal of these aliases is a checklist row on
// #3383 for next release.
func deprecatedAlias(canonical string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Deprecation", "true")
		w.Header().Set("Sunset", "Wed, 01 Jul 2026 00:00:00 GMT")
		if canonical != "" {
			w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"successor-version\"", canonical))
		}
		next(w, r)
	}
}

// Routes returns an http.Handler with all tenant endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public — slug availability check.
	mux.HandleFunc("GET /tenant/check-slug/{slug}", h.CheckSlug)

	// Authenticated — Organization CRUD (#3383: canonical `/organizations`).
	// The marketplace SPA + console org pages create/list Organizations
	// here; the gateway exposes these as `/api/organizations`.
	mux.HandleFunc("POST /organizations", h.CreateOrg)
	mux.HandleFunc("GET /organizations", h.ListOrgs)
	mux.HandleFunc("GET /organizations/{id}", h.GetOrg)
	mux.HandleFunc("PUT /organizations/{id}", h.UpdateOrg)
	mux.HandleFunc("DELETE /organizations/{id}", h.DeleteOrg)
	// naming-guard: alias — legacy `/tenant/orgs` paths, one-release
	// deprecation (marketplace SPA stays on these until the alias-removal
	// release; each emits a Deprecation/Sunset header).
	mux.HandleFunc("POST /tenant/orgs", deprecatedAlias("/api/organizations", h.CreateOrg))
	mux.HandleFunc("GET /tenant/orgs", deprecatedAlias("/api/organizations", h.ListOrgs))
	mux.HandleFunc("GET /tenant/orgs/{id}", deprecatedAlias("/api/organizations/{id}", h.GetOrg))
	mux.HandleFunc("PUT /tenant/orgs/{id}", deprecatedAlias("/api/organizations/{id}", h.UpdateOrg))
	mux.HandleFunc("DELETE /tenant/orgs/{id}", deprecatedAlias("/api/organizations/{id}", h.DeleteOrg))

	// Authenticated — member management (#3383: canonical `/organizations`).
	mux.HandleFunc("GET /organizations/{id}/members", h.ListMembers)
	mux.HandleFunc("POST /organizations/{id}/members", h.InviteMember)
	mux.HandleFunc("DELETE /organizations/{id}/members/{userId}", h.RemoveMember)

	// Authenticated — day-2 app install/uninstall.
	mux.HandleFunc("POST /organizations/{id}/apps", h.InstallApp)
	mux.HandleFunc("DELETE /organizations/{id}/apps/{slug}", h.UninstallApp)
	// Preview: what data will be purged vs retained before an uninstall. Feeds
	// the console's confirm modal so the user sees exactly what they're
	// about to lose before clicking through.
	mux.HandleFunc("GET /organizations/{id}/apps/{slug}/uninstall-preview", h.UninstallPreview)

	// Authenticated — backing-service inventory per Organization (databases,
	// caches). Metadata comes from the catalog; pod status is proxied from
	// provisioning.
	mux.HandleFunc("GET /organizations/{id}/backing-services", h.ListBackingServices)

	// naming-guard: alias — legacy `/tenant/orgs/{id}/...` sub-resource paths,
	// one-release deprecation. #3383 renamed the member / app / backing-service
	// sub-resources to the canonical `/organizations/{id}/...` block above; the
	// aliases stay for exactly one release so any consumer still pinned to the
	// old path keeps working (each emits a Deprecation/Sunset header). Removal
	// is a checklist row on #3383.
	mux.HandleFunc("GET /tenant/orgs/{id}/members", deprecatedAlias("/api/organizations/{id}/members", h.ListMembers))
	mux.HandleFunc("POST /tenant/orgs/{id}/members", deprecatedAlias("/api/organizations/{id}/members", h.InviteMember))
	mux.HandleFunc("DELETE /tenant/orgs/{id}/members/{userId}", deprecatedAlias("/api/organizations/{id}/members/{userId}", h.RemoveMember))
	mux.HandleFunc("POST /tenant/orgs/{id}/apps", deprecatedAlias("/api/organizations/{id}/apps", h.InstallApp))
	mux.HandleFunc("DELETE /tenant/orgs/{id}/apps/{slug}", deprecatedAlias("/api/organizations/{id}/apps/{slug}", h.UninstallApp))
	mux.HandleFunc("GET /tenant/orgs/{id}/apps/{slug}/uninstall-preview", deprecatedAlias("/api/organizations/{id}/apps/{slug}/uninstall-preview", h.UninstallPreview))
	mux.HandleFunc("GET /tenant/orgs/{id}/backing-services", deprecatedAlias("/api/organizations/{id}/backing-services", h.ListBackingServices))

	// Internal — unauthenticated service-to-service lookups. Expected to be
	// reachable only via the in-cluster service IP (no gateway / ingress),
	// same security model as the catalog HTTP API used by tenant + billing.
	// Only returns the safe public subset; used by billing to enrich
	// order.placed events with the tenant's subdomain (issue #105).
	mux.HandleFunc("GET /tenant/internal/tenants/{id}/subdomain", h.InternalGetSubdomain)
	// TBD-V27 (#2042) — provisioning consumer reads per-app configSchema
	// values (Tenant.AppConfigs) here so dispatchOrderPlaced can attach
	// them to order.placed events; without this the customer-picked
	// replicas/disk_gb/backups_enabled values were dropped between the
	// store (PR #2043) and the rendered manifest.
	mux.HandleFunc("GET /tenant/internal/tenants/{id}/app-configs", h.InternalGetAppConfigs)
	// #4956 — billing calls this after a checkout settles (credit-only or
	// Stripe webhook) to launch a DEFERRED (pending_payment) funnel Org. Gated
	// to in-cluster callers by the gateway 401 on /tenant/internal/* (same
	// posture as the two GETs above), so the browser can never self-launch to
	// skip payment. Idempotent.
	mux.HandleFunc("POST /tenant/internal/tenants/{id}/launch", h.InternalLaunchTenant)

	// Admin — tenant management (superadmin only).
	mux.HandleFunc("GET /tenant/admin/tenants", h.AdminListTenants)
	mux.HandleFunc("GET /tenant/admin/tenants/{id}", h.AdminGetTenant)
	mux.HandleFunc("GET /tenant/admin/tenants/{id}/backing-services", h.AdminListBackingServices)
	mux.HandleFunc("PUT /tenant/admin/tenants/{id}/status", h.AdminUpdateStatus)
	mux.HandleFunc("DELETE /tenant/admin/tenants/{id}", h.AdminDeleteTenant)

	return mux
}
