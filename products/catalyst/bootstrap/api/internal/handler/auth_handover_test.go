package handler

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jtistore"
)

// ─── test helpers ────────────────────────────────────────────────────────────

// testHandoverSetup creates a Handler wired for AuthHandover tests.
// Returns the handler, the raw RSA private key (for forging claims in negative
// tests), and the path to the public JWK file.
func testHandoverSetup(t *testing.T) (h *Handler, privKey *rsa.PrivateKey, keyPath string) {
	t.Helper()
	dir := t.TempDir()
	keyPath = filepath.Join(dir, "public.jwk")

	privPEM, pubJWK, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	if err := os.WriteFile(keyPath, pubJWK, 0o644); err != nil {
		t.Fatalf("write pubJWK: %v", err)
	}

	privKey, err = jwt.ParseRSAPrivateKeyFromPEM(privPEM)
	if err != nil {
		t.Fatalf("ParseRSAPrivateKeyFromPEM: %v", err)
	}

	// Build the same handover signer the production main.go wires up
	// (commit b1ff09bf moved AuthHandover off Keycloak token-exchange
	// to a locally-minted RS256 session JWT signed by handoverSigner).
	// Without this the happy-path test panics in
	// (*handoverjwt.Signer).SignCustomClaims with a nil-pointer deref.
	signer, err := handoverjwt.New(privPEM, "https://console.openova.io", 8*time.Hour)
	if err != nil {
		t.Fatalf("handoverjwt.New: %v", err)
	}

	jtiSt := jtistore.New(filepath.Join(dir, "jti.log"))

	h = &Handler{
		log:                       slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverJWTPublicKeyPath:  keyPath,
		handoverSigner:            signer,
		authHandoverSovereignFQDN: "sov.test",
		authHandoverRedirect:      "/dashboard",
		jtiStore:                  jtiSt,
		kc:                        &stubKeycloakClient{},
	}
	return h, privKey, keyPath
}

// mintValidToken signs a claims value that passes all handler checks for sov.test.
func mintValidToken(t *testing.T, privKey *rsa.PrivateKey) string {
	t.Helper()
	return signClaims(t, privKey, validClaims("sov.test"))
}

// mintTokenForSov signs a claims value for a given FQDN.
func mintTokenForSov(t *testing.T, privKey *rsa.PrivateKey, sov string) string {
	t.Helper()
	return signClaims(t, privKey, validClaims(sov))
}

// ─── stub implementations ────────────────────────────────────────────────────

// stubKeycloakClient satisfies keycloakClient for tests.
type stubKeycloakClient struct {
	ensureUserErr      error
	impersonateErr     error
	ensureUserID       string
	impersonateAccess  string
	impersonateRefresh string
	impersonateExpiry  int
}

func (s *stubKeycloakClient) EnsureUser(_ context.Context, _, _ string) (string, error) {
	if s.ensureUserErr != nil {
		return "", s.ensureUserErr
	}
	id := s.ensureUserID
	if id == "" {
		id = "user-uuid-001"
	}
	return id, nil
}

func (s *stubKeycloakClient) ImpersonateToken(_ context.Context, _, _ string) (string, string, int, error) {
	if s.impersonateErr != nil {
		return "", "", 0, s.impersonateErr
	}
	access := s.impersonateAccess
	if access == "" {
		access = "test-access-token"
	}
	refresh := s.impersonateRefresh
	if refresh == "" {
		refresh = "test-refresh-token"
	}
	expiry := s.impersonateExpiry
	if expiry == 0 {
		expiry = 3600
	}
	return access, refresh, expiry, nil
}

// ─── GET /auth/handover tests ─────────────────────────────────────────────────

