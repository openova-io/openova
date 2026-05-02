package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jtistore"
)

// ─── magic-link test helpers ──────────────────────────────────────────────────

// testMagicSetup creates a Handler wired for magic-link Option-B tests.
// Returns the handler. The handoverSigner is a real signer backed by a
// freshly-generated RSA-2048 keypair so JWT round-trips work end-to-end.
func testMagicSetup(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()

	privPEM, pubJWK, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}

	keyPath := filepath.Join(dir, "handover.pem")
	pubPath := filepath.Join(dir, "public.jwk")
	if err := writeFile(t, keyPath, privPEM, 0o600); err != nil {
		t.Fatalf("write privPEM: %v", err)
	}
	if err := writeFile(t, pubPath, pubJWK, 0o644); err != nil { //nolint:gocritic
		t.Fatalf("write pubJWK: %v", err)
	}

	signer, err := handoverjwt.LoadOrGenerate(keyPath, pubPath, "https://console.openova.io", 5*time.Minute)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	jtiSt := jtistore.New(filepath.Join(dir, "magic-jti.log"))

	return &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: signer,
		jtiStore:       jtiSt,
		// openovaKC is the openova-realm KC client; kc is the sovereign-realm KC client.
		// Tests for magic-link inject openovaKC; auth_handover tests inject kc.
		openovaKC: &stubKeycloakClient{},
	}
}

// mintMagicToken uses the handler's signer to mint a valid magic-link JWT.
func mintMagicToken(t *testing.T, h *Handler, email string) string {
	t.Helper()
	jti := fmt.Sprintf("test-jti-%d", time.Now().UnixNano())
	now := time.Now().UTC()
	claims := magicLinkClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    magicLinkIssuer,
			Audience:  jwt.ClaimStrings{magicLinkAudience},
			Subject:   "user-uuid-001",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(magicLinkTTL)),
			ID:        jti,
		},
		Email:         email,
		EmailVerified: true,
		Role:          magicLinkRole,
	}
	tok, err := h.handoverSigner.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("SignCustomClaims: %v", err)
	}
	return tok
}

// ─── POST /api/v1/auth/magic-link tests ──────────────────────────────────────

// TestMagicLink_NoSignerReturns503 tests that the handler returns 503 when the
// handover signer is not configured (CATALYST_HANDOVER_KEY_PATH not set).
func TestMagicLink_NoSignerReturns503(t *testing.T) {
	h := &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: nil, // signer absent
	}
	body := `{"email":"test@example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/magic-link",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleMagicLink(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503", w.Code)
	}
}

// TestMagicLink_EmptyEmailReturns400 tests that a missing email in the body
// returns 400.
func TestMagicLink_EmptyEmailReturns400(t *testing.T) {
	h := testMagicSetup(t)
	// openovaKCClient will return nil without CATALYST_OPENOVA_KC_SA_CLIENT_SECRET
	// set, but the email validation fires first.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/magic-link",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleMagicLink(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", w.Code)
	}
}

// TestMagicLink_NoKCReturns503 tests that missing KC config returns 503.
func TestMagicLink_NoKCReturns503(t *testing.T) {
	h := testMagicSetup(t)
	// No openovaKC injected and no env var set — openovaKCClient() returns nil.
	h.openovaKC = nil
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/magic-link",
		strings.NewReader(`{"email":"test@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleMagicLink(w, req)

	// openovaKCClient checks CATALYST_OPENOVA_KC_SA_CLIENT_SECRET — not set.
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503", w.Code)
	}
}

// ─── GET /api/v1/auth/magic tests ────────────────────────────────────────────

// magicValidateHandler wires the handler and a stub KC client that returns
// tokens so the full happy path can be tested.
func magicValidateHandler(t *testing.T) *Handler {
	t.Helper()
	h := testMagicSetup(t)
	h.openovaKC = &stubKeycloakClient{
		impersonateAccess:  "kc-access-token",
		impersonateRefresh: "kc-refresh-token",
		impersonateExpiry:  3600,
	}
	return h
}

// TestMagicValidate_HappyPath tests the full validate flow:
// valid JWT → KC token-exchange → cookies set → redirect to wizard.
func TestMagicValidate_HappyPath(t *testing.T) {
	h := magicValidateHandler(t)
	tok := mintMagicToken(t, h, "operator@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status: got %d want 302 (body: %s)", resp.StatusCode, w.Body.String())
	}

	// Cookies.
	sessionCookie := findCookie(resp.Cookies(), "catalyst_session")
	if sessionCookie == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	if sessionCookie.Value != "kc-access-token" {
		t.Errorf("catalyst_session value: got %q want kc-access-token", sessionCookie.Value)
	}
	if !sessionCookie.HttpOnly {
		t.Error("catalyst_session must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Error("catalyst_session must be SameSite=Lax")
	}

	refreshCookie := findCookie(resp.Cookies(), "catalyst_refresh")
	if refreshCookie == nil {
		t.Fatal("catalyst_refresh cookie not set")
	}
	if refreshCookie.Value != "kc-refresh-token" {
		t.Errorf("catalyst_refresh value: got %q", refreshCookie.Value)
	}
}

// TestMagicValidate_MissingToken tests that a missing ?token param redirects to /login.
func TestMagicValidate_MissingToken(t *testing.T) {
	h := magicValidateHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic", nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	// Missing token → redirect to /login (not 401) so the user sees the login UI.
	if w.Code != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", w.Code)
	}
}

