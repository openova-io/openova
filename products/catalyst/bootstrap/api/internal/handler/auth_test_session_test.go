// auth_test_session_test.go — unit tests for HandleAuthTestSession.
//
// Coverage:
//   - Disabled (env unset / != "true") → 404 Not Found, no Set-Cookie.
//   - Enabled, missing tier param      → 400 tier-invalid.
//   - Enabled, invalid tier param      → 400 tier-invalid.
//   - Enabled, each of 5 supported tiers → 200, Set-Cookie, JWT carries
//     correct tier + role claim.
//   - Enabled, signer not wired (h.handoverSigner == nil) → 503.

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// withTestSessionEnv flips CATALYST_TEST_SESSION_ENABLED for the
// duration of one test and returns a restore func. Use:
//
//	defer withTestSessionEnv("true")()
func withTestSessionEnv(t *testing.T, value string) func() {
	t.Helper()
	t.Setenv("CATALYST_TEST_SESSION_ENABLED", value)
	return func() { /* t.Setenv auto-restores via t.Cleanup */ }
}

func TestHandleAuthTestSession_Disabled_Returns404(t *testing.T) {
	h := testPinSetup(t)
	// env not set → endpoint returns 404
	t.Setenv("CATALYST_TEST_SESSION_ENABLED", "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session?tier=viewer", nil)
	w := httptest.NewRecorder()
	h.HandleAuthTestSession(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("disabled: want 404, got %d (body: %s)", w.Code, w.Body.String())
	}
	if cookies := w.Result().Cookies(); len(cookies) != 0 {
		t.Fatalf("disabled: want 0 Set-Cookie, got %d", len(cookies))
	}
}

func TestHandleAuthTestSession_DisabledAttempts(t *testing.T) {
	// Various values that should NOT enable the endpoint.
	cases := []string{"", "false", "0", "no", "FALSE", "anythingElse"}
	for _, v := range cases {
		v := v
		t.Run("env="+v, func(t *testing.T) {
			h := testPinSetup(t)
			t.Setenv("CATALYST_TEST_SESSION_ENABLED", v)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session?tier=admin", nil)
			w := httptest.NewRecorder()
			h.HandleAuthTestSession(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("env=%q: want 404, got %d", v, w.Code)
			}
		})
	}
}

func TestHandleAuthTestSession_MissingTier_Returns400(t *testing.T) {
	h := testPinSetup(t)
	defer withTestSessionEnv(t, "true")()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session", nil)
	w := httptest.NewRecorder()
	h.HandleAuthTestSession(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing tier: want 400, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "tier-invalid") {
		t.Fatalf("missing tier: body should contain 'tier-invalid', got %s", w.Body.String())
	}
}

func TestHandleAuthTestSession_BadTier_Returns400(t *testing.T) {
	h := testPinSetup(t)
	defer withTestSessionEnv(t, "true")()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session?tier=superuser", nil)
	w := httptest.NewRecorder()
	h.HandleAuthTestSession(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad tier: want 400, got %d", w.Code)
	}
}

func TestHandleAuthTestSession_NoSigner_Returns503(t *testing.T) {
	h := testPinSetup(t)
	h.handoverSigner = nil // simulate Sovereign without signer
	defer withTestSessionEnv(t, "true")()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session?tier=viewer", nil)
	w := httptest.NewRecorder()
	h.HandleAuthTestSession(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no signer: want 503, got %d", w.Code)
	}
}