func TestAuthHandover_HappyPath(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	tok := mintValidToken(t, privKey)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status: got %d want 302", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/dashboard" {
		t.Errorf("Location: got %q want /dashboard", loc)
	}

	cookies := resp.Cookies()
	sessionCookie := findCookie(cookies, "catalyst_session")
	if sessionCookie == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	if !sessionCookie.HttpOnly {
		t.Error("catalyst_session must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Error("catalyst_session must be SameSite=Lax")
	}
	// Session cookie carries a locally-minted RS256 JWT (commit b1ff09bf
	// retired the Keycloak token-exchange flow per Inviolable Principle
	// #11 — Sovereigns must not depend on the mothership). Decode the
	// JWT and assert the canonical claims rather than a fixed stub
	// value.
	if sessionCookie.Value == "" {
		t.Fatal("catalyst_session must contain a session JWT, got empty")
	}
	parsedSession, err := jwt.Parse(sessionCookie.Value, func(t *jwt.Token) (interface{}, error) {
		return &privKey.PublicKey, nil
	})
	if err != nil || !parsedSession.Valid {
		t.Fatalf("catalyst_session JWT did not validate against handover public key: err=%v valid=%v", err, parsedSession != nil && parsedSession.Valid)
	}
	sessionMap, ok := parsedSession.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("catalyst_session JWT claims unexpected type %T", parsedSession.Claims)
	}
	if got, want := sessionMap["typ"], "session"; got != want {
		t.Errorf("catalyst_session.typ: got %v want %v", got, want)
	}
	if got, want := sessionMap["email"], "admin@sov.test"; got != want {
		t.Errorf("catalyst_session.email: got %v want %v", got, want)
	}
	if got, want := sessionMap["sovereign_fqdn"], "sov.test"; got != want {
		t.Errorf("catalyst_session.sovereign_fqdn: got %v want %v", got, want)
	}
	if got, want := sessionMap["deployment_id"], "dep-001"; got != want {
		t.Errorf("catalyst_session.deployment_id: got %v want %v", got, want)
	}
	if got, want := sessionMap["keycloak_uid"], "user-uuid-001"; got != want {
		t.Errorf("catalyst_session.keycloak_uid: got %v want %v", got, want)
	}
	if got, want := sessionMap["role"], "sovereign-admin"; got != want {
		t.Errorf("catalyst_session.role: got %v want %v", got, want)
	}

	// catalyst_refresh is the legacy token-exchange refresh cookie. The
	// new local-mint flow clears it (MaxAge=-1, empty value) on every
	// successful handover so any stale cookie from an earlier
	// token-exchange-era login does not linger.
	refreshCookie := findCookie(cookies, "catalyst_refresh")
	if refreshCookie == nil {
		t.Fatal("catalyst_refresh cookie not set (handler must explicitly clear it)")
	}
	if refreshCookie.Value != "" {
		t.Errorf("catalyst_refresh value: got %q want empty (cookie should be cleared)", refreshCookie.Value)
	}
	if refreshCookie.MaxAge >= 0 {
		t.Errorf("catalyst_refresh MaxAge: got %d want negative (cookie should be cleared)", refreshCookie.MaxAge)
	}
}

// TestAuthHandover_SessionCookieDomainWidened (#3374) — the handover
// session cookie MUST carry Domain=.<sovereign-fqdn>. Host-only on
// console.<fqdn> breaks EVERY silent zero-click chain: Keycloak's
// catalyst-pin broker redirect to api.<fqdn>/oidc/auth never carries
// the cookie, so a fresh handover session bounces to the PIN form
// (measured live on hw130 2026-06-13). Mirrors the PIN-verify path's
// G113-followup fix (auth.go).
func TestAuthHandover_SessionCookieDomainWidened(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	tok := mintValidToken(t, privKey)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	sessionCookie := findCookie(w.Result().Cookies(), "catalyst_session")
	if sessionCookie == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	// mintValidToken's sovereign_fqdn is sov.test; no env overrides in
	// the test process, so the claim-derived fallback must fire.
	// NB: Go serialises the Domain attribute WITHOUT the leading dot
	// (RFC 6265 — a present Domain attribute is always domain-wide),
	// so the round-tripped cookie reads "sov.test". An EMPTY Domain is
	// the host-only failure mode this test guards against.
	if sessionCookie.Domain != "sov.test" {
		t.Errorf("catalyst_session Domain: got %q want %q (host-only cookies break the api.<fqdn>/oidc silent chain)", sessionCookie.Domain, "sov.test")
	}
}

func TestAuthHandover_MissingToken(t *testing.T) {
	h, _, _ := testHandoverSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/handover", nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "missing token parameter")
}

