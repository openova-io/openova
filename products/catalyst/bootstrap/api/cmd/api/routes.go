package main

import (
	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handler"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobauth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/oidcprovider"
)

// routeDeps holds every value of main() that a route registration reads,
// in the order main() declares them.
type routeDeps struct {
	corsOrigin   string
	log          *slog.Logger
	h            *handler.Handler
	pprofEnabled bool
	oidcProv     *oidcprovider.Provider
}

// buildRouter builds the router of catalyst-api: the middleware of every
// route, the routes that need no session, then the routes behind the
// session gate. registerPublic<Concern> and registerSession<Concern> in
// routes_<concern>.go hold the routes of one concern: the first path
// segment after /api/v1/; catalyst for /catalyst/v1/; auth for /auth/;
// probes for /healthz, /readyz, /metrics and /debug; oidc for /oidc/.
// TestRoutesGolden pins the table this function builds.
func buildRouter(d routeDeps) chi.Router {
	corsOrigin, log, h := d.corsOrigin, d.log, d.h

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	// #4182: use the path-only formatter, NOT chi's default middleware.Logger.
	// The default emits r.RequestURI verbatim, which leaks every credential
	// carried in a query string to stdout/access logs — the /auth/handover +
	// /auth/org-handover `?token=<jwt>` handoff tokens and the chroot SPA's
	// `?access_token=<jwt>` EventSource tokens. pathOnlyLogFormatter logs the
	// path only (never the query). Credential hygiene per CLAUDE.md §10.
	r.Use(middleware.RequestLogger(pathOnlyLogFormatter{}))
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{corsOrigin},
		// PUT enabled for the cloud-init kubeconfig postback (issue
		// #183) — the new Sovereign's cloud-init writes its own
		// kubeconfig to /api/v1/deployments/{id}/kubeconfig with a
		// bearer token. CORS is irrelevant for that caller (curl
		// from the new VM, not a browser), but enabling PUT here
		// keeps the policy consistent for any future browser-side
		// resume flow that re-uses the same endpoint.
		AllowedMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		// Cookie is required for SameSite=Strict session cookies to be
		// forwarded across the Traefik ingress path (issue #608).
		// X-User-Email + X-User-Sub are injected by the RequireSession
		// middleware into downstream requests.
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization", "Cookie"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	registerPublicProbes(r, d)
	registerPublicVersion(r, d)
	registerPublicAuth(r, d)
	registerPublicOIDC(r, d)
	registerPublicDeployments(r, d)
	registerPublicSubdomains(r, d)
	registerPublicCredentials(r, d)
	registerPublicSSHKey(r, d)
	registerPublicRegistrar(r, d)
	registerPublicTenant(r, d)
	registerPublicSovereign(r, d)
	registerPublicInternal(r, d)
	registerPublicHandover(r, d)
	registerPublicCatalyst(r, d)

	// Auth-gated wizard endpoints — RequireSession validates the
	// HMAC-signed catalyst_session cookie on every request. When
	// cfg is nil (Sovereign clusters, CI without CATALYST_KC_ADDR)
	// the middleware is a transparent passthrough and all requests
	// proceed without any auth check, preserving existing behaviour.
	r.Group(func(rg chi.Router) {
		// The conformance Job of a Sovereign has no session: jobauth admits
		// its ServiceAccount token to the Application routes of Organization
		// conformance and hands every other request to RequireSession.
		rg.Use(jobauth.RequireSessionOrJob(jobauth.InCluster, auth.RequireSession(h.GetAuthConfig(), log), log))
		// #4110 — Org-console scope guard. Transparent passthrough for
		// Sovereign-admin/operator sessions (zero regression); for an
		// Org-scoped customer session (PIN-authenticated on an Org pool-
		// domain console host, e.g. console.demo.omani.homes) it enforces a
		// deny-by-default allowlist so the customer can reach ONLY their own
		// Organization's surfaces — never the deployments API / deployment-
		// stream, the whole-cluster estate, BSS/billing, parent-domains, the
		// cross-org directory, or sovereign settings. Must run AFTER
		// RequireSession (which populates auth.ClaimsFromContext).
		rg.Use(h.OrgScopeGuard)

		registerSessionWhoami(rg, d)
		registerSessionSovereigns(rg, d)
		registerSessionDeployments(rg, d)
		registerSessionRegistrar(rg, d)
		registerSessionHandover(rg, d)
		registerSessionActions(rg, d)
		registerSessionFlows(rg, d)
		registerSessionDashboard(rg, d)
		registerSessionCatalog(rg, d)
		registerSessionFleet(rg, d)
		registerSessionCatalyst(rg, d)
		registerSessionRefs(rg, d)
		registerSessionOrg(rg, d)
		registerSessionOrganizations(rg, d)
		registerSessionBilling(rg, d)
		registerSessionSovereign(rg, d)
	})
	return r
}
