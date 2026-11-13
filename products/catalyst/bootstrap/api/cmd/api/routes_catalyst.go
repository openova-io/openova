package main

import "github.com/go-chi/chi/v5"

func registerPublicCatalyst(r chi.Router, d routeDeps) {
	h := d.h
	// #3374 — server-side zero-click silent-SSO shim for OpenBao.
	// OpenBao's UI is a client-side SPA, so a static ssoInitPath can only
	// pre-select the OIDC method (still one click). This shim asks Vault
	// for the OIDC auth_url server-side and 302s the browser to Keycloak,
	// giving grafana/harbor parity. The openbao HTTPRoute (bp-openbao)
	// 302s the BARE URL (`/`, `/ui`, `/ui/`) straight here, so the very
	// first request comes from a FRESH browser with NO catalyst_session
	// cookie — therefore this route MUST live OUTSIDE RequireSession.
	// Putting it inside (where it shipped through #3226) made the session
	// middleware reject every bare-URL visit with 401 {"error":
	// "unauthenticated"} before HandleOpenBaoSSOInit ever ran, so the
	// founder-witnessed token form never got bypassed (caught live on
	// hw133). The handler is self-contained and session-free: it resolves
	// the blueprint from the catalog (empty session token tolerated), asks
	// Vault for the auth_url, and 302s to Keycloak — falling back to the
	// app deep-link on any error so it can NEVER 500, and 404ing only when
	// no SSO-enabled openbao endpoint resolves.
	r.Get("/catalyst/v1/apps/{id}/openbao-sso-init", h.HandleOpenBaoSSOInit)
}

func registerSessionCatalyst(rg chi.Router, d routeDeps) {
	h := d.h
	// ── G117.3 (#2742) — endpoint list + silent-SSO Launch URL +
	// multi-instance Application create.
	// Routes match docs/api/catalyst-api-openapi.yaml exactly.
	// See products/catalyst/bootstrap/api/internal/handler/
	// endpoint_handler.go for the per-handler contract.
	rg.Get("/catalyst/v1/apps/{id}/endpoints", h.HandleListAppEndpoints)
	rg.Get("/catalyst/v1/apps/{id}/launch-url", h.HandleGetLaunchURL)
	// NOTE: the OpenBao silent-SSO shim (openbao-sso-init) is
	// registered OUTSIDE this RequireSession group — see the public
	// route above. A fresh browser hitting bao.<fqdn>/ has NO
	// catalyst_session cookie, so RequireSession would 401 it before
	// the shim ever ran (#3374 root cause).
	rg.Post("/catalyst/v1/apps/instances", h.HandleCreateInstance)
	rg.Get("/catalyst/v1/catalog/{blueprint}/instances", h.HandleListBlueprintInstances)
}