// TC-004 / 2026-05-07 — browser visits to /auth/handover without a
// token receive a 302 redirect to the SPA error page, NOT raw JSON.
// Two browser markers exercised: explicit `Accept: text/html` and
// `Sec-Fetch-Mode: navigate` (handles `Accept: */*` browsers).
func TestAuthHandover_MissingTokenHTMLBrowser(t *testing.T) {
	h, _, _ := testHandoverSetup(t)
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{
			name:    "Accept text/html",
			headers: map[string]string{"Accept": "text/html,application/xhtml+xml"},
		},
		{
			name: "Sec-Fetch-Mode navigate",
			headers: map[string]string{
				"Accept":         "*/*",
				"Sec-Fetch-Mode": "navigate",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/handover", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h.AuthHandover(w, req)
			if w.Code != http.StatusFound {
				t.Errorf("status: got %d, want %d", w.Code, http.StatusFound)
			}
			loc := w.Header().Get("Location")
			if loc != "/auth/handover-error?reason=missing_token" {
				t.Errorf("Location: got %q", loc)
			}
		})
	}
}

// TC-004 / 2026-05-07 — programmatic callers (explicit JSON Accept)
// keep the legacy 401 JSON contract unchanged.
func TestAuthHandover_MissingTokenJSONClient(t *testing.T) {
	h, _, _ := testHandoverSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/handover", nil)
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "missing token parameter")
}

// TC-004 / 2026-05-07 — an authenticated browser visit to /auth/handover
// without a token must be redirected to the post-handover landing
// (h.authHandoverRedirect) instead of seeing the "Handover incomplete"
// error page. Repeats the same fix on three session-token channels:
// HMAC-wrapped catalyst_session cookie, raw-JWT catalyst_session
// cookie, Authorization: Bearer header.
func TestAuthHandover_MissingTokenAuthedRedirectsToDashboard(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	// Wire authConfig so hasValidCatalystSession can run. Sovereign-side
	// uses the local handover signer's public key as the validator key
	// (Keycloak token-exchange was removed in 26 — every session JWT is
	// self-signed with no kid header).
	h.authConfig = &auth.Config{
		LocalPublicKey: &privKey.PublicKey,
		// JWKSCache stays nil — ValidateToken short-circuits on
		// LocalPublicKey when the JWT has no kid header.
	}
	sessionTok := mintSessionJWT(t, privKey)

	cases := []struct {
		name    string
		setAuth func(r *http.Request)
	}{
		{
			name: "raw JWT cookie",
			setAuth: func(r *http.Request) {
				r.AddCookie(&http.Cookie{Name: "catalyst_session", Value: sessionTok})
			},
		},
		{
			name: "Authorization Bearer header",
			setAuth: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer "+sessionTok)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/handover", nil)
			req.Header.Set("Accept", "text/html")
			tc.setAuth(req)
			w := httptest.NewRecorder()
			h.AuthHandover(w, req)
			if w.Code != http.StatusFound {
				t.Errorf("status: got %d, want %d (body: %s)", w.Code, http.StatusFound, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != "/dashboard" {
				t.Errorf("Location: got %q want /dashboard", loc)
			}
		})
	}
}

// TC-004 / 2026-05-07 — an EXPIRED catalyst_session cookie on a no-token
// browser visit MUST fall through to the handover-error page (NOT to
// /dashboard). Confirms hasValidCatalystSession enforces expiry.
func TestAuthHandover_MissingTokenExpiredSessionFallsThrough(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	h.authConfig = &auth.Config{LocalPublicKey: &privKey.PublicKey}
	expired := mintSessionJWTExpired(t, privKey)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(&http.Cookie{Name: "catalyst_session", Value: expired})
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d want %d", w.Code, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/handover-error?reason=missing_token" {
		t.Errorf("Location: got %q want handover-error page", loc)
	}
}

// TC-004 / 2026-05-07 — when h.authConfig is nil (Sovereign not yet
// configured / CI), the authed-redirect branch is skipped and the
// existing html-vs-json branches keep working.
func TestAuthHandover_MissingTokenNoAuthConfigKeepsHTMLBranch(t *testing.T) {
	h, _, _ := testHandoverSetup(t)
	h.authConfig = nil

	req := httptest.NewRequest(http.MethodGet, "/auth/handover", nil)
	req.Header.Set("Accept", "text/html")
	// Even a (now unverifiable) cookie present — must NOT short-circuit.
	req.AddCookie(&http.Cookie{Name: "catalyst_session", Value: "some.cookie.value"})
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d want %d", w.Code, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/auth/handover-error?reason=missing_token" {
		t.Errorf("Location: got %q want handover-error page", loc)
	}
}

