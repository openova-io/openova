// sandbox-bridge — sidecar binary serving the
// /admin/tokens/sandbox bridge handler defined in
// platform/newapi/internal/handler (PR #1638 SandboxToken).
//
// Runs as a sidecar in the bp-newapi Pod alongside the upstream
// Calcium-Ion newapi binary + the metering-sidecar. The Pod's
// Service exposes this listener on a second port so the
// sandbox-controller (catalyst-system in each Sovereign's vCluster)
// can POST a per-Sandbox token-mint request without touching the
// upstream binary, which has no such route and returns its default
// HTML 404 page (root cause of Wave 5.57 #2303).
//
// Listener layout
//
//	:8080  POST /admin/tokens/sandbox  → handler.Handler.SandboxToken
//	:8080  GET  /metrics               → promhttp.Handler (scrape target)
//	:8080  GET  /healthz               → 200 OK (kubelet probe)
//	:8080  GET  /                      → handler.SSOInitHandler (#3374
//	                                     zero-click bare-URL SSO landing page).
//	                                     The bp-newapi HTTPRoute sends ONLY the
//	                                     bare root (Exact `/`) here; every other
//	                                     path goes to NewAPI, so this never
//	                                     shadows the SPA / API / OIDC callback.
//	:8080  GET  /login                 → handler.SSOInitHandler (#5612 —
//	                                     the same landing page also answers
//	                                     `/login`, the recovery path NewAPI's
//	                                     own SPA bounces an expired session to.
//	                                     Its "Continue with OpenOva SSO" button
//	                                     is inert (upstream SPA bug, cannot be
//	                                     patched — pinned mirror); the chart's
//	                                     HTTPRoute sends an Exact `/login` match
//	                                     here ONLY when SSO is fully configured
//	                                     (sovereignFQDN set), so a full
//	                                     navigation/reload of the expired-
//	                                     session page runs the deterministic
//	                                     OIDC round-trip instead of the dead
//	                                     button.
//
// Env contract
//
//	NEWAPI_TOKEN_SIGNING_KEY  required — HS256 mint key (same Secret
//	                          as the upstream Calcium-Ion binary).
//	NEWAPI_ADMIN_SECRET       required — bearer the sandbox-controller
//	                          presents. Same Secret, `ADMIN_SECRET` key.
//	BRIDGE_LISTEN             optional — listen address, default ":8080".
//	NEWAPI_SSO_INIT_SLUG      optional — custom_oauth_providers.slug the
//	                          #3374 landing page initiates (default
//	                          "sovereign"). The bp-newapi admin-sso-seed-job
//	                          seeds the provider under this slug.
//	NEWAPI_SSO_AUTHORIZE_URL  optional — the Keycloak authorize endpoint the
//	                          #3374 landing page redirects to (carries
//	                          ?kc_idp_hint=catalyst-pin). 0.1.16: the page
//	                          builds the redirect DIRECTLY from this instead of
//	                          discovering it from /api/status (v0.13.2 does NOT
//	                          expose custom_oauth_providers there). Empty ⇒ the
//	                          page degrades to the SPA /login link.
//	NEWAPI_SSO_CLIENT_ID      optional — the Keycloak client_id (default
//	                          "newapi-admin"). Empty ⇒ degrade to /login.
//	NEWAPI_SSO_SCOPES         optional — OAuth scope string (default
//	                          "openid profile email groups").
//
// Refs #2303, Wave 5.57; #3374 (2026-06-14) zero-click SSO landing;
// #3374 0.1.16 (2026-06-15) deterministic redirect (no /api/status discovery).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/openova-io/openova/platform/newapi/internal/handler"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	h, err := handler.NewHandlerFromEnv(log)
	if err != nil {
		log.Error("bridge: config error at start", "err", err)
		os.Exit(1)
	}

	addr := os.Getenv("BRIDGE_LISTEN")
	if addr == "" {
		addr = ":8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/tokens/sandbox", h.SandboxToken)
	mux.Handle("/metrics", handler.MetricsHandler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// #3374 zero-click bare-URL SSO landing page, ALSO answering `/login`
	// (#5612 — the recovery path for an expired session, whose own
	// "Continue with OpenOva SSO" button is inert). Registered LAST on both
	// the bare-root and `/login` paths; the bp-newapi HTTPRoute's Exact `/`
	// and Exact `/login` rules are what actually steer these two paths to
	// this sidecar (the SPA, API, and OIDC callback keep flowing to NewAPI
	// for everything else — including `/login` on an overlay where SSO is
	// unconfigured, since the chart only adds the `/login` HTTPRoute match
	// once sovereignFQDN is set). SSOInitHandler itself 404s any path other
	// than "/" or "/login" as a belt-and-braces guard.
	ssoInit := handler.SSOInitHandler(handler.SSOInitConfig{
		Slug:         os.Getenv("NEWAPI_SSO_INIT_SLUG"),
		AuthorizeURL: os.Getenv("NEWAPI_SSO_AUTHORIZE_URL"),
		ClientID:     os.Getenv("NEWAPI_SSO_CLIENT_ID"),
		Scopes:       os.Getenv("NEWAPI_SSO_SCOPES"),
	})
	mux.HandleFunc("/", ssoInit)
	mux.HandleFunc("/login", ssoInit)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("bridge: listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("bridge: ListenAndServe", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("bridge: shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}
