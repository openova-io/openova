package handlers

import "net/http"

// Routes returns an http.Handler with all tenant endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public — slug availability check.
	mux.HandleFunc("GET /tenant/check-slug/{slug}", h.CheckSlug)

	// Authenticated — organization CRUD.
	mux.HandleFunc("POST /tenant/orgs", h.CreateOrg)
	mux.HandleFunc("GET /tenant/orgs", h.ListOrgs)
	mux.HandleFunc("GET /tenant/orgs/{id}", h.GetOrg)
	mux.HandleFunc("PUT /tenant/orgs/{id}", h.UpdateOrg)
	mux.HandleFunc("DELETE /tenant/orgs/{id}", h.DeleteOrg)

	// Authenticated — member management.
	mux.HandleFunc("GET /tenant/orgs/{id}/members", h.ListMembers)
	mux.HandleFunc("POST /tenant/orgs/{id}/members", h.InviteMember)
	mux.HandleFunc("DELETE /tenant/orgs/{id}/members/{userId}", h.RemoveMember)

	// Authenticated — day-2 app install/uninstall.
	mux.HandleFunc("POST /tenant/orgs/{id}/apps", h.InstallApp)
	mux.HandleFunc("DELETE /tenant/orgs/{id}/apps/{slug}", h.UninstallApp)
	// Preview: what data will be purged vs retained before an uninstall. Feeds
	// the console's confirm modal so the user sees exactly what they're
	// about to lose before clicking through.
	mux.HandleFunc("GET /tenant/orgs/{id}/apps/{slug}/uninstall-preview", h.UninstallPreview)

	// Authenticated — backing-service inventory per tenant (databases, caches).
	// Metadata comes from the catalog; pod status is proxied from provisioning.
	mux.HandleFunc("GET /tenant/orgs/{id}/backing-services", h.ListBackingServices)

	// Internal — unauthenticated service-to-service lookups. Expected to be
	// reachable only via the in-cluster service IP (no gateway / ingress),
	// same security model as the catalog HTTP API used by tenant + billing.
	// Only returns the safe public subset; used by billing to enrich
	// order.placed events with the tenant's subdomain (issue #105).
	mux.HandleFunc("GET /tenant/internal/tenants/{id}/subdomain", h.InternalGetSubdomain)

	// Admin — tenant management (superadmin only).
	mux.HandleFunc("GET /tenant/admin/tenants", h.AdminListTenants)
	mux.HandleFunc("GET /tenant/admin/tenants/{id}", h.AdminGetTenant)
	mux.HandleFunc("GET /tenant/admin/tenants/{id}/backing-services", h.AdminListBackingServices)
	mux.HandleFunc("PUT /tenant/admin/tenants/{id}/status", h.AdminUpdateStatus)
	mux.HandleFunc("DELETE /tenant/admin/tenants/{id}", h.AdminDeleteTenant)

	return mux
}