// mintSessionJWT signs a session-shaped JWT (matching the shape minted
// by AuthHandover after a successful handover) with privKey and returns
// the compact 3-part string. No `kid` header — exercises the
// LocalPublicKey fallback in auth.Config.ValidateToken.
func mintSessionJWT(t *testing.T, privKey *rsa.PrivateKey) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":            "https://console.openova.io",
		"sub":            "operator@sov.test",
		"email":          "operator@sov.test",
		"email_verified": true,
		"role":           "sovereign-admin",
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(1 * time.Hour).Unix(),
		"jti":            "test-session-jti",
		"typ":            "session",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(privKey)
	if err != nil {
		t.Fatalf("mintSessionJWT: %v", err)
	}
	return signed
}

// mintSessionJWTExpired returns a session JWT whose exp is in the past.
func mintSessionJWTExpired(t *testing.T, privKey *rsa.PrivateKey) string {
	t.Helper()
	claims := jwt.MapClaims{
		"iss":            "https://console.openova.io",
		"sub":            "operator@sov.test",
		"email":          "operator@sov.test",
		"email_verified": true,
		"role":           "sovereign-admin",
		"iat":            time.Now().Add(-2 * time.Hour).Unix(),
		"exp":            time.Now().Add(-1 * time.Hour).Unix(),
		"jti":            "test-session-jti-expired",
		"typ":            "session",
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := tok.SignedString(privKey)
	if err != nil {
		t.Fatalf("mintSessionJWTExpired: %v", err)
	}
	return signed
}

func TestAuthHandover_MalformedToken(t *testing.T) {
	h, _, _ := testHandoverSetup(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token=not-a-jwt", nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "invalid token")
}

func TestAuthHandover_ExpiredToken(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)

	c := validClaims("sov.test")
	c.IssuedAt = jwt.NewNumericDate(time.Now().Add(-10 * time.Minute))
	c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-5 * time.Minute))
	tok := signClaims(t, privKey, c)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "invalid token")
}

func TestAuthHandover_WrongAudience(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	// Mint a token for a different Sovereign — valid sig, wrong aud.
	tok := mintTokenForSov(t, privKey, "other.sovereign.io")

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "invalid audience")
}

func TestAuthHandover_WrongIssuer(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)

	c := validClaims("sov.test")
	c.Issuer = "https://evil.example.com"
	tok := signClaims(t, privKey, c)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "invalid issuer")
}

// TestAuthHandover_PerSovereignIssuer_5614 — a cut-over Sovereign mints a handover
// token with its OWN console as `iss`; the verifier must resolve the expected
// issuer through handoverjwt.DefaultIssuer() (which honours
// CATALYST_HANDOVER_JWT_ISSUER, the SAME single source the minter uses) and ACCEPT
// it. The former hardcoded `const expectedIss = "https://console.openova.io"`
// 401'd the Sovereign's own token as "invalid issuer" (the #2940 duplicate-literal
// drift surviving on the verify side) — this test FAILS on that hardcode and
// passes only when both sides share one resolver.
func TestAuthHandover_PerSovereignIssuer_5614(t *testing.T) {
	const sovIssuer = "https://console.hw292.omani.works"
	t.Setenv("CATALYST_HANDOVER_JWT_ISSUER", sovIssuer)

	h, privKey, _ := testHandoverSetup(t)

	c := validClaims("sov.test")
	c.Issuer = sovIssuer // the Sovereign's OWN console — what its minter emits post-cutover
	tok := signClaims(t, privKey, c)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	if w.Code == http.StatusUnauthorized {
		t.Fatalf("#5614 regression: Sovereign rejected its OWN handover token — got 401 %q", w.Body.String())
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d want 302 (own token accepted → dashboard redirect)", w.Code)
	}
}

