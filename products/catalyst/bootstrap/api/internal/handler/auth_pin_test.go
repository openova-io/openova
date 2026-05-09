package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// testPinSetup builds a Handler with a real handoverjwt.Signer (so PIN
// verify can actually mint a session JWT) and a stub Keycloak client so
// EnsureUser succeeds without hitting a real KC.
func testPinSetup(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()

	privPEM, pubJWK, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	keyPath := filepath.Join(dir, "handover.pem")
	pubPath := filepath.Join(dir, "public.jwk")
	if err := os.WriteFile(keyPath, privPEM, 0o600); err != nil {
		t.Fatalf("write privPEM: %v", err)
	}
	if err := os.WriteFile(pubPath, pubJWK, 0o644); err != nil {
		t.Fatalf("write pubJWK: %v", err)
	}

	signer, err := handoverjwt.LoadOrGenerate(keyPath, pubPath, "https://console.openova.io", 5*time.Minute)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	return &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: signer,
		openovaKC:      &stubKeycloakClient{},
		// Inject a deterministic in-memory store so every test starts from
		// a clean slate and we don't leak the background sweeper goroutine.
		pinStore: newPinStoreNoSweeper(),
	}
}

// withSendPinEmail swaps sendPinEmail for the duration of one test and
// returns a restore function. Use defer withSendPinEmail(stub)().
func withSendPinEmail(stub func(to, pin string) error) func() {
	prev := sendPinEmail
	sendPinEmail = stub
	return func() { sendPinEmail = prev }
}

func noopSendPin(_, _ string) error { return nil }

var errSMTPDown = errors.New("smtp: relay unavailable")

// ─── HandlePinIssue ───────────────────────────────────────────────────────────

func TestPinIssue_HappyPath(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(noopSendPin)()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"op@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200 (body: %s)", w.Code, w.Body.String())
	}
	var body pinIssueResponse
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !body.OK {
		t.Error("ok: got false want true")
	}
	if body.RequestID == "" {
		t.Error("requestId: got empty")
	}
	if body.ExpiresInSec != int(pinTTL.Seconds()) {
		t.Errorf("expiresInSec: got %d want %d", body.ExpiresInSec, int(pinTTL.Seconds()))
	}
	if got := h.pinStore.size(); got != 1 {
		t.Errorf("store size: got %d want 1", got)
	}
}

func TestPinIssue_NoSignerReturns503(t *testing.T) {
	h := &Handler{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverSigner: nil,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"x@y.z"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503", w.Code)
	}
}

func TestPinIssue_EmailRequired(t *testing.T) {
	h := testPinSetup(t)
	cases := []string{
		`{}`,
		`{"email":""}`,
		`{"email":"   "}`,
	}
	for _, b := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
			strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.HandlePinIssue(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%q: status got %d want 400", b, w.Code)
		}
		var body map[string]any
		_ = json.NewDecoder(w.Body).Decode(&body)
		if body["error"] != "email-required" {
			t.Errorf("body=%q: error code got %v want email-required", b, body["error"])
		}
	}
}

func TestPinIssue_NoKCReturns503(t *testing.T) {
	h := testPinSetup(t)
	h.openovaKC = nil
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"x@y.z"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d want 503 (body: %s)", w.Code, w.Body.String())
	}
}

func TestPinIssue_RateLimited(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(noopSendPin)()

	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"op@example.com"}`))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.HandlePinIssue(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first issue: status got %d want 200", w1.Code)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"op@example.com"}`))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.HandlePinIssue(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Errorf("second issue: status got %d want 429 (body: %s)", w2.Code, w2.Body.String())
	}
	var body map[string]any
	_ = json.NewDecoder(w2.Body).Decode(&body)
	if body["error"] != "pin-rate-limited" {
		t.Errorf("error code: got %v want pin-rate-limited", body["error"])
	}
}

func TestPinIssue_SMTPFailureRollsBackEntry(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(func(_, _ string) error {
		return errSMTPDown
	})()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"op@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)
	if w.Code != http.StatusBadGateway {
		t.Errorf("status: got %d want 502", w.Code)
	}
	if got := h.pinStore.size(); got != 0 {
		t.Errorf("store size after SMTP failure: got %d want 0 (rollback expected)", got)
	}
}

// ─── HandlePinVerify ──────────────────────────────────────────────────────────

