package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// #6870 — Sovereign console sign-out redirected the browser to
// keycloak.keycloak.svc.cluster.local.
//
// Measured on hw307 (2026-09-06, re-measured 2026-11-05): DELETE
// /api/v1/auth/session returned a keycloakLogoutURL whose host was the
// IN-CLUSTER CATALYST_KC_ADDR, whose client_id was the mothership default
// "catalyst-zero-ui" (not a client of the sovereign realm — Keycloak 26.3
// answers 400 "Invalid redirect uri" for it, probed live), and whose
// post_logout_redirect_uri path was /sovereign/login (derived from the
// shipped CATALYST_POST_AUTH_REDIRECT=/sovereign/components, although the
// Sovereign SPA is root-mounted and its login route is /login). Each of the
// three alone strands the user; the fixture below is the exact hw307 env.

// sovereignLogoutEnv pins the env a Sovereign catalyst-api actually runs
// with (products/catalyst/chart/templates/api-deployment.yaml + the
// catalyst-kc-sa-credentials host-bridge): the KC address is the
// in-cluster Service, the realm is "sovereign", no CATALYST_KC_CLIENT_ID
// (its Secret is mothership-only), and the shipped post-auth default.
func sovereignLogoutEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SOVEREIGN_FQDN", "hw307.omani.works")
	t.Setenv("CATALYST_KC_ADDR", "http://keycloak.keycloak.svc.cluster.local")
	t.Setenv("CATALYST_KC_REALM", "sovereign")
	t.Setenv("CATALYST_KC_CLIENT_ID", "")
	t.Setenv("CATALYST_KC_PUBLIC_ADDR", "")
	t.Setenv("CATALYST_KC_POST_LOGOUT_PATH", "")
	t.Setenv("CATALYST_POST_AUTH_REDIRECT", "/sovereign/components")
}

func parseLogoutURL(t *testing.T, raw string) (*url.URL, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("logout URL does not parse: %q: %v", raw, err)
	}
	return u, u.Query()
}

// TestBuildKeycloakLogoutURL_Sovereign_PublicIssuerCatalystUILogin is the
// #6870 regression: on a Sovereign the end-session hop must use the public
// issuer host, the realm's SPA client and the root-mounted /login.
func TestBuildKeycloakLogoutURL_Sovereign_PublicIssuerCatalystUILogin(t *testing.T) {
	sovereignLogoutEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "http://catalyst-api.catalyst-system.svc:8080/api/v1/auth/session", nil)
	req.Host = "catalyst-api.catalyst-system.svc:8080" // what the gateway hands the pod
	req.Header.Set("X-Forwarded-Host", "console.hw307.omani.works")
	req.Header.Set("X-Forwarded-Proto", "https")

	raw := buildKeycloakLogoutURL(req)
	if raw == "" {
		t.Fatal("expected a Keycloak logout URL on a Sovereign, got empty")
	}
	u, q := parseLogoutURL(t, raw)

	// (1) The host the BROWSER is sent to is the public issuer, never the
	// in-cluster Service the page title on hw307 was showing.
	if strings.Contains(raw, "svc.cluster.local") {
		t.Fatalf("#6870: logout URL still names the in-cluster Keycloak Service: %s", raw)
	}
	if u.Scheme != "https" || u.Host != "auth.hw307.omani.works" {
		t.Fatalf("#6870: expected https://auth.hw307.omani.works, got %s://%s (%s)", u.Scheme, u.Host, raw)
	}
	if u.Path != "/realms/sovereign/protocol/openid-connect/logout" {
		t.Fatalf("#6870: unexpected end-session path %q", u.Path)
	}
	// (2) The realm's SPA client — an unknown client_id makes Keycloak reject
	// the post-logout URI with 400 "Invalid redirect uri".
	if got := q.Get("client_id"); got != "catalyst-ui" {
		t.Fatalf("#6870: expected client_id=catalyst-ui on a Sovereign, got %q", got)
	}
	// (3) Back to the login page of the host the request came in on, at the
	// root-mounted SPA's /login (never the Catalyst-Zero /sovereign/login).
	if got := q.Get("post_logout_redirect_uri"); got != "https://console.hw307.omani.works/login" {
		t.Fatalf("#6870: expected post_logout_redirect_uri=https://console.hw307.omani.works/login, got %q", got)
	}
}

// TestBuildKeycloakLogoutURL_Sovereign_PerOrgConsoleHost: a per-Org console
// (console.<slug>.<pool>, served by the same catalyst-ui + catalyst-api,
// #4101) returns to ITS OWN login page, port suffix stripped so it matches
// the client's `https://console.<host>/*` wildcard as a string prefix.
func TestBuildKeycloakLogoutURL_Sovereign_PerOrgConsoleHost(t *testing.T) {
	sovereignLogoutEnv(t)

	req := httptest.NewRequest(http.MethodDelete, "http://catalyst-api/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Host", "console.acmewalk307.omani.rest:443")
	req.Header.Set("X-Forwarded-Proto", "https")

	_, q := parseLogoutURL(t, buildKeycloakLogoutURL(req))
	if got := q.Get("post_logout_redirect_uri"); got != "https://console.acmewalk307.omani.rest/login" {
		t.Fatalf("#6870: per-Org console must return to its own /login (no :port), got %q", got)
	}
	if got := q.Get("client_id"); got != "catalyst-ui" {
		t.Fatalf("#6870: per-Org console signs out of the same catalyst-ui client, got %q", got)
	}
}

