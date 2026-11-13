package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func registerPublicAuth(r chi.Router, d routeDeps) {
	h := d.h
	// Unauthenticated auth endpoints — 6-digit PIN issue + verify (#688)
	// and logout. These MUST remain outside the session gate (the user is
	// not yet authenticated when they hit /pin/issue and /pin/verify).
	//
	// PIN auth replaces the magic-link flow rejected by the founder
	// (looks like phishing, 2026-05-03):
	//   POST /api/v1/auth/pin/issue   — generate + email a 6-digit code
	//   POST /api/v1/auth/pin/verify  — validate code + set session cookie
	//
	// The legacy /auth/callback and /auth/magic routes are kept as 302
	// redirects to /login so any cached Keycloak redirect_uri bookmarks
	// or stale magic-link emails degrade gracefully.
	r.Post("/api/v1/auth/pin/issue", h.HandlePinIssue)
	r.Post("/api/v1/auth/pin/verify", h.HandlePinVerify)

	// QA-loop iter-11 Cluster-A: tier-scoped session minting.
	// POST /api/v1/auth/test-session?tier=<viewer|developer|operator|admin|owner>
	// Gated by env CATALYST_TEST_SESSION_ENABLED — returns 404 to
	// the public when unset (production-safe). On QA Sovereigns
	// (chroot, omantel-style) the env is set to "true" so the
	// 5-agent QA executor can mint per-tier session cookies and
	// assert the tier-boundary 403/200 contract on every privileged
	// endpoint. See handler/auth_test_session.go for the rationale.
	r.Post("/api/v1/auth/test-session", h.HandleAuthTestSession)
	r.Get("/api/v1/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login?error=flow_changed", http.StatusFound)
	})
	r.Get("/api/v1/auth/magic", func(w http.ResponseWriter, r *http.Request) {
		// Legacy magic-link callback — replaced by PIN auth in #688.
		// Redirect to login so cached email links degrade gracefully.
		http.Redirect(w, r, "/login?error=flow_changed", http.StatusFound)
	})
	r.Delete("/api/v1/auth/session", h.HandleAuthLogout)
	// POST /auth/session — SPA-driven logout (qa-loop iter-4
	// auth-session-logout-405). DELETE remains for backwards-compat;
	// POST is the canonical SPA path because some proxies strip
	// body+credentials from DELETE on cross-origin XHR.
	r.Post("/api/v1/auth/session", h.HandleAuthSessionLogout)

	// Sovereign-side handover receiver (issue #606). The operator's
	// browser arrives at GET /auth/handover?token=<jwt> from the
	// Catalyst-Zero wizard. The JWT IS the auth — there is NO
	// catalyst_session cookie yet (the handler MINTS one). Therefore
	// this route MUST live outside RequireSession. Putting it inside
	// rejected every handover with 401 {"error":"unauthenticated"}
	// before AuthHandover ever ran (caught live on otech49 2026-05-03).
	// AuthHandover validates the RS256 JWT against the public-key file,
	// creates the user in the Sovereign Keycloak, exchanges for a
	// session, sets HttpOnly Secure SameSite=Lax cookies, and redirects
	// to /sovereign/dashboard. The cookies it sets are what gates every
	// subsequent /sovereign/api/v1/* call inside the RequireSession group.
	r.Get("/auth/handover", h.AuthHandover)

	// Secure marketplace→console session handoff (issue #4182 + #4186).
	// The marketplace post-checkout redirect lands the browser here with
	// the Org member-session token (HS256, signed with the Org mesh's
	// org-services-secrets/JWT_SECRET). AuthOrgHandover validates it against
	// CATALYST_ORG_JWT_SECRET, resolves the Org from the request host,
	// mints an RS256 Org-scoped catalyst_session, sets the HttpOnly Secure
	// SameSite=Lax cookie, stamps Referrer-Policy: no-referrer, and 302s to
	// a clean /jobs. Like AuthHandover above, the token IS the auth (no
	// catalyst_session cookie exists yet — this handler MINTS one), so the
	// route MUST live OUTSIDE the RequireSession group below; inside it
	// every handoff would 401 {"error":"unauthenticated"} before the
	// handler ever ran. This replaces the old token-in-URL handoff that
	// leaked the bearer+refresh via history/logs/Referer (#4182) and never
	// established a cookie, bouncing /jobs→/login (#4186).
	r.Get("/auth/org-handover", h.AuthOrgHandover)
}