// TestHandleAuthTestSession_AllTiers_HappyPath verifies that for each of
// the 5 supported tiers, the endpoint:
//   - returns 200
//   - sets the catalyst_session cookie
//   - the JWT in the cookie carries the requested tier + matching role
//   - the JSON body echoes the same tier + role + email
func TestHandleAuthTestSession_AllTiers_HappyPath(t *testing.T) {
	tiers := []struct {
		tier         string
		expectedRole string
		expectedMail string
	}{
		{"viewer", "catalyst-viewer", "qa-test-viewer@openova.io"},
		{"developer", "catalyst-developer", "qa-test-developer@openova.io"},
		{"operator", "catalyst-operator", "qa-test-operator@openova.io"},
		{"admin", "catalyst-admin", "qa-test-admin@openova.io"},
		{"owner", "catalyst-owner", "qa-test-owner@openova.io"},
	}
	for _, tc := range tiers {
		tc := tc
		t.Run("tier="+tc.tier, func(t *testing.T) {
			h := testPinSetup(t)
			defer withTestSessionEnv(t, "true")()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session?tier="+tc.tier, nil)
			req.Header.Set("X-Forwarded-Proto", "https") // make Secure=true
			w := httptest.NewRecorder()
			h.HandleAuthTestSession(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("tier=%s: want 200, got %d (body: %s)", tc.tier, w.Code, w.Body.String())
			}

			// Body assertions
			var body testSessionResponse
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body: %v (raw: %s)", err, w.Body.String())
			}
			if !body.OK {
				t.Fatalf("tier=%s: body.OK = false", tc.tier)
			}
			if body.Tier != tc.tier {
				t.Fatalf("tier=%s: body.Tier = %q want %q", tc.tier, body.Tier, tc.tier)
			}
			if body.Role != tc.expectedRole {
				t.Fatalf("tier=%s: body.Role = %q want %q", tc.tier, body.Role, tc.expectedRole)
			}
			if body.Email != tc.expectedMail {
				t.Fatalf("tier=%s: body.Email = %q want %q", tc.tier, body.Email, tc.expectedMail)
			}
			if body.Token == "" {
				t.Fatalf("tier=%s: body.Token is empty", tc.tier)
			}

			// Cookie assertions
			var sessionCookie *http.Cookie
			for _, c := range w.Result().Cookies() {
				if c.Name == auth.SessionCookieName {
					sessionCookie = c
					break
				}
			}
			if sessionCookie == nil {
				t.Fatalf("tier=%s: no %s cookie set", tc.tier, auth.SessionCookieName)
			}
			if !sessionCookie.HttpOnly {
				t.Fatalf("tier=%s: cookie not HttpOnly", tc.tier)
			}
			if !sessionCookie.Secure {
				t.Fatalf("tier=%s: cookie not Secure", tc.tier)
			}
			if sessionCookie.SameSite != http.SameSiteLaxMode {
				t.Fatalf("tier=%s: cookie SameSite want Lax got %v", tc.tier, sessionCookie.SameSite)
			}
			if sessionCookie.Value != body.Token {
				t.Fatalf("tier=%s: cookie value != body.Token", tc.tier)
			}

			// JWT claim assertions — parse without verifying signature
			// (we trust the same signer that PIN-verify uses; the
			// integration is exercised in the existing PIN tests).
			parser := jwt.NewParser(jwt.WithoutClaimsValidation())
			token, _, err := parser.ParseUnverified(body.Token, jwt.MapClaims{})
			if err != nil {
				t.Fatalf("tier=%s: ParseUnverified: %v", tc.tier, err)
			}
			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				t.Fatalf("tier=%s: claims not MapClaims", tc.tier)
			}
			if got := claims["tier"]; got != tc.tier {
				t.Fatalf("tier=%s: claim 'tier' = %v want %s", tc.tier, got, tc.tier)
			}
			if got := claims["email"]; got != tc.expectedMail {
				t.Fatalf("tier=%s: claim 'email' = %v want %s", tc.tier, got, tc.expectedMail)
			}
			ra, ok := claims["realm_access"].(map[string]any)
			if !ok {
				t.Fatalf("tier=%s: claim 'realm_access' missing or wrong shape", tc.tier)
			}
			roles, ok := ra["roles"].([]any)
			if !ok || len(roles) == 0 {
				t.Fatalf("tier=%s: realm_access.roles missing/empty", tc.tier)
			}
			if roles[0] != tc.expectedRole {
				t.Fatalf("tier=%s: realm_access.roles[0] = %v want %s", tc.tier, roles[0], tc.expectedRole)
			}
			if qa, _ := claims["qa_test_session"].(bool); !qa {
				t.Fatalf("tier=%s: missing qa_test_session=true marker", tc.tier)
			}
		})
	}
}

// TestHandleAuthTestSession_BodyOverrides verifies the optional JSON
// body's email + subject overrides take effect (multi-user scenarios).
func TestHandleAuthTestSession_BodyOverrides(t *testing.T) {
	h := testPinSetup(t)
	defer withTestSessionEnv(t, "true")()

	body := strings.NewReader(`{"email":"alt@example.com","subject":"alt-subject"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/test-session?tier=admin", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleAuthTestSession(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("body overrides: want 200, got %d (body: %s)", w.Code, w.Body.String())
	}
	var resp testSessionResponse
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Email != "alt@example.com" {
		t.Fatalf("body overrides: email = %q want alt@example.com", resp.Email)
	}
}
