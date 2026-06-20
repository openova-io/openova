// Package handler — tenant_discover.go: public tenant discovery
// endpoint backing host-header-driven SPA bootstrap (issue #802).
//
// The same Sovereign Console SPA bundle (products/catalyst/bootstrap/ui)
// serves both otech-admin and Organization-admin views. On boot the SPA reads
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
	"os"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// tenantDiscoverResponse is the wire shape returned to the SPA on
// 200 OK. It is intentionally a public-safe subset of
// store.TenantRegistration — the Organization-admin admin-API URL (used by the
// hook) is NOT exposed here.
//
// Fields added in qa-loop iter-6 TC-013 (DeploymentID, TenantHost,
// Realm, OIDCIssuer) are camelCase aliases for the existing snake_case
// keys so both legacy SPA bootstrap callers and the QA matrix's
// target-state keyword set resolve against the same payload without a
// breaking rename.
type tenantDiscoverResponse struct {
	Host             string           `json:"host"`
	TenantID         string           `json:"tenant_id"`
	TenantKind       store.TenantKind `json:"tenant_kind"`
	KeycloakRealmURL string           `json:"keycloak_realm_url"`
	KeycloakClientID string           `json:"keycloak_client_id"`

	// DeploymentID — Sovereign deployment id this tenant resolves to.
	// On a chroot Sovereign self-discovery branch this is the same id
	// HandleSovereignSelf reports (synthesized as `sovereign-<fqdn>`
	// when the orchestrator hasn't stamped CATALYST_SELF_DEPLOYMENT_ID
	// yet). Required by qa-loop iter-6 TC-013.
	DeploymentID string `json:"deploymentId,omitempty"`

	// TenantHost — camelCase alias of Host. Same value; emitted as a
	// separate key so the QA matrix's keyword assertion on
	// "tenantHost" + "deploymentId" matches without breaking the
	// legacy `host` consumer.
	TenantHost string `json:"tenantHost,omitempty"`

	// Realm — Keycloak realm name (the trailing path segment of
	// KeycloakRealmURL). Convenience field for the SPA bootstrap path
	// that needs the realm name without parsing the URL.
	Realm string `json:"realm,omitempty"`

	// OIDCIssuer — same value as KeycloakRealmURL; emitted under the
	// canonical OIDC vocabulary so SPA / SDK code that bootstraps via
	// `issuer` resolves directly.
	OIDCIssuer string `json:"oidcIssuer,omitempty"`
}

// HandleTenantDiscover serves GET /api/v1/tenant/discover?host=<host>.
//
// The host can also be implicit — when the `?host=` query param is
// missing or empty the handler falls back to the request's Host
// header. The pre-auth bootstrap call is served on the same origin
// as the tenant being looked up, so the Host header already names
// it; requiring an explicit query param broke any caller that relied
// on Host-only discovery (TC-R-045 regression).
//
// 200 → registered tenant (subset of TenantRegistration above).
// 404 → host not in the registry.
// 400 → both `?host=` AND r.Host are empty (only when an HTTP client
//
//	elides the Host header entirely; conformant clients always
//	send one).
//
// 503 → registry not wired (catalyst-api running without the store
//
//	initialised; rare — only test/CI without a writable PVC).
func (h *Handler) HandleTenantDiscover(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.URL.Query().Get("host"))
	if host == "" {
		// Fall back to the request's Host header (HTTP/1.1 Host:, or
		// HTTP/2 :authority — both surfaced through r.Host). The
		// upstream proxy chain (Envoy / Traefik) preserves the
		// originator's Host so this resolves to the tenant the operator
		// actually visited.
		host = strings.TrimSpace(r.Host)
	}
	// qa-loop iter-6 TC-013 — the matrix probes
	// `?email=<addr>` (no `host=`) and expects the chroot to
	// self-discover from the request authority. When the email param
	// is set but host is still empty AFTER the Host fallback, that's
	// fine — Host header always carries something in practice. The
	// email is parsed for the domain only as a last-resort hint
	// (the matrix's emrah.baysal@openova.io maps to no specific
	// tenant; the chroot's SOVEREIGN_FQDN owns the response).
	if host == "" {
		if email := strings.TrimSpace(r.URL.Query().Get("email")); email != "" {
			if at := strings.LastIndexByte(email, '@'); at >= 0 && at+1 < len(email) {
				host = strings.TrimSpace(email[at+1:])
			}
		}
	}
	if host == "" {
		writeBadRequest(w, "host-required", "?host= query parameter is required when the request carries no Host header")
		return
	}

	// Strip port if the SPA passed `console.example.com:443` — the
	// registry is keyed by hostname only.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}

	// ── Chroot self-discovery branch ────────────────────────────────────
	//
	// qa-loop iter-6 (TC-013) live failure on console.omantel.biz: the
	// chroot's tenant registry is empty (the cutover orchestrator
	// hasn't POSTed a TenantRegistration back to the Sovereign yet,
	// and on BYO it never will). Without a fallback every visitor
	// to /api/v1/tenant/discover sees 404 tenant-not-registered and
	// the SPA bootstrap path can't resolve OIDC config.
	//
	// On a chroot we already KNOW which tenant we're serving — the
	// Pod was launched with SOVEREIGN_FQDN env (canonical) and
	// CATALYST_OTECH_FQDN (legacy alias) injected from the
	// bp-catalyst-platform sovereign-fqdn ConfigMap. When the
	// requested host matches that FQDN (or the conventional
	// `console.<fqdn>` fronting), synthesize the discovery payload
	// from the chroot's own config. The payload mirrors what the
	// orchestrator WOULD have POSTed: realm `openova` (chart
	// default), oidcIssuer `https://console.<fqdn>/auth/realms/openova`,
	// deploymentId derived as `sovereign-<fqdn>` (matches
	// HandleSovereignSelf's stable-id convention so the SPA's
	// subsequent /sovereigns/{id}/* calls resolve to the same
	// chroot k8s alias).
	//
	// This branch runs BEFORE the registry lookup so a chroot
	// without tenantRegistry wired (test envs, early cutover) still
	// returns 200 for its own host.
	if resp, ok := chrootSelfDiscover(host); ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	if h.tenantRegistry == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "tenant-registry-unavailable",
			"detail": "catalyst-api was started without a tenant registry; /tenant/discover disabled",
		})
		return
	}

	t, ok := h.tenantRegistry.Get(host)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "tenant-not-registered",
			"detail": "no tenant registered for host " + host,
		})
		return
	}

	realm := realmFromIssuer(t.KeycloakRealmURL)
	writeJSON(w, http.StatusOK, tenantDiscoverResponse{
		Host:             t.Host,
		TenantID:         t.TenantID,
		TenantKind:       t.TenantKind,
		KeycloakRealmURL: t.KeycloakRealmURL,
		KeycloakClientID: t.KeycloakClientID,
		DeploymentID:     t.TenantID,
		TenantHost:       t.Host,
		Realm:            realm,
		OIDCIssuer:       t.KeycloakRealmURL,
	})
}