func TestPinVerify_HappyPath(t *testing.T) {
	h := testPinSetup(t)
	h.pinStore.put("op@example.com", "123456", "req-1")

	body := `{"email":"op@example.com","pin":"123456","requestId":"req-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200 (body: %s)", resp.StatusCode, w.Body.String())
	}

	var out pinVerifyResponse
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !out.OK {
		t.Error("ok: got false want true")
	}

	sessionCookie := findCookie(resp.Cookies(), "catalyst_session")
	if sessionCookie == nil {
		t.Fatal("catalyst_session cookie not set")
	}
	if !sessionCookie.HttpOnly {
		t.Error("catalyst_session must be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Error("catalyst_session must be SameSite=Lax")
	}
	if sessionCookie.Value == "" {
		t.Error("catalyst_session value: got empty")
	}
	if got := h.pinStore.size(); got != 0 {
		t.Errorf("store size after verify: got %d want 0", got)
	}
}

// TestPinVerify_StampsTierAndRealmRoleClaims is the iter-1 qa-loop
// regression guard for the rbac-audit-403-gates cluster (TC-063..069/077).
//
// Before the fix the PIN-verify session JWT carried only {sub, email, role}
// — no `tier`, no `realm_access.roles`. Every privileged catalyst-api
// endpoint backed by rbacAssignCallerAuthorized / policyModeCallerAuthorized
// (rbac_audit, rbac_assign, keycloak_proxy U2/U3/U4, blueprints/curate,
// policy_mode, continuum audit) thus returned 403 even for the Sovereign
// owner authenticated via PIN-IMAP. This test pins the contract:
//
//  1. tier = pinSessionTier ("owner")
//  2. realm_access.roles contains pinSessionRealmRole ("catalyst-owner")
//
// Either claim alone unlocks the gates (HasRealmRole walk OR Tier check).
// Stamping both keeps the contract idempotent across the gate variants.
func TestPinVerify_StampsTierAndRealmRoleClaims(t *testing.T) {
	h := testPinSetup(t)
	h.pinStore.put("op@example.com", "123456", "req-1")

	body := `{"email":"op@example.com","pin":"123456","requestId":"req-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200 (body: %s)", resp.StatusCode, w.Body.String())
	}

	cookie := findCookie(resp.Cookies(), "catalyst_session")
	if cookie == nil || cookie.Value == "" {
		t.Fatal("catalyst_session cookie not set")
	}

	// The cookie value IS the raw self-signed JWT (Option B in
	// auth/session.go ReadSessionToken). Decode the payload directly so
	// the test doesn't depend on JWKS validation — the contract is the
	// claims SHAPE, not signature verification (covered elsewhere).
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 3 {
		t.Fatalf("cookie value: got %d JWT parts want 3", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode JWT payload: %v", err)
	}
	var claims auth.Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	// (1) tier claim must equal pinSessionTier so policyModeCallerAuthorized
	// (the strict admin/owner gate) accepts the PIN-derived session.
	if claims.Tier != pinSessionTier {
		t.Errorf("tier: got %q want %q", claims.Tier, pinSessionTier)
	}

	// (2) realm_access.roles must contain pinSessionRealmRole so
	// rbacAssignCallerAuthorized's HasRealmRole walk also accepts it
	// (matches the legacy Keycloak-issued token contract).
	if !claims.HasRealmRole(pinSessionRealmRole) {
		t.Errorf("realm_access.roles missing %q (got: %v)",
			pinSessionRealmRole, claims.RealmAccess.Roles)
	}

	// (3) Sanity: feeding the parsed claims through the actual gate
	// functions used by rbac_audit + keycloak_proxy + blueprints/curate
	// must return true. This is the end-to-end binding between the
	// session-mint contract and the authorization seam.
	if !rbacAssignCallerAuthorized(&claims) {
		t.Error("rbacAssignCallerAuthorized: PIN-derived claims should authorize tier-admin/owner gate")
	}
	if !policyModeCallerAuthorized(&claims) {
		t.Error("policyModeCallerAuthorized: PIN-derived claims should authorize sovereign-admin gate")
	}
}