// TestBuildKeycloakLogoutURL_Sovereign_ExplicitOverridesWin: the operator
// overrides keep their precedence on a Sovereign.
func TestBuildKeycloakLogoutURL_Sovereign_ExplicitOverridesWin(t *testing.T) {
	sovereignLogoutEnv(t)
	t.Setenv("CATALYST_KC_PUBLIC_ADDR", "https://sso.example.test/")
	t.Setenv("CATALYST_KC_CLIENT_ID", "custom-console")
	t.Setenv("CATALYST_KC_POST_LOGOUT_PATH", "/bye")

	req := httptest.NewRequest(http.MethodDelete, "https://console.hw307.omani.works/api/v1/auth/session", nil)
	req.TLS = nil
	req.Header.Set("X-Forwarded-Proto", "https")

	u, q := parseLogoutURL(t, buildKeycloakLogoutURL(req))
	if u.Host != "sso.example.test" {
		t.Fatalf("CATALYST_KC_PUBLIC_ADDR must win, got host %q", u.Host)
	}
	if got := q.Get("client_id"); got != "custom-console" {
		t.Fatalf("CATALYST_KC_CLIENT_ID must win, got %q", got)
	}
	if got := q.Get("post_logout_redirect_uri"); got != "https://console.hw307.omani.works/bye" {
		t.Fatalf("CATALYST_KC_POST_LOGOUT_PATH must win, got %q", got)
	}
}

// TestBuildKeycloakLogoutURL_CatalystZero_Unchanged: the mothership shape
// (no SOVEREIGN_FQDN) is byte-identical to before — CATALYST_KC_ADDR is the
// browser-facing address there, the client is catalyst-zero-ui, and the
// path derives from CATALYST_POST_AUTH_REDIRECT (/sovereign/wizard →
// /sovereign/login).
func TestBuildKeycloakLogoutURL_CatalystZero_Unchanged(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_KC_ADDR", "https://auth.openova.io/")
	t.Setenv("CATALYST_KC_REALM", "")
	t.Setenv("CATALYST_KC_CLIENT_ID", "")
	t.Setenv("CATALYST_KC_PUBLIC_ADDR", "")
	t.Setenv("CATALYST_KC_POST_LOGOUT_PATH", "")
	t.Setenv("CATALYST_POST_AUTH_REDIRECT", "/sovereign/wizard")

	req := httptest.NewRequest(http.MethodDelete, "https://console.openova.io/sovereign/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Proto", "https")

	raw := buildKeycloakLogoutURL(req)
	want := "https://auth.openova.io/realms/openova/protocol/openid-connect/logout?" +
		"client_id=catalyst-zero-ui&post_logout_redirect_uri=" + url.QueryEscape("https://console.openova.io/sovereign/login")
	if raw != want {
		t.Fatalf("Catalyst-Zero logout URL changed:\n got %s\nwant %s", raw, want)
	}
}

// TestBuildKeycloakLogoutURL_NoKeycloakWired: nothing configured → "" so the
// UI falls back to its local clear + /login (CI, contabo bring-up).
func TestBuildKeycloakLogoutURL_NoKeycloakWired(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_KC_ADDR", "")
	t.Setenv("CATALYST_KC_PUBLIC_ADDR", "")
	req := httptest.NewRequest(http.MethodDelete, "http://localhost/api/v1/auth/session", nil)
	if got := buildKeycloakLogoutURL(req); got != "" {
		t.Fatalf("expected empty logout URL when no Keycloak is wired, got %q", got)
	}
}

// TestHandleAuthLogout_SovereignWireShape_6870: the handler's JSON body —
// what useSession.ts hard-navigates to — carries the public-issuer URL.
func TestHandleAuthLogout_SovereignWireShape_6870(t *testing.T) {
	sovereignLogoutEnv(t)
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")

	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "http://catalyst-api/api/v1/auth/session", nil)
	req.Header.Set("X-Forwarded-Host", "console.hw307.omani.works")
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()
	h.HandleAuthLogout(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		OK                bool   `json:"ok"`
		KeycloakLogoutURL string `json:"keycloakLogoutURL"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	if !body.OK {
		t.Fatalf("expected ok=true, got %s", rec.Body.String())
	}
	want := "https://auth.hw307.omani.works/realms/sovereign/protocol/openid-connect/logout?" +
		"client_id=catalyst-ui&post_logout_redirect_uri=" + url.QueryEscape("https://console.hw307.omani.works/login")
	if body.KeycloakLogoutURL != want {
		t.Fatalf("#6870 keycloakLogoutURL on the wire:\n got %s\nwant %s", body.KeycloakLogoutURL, want)
	}
	// The session cookies are still cleared on the same response (#4101 shape).
	if !strings.Contains(strings.Join(rec.Header().Values("Set-Cookie"), "\n"), "catalyst_session=; Path=/") {
		t.Fatalf("logout response lost the catalyst_session clear-cookie: %v", rec.Header().Values("Set-Cookie"))
	}
	_ = os.Getenv // keep the import honest if the helpers above change
}
