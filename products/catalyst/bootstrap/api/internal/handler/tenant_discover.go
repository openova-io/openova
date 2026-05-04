// Package handler — tenant_discover.go: public tenant discovery
// endpoint backing host-header-driven SPA bootstrap (issue #802).
//
// The same Sovereign Console SPA bundle (products/catalyst/bootstrap/ui)
// serves both otech-admin and SME-admin views. On boot the SPA reads
// `window.location.host` and calls
//
//	GET /api/v1/tenant/discover?host=<host>
//
// to receive the {tenant_id, keycloak_realm_url, keycloak_client_id,
// tenant_kind} payload it needs to bootstrap OIDC against the right
// realm. This is the canonical seam for [Q-mine-1] of #795 — tenancy
// is derived from the host registry, NOT from path/subdomain string
// parsing — so a BYO domain (`console.acme.com`, CNAME-fronted)
// resolves the same way as a free-subdomain
// (`console.acme.<otech-fqdn>`).
//
// The endpoint is PUBLIC (no auth gate) because the SPA hasn't
// authenticated yet at the moment it calls this. The response contains
// only the OIDC client config needed to start the redirect — never any
// admin credentials. A 404 is returned when the host is unknown so a
// random visitor to a non-registered hostname sees the SPA's generic
// "unknown tenant" landing rather than crashing.
package handler

import (
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// tenantDiscoverResponse is the wire shape returned to the SPA on
// 200 OK. It is intentionally a public-safe subset of
// store.TenantRegistration — the SME-admin admin-API URL (used by the
// hook) is NOT exposed here.
type tenantDiscoverResponse struct {
	Host             string            `json:"host"`
	TenantID         string            `json:"tenant_id"`
	TenantKind       store.TenantKind  `json:"tenant_kind"`
	KeycloakRealmURL string            `json:"keycloak_realm_url"`
	KeycloakClientID string            `json:"keycloak_client_id"`
}

// HandleTenantDiscover serves GET /api/v1/tenant/discover?host=<host>.
//
// 200 → registered tenant (subset of TenantRegistration above).
// 400 → host query param missing or empty.
// 404 → host not in the registry.
// 503 → registry not wired (catalyst-api running without the store
//       initialised; rare — only test/CI without a writable PVC).
func (h *Handler) HandleTenantDiscover(w http.ResponseWriter, r *http.Request) {
	if h.tenantRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "tenant-registry-unavailable",
			"detail": "catalyst-api was started without a tenant registry; /tenant/discover disabled",
		})
		return
	}

	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		writeBadRequest(w, "host-required", "?host= query parameter is required")
		return
	}

	// Strip port if the SPA passed `console.example.com:443` — the
	// registry is keyed by hostname only.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}

	t, ok := h.tenantRegistry.Get(host)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "tenant-not-registered",
			"detail": "no tenant registered for host " + host,
		})
		return
	}

	writeJSON(w, http.StatusOK, tenantDiscoverResponse{
		Host:             t.Host,
		TenantID:         t.TenantID,
		TenantKind:       t.TenantKind,
		KeycloakRealmURL: t.KeycloakRealmURL,
		KeycloakClientID: t.KeycloakClientID,
	})
}

// SetTenantRegistry wires a tenant registry into the Handler. Called
// by main.go at startup; tests inject directly.
func (h *Handler) SetTenantRegistry(reg *store.TenantRegistry) {
	h.tenantRegistry = reg
}