// TestMagicValidate_ExpiredToken tests that an expired JWT redirects to /login?error=.
func TestMagicValidate_ExpiredToken(t *testing.T) {
	h := magicValidateHandler(t)

	now := time.Now().UTC()
	claims := magicLinkClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    magicLinkIssuer,
			Audience:  jwt.ClaimStrings{magicLinkAudience},
			Subject:   "user-uuid-001",
			IssuedAt:  jwt.NewNumericDate(now.Add(-30 * time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(-15 * time.Minute)),
			ID:        "expired-jti",
		},
		Email:         "op@example.com",
		EmailVerified: true,
		Role:          magicLinkRole,
	}
	tok, err := h.handoverSigner.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("SignCustomClaims: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expired token: got %d want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "error=") {
		t.Errorf("Location should contain error param, got %q", loc)
	}
}

// TestMagicValidate_ReplayedJTI tests that a second use of the same JTI is rejected.
func TestMagicValidate_ReplayedJTI(t *testing.T) {
	h := magicValidateHandler(t)
	tok := mintMagicToken(t, h, "op@example.com")

	// First use — should succeed.
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w1 := httptest.NewRecorder()
	h.HandleMagicValidate(w1, req1)
	if w1.Code != http.StatusFound {
		t.Fatalf("first use: expected 302, got %d (body: %s)", w1.Code, w1.Body.String())
	}

	// Second use — should be rejected (redirect to /login?error=token_replayed).
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w2 := httptest.NewRecorder()
	h.HandleMagicValidate(w2, req2)
	if w2.Code != http.StatusSeeOther {
		t.Errorf("replayed jti: got %d want 303", w2.Code)
	}
	loc := w2.Header().Get("Location")
	if !strings.Contains(loc, "token_replayed") {
		t.Errorf("Location should contain token_replayed, got %q", loc)
	}
}

// TestMagicValidate_WrongAudience tests that a token with the wrong audience claim
// redirects to /login?error=invalid_audience.
func TestMagicValidate_WrongAudience(t *testing.T) {
	h := magicValidateHandler(t)

	now := time.Now().UTC()
	claims := magicLinkClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    magicLinkIssuer,
			Audience:  jwt.ClaimStrings{"https://evil.example.com/auth/magic"},
			Subject:   "user-uuid-001",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(magicLinkTTL)),
			ID:        "aud-test-jti",
		},
		Email:         "op@example.com",
		EmailVerified: true,
		Role:          magicLinkRole,
	}
	tok, err := h.handoverSigner.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("SignCustomClaims: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	if w.Code != http.StatusSeeOther {
		t.Errorf("wrong aud: got %d want 303", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.Contains(loc, "invalid_audience") {
		t.Errorf("Location should contain invalid_audience, got %q", loc)
	}
}

// TestMagicValidate_KCTokenExchangeFailure tests that a Keycloak failure returns
// a 401 error (not a redirect to login).
func TestMagicValidate_KCTokenExchangeFailure(t *testing.T) {
	h := magicValidateHandler(t)
	h.openovaKC = &stubKeycloakClient{
		impersonateErr: fmt.Errorf("token exchange denied by keycloak"),
	}
	tok := mintMagicToken(t, h, "op@example.com")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("KC failure: got %d want 401", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "keycloak error: token exchange" {
		t.Errorf("error message: got %q", body["error"])
	}
}

// TestMagicValidate_NoSignerReturns503 ensures that without a signer the
// endpoint returns 503.
func TestMagicValidate_NoSignerReturns503(t *testing.T) {
	h := &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: nil,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token=sometoken", nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("no signer: got %d want 401", w.Code)
	}
}

// TestMagicValidate_UnknownUser ensures that a token with empty email/sub is
// rejected before reaching KC.
func TestMagicValidate_UnknownUser(t *testing.T) {
	h := magicValidateHandler(t)

	now := time.Now().UTC()
	claims := magicLinkClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    magicLinkIssuer,
			Audience:  jwt.ClaimStrings{magicLinkAudience},
			Subject:   "", // empty sub
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(magicLinkTTL)),
			ID:        "empty-sub-jti",
		},
		Email:         "",   // empty email
		EmailVerified: true,
		Role:          magicLinkRole,
	}
	tok, err := h.handoverSigner.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("SignCustomClaims: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/magic?token="+tok, nil)
	w := httptest.NewRecorder()
	h.HandleMagicValidate(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("empty sub/email: got %d want 401", w.Code)
	}
}

// ─── helper ────────────────────────────────────────────────────────────────────

// stubOpenovaKC is an alias for stubKeycloakClient to make tests self-documenting.
type stubOpenovaKC = stubKeycloakClient

// writeFile writes data to path. Simple wrapper around os.WriteFile.
func writeFile(_ *testing.T, path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}

// ─── overridden stubKeycloakClient for openova realm ─────────────────────────

type openovaStubKC struct {
	ensureUserID string
	ensureErr    error
	impAccess    string
	impRefresh   string
	impExpiry    int
	impErr       error
}

func (s *openovaStubKC) EnsureUser(_ context.Context, _, _ string) (string, error) {
	if s.ensureErr != nil {
		return "", s.ensureErr
	}
	id := s.ensureUserID
	if id == "" {
		id = "openova-user-uuid"
	}
	return id, nil
}

func (s *openovaStubKC) ImpersonateToken(_ context.Context, _, _ string) (string, string, int, error) {
	if s.impErr != nil {
		return "", "", 0, s.impErr
	}
	access := s.impAccess
	if access == "" {
		access = "openova-access-token"
	}
	refresh := s.impRefresh
	if refresh == "" {
		refresh = "openova-refresh-token"
	}
	expiry := s.impExpiry
	if expiry == 0 {
		expiry = 3600
	}
	return access, refresh, expiry, nil
}
