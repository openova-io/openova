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
// newapi.<fqdn>), (2) read the custom OAuth provider from /api/status,
// (3) redirect to the Keycloak authorize URL (which carries
// kc_idp_hint=catalyst-pin → silent broker, no KC login form) with
// redirect_uri=${origin}/oauth/<slug>&state=<state>. Keycloak returns to the
// SPA callback /oauth/<slug>?code&state, the SPA posts /api/oauth/<slug>, the
// session cookie is present → CSRF passes → NewAPI mints its session → the
// owner lands in /console signed-in. The whole round-trip is proven live on
// hw136. The bp-newapi admin-sso-seed-job's promote step (keyed on
// user_oauth_bindings) elevates the owner to root.
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

// ssoInitPage is the zero-click landing page. It runs NewAPI's own
// onCustomOAuthClicked sequence for the configured provider slug. Kept
// dependency-free (vanilla fetch + a <noscript> fallback link to the SPA
// login) so it works in any browser and degrades safely.
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
  function fallback(){ window.location.replace("/login"); }
  try {
    // 1. Mint the CSRF state (also sets the same-origin session cookie that
    //    NewAPI's /api/oauth/<slug> callback checks).
    var sr = await fetch("/api/oauth/state", { credentials: "include" });
    if (!sr.ok) return fallback();
    var sj = await sr.json();
    if (!sj || !sj.success || !sj.data) return fallback();
    var state = sj.data;
    // 2. Read the custom OAuth provider (authorize endpoint + client_id +
    //    scopes) that the bp-newapi admin-sso-seed-job seeded.
    var pr = await fetch("/api/status", { credentials: "include" });
    if (!pr.ok) return fallback();
    var pj = await pr.json();
    var provs = (pj && pj.data && pj.data.custom_oauth_providers) || [];
    var p = null;
    for (var i = 0; i < provs.length; i++) {
      if (provs[i].slug === SLUG) { p = provs[i]; break; }
    }
    if (!p && provs.length === 1) p = provs[0];
    if (!p || !p.authorization_endpoint || !p.client_id) return fallback();
    // 3. Build the authorize URL EXACTLY like the SPA does
    //    (web/src/helpers/api.js onCustomOAuthClicked) and redirect.
    var redirectUri = window.location.origin + "/oauth/" + p.slug;
    var base = p.authorization_endpoint; // carries ?kc_idp_hint=catalyst-pin
    var sep = base.indexOf("?") === -1 ? "?" : "&";
    var scope = encodeURIComponent(p.scopes || "openid profile email");
    var url = base + sep +
      "client_id=" + encodeURIComponent(p.client_id) +
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

// SSOInitConfig carries the one operationally-meaningful knob (the OIDC
// provider slug) so nothing is hardcoded (Inviolable Principle #4).
type SSOInitConfig struct {
	// Slug is the custom_oauth_providers.slug the bp-newapi admin-sso-seed-job
	// seeds (default "sovereign"). The init page selects this provider from
	// /api/status; if absent and exactly one provider exists, it uses that.
	Slug string
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
		// template.JS-safe: Slug is rendered via html/template into a JS
		// string context. Pass it as a quoted JS literal.
		_ = ssoInitTemplate.Execute(w, struct{ Slug template.JS }{
			Slug: template.JS("\"" + template.JSEscapeString(slug) + "\""),
		})
	}
}
