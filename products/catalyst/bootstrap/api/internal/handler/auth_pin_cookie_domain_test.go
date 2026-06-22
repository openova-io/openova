// auth_pin_cookie_domain_test.go — #4101 BUG 1 coverage.
//
// On a Sovereign (SOVEREIGN_FQDN=omantel.biz) that ALSO serves a pool-domain
// Org console (console.demo.omani.homes), HandlePinVerify must scope the
// catalyst_session cookie to the REQUEST HOST, not blindly to .omantel.biz.
//
// Pre-#4101: the cookie carried Domain=.omantel.biz even when the PIN was
// verified on console.demo.omani.homes. Browsers REJECT a Set-Cookie whose
// Domain doesn't cover the current host, so the cookie was silently dropped
// and the very next whoami on console.demo.omani.homes returned 401 (live on
// omantel.biz 2026-06-22).
//
// These tests pin sessionCookieDomain() and the HandlePinVerify wiring.
package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSessionCookieDomain_Table covers the request-host → cookie-Domain
// derivation matrix (#4101).
func TestSessionCookieDomain_Table(t *testing.T) {
	cases := []struct {
		name       string
		envDomain  string // CATALYST_SESSION_COOKIE_DOMAIN
		sovFQDN    string // SOVEREIGN_FQDN
		fwdHost    string // X-Forwarded-Host
		rawHost    string // r.Host (used when fwdHost empty)
		wantDomain string // expected Set-Cookie Domain seed (with leading dot or "")
	}{
		{
			name:       "explicit env override wins",
			envDomain:  "console.openova.io",
			sovFQDN:    "omantel.biz",
			fwdHost:    "console.demo.omani.homes",
			wantDomain: "console.openova.io",
		},
		{
			name:       "sovereign own console → .<sovereign-fqdn>",
			sovFQDN:    "omantel.biz",
			fwdHost:    "console.omantel.biz",
			wantDomain: ".omantel.biz",
		},
		{
			name:       "sovereign api host → .<sovereign-fqdn>",
			sovFQDN:    "omantel.biz",
			fwdHost:    "api.omantel.biz",
			wantDomain: ".omantel.biz",
		},
		{
			name:       "POOL-domain Org console → .<org>.<pool-zone> (BUG 1)",
			sovFQDN:    "omantel.biz",
			fwdHost:    "console.demo.omani.homes",
			wantDomain: ".demo.omani.homes",
		},
		{
			name:       "POOL-domain Org api host → .<org>.<pool-zone>",
			sovFQDN:    "omantel.biz",
			fwdHost:    "api.demo.omani.homes",
			wantDomain: ".demo.omani.homes",
		},
		{
			name:       "host with port stripped",
			sovFQDN:    "omantel.biz",
			fwdHost:    "console.demo.omani.homes:443",
			wantDomain: ".demo.omani.homes",
		},
		{
			name:       "falls back to r.Host when no X-Forwarded-Host",
			sovFQDN:    "omantel.biz",
			rawHost:    "console.demo.omani.homes",
			wantDomain: ".demo.omani.homes",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", tc.envDomain)
			t.Setenv("SOVEREIGN_FQDN", tc.sovFQDN)

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tc.rawHost != "" {
				req.Host = tc.rawHost
			}
			if tc.fwdHost != "" {
				req.Header.Set("X-Forwarded-Host", tc.fwdHost)
			}
			got := sessionCookieDomain(req)
			if got != tc.wantDomain {
				t.Errorf("sessionCookieDomain = %q, want %q", got, tc.wantDomain)
			}
		})
	}
}

// TestPinVerify_PoolDomainCookieScopedToOrgHost is the end-to-end BUG 1
// guard: a PIN verified on console.demo.omani.homes must mint a cookie the
// browser will actually keep (Domain scoped to the Org host), NOT .omantel.biz.
func TestPinVerify_PoolDomainCookieScopedToOrgHost(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")

	h := testPinSetup(t)
	h.pinStore.put("demo@openova.io", "123456", "req-1")

	body := `{"email":"demo@openova.io","pin":"123456","requestId":"req-1"}`
	req := httptest.NewRequest(http.MethodPost,
		"http://console.demo.omani.homes/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "console.demo.omani.homes")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	sc := findCookie(w.Result().Cookies(), "catalyst_session")
	if sc == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	// Go serialises Domain without the leading dot.
	if sc.Domain != "demo.omani.homes" {
		t.Errorf("catalyst_session Domain: got %q want %q (BUG 1 — a .omantel.biz cookie is dropped by the browser on this host → whoami 401)",
			sc.Domain, "demo.omani.homes")
	}
	if !sc.Secure {
		t.Error("catalyst_session must be Secure on an https forwarded request")
	}
}

// TestPinVerify_SovereignConsoleCookieUnchanged confirms the Sovereign's OWN
// console keeps the historical .<SOVEREIGN_FQDN> scope (no regression of the
// G113-followup hw86 fix).
func TestPinVerify_SovereignConsoleCookieUnchanged(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_SESSION_COOKIE_DOMAIN", "")

	h := testPinSetup(t)
	h.pinStore.put("ops@omantel.biz", "123456", "req-1")

	body := `{"email":"ops@omantel.biz","pin":"123456","requestId":"req-1"}`
	req := httptest.NewRequest(http.MethodPost,
		"http://console.omantel.biz/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Host", "console.omantel.biz")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", w.Code, w.Body.String())
	}
	sc := findCookie(w.Result().Cookies(), "catalyst_session")
	if sc == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	if sc.Domain != "omantel.biz" {
		t.Errorf("catalyst_session Domain: got %q want %q (Sovereign own console)", sc.Domain, "omantel.biz")
	}
}
