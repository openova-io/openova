// openbao_sso_init.go — #3226 server-side zero-click silent-SSO shim that
// gives OpenBao grafana/harbor parity.
//
// Why a server-side shim is required (PROVEN in #3150 + the issue):
//
//   - Grafana `/login/generic_oauth` and Harbor `/c/oidc/login` are
//     SERVER-side GET routes that 302 to Keycloak's authorize endpoint.
//     A static ssoInitPath pointing at those routes auto-redirects, so the
//     console Open button lands the operator already signed-in.
//
//   - OpenBao's UI is a client-side SPA (Vault-UI / Ember fork). OIDC is
//     started in JavaScript, so there is no server GET route to land on.
//     The best a static ssoInitPath (`/ui/vault/auth?with=oidc`) can do is
//     PRE-SELECT the OIDC method in the UI — the operator STILL has to click
//     "Sign in with OIDC". That is the one-click gap #3226 closes.
//
// What this shim does, replicating server-side what those apps do natively:
//
//  1. server-side POST to the Vault OIDC auth_url API
//     (`POST <vaultAddr>/v1/auth/<mount>/oidc/auth_url` with
//     {role, redirect_uri:<the Vault UI callback>}) using the in-cluster
//     Vault address (the openbao client already wired for the handover
//     receiver);
//  2. read the returned `auth_url` (the Keycloak authorize URL — already
//     carries kc_idp_hint=catalyst-pin via the realm IDP defaultProvider
//     binding, NOT appended here);
//  3. 302-redirect the browser to that auth_url.
//
// So: console Open → this shim → Keycloak silent PIN → Vault callback →
// landed signed-in. The openbao blueprint's ssoInitPath is pointed at this
// shim (relative path on the console origin) so the Open button calls it.
//
// Failure path (Inviolable: the Open button must NEVER 500): if the
// auth_url POST fails for any reason (Vault sealed, oidc method not mounted
// yet, openbao client unwired on Catalyst-Zero), the shim 302-redirects to
// the app's deep-link (`https://<host>/ui/vault/auth?with=oidc`) so the
// operator at least lands on the pre-selected OIDC method — the legacy
// one-click behaviour — instead of an error page.
package handler

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
)

// HandleOpenBaoSSOInit — GET /catalyst/v1/apps/{id}/openbao-sso-init
//
// Resolves the openbao endpoint (the same HR-backed blueprint resolution
// the launch-url handler uses — openbao installs as a HelmRelease with no
// Application CR), computes the Vault UI OIDC callback, asks Vault for the
// auth_url, and 302s the browser there. Falls back to the app deep-link on
// any error; 404s only when no SSO-enabled openbao endpoint resolves.
func (h *Handler) HandleOpenBaoSSOInit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Resolve the blueprint the same way launch-url does: openbao ships as
	// an HR with no Application CR, so treat {id} as the blueprint name
	// ("openbao" or "bp-openbao").
	bp := strings.TrimPrefix(strings.TrimSpace(id), "bp-")
	bpDoc, bpErr := h.fetchBlueprint(r.Context(), r, bp)
	if bpErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "blueprint-unavailable",
			"message": bpErr.Error(),
		})
		return
	}

	ep := pickEndpoint(bpDoc, "")
	if ep == nil || !ep.SSOEnabled {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"code":    "endpoint-not-found",
			"message": fmt.Sprintf("Blueprint %q has no SSO-enabled endpoint for the OpenBao SSO-init shim", bp),
		})
		return
	}

	tls := true
	if ep.TLS != nil {
		tls = *ep.TLS
	}
	scheme := "https"
	if !tls {
		scheme = "http"
	}
	// This shim only ever serves Sovereign-singleton HR-backed apps (openbao
	// et al.), so there is no Org context: OrgSlug/OrgDomain are empty and a
	// template that references either FAILS here rather than redirecting the
	// browser to a host with an unsubstituted token in it (#5389).
	hostname, hostErr := resolveHostnameTemplate(ep.HostnameTemplate, hostnameVars{
		SovereignFQDN: h.endpointSovereignFQDN(),
		AppName:       bp,
	})
	if hostErr != nil {
		h.log.Warn("openbao-sso-init: hostnameTemplate unresolved; refusing to redirect to a dead host",
			"app", bp, "endpoint", ep.Name, "hostnameTemplate", ep.HostnameTemplate, "error", hostErr.Error())
		writeJSON(w, http.StatusConflict, map[string]string{
			"code": "hostname-unresolved",
			"message": fmt.Sprintf("endpoint %q hostnameTemplate %q could not be resolved: %v",
				ep.Name, ep.HostnameTemplate, hostErr),
		})
		return
	}

	// The deep-link fallback is the blueprint's own ssoInitPath rendered
	// against the resolved host. This is exactly what the legacy launch-url
	// produced — pre-selects the OIDC method, one click.
	deepLink := buildLaunchURL(hostname, tls, ep.SSOInitPath)
	if deepLink == "" {
		// No ssoInitPath declared → fall back to the app root with the
		// legacy silent-SSO query (buildLaunchURL handles empty path).
		deepLink = fmt.Sprintf("%s://%s/", scheme, hostname)
	}

	// No Vault client (Catalyst-Zero / not a handover target) → deep-link.
	bao := h.openbao
	if bao == nil {
		http.Redirect(w, r, deepLink, http.StatusFound)
		return
	}

	mount := openbaoOIDCMount()
	role := openbaoOIDCRole()
	callback := computeVaultUICallback(scheme, hostname, mount)

	authURL, err := bao.OIDCAuthURL(r.Context(), mount, role, callback)
	if err != nil {
		// Vault sealed / oidc method not mounted / transport error — the
		// Open button must never 500. Fall back to the deep-link.
		h.log.Warn("openbao-sso-init: auth_url failed, falling back to deep-link",
			"app", bp, "error", err.Error())
		http.Redirect(w, r, deepLink, http.StatusFound)
		return
	}

	http.Redirect(w, r, authURL, http.StatusFound)
}

// computeVaultUICallback builds the Vault-UI OIDC callback redirect_uri.
// The Vault web UI completes the OIDC round-trip at
// `https://<host>/ui/vault/auth/<mount>/oidc/callback` — sending the
// browser back into the SPA which then exchanges the code. This must match
// the redirect_uri allow-listed on the Vault OIDC role + the Keycloak
// client, otherwise Keycloak rejects the authorize request.
func computeVaultUICallback(scheme, host, mount string) string {
	mount = strings.Trim(strings.TrimSpace(mount), "/")
	if mount == "" {
		mount = "oidc"
	}
	return fmt.Sprintf("%s://%s/ui/vault/auth/%s/oidc/callback", scheme, host, mount)
}

// openbaoOIDCMount reads the Vault OIDC auth-method mount path from env
// (Inviolable Principle #4 — env-driven, no hardcoded value), defaulting
// to the canonical "oidc" mount the sso-configure Deployment enables.
func openbaoOIDCMount() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_OPENBAO_OIDC_MOUNT")); v != "" {
		return v
	}
	return "oidc"
}

// openbaoOIDCRole reads the Vault OIDC role name from env, defaulting to
// "operator" — the role the openbao sso-configure Deployment creates
// (`auth/oidc/role/operator`, default_role=operator). The OIDC auth_url
// API requires an existing role; "operator" is the only one the bootstrap
// kit provisions.
func openbaoOIDCRole() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_OPENBAO_OIDC_ROLE")); v != "" {
		return v
	}
	return "operator"
}