func TestAuthHandover_WrongRole(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)

	c := validClaims("sov.test")
	c.Role = "viewer"
	tok := signClaims(t, privKey, c)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "insufficient role")
}

func TestAuthHandover_EmailNotVerified(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)

	c := validClaims("sov.test")
	c.EmailVerified = false
	tok := signClaims(t, privKey, c)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "email not verified")
}

func TestAuthHandover_ReplayedJTI(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	tok := mintValidToken(t, privKey)

	// First use should succeed.
	req1 := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w1 := httptest.NewRecorder()
	h.AuthHandover(w1, req1)
	if w1.Code != http.StatusFound {
		t.Fatalf("first use: expected 302, got %d", w1.Code)
	}

	// Second use must fail.
	req2 := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w2 := httptest.NewRecorder()
	h.AuthHandover(w2, req2)
	assertAuthError(t, w2, http.StatusUnauthorized, "token already used")
}

func TestAuthHandover_KCEnsureUserFailure(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	h.kc = &stubKeycloakClient{
		ensureUserErr: fmt.Errorf("keycloak unreachable"),
	}
	tok := mintValidToken(t, privKey)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "keycloak error: ensure user")
}

// TestAuthHandover_KCImpersonateFailure proves AuthHandover NO LONGER
// depends on the Keycloak Admin token-exchange flow. Commit b1ff09bf
// retired ImpersonateToken in favour of a locally-minted RS256 session
// JWT signed by handoverSigner — the migration was driven by Keycloak
// v26 dropping `requested_subject` ("Parameter 'requested_subject' is
// not supported for standard token exchange") AND by Inviolable
// Principle #11 (Sovereigns must not stay tethered to the mothership).
//
// Pre-migration this test asserted that an ImpersonateToken error
// produced a 401. Post-migration the production code never calls
// ImpersonateToken on the AuthHandover path; this test now asserts
// the migration is durable: a stub Keycloak whose ImpersonateToken
// errors must NOT block the handover, and the operator must still
// reach /dashboard with a valid catalyst_session JWT.
func TestAuthHandover_KCImpersonateFailure(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	h.kc = &stubKeycloakClient{
		impersonateErr: fmt.Errorf("token exchange denied — must not reach this path"),
	}
	tok := mintValidToken(t, privKey)

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("status: got %d want 302 (AuthHandover must NOT call ImpersonateToken; body: %s)", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("Location: got %q want /dashboard", loc)
	}
	// Sanity: the catalyst_session cookie carries a non-empty locally-
	// minted JWT (NOT the stub's impersonate-access value).
	sessionCookie := findCookie(w.Result().Cookies(), "catalyst_session")
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatal("catalyst_session must be set with a locally-minted JWT")
	}
}

func TestAuthHandover_KCNotConfigured(t *testing.T) {
	h, privKey, _ := testHandoverSetup(t)
	h.kc = nil

	tok := mintValidToken(t, privKey)
	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "server misconfiguration: keycloak not configured")
}

// TestAuthHandover_WrongSigningKey verifies 401 when the token is signed with a
// different private key (i.e. Catalyst-Zero keypair rotated).
func TestAuthHandover_WrongSigningKey(t *testing.T) {
	h, _, _ := testHandoverSetup(t)

	// Generate a DIFFERENT keypair and sign with it.
	otherPrivPEM, _, _ := handoverjwt.GenerateKeypair()
	otherKey, _ := jwt.ParseRSAPrivateKeyFromPEM(otherPrivPEM)
	tok := mintTokenForSov(t, otherKey, "sov.test")

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	assertAuthError(t, w, http.StatusUnauthorized, "invalid token")
}

// ─── JWK loader tests ─────────────────────────────────────────────────────────

