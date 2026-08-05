// sso_init.go — #3374 zero-click bare-URL SSO landing page.
//
// NewAPI (Calcium-Ion/new-api) is consumed as a PINNED MIRROR, not a fork —
// its embedded React SPA serves a marketing/login homepage at `/` and only
// fires the OIDC flow when the operator CLICKS "Sign in with <provider>"
// (web/src/helpers/api.js onCustomOAuthClicked → /api/oauth/state then
// window.location.assign to the Keycloak authorize URL). That click breaks
// the #3374 zero-click contract: a bare URL must land the owner signed-in
// with NO click.
//
// We cannot patch the embedded SPA (mirror, not fork) and oauth2-proxy is the
// WRONG tool here — NewAPI authenticates ONLY via its own DB-backed session
// cookie set by its /api/oauth/<slug> callback and IGNORES proxy-auth headers
// (middleware/auth.go reads session.Get("id") only), so a front-door proxy
// would gate the user but NewAPI would still show its own login page. The
// authorize redirect also CANNOT be a static gateway 302, because NewAPI's
// callback hard-403s on a CSRF `state` that must first be minted by
// GET /api/oauth/state (which sets the session cookie) — verified live on
// hw136 (a bare GET to /api/oauth/sovereign returns 403 MsgOAuthStateInvalid).
//
// So the only mechanism that respects every constraint is a SAME-ORIGIN init
// page that runs NewAPI's own init sequence: (1) fetch /api/oauth/state →
// state + session cookie (same-origin, so the cookie lands on
// newapi.<fqdn>), (2) redirect to the Keycloak authorize URL (which carries
// kc_idp_hint=catalyst-pin → silent broker, no KC login form) with
// redirect_uri=${origin}/oauth/<slug>&state=<state>. Keycloak returns to the
// SPA callback /oauth/<slug>?code&state, the SPA posts /api/oauth/<slug>, the
// session cookie is present → CSRF passes → NewAPI mints its session → the
// owner lands in /console signed-in. The bp-newapi admin-sso-seed-job's
// promote step (keyed on user_oauth_bindings) elevates the owner to root.
//
// ── #3374 0.1.16 (2026-06-15): why the prior page was UNRELIABLE ──────────
// The original init page DISCOVERED the authorize endpoint + client_id at
// runtime by reading `pj.data.custom_oauth_providers` out of GET /api/status.
// Measured live on hw138 (dep 4b5ff7852e33fc15): NewAPI v0.13.2's /api/status
// payload does NOT contain a `custom_oauth_providers` field AT ALL — it only
// exposes the BUILT-IN provider flags (oidc_enabled / github_oauth / …, all
// false on this Sovereign). So `provs` was ALWAYS [], the provider was never
// found, the page fell through to /login, and the SPA bounced the
// unauthenticated owner to the /setup wizard — exactly the symptom the bare
// URL showed (even though the DB was correctly seeded: setups row present,
// catalyst-root role=100, the `sovereign` custom_oauth_providers row enabled).
// The runtime-discovery dependency was the whole defect.
//
// ROBUST FIX: stop discovering. The chart already KNOWS the authorize
// endpoint, client_id and scopes (the bp-newapi admin-sso-seed-job seeds the
// identical values into custom_oauth_providers). Pass them to this sidecar via
// env (NEWAPI_SSO_AUTHORIZE_URL / NEWAPI_SSO_CLIENT_ID / NEWAPI_SSO_SCOPES) so
// the page builds the authorize redirect DIRECTLY from data it controls. The
// ONLY runtime call left is GET /api/oauth/state (proven to return
// {"success":true,"data":...} + set the session cookie live on hw138), which
// is structurally required for NewAPI's callback CSRF check and cannot be
// elided. If the authorize URL / client_id are not configured (older overlay),
// the page degrades to a graceful /login link instead of a broken redirect.
//
// This handler is mounted at `/` on the sandbox-bridge sidecar (already an
// openova-io-globbed pod sidecar, so it satisfies kyverno harbor-proxy-pull
// without a new image). The bp-newapi HTTPRoute routes ONLY the bare root
// (Exact `/`) to the bridge; every other path (the SPA, /assets, /api,
// /oauth/<slug> callback, /console) continues to NewAPI, so this page never
// shadows the app. Refs #3374.
//
// ── #3563 0.1.17 (2026-06-15): zero-click RE-login (the "already bound" loop) ──
// The prior page ALWAYS ran the OAuth flow on every bare-URL hit. That works on
// the FIRST visit (binds the SSO identity to a NewAPI user + logs in) but FAILS
// on every subsequent visit with "This OpenOva SSO account has already been
// bound" — measured live on hw139 (dep c89aa7059556b342). ROOT CAUSE is in
// upstream NewAPI v0.13.2 controller/oauth.go HandleOAuth, which picks bind-vs-
// login purely off the session:
//
//	username := session.Get("username")
//	if username != nil { handleOAuthBind(c, provider); return } // BIND mode
//	// else: findOrCreateOAuthUser → logs the EXISTING binding's user in
//
// handleOAuthBind rejects with MsgOAuthAlreadyBound when the identity is already
// taken (provider.IsUserIDTaken). GenerateOAuthCode (/api/oauth/state) sets
// `oauth_state` but NEVER clears `username`. So on the 2nd visit the browser
// still carries the 1st visit's valid session cookie (username set) → the
// callback lands in BIND mode → "already bound" → NewAPI mints no NEW session
// relative to /console → the SPA bounces to /login?expired=true. NewAPI is a
// pinned MIRROR (not a fork), so the binary cannot be patched; the fix lives
// here, the only OpenOva-owned seam.
//
// FIX — make the bare-URL contract idempotent:
//  1. GET /api/user/self first. If it returns success:true the owner ALREADY
//     has a valid session (they ARE signed in) → redirect straight to /console.
//     This is the common re-visit path: instant, NO Keycloak round-trip, the
//     literal "URL → signed in as admin".
//  2. Else (401 / not authenticated) GET /api/user/logout (upstream Logout does
//     session.Clear()+Save()) to wipe any stale `username`, THEN run the OAuth
//     flow. With `username` cleared, HandleOAuth takes the LOGIN branch
//     (findOrCreateOAuthUser logs the existing user in) instead of the BIND
//     branch — so a re-auth after a session expiry also lands signed-in instead
//     of looping on "already bound".
//
// Both calls are same-origin (the page is served at newapi.<fqdn>/), so the
// session cookie is in scope. The logout is best-effort: a failure still falls
// through to the OAuth flow (no worse than before). Refs #3563, #3374.
//
// ── #3374 (2026-06-18): 1st-hit 111 + /setup on a fresh prov ──────────────
// Re-walked live on hw159: the bare URL's 1st hit returned
// `upstream-connect-error ... delayed connect error: 111` and the 2nd hit
// landed on the /setup wizard instead of signed-in /console. The 111 is NOT a
// landing-page bug — it is the gateway finding no healthy upstream for the
// Exact `/` route while NewAPI warms up (~2min: the wait-for-sql-dsn
// initContainer + GORM AutoMigrate). As a plain container the bridge (a) did
// not even START until the DSN gate completed, and (b) had its Service
// endpoint suppressed because a Pod only joins a Service EndpointSlice once
// ALL its containers are Ready and the `newapi` container is NotReady during
// migrate. The REAL fix lives in the chart: the bridge is now a NATIVE sidecar
// (starts first, before the DSN gate) behind a bridge-only Service with
// publishNotReadyAddresses:true (so its endpoint publishes the instant it
// binds, independent of `newapi`). With those in place the landing page LOADS
// on the 1st hit; this file's job is then to ride out NewAPI's own warm-up:
//   - the /api/oauth/state mint RETRIES (8× / 1.5s) so the first visit during
//     convergence still completes the silent-SSO redirect; and
//   - on retry-exhaustion (NewAPI still not up) the page RELOADS ITSELF rather
//     than falling through to /login — because the SPA's /login bounces an
//     unseeded NewAPI to /setup. So the first human visit never dead-ends at
//     the setup wizard. (An overlay with no SSO configured still degrades to
//     /login, since reloading would loop forever with nothing to land on.)
// Refs #3374.
//
// ── #5612 (2026-08-05): the inert "Continue with OpenOva SSO" RECOVERY button ──
// newapi's own session TTL is under an hour (facet 1 of #5612 — a separate
// session-lifetime issue, not addressed here). When the session lapses
// mid-visit, NewAPI's embedded SPA client-routes to `/login?expired=true`
// and offers a "Continue with OpenOva SSO" button. Live evidence on hw292:
// clicking it does NOTHING — no navigation, no /api/oauth/state call, no
// network request at all. Root cause is the SAME class of defect already
// diagnosed above for the bare-root page: the SPA's onCustomOAuthClicked
// handler (web/src/helpers/api.js) needs a `custom_oauth_providers` entry to
// act on, and NewAPI v0.13.2's discovery surface never exposes one to the
// client at `/login` render time (the exact gap the 0.1.16 fix above routed
// AROUND for `/`, rather than fixed — the SPA is a pinned mirror, not a
// fork, so its button-click JS cannot be patched here).
//
// FIX: apply the SAME workaround NewAPI's `/` never needed a click for —
// serve THIS deterministic, chart-driven landing page for `/login` too. The
// chart's HTTPRoute (templates/httproute.yaml) now sends an Exact `/login`
// match to this same bridge, so any FULL navigation to the expired-session
// page (a reload, a bookmark, a hard `window.location` redirect, or the
// User simply retyping the URL after the button no-ops) runs the identical
// self-check -> state-mint -> authorize-redirect chain that already
// recovers the bare root — no dependency on the SPA's broken button at all.
// The HTTPRoute only adds this second Exact match when `sovereignFQDN` is
// set (mirrors the gate on NEWAPI_SSO_AUTHORIZE_URL below), so an
// unconfigured overlay never routes `/login` here — see the fallback()
// same-path guard below for why that gate matters even with it in place.
// Refs #5612.
package handler