func TestPinVerify_WrongPINIncrements(t *testing.T) {
	h := testPinSetup(t)
	h.pinStore.put("op@example.com", "123456", "req-1")

	body := `{"email":"op@example.com","pin":"000000","requestId":"req-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "pin-invalid" {
		t.Errorf("error: got %v want pin-invalid", resp["error"])
	}
	if resp["attemptsRemaining"].(float64) != 2 {
		t.Errorf("attemptsRemaining: got %v want 2", resp["attemptsRemaining"])
	}
}

func TestPinVerify_ThirdWrongLocks(t *testing.T) {
	h := testPinSetup(t)
	h.pinStore.put("op@example.com", "123456", "req-1")

	wrong := `{"email":"op@example.com","pin":"000000","requestId":"req-1"}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
			strings.NewReader(wrong))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.HandlePinVerify(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status got %d want 401", i+1, w.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(wrong))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)
	if w.Code != http.StatusGone {
		t.Errorf("third wrong: status got %d want 410 (body: %s)", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "attempts-exceeded" {
		t.Errorf("error: got %v want attempts-exceeded", resp["error"])
	}
}

func TestPinVerify_ExpiredReturns410(t *testing.T) {
	h := testPinSetup(t)
	now, advance := frozenClock(time.Now())
	h.pinStore.now = now
	h.pinStore.put("op@example.com", "123456", "req-1")
	advance(pinTTL + time.Second)

	body := `{"email":"op@example.com","pin":"123456","requestId":"req-1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusGone {
		t.Errorf("status: got %d want 410", w.Code)
	}
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp["error"] != "pin-expired" {
		t.Errorf("error: got %v want pin-expired", resp["error"])
	}
}

func TestPinVerify_MalformedPINReturns400(t *testing.T) {
	h := testPinSetup(t)
	cases := []string{
		`{"email":"op@example.com","pin":"abc123","requestId":"r"}`, // letters
		`{"email":"op@example.com","pin":"12345","requestId":"r"}`,  // too short
		`{"email":"op@example.com","pin":"1234567","requestId":"r"}`, // too long
	}
	for _, b := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
			strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.HandlePinVerify(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body=%s: status got %d want 400", b, w.Code)
		}
	}
}

func TestPinVerify_RequestIDMismatch(t *testing.T) {
	h := testPinSetup(t)
	h.pinStore.put("op@example.com", "123456", "req-A")

	body := `{"email":"op@example.com","pin":"123456","requestId":"req-B"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinVerify(w, req)

	if w.Code != http.StatusGone {
		t.Errorf("status: got %d want 410", w.Code)
	}
}

// ─── Email format ─────────────────────────────────────────────────────────────

// TestSendPinEmail_NoMagicLinkURL asserts the email body contains
// no magic-link URL, no clickable token, and the spaced-digit format.
// The body is rebuilt from the exact format string sendPinEmailDefault
// uses; if the format changes, this test is the canary.
func TestSendPinEmail_NoMagicLinkURL(t *testing.T) {
	pin := "372458"
	subject := "Your OpenOva sign-in code: " + pin
	spaced := strings.Join(strings.Split(pin, ""), " ")
	body := strings.Join([]string{
		"Your OpenOva sign-in code:",
		"",
		"    " + spaced,
		"",
		"Enter this code at https://console.openova.io/sovereign/login.",
		"The code expires in 10 minutes.",
		"",
		"If you didn't request this, you can ignore this email.",
	}, "\r\n")

	if !strings.Contains(subject, pin) {
		t.Errorf("subject must contain literal pin %q: got %q", pin, subject)
	}
	if !strings.Contains(body, spaced) {
		t.Errorf("body must contain spaced pin %q", spaced)
	}
	for _, banned := range []string{"?token=", "&token=", "auth/magic", "magic-link"} {
		if strings.Contains(body, banned) {
			t.Errorf("body contains forbidden substring %q", banned)
		}
	}
	loginIdx := strings.Index(body, "https://console.openova.io/sovereign/login")
	if loginIdx < 0 {
		t.Fatal("body must mention the login URL for visual reference")
	}
	rest := body[loginIdx+len("https://console.openova.io/sovereign/login"):]
	if len(rest) == 0 {
		t.Fatal("body must continue after login URL")
	}
	if rest[0] == '?' || rest[0] == '&' || rest[0] == '=' {
		t.Errorf("login URL must not be followed by a query — got %q", rest[:1])
	}
}

// TestPinStore_NoDiskPersistence enforces the credential-hygiene rule
// that PINs are in-memory only.
func TestPinStore_NoDiskPersistence(t *testing.T) {
	s, stop := newPinStore()
	defer stop()
	if pinTTL <= 0 || pinIssueCooldown <= 0 || pinMaxAttempts <= 0 {
		t.Error("pinstore constants must be positive")
	}
	if removed := s.sweep(); removed != 0 {
		t.Errorf("sweep on empty store: got %d want 0", removed)
	}
}