func TestLoadRSAPublicKey_JWK(t *testing.T) {
	privPEM, pubJWK, _ := handoverjwt.GenerateKeypair()
	privKey, _ := jwt.ParseRSAPrivateKeyFromPEM(privPEM)

	dir := t.TempDir()
	path := filepath.Join(dir, "public.jwk")
	if err := os.WriteFile(path, pubJWK, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := loadRSAPublicKey(path)
	if err != nil {
		t.Fatalf("loadRSAPublicKey: %v", err)
	}
	if loaded.N.Cmp(privKey.PublicKey.N) != 0 {
		t.Error("N mismatch")
	}
	if loaded.E != privKey.PublicKey.E {
		t.Errorf("E: got %d want %d", loaded.E, privKey.PublicKey.E)
	}
}

func TestLoadRSAPublicKey_PEM(t *testing.T) {
	privPEM, _, _ := handoverjwt.GenerateKeypair()
	privKey, _ := jwt.ParseRSAPrivateKeyFromPEM(privPEM)

	// Write as PKIX PEM ("PUBLIC KEY").
	pubDER, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})

	dir := t.TempDir()
	path := filepath.Join(dir, "public.pem")
	if err := os.WriteFile(path, pubPEM, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := loadRSAPublicKey(path)
	if err != nil {
		t.Fatalf("loadRSAPublicKey PEM: %v", err)
	}
	if loaded.N.Cmp(privKey.PublicKey.N) != 0 {
		t.Error("N mismatch")
	}
}

func TestRSAPublicKeyFromJWK_RoundTrip(t *testing.T) {
	privPEM, pubJWK, _ := handoverjwt.GenerateKeypair()
	privKey, _ := jwt.ParseRSAPrivateKeyFromPEM(privPEM)

	loaded, err := rsaPublicKeyFromJWK(pubJWK)
	if err != nil {
		t.Fatalf("rsaPublicKeyFromJWK: %v", err)
	}
	if loaded.N.Cmp(privKey.PublicKey.N) != 0 {
		t.Error("N mismatch")
	}
}

func TestRSAPublicKeyFromJWK_InvalidJSON(t *testing.T) {
	_, err := rsaPublicKeyFromJWK([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRSAPublicKeyFromJWK_WrongKty(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{"kty": "EC", "n": "aaa", "e": "AQAB"})
	_, err := rsaPublicKeyFromJWK(raw)
	if err == nil {
		t.Fatal("expected error for non-RSA kty")
	}
}

// ─── JTI store file persistence test ─────────────────────────────────────────

func TestJTIStore_FileBackedReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jti.log")

	s1 := jtistore.New(path)
	first, err := s1.Mark("jti-aaa")
	if err != nil || !first {
		t.Fatalf("first Mark: first=%v err=%v", first, err)
	}

	// New store instance (simulates restart) — must still reject the same jti.
	s2 := jtistore.New(path)
	second, err := s2.Mark("jti-aaa")
	if err != nil {
		t.Fatalf("second Mark error: %v", err)
	}
	if second {
		t.Error("second Mark returned true (firstUse) — jti was not persisted")
	}
}

// ─── helper utilities ─────────────────────────────────────────────────────────

func findCookie(cookies []*http.Cookie, name string) *http.Cookie {
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func assertAuthError(t *testing.T, w *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	if w.Code != code {
		t.Errorf("status: got %d want %d (body: %s)", w.Code, code, w.Body.String())
	}
	var body authHandoverError
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error != msg {
		t.Errorf("error.message: got %q want %q", body.Error, msg)
	}
}

// validClaims returns a claims struct that passes all handler validation for
// sovereign FQDN `sov`. Each call produces a unique jti.
func validClaims(sov string) handoverjwt.Claims {
	return handoverjwt.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "https://console.openova.io",
			Subject:   "sub-001",
			Audience:  jwt.ClaimStrings{"https://console." + sov},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(5 * time.Minute)),
			ID:        "jti-" + sov + "-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		},
		Email:         "admin@" + sov,
		EmailVerified: true,
		SovereignFQDN: sov,
		DeploymentID:  "dep-001",
		Role:          "sovereign-admin",
	}
}

// signClaims signs the claims with privKey and returns the token string.
func signClaims(t *testing.T, privKey *rsa.PrivateKey, claims handoverjwt.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(privKey)
	if err != nil {
		t.Fatalf("sign claims: %v", err)
	}
	return signed
}

// publicJWKBytesForKey encodes pub as a JWK JSON byte slice.
// Used in a few tests to construct a JWK from a known key.
func publicJWKBytesForKey(pub *rsa.PublicKey) []byte {
	jwk := map[string]string{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
	raw, _ := json.Marshal(jwk)
	return raw
}
