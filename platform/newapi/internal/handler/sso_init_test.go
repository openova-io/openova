// sso_init_test.go — unit tests for the #3374 zero-click bare-URL SSO
// landing page handler.
package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fullConfig is the production shape: slug + authorize URL + client_id seeded
// by the bp-newapi admin-sso-seed-job.
func fullConfig() SSOInitConfig {
	return SSOInitConfig{
		Slug:         "sovereign",
		AuthorizeURL: "https://auth.hw138.omani.works/realms/sovereign/protocol/openid-connect/auth?kc_idp_hint=catalyst-pin",
		ClientID:     "newapi-admin",
		Scopes:       "openid profile email groups",
	}
}

func TestSSOInitHandler_ServesLandingAtRoot(t *testing.T) {
	h := SSOInitHandler(fullConfig())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	// 0.1.16: the page mints state, then redirects DIRECTLY to the
	// chart-provided authorize URL — it must NOT read provider discovery from
	// /api/status (v0.13.2 does not expose custom_oauth_providers there).
	for _, want := range []string{
		"/api/user/self",    // #3563 — idempotent re-login: already-signed-in check
		"/console",          // #3563 — a valid session redirects straight to the app
		"/api/user/logout",  // #3563 — clear stale session so re-auth = login (not bind)
		"/api/oauth/state",  // the only runtime dependency (CSRF state + cookie)
		`"sovereign"`,       // the seeded provider slug, embedded as a JS literal
		`/realms/sovereign`, // the injected authorize URL is present
		// kc_idp_hint carried through. html/template JS-escapes `=` to the
		// unicode escape `=` inside the JS string literal; the browser
		// decodes it back at parse time, so the runtime URL is correct. Assert
		// the param name + value (neither is escaped) to prove it's present.
		`kc_idp_hint`,
		`catalyst-pin`,
		`"newapi-admin"`,   // the injected client_id is present
		`"/oauth/" + SLUG`, // redirect_uri construction matches the SPA
		"window.location.replace(url)",
		// #3374 (2026-06-18) — the /api/oauth/state mint must RETRY (a loop +
		// setTimeout backoff) so a fresh-prov NewAPI still warming up
		// (DSN-gate + GORM migrate) does not dead-end the first visit at
		// /setup. Assert the retry loop + a backoff sleep are present.
		"attempt < 8",
		"setTimeout(r, 1500)",
		// #3374 (2026-06-18) — on retry-exhaustion with SSO configured the page
		// must RELOAD ITSELF (keep trying) rather than fall through to /login,
		// because the SPA's /login bounces an unseeded NewAPI to /setup. Assert
		// the self-reload helper + that the exhausted-state path calls it.
		"function reloadSelf()",
		`window.location.replace("/")`,
		"if (!state) return reloadSelf();",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing page missing %q", want)
		}
	}
	// It must NOT depend on the non-existent /api/status custom_oauth_providers
	// field — that dependency was the #3374 0.1.16 defect.
	for _, banned := range []string{"/api/status", "custom_oauth_providers"} {
		if strings.Contains(body, banned) {
			t.Errorf("landing page still references %q — the unreliable runtime-discovery path", banned)
		}
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

func TestSSOInitHandler_DefaultsSlugAndScopes(t *testing.T) {
	// Empty slug must default to "sovereign"; empty scopes to the full set.
	h := SSOInitHandler(SSOInitConfig{
		Slug:         "",
		AuthorizeURL: "https://auth.x/realms/sovereign/protocol/openid-connect/auth",
		ClientID:     "newapi-admin",
		Scopes:       "",
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"sovereign"`) {
		t.Errorf("empty slug did not default to \"sovereign\"")
	}
	if !strings.Contains(body, `"openid profile email groups"`) {
		t.Errorf("empty scopes did not default to the full scope set")
	}
}

func TestSSOInitHandler_DegradesWhenAuthorizeURLMissing(t *testing.T) {
	// Without an authorize URL / client_id the page must degrade to the SPA
	// /login link, not build a broken redirect (graceful for older overlays).
	h := SSOInitHandler(SSOInitConfig{Slug: "sovereign"})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200 (still serves the page)", rec.Code)
	}
	// The guard `if (!AUTHORIZE_URL || !CLIENT_ID) return fallback();` must be
	// present so a missing config degrades gracefully.
	if !strings.Contains(body, "!AUTHORIZE_URL || !CLIENT_ID") {
		t.Errorf("missing the graceful-degrade guard for an unset authorize URL")
	}
}

func TestSSOInitHandler_404sNonRootPath(t *testing.T) {
	// Belt-and-braces: even though the HTTPRoute only sends the bare root
	// (and, per #5612, /login) here, the handler must NOT serve the landing
	// page for any other path (it would shadow the SPA / API if the route
	// were ever misconfigured).
	h := SSOInitHandler(fullConfig())
	for _, p := range []string{"/console", "/api/status", "/oauth/sovereign", "/assets/x.js", "/login/", "/login/foo"} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", p, rec.Code)
		}
	}
}

// TestSSOInitHandler_ServesLandingAtLogin — #5612. NewAPI's own SPA
// client-routes an expired session to `/login?expired=true` and offers a
// "Continue with OpenOva SSO" button that does nothing at all (verified live
// on hw292: no navigation, no /api/oauth/state call, no network request).
// The bp-newapi HTTPRoute now sends an Exact `/login` match to this same
// bridge, so a full navigation/reload of the recovery page must run the
// IDENTICAL deterministic OIDC round-trip the bare root already runs — not
// depend on the SPA's broken button.
func TestSSOInitHandler_ServesLandingAtLogin(t *testing.T) {
	h := SSOInitHandler(fullConfig())
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"/api/user/self", "/api/oauth/state", `"sovereign"`, `/realms/sovereign`} {
		if !strings.Contains(body, want) {
			t.Errorf("/login landing page missing %q", want)
		}
	}
}

// TestSSOInitHandler_LoginFallbackDoesNotSelfLoop — #5612 belt-and-braces.
// When SSO is unconfigured, fallback() would normally redirect the visitor
// to /login — but if THIS page is already /login that would be a redirect
// to itself. In production the chart never routes /login here without SSO
// configured, but the handler must still degrade safely rather than emit a
// self-redirecting page if it is ever reached that way.
func TestSSOInitHandler_LoginFallbackDoesNotSelfLoop(t *testing.T) {
	h := SSOInitHandler(SSOInitConfig{Slug: "sovereign"}) // no AuthorizeURL/ClientID
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login status = %d, want 200 (still serves the page)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `window.location.pathname === "/login"`) {
		t.Errorf("missing the same-path guard that prevents fallback() from redirecting /login to itself")
	}
	if strings.Contains(body, `window.location.replace("/login")`) == false {
		// The replace-to-/login call must still exist for the bare-root case —
		// just gated behind the pathname check above.
		t.Errorf("expected the /login fallback redirect to still be present (guarded)")
	}
}

func TestSSOInitHandler_HeadOK_PostRejected(t *testing.T) {
	h := SSOInitHandler(fullConfig())

	recHead := httptest.NewRecorder()
	h(recHead, httptest.NewRequest(http.MethodHead, "/", nil))
	if recHead.Code != http.StatusOK {
		t.Errorf("HEAD / status = %d, want 200", recHead.Code)
	}

	recPost := httptest.NewRecorder()
	h(recPost, httptest.NewRequest(http.MethodPost, "/", nil))
	if recPost.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / status = %d, want 405", recPost.Code)
	}
}

func TestSSOInitHandler_ValuesAreJSEscaped(t *testing.T) {
	// A slug / client_id with a quote must not break out of the JS string
	// literal (XSS / break-out risk).
	h := SSOInitHandler(SSOInitConfig{
		Slug:         `ev"il`,
		AuthorizeURL: `https://auth.x/realms/sovereign/auth"`,
		ClientID:     `cli"ent`,
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if strings.Contains(body, `var SLUG = "ev"il"`) {
		t.Errorf("slug was not JS-escaped — XSS/break-out risk: %q", body)
	}
	if strings.Contains(body, `var CLIENT_ID = "cli"ent"`) {
		t.Errorf("client_id was not JS-escaped — XSS/break-out risk")
	}
}
