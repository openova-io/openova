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
package handler

import (
	"html/template"
	"net/http"
	"strings"
)

// ssoInitTemplate is the zero-click landing page. It mints NewAPI's CSRF
// state via /api/oauth/state, then redirects to the chart-provided Keycloak
// authorize URL — no runtime provider discovery. Kept dependency-free
// (vanilla fetch + a <noscript> fallback link to the SPA login) so it works
// in any browser and degrades safely. All operationally-meaningful values are
// injected by the handler as JS string literals (html/template JS-context
// escaped). When AuthorizeURL/ClientID are empty the script logs and falls
// back to /login rather than building a broken redirect.
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
  function fallback(){ window.location.replace("/login"); }
  // The chart must inject the authorize URL + client_id (the bp-newapi
  // admin-sso-seed-job seeds the identical values). Without them we cannot
  // build a valid redirect — degrade to the SPA login rather than loop.
  if (!AUTHORIZE_URL || !CLIENT_ID) return fallback();
  try {
    // 1. Mint the CSRF state (also sets the same-origin session cookie that
    //    NewAPI's /api/oauth/<slug> callback checks). This is the ONLY
    //    runtime dependency and is structurally required.
    var sr = await fetch("/api/oauth/state", { credentials: "include" });
    if (!sr.ok) return fallback();
    var sj = await sr.json();
    if (!sj || !sj.success || !sj.data) return fallback();
    var state = sj.data;
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

// SSOInitHandler returns an http.HandlerFunc that serves the zero-click
// landing page for EXACTLY the bare root path. It 404s any other path so a
// misconfigured route (sending more than `/` here) fails loud instead of
// shadowing the SPA. Method is GET/HEAD only.
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
		if r.URL.Path != "/" {
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
