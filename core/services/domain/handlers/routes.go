package handlers

import "net/http"

// Routes returns an http.Handler with all domain endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Subdomain registration (requires auth via gateway).
	mux.HandleFunc("POST /domain/subdomains", h.RegisterSubdomain)

	// BYOD registration (requires auth via gateway).
	mux.HandleFunc("POST /domain/byod", h.RegisterBYOD)

	// List domains for a tenant (requires auth via gateway).
	mux.HandleFunc("GET /domain/domains/{tenantId}", h.ListDomains)

	// Delete a domain (requires auth via gateway).
	mux.HandleFunc("DELETE /domain/domains/{id}", h.DeleteDomain)

	// Verify BYOD DNS configuration (requires auth via gateway).
	mux.HandleFunc("POST /domain/verify/{id}", h.VerifyDNS)

	// Check subdomain availability (public).
	mux.HandleFunc("GET /domain/check/{subdomain}/{tld}", h.CheckAvailability)

	return mux
}