import (
	"html/template"
	"net/http"
	"strings"
)

// ssoInitTemplate is the zero-click landing page. On each hit it first checks
// whether the visitor already has a valid NewAPI session (GET /api/user/self)
// and, if so, redirects straight to /console — the idempotent re-login path
// (#3563). Otherwise it clears any stale session (GET /api/user/logout, so the
// callback takes NewAPI's login branch rather than the bind branch that errors
// "already bound"), mints NewAPI's CSRF state via /api/oauth/state, then
// redirects to the chart-provided Keycloak authorize URL — no runtime provider
// discovery. Kept dependency-free (vanilla fetch + a <noscript> fallback link
// to the SPA login) so it works in any browser and degrades safely. All
// operationally-meaningful values are injected by the handler as JS string
// literals (html/template JS-context escaped). When AuthorizeURL/ClientID are
// empty the script logs and falls back to /login rather than building a broken
// redirect.
var ssoInitTemplate = template.Must(template.New("sso-init").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Signing you in…</title>
<style>
  html,body{height:100%;margin:0}
  body{display:flex;align-items:center;justify-content:center;
       font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;
       color:#334155;background:#f8fafc}
  .box{text-align:center}
  .spinner{width:34px;height:34px;margin:0 auto 14px;border:3px solid #cbd5e1;
           border-top-color:#2563eb;border-radius:50%;animation:spin .8s linear infinite}
  @keyframes spin{to{transform:rotate(360deg)}}
  a{color:#2563eb}
</style>
</head>
<body>
<div class="box">
  <div class="spinner"></div>
  <div>Signing you in…</div>
  <noscript><p>JavaScript is required. <a href="/login">Continue to sign in</a>.</p></noscript>
</div>
<script>
(async function () {
  var SLUG = {{ .Slug }};
  var AUTHORIZE_URL = {{ .AuthorizeURL }}; // KC authorize endpoint, carries ?kc_idp_hint=catalyst-pin
  var CLIENT_ID = {{ .ClientID }};
  var SCOPES = {{ .Scopes }};
  // #5612 - this same handler now also serves /login (the recovery path
  // for an expired session). If SSO is unconfigured (AUTHORIZE_URL/CLIENT_ID
  // empty) fallback() would normally send the visitor to /login - but when
  // THIS page IS /login that would loop forever. The chart only routes
  // /login here when SSO IS configured (httproute.yaml gates the Exact
  // /login match on sovereignFQDN, the same signal that populates
  // AUTHORIZE_URL/CLIENT_ID), so this branch should be unreachable in
  // practice; it stays as a belt-and-braces guard against ever redirecting a
  // page to itself.
  function fallback(){
    if (window.location.pathname === "/login") {
      document.title = "Sign-in unavailable";
      document.querySelector(".box").innerHTML =
        "<div>OpenOva SSO is not available right now. Please contact your administrator.</div>";
      return;
    }
    window.location.replace("/login");
  }
  // #3374 (2026-06-18) — self-reload instead of falling through to /login when
  // NewAPI is configured for SSO but still WARMING UP (DSN-gate + GORM migrate,
  // up to ~2min on a fresh prov). The SPA's /login bounces an unseeded NewAPI
  // to the /setup wizard — the exact hw159 symptom. Reloading THIS bare-root
  // page keeps re-running the whole chain (self-check → state mint → redirect)
  // until NewAPI is up, so the FIRST human visit never dead-ends at /setup. We
  // only reach this when AUTHORIZE_URL+CLIENT_ID ARE set (SSO IS configured),
  // so the loop is bounded by NewAPI coming up; an unconfigured overlay takes
  // fallback() instead and never reloads.
  function reloadSelf(){ setTimeout(function(){ window.location.replace("/"); }, 2000); }
  // #3563 — idempotent re-login. If the visitor ALREADY holds a valid NewAPI
  // session they ARE signed in; send them straight to the app. This is the
  // common re-visit path and avoids the Keycloak round-trip entirely. Only
  // GET /api/user/self with a live session returns success:true (it is behind
  // UserAuth → 401 when unauthenticated), so this never false-positives.
  try {
    var me = await fetch("/api/user/self", { credentials: "include" });
    if (me.ok) {
      var mj = await me.json();
      if (mj && mj.success) { window.location.replace("/console"); return; }
    }
  } catch (e) { /* not signed in / unreachable — fall through to OAuth */ }
  // The chart must inject the authorize URL + client_id (the bp-newapi
  // admin-sso-seed-job seeds the identical values). Without them we cannot
  // build a valid redirect — degrade to the SPA login rather than loop.
  if (!AUTHORIZE_URL || !CLIENT_ID) return fallback();
  try {
    // #3563 — clear any STALE session before re-authenticating. NewAPI's
    // HandleOAuth routes to the BIND path (which errors "already bound") when
    // session.Get("username") is set, and /api/oauth/state never clears it. A
    // best-effort logout (upstream Logout = session.Clear()+Save()) guarantees
    // the callback takes the LOGIN branch (findOrCreateOAuthUser logs the
    // existing user in). Errors are ignored — worst case is the prior behaviour.
    try { await fetch("/api/user/logout", { credentials: "include" }); } catch (e) {}
    // 1. Mint the CSRF state (also sets the same-origin session cookie that
    //    NewAPI's /api/oauth/<slug> callback checks). This is the ONLY
    //    runtime dependency and is structurally required.
    //
    // #3374 (2026-06-18) — RETRY before falling back. On a fresh prov NewAPI's
    // SQL_DSN initContainer + GORM AutoMigrate keep the backend unreachable for
    // up to ~2min after the gateway first routes here (the bare-URL hit returns
    // an upstream-connect-error 111 / the /api/oauth/state fetch fails). The
    // prior code fell through to /login on the FIRST failure, and the NewAPI SPA
    // then showed the /setup wizard — the exact hw159 symptom (bare URL → /setup
    // instead of signed-in). A few short retries ride out NewAPI's warm-up so
    // the FIRST human visit during convergence still lands signed-in instead of
    // dead-ending at /setup. Same-origin, idempotent; if it never recovers we
    // still degrade to /login (no worse than before).
    var state = null;
    for (var attempt = 0; attempt < 8; attempt++) {
      try {
        var sr = await fetch("/api/oauth/state", { credentials: "include" });
        if (sr.ok) {
          var sj = await sr.json();
          if (sj && sj.success && sj.data) { state = sj.data; break; }
        }
      } catch (e) { /* NewAPI still warming up — retry */ }
      await new Promise(function (r) { setTimeout(r, 1500); });
    }
    // #3374 (2026-06-18) — exhausted the state-mint retries while SSO IS
    // configured ⇒ NewAPI is still warming up. RELOAD this page (keep trying)
    // instead of falling through to /login (which the SPA bounces to /setup on
    // an unseeded NewAPI — the hw159 symptom). The bare URL therefore never
    // lands the first visitor on the setup wizard during convergence.
    if (!state) return reloadSelf();
    // 2. Build the authorize URL EXACTLY like the SPA does
    //    (web/src/helpers/api.js onCustomOAuthClicked: redirect_uri =
    //    window.location.origin + "/oauth/" + slug) and redirect. The
    //    token-exchange redirect_uri (ServerAddress + "/oauth/" + slug, seeded
    //    to the same origin) byte-matches this per RFC-6749 §4.1.3.
    var redirectUri = window.location.origin + "/oauth/" + SLUG;
    var sep = AUTHORIZE_URL.indexOf("?") === -1 ? "?" : "&";
    var scope = encodeURIComponent(SCOPES || "openid profile email groups");
    var url = AUTHORIZE_URL + sep +
      "client_id=" + encodeURIComponent(CLIENT_ID) +
      "&redirect_uri=" + encodeURIComponent(redirectUri) +
      "&response_type=code" +
      "&scope=" + scope +
      "&state=" + encodeURIComponent(state);
    window.location.replace(url);
  } catch (e) {
    fallback();
  }
})();
</script>
</body>
</html>`))

// SSOInitConfig carries the operationally-meaningful knobs so nothing is
// hardcoded (Inviolable Principle #4). Slug selects the provider callback
// path; AuthorizeURL/ClientID/Scopes drive the deterministic redirect.
type SSOInitConfig struct {
	// Slug is the custom_oauth_providers.slug the bp-newapi admin-sso-seed-job
	// seeds (default "sovereign"). It forms the SPA callback redirect_uri
	// ${origin}/oauth/<slug>.
	Slug string
	// AuthorizeURL is the Keycloak authorize endpoint the bp-newapi
	// admin-sso-seed-job seeds into custom_oauth_providers.authorization_endpoint
	// — e.g. https://auth.<fqdn>/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin.
	// When empty the page degrades to the SPA /login link.
	AuthorizeURL string
	// ClientID is the Keycloak client_id (default "newapi-admin"). When empty
	// the page degrades to the SPA /login link.
	ClientID string
	// Scopes is the OAuth scope string (default "openid profile email groups").
	Scopes string
}

// jsLiteral renders s as a safe quoted JS string literal for the html/template
// JS context (so a value containing a quote cannot break out of the literal).
func jsLiteral(s string) template.JS {
	return template.JS("\"" + template.JSEscapeString(s) + "\"")
}

// ssoInitAllowedPaths are the ONLY two paths this handler ever serves. `/` is
// the #3374 zero-click bare-URL landing page; `/login` is the #5612 recovery
// path for an expired session (NewAPI's own "Continue with OpenOva SSO"
// button on `/login?expired=true` is inert — see the file header). Any other
// path 404s so a misconfigured HTTPRoute (sending more than these two paths
// here) fails loud instead of shadowing the SPA.
var ssoInitAllowedPaths = map[string]bool{"/": true, "/login": true}

// SSOInitHandler returns an http.HandlerFunc that serves the zero-click
// landing page for EXACTLY the bare root path and, per #5612, the `/login`
// recovery path. It 404s any other path so a misconfigured route (sending
// more than `/` or `/login` here) fails loud instead of shadowing the SPA.
// Method is GET/HEAD only.
func SSOInitHandler(cfg SSOInitConfig) http.HandlerFunc {
	slug := strings.TrimSpace(cfg.Slug)
	if slug == "" {
		slug = "sovereign"
	}
	authorizeURL := strings.TrimSpace(cfg.AuthorizeURL)
	clientID := strings.TrimSpace(cfg.ClientID)
	scopes := strings.TrimSpace(cfg.Scopes)
	if scopes == "" {
		scopes = "openid profile email groups"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !ssoInitAllowedPaths[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		// All values are rendered via html/template into a JS string context
		// as quoted JS literals — break-out safe.
		_ = ssoInitTemplate.Execute(w, struct {
			Slug         template.JS
			AuthorizeURL template.JS
			ClientID     template.JS
			Scopes       template.JS
		}{
			Slug:         jsLiteral(slug),
			AuthorizeURL: jsLiteral(authorizeURL),
			ClientID:     jsLiteral(clientID),
			Scopes:       jsLiteral(scopes),
		})
	}
}