// chrootSelfDiscover returns a synthesized tenantDiscoverResponse
// when `host` matches the chroot Sovereign's own FQDN (or the
// `console.<fqdn>` fronting). Returns (zero, false) when this Pod is
// not running as a chroot or the host doesn't match. Caller MUST
// fall through to the normal registry lookup on false.
//
// The realm + clientID values mirror the bp-catalyst-platform chart
// defaults baked into every Sovereign rollout: realm "openova",
// client "catalyst-ui". A future BYO-realm Sovereign can override
// via CATALYST_DISCOVERY_REALM / CATALYST_DISCOVERY_CLIENT_ID env
// without changing this signature.
func chrootSelfDiscover(host string) (tenantDiscoverResponse, bool) {
	fqdn := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if fqdn == "" {
		fqdn = strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	}
	if fqdn == "" {
		return tenantDiscoverResponse{}, false
	}
	h := strings.ToLower(host)
	f := strings.ToLower(fqdn)
	matches := h == f ||
		h == "console."+f ||
		strings.HasSuffix(h, "."+f)
	if !matches {
		return tenantDiscoverResponse{}, false
	}
	realm := envOrTrim("CATALYST_DISCOVERY_REALM", "openova")
	clientID := envOrTrim("CATALYST_DISCOVERY_CLIENT_ID", "catalyst-ui")
	// Issuer URL: chart convention is
	// https://console.<fqdn>/auth/realms/<realm>. Override via
	// CATALYST_DISCOVERY_ISSUER for non-default fronting.
	issuer := envOrTrim("CATALYST_DISCOVERY_ISSUER", "https://console."+f+"/auth/realms/"+realm)
	deploymentID := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID"))
	if deploymentID == "" {
		// Match the stable-id convention from sovereign_self.go so
		// downstream /sovereigns/{id}/* resolves to the same chroot
		// k8s alias.
		deploymentID = "sovereign-" + f
	}
	tenantHost := host
	return tenantDiscoverResponse{
		Host:             tenantHost,
		TenantID:         deploymentID,
		TenantKind:       store.TenantKindOTECH,
		KeycloakRealmURL: issuer,
		KeycloakClientID: clientID,
		DeploymentID:     deploymentID,
		TenantHost:       tenantHost,
		Realm:            realm,
		OIDCIssuer:       issuer,
	}, true
}

// realmFromIssuer extracts the trailing realm segment from a Keycloak
// realm URL (`https://.../auth/realms/<realm>`). Returns the URL
// unchanged on no-match so the caller always has a non-empty realm
// hint.
func realmFromIssuer(issuer string) string {
	if issuer == "" {
		return ""
	}
	// Strip trailing slash for tolerance.
	s := strings.TrimRight(issuer, "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

// SetTenantRegistry wires a tenant registry into the Handler. Called
// by main.go at startup; tests inject directly.
func (h *Handler) SetTenantRegistry(reg *store.TenantRegistry) {
	h.tenantRegistry = reg
}
