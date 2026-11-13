package main

import "github.com/go-chi/chi/v5"

func registerPublicTenant(r chi.Router, d routeDeps) {
	h := d.h
	// Unauthenticated tenant-discovery endpoint (issue #802) — the SPA
	// calls this on bootstrap to learn which Keycloak realm to OIDC
	// against for the current `window.location.host`. Returns 404 for
	// unknown hosts (the SPA renders a generic landing) and never
	// exposes admin URLs or credentials. Wired BEFORE the session-cookie
	// gate because the SPA hasn't authenticated yet at the moment it
	// calls this.
	r.Get("/api/v1/tenant/discover", h.HandleTenantDiscover)
}
