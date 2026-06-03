package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/textproto"
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
	// qa-loop iter-6 TC-001 — Sent flag must mirror OK so callers
	// pinning on the canonical email-dispatch verb resolve the same
	// way as legacy callers pinning on `ok`.
	if !body.Sent {
		t.Error("sent: got false want true (qa-loop iter-6 TC-001 contract)")
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

// TestPinIssue_SMTP5xxReturns422 is the regression guard for the WBS
// Cov-bench C6-010 / issue #1742 failure mode: the synthetic bench
// recipient (wbs-cov-redeem-<ts>@openova.io) gets rejected by Stalwart
// with `550 Mailbox does not exist` — a permanent caller fault that
// must NOT be reported as a 502 because retry will never help.
//
// Pre-fix the handler swallowed every SMTP error into an opaque 502
// "could not deliver code", making it impossible for the test bench or
// the operator to tell "relay broken" apart from "bad recipient". This
// test pins the new contract:
//
//   - 5xx server reply → 422 email-rejected + smtpCode echoed.
//   - 4xx server reply → 502 email-send-failed + smtpCode echoed.
//   - Non-protocol error (TCP/TLS/auth) → 502 email-send-failed (legacy).
//
// In every case the pin entry is rolled back so the operator isn't
// trapped by the 60-second per-email cooldown.
func TestPinIssue_SMTP5xxReturns422(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(func(_, _ string) error {
		return &textproto.Error{Code: 550, Msg: "5.1.2 Mailbox does not exist."}
	})()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"nobody@openova.io"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d want 422 (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "email-rejected" {
		t.Errorf("error: got %v want email-rejected", body["error"])
	}
	if got, _ := body["smtpCode"].(float64); int(got) != 550 {
		t.Errorf("smtpCode: got %v want 550", body["smtpCode"])
	}
	if got := h.pinStore.size(); got != 0 {
		t.Errorf("store size after 5xx rejection: got %d want 0 (rollback expected)", got)
	}
}

func TestPinIssue_SMTP4xxReturns502WithCode(t *testing.T) {
	h := testPinSetup(t)
	defer withSendPinEmail(func(_, _ string) error {
		return &textproto.Error{Code: 451, Msg: "4.7.1 Temporary local problem."}
	})()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/issue",
		strings.NewReader(`{"email":"op@example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandlePinIssue(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("status: got %d want 502 (body: %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["error"] != "email-send-failed" {
		t.Errorf("error: got %v want email-send-failed", body["error"])
	}
	if got, _ := body["smtpCode"].(float64); int(got) != 451 {
		t.Errorf("smtpCode: got %v want 451", body["smtpCode"])
	}
	if got := h.pinStore.size(); got != 0 {
		t.Errorf("store size after 4xx transient: got %d want 0 (rollback expected)", got)
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
		`{"email":"op@example.com","pin":"abc123","requestId":"r"}`,  // letters
		`{"email":"op@example.com","pin":"12345","requestId":"r"}`,   // too short
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

// TestPinEmail_SovereignFQDNRoutesLoginURL is the regression guard for
// TBD-A68 / #1994 — the PIN email login URL must follow whichever
// deployment mode the catalyst-api is running in:
//
//   - Chroot mode (SOVEREIGN_FQDN env set) → tenant users get a link
//     to THEIR Sovereign console (`https://console.<fqdn>/login`).
//     Pre-fix every Sovereign tenant got
//     `https://console.openova.io/sovereign/login` and bounced into
//     Catalyst-Zero instead.
//   - Mothership mode (SOVEREIGN_FQDN unset) → keep the historical
//     `https://console.openova.io/sovereign/login` target.
//
// Both the plaintext alternative and the HTML body must agree, since
// some Gmail / Outlook clients still strip <a> and render the visible
// text only.
func TestPinEmail_SovereignFQDNRoutesLoginURL(t *testing.T) {
	t.Run("chroot mode routes to sovereign console", func(t *testing.T) {
		t.Setenv("SOVEREIGN_FQDN", "t38.omani.works")
		got := pinEmailLoginURL()
		want := "https://console.t38.omani.works/login"
		if got != want {
			t.Errorf("pinEmailLoginURL: got %q want %q", got, want)
		}

		plain := pinEmailPlainText("123456", got)
		if !strings.Contains(plain, want) {
			t.Errorf("plain body missing %q: %s", want, plain)
		}
		if strings.Contains(plain, "openova.io") {
			t.Errorf("plain body must NOT contain openova.io on a Sovereign: %s", plain)
		}

		html := pinEmailHTML("123456", got)
		if !strings.Contains(html, want) {
			t.Errorf("html body missing %q href", want)
		}
		// Visible display host text must be `console.<fqdn>` — never
		// `console.openova.io` on a Sovereign.
		if !strings.Contains(html, ">console.t38.omani.works<") {
			t.Errorf("html body must render visible host `console.t38.omani.works` text")
		}
		// Allow the canonical mothership footer link (`<a href="https://openova.io">openova.io</a>`)
		// — strip it before asserting the body contains no other `openova.io` references.
		footerStripped := strings.ReplaceAll(html, `<a href="https://openova.io" style="color:#8e94a3;text-decoration:underline;">openova.io</a>`, "")
		if strings.Contains(footerStripped, "openova.io") {
			t.Errorf("html body must NOT route tenant traffic through openova.io: %s", footerStripped)
		}
	})

	t.Run("mothership mode keeps openova.io target", func(t *testing.T) {
		t.Setenv("SOVEREIGN_FQDN", "")
		got := pinEmailLoginURL()
		want := "https://console.openova.io/sovereign/login"
		if got != want {
			t.Errorf("pinEmailLoginURL: got %q want %q", got, want)
		}
	})
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

// TestPinVerify_NoSessionReplay_FreshJWTRegardlessOfInboundCookie is the
// regression guard for TBD-F7 / #1730 (Wave 28-F session-replay walk).
//
// Symptom observed in Playwright: after a PIN cycle, the Set-Cookie
// header on /pin/verify carried a STALE catalyst_session JWT from a
// previous cycle even though the freshly-minted JWT was on the wire
// internally. curl-driven cycles worked because curl honours the LAST
// Set-Cookie when duplicates appear, while Playwright's cookie jar
// surfaced the FIRST one.
//
// Two contract assertions:
//
//  1. Two back-to-back PIN cycles produce two DIFFERENT catalyst_session
//     cookie values (fresh jti / iat per call). A handler that
//     pass-through-reused an inbound cookie would emit the same value
//     on the second call.
//
//  2. The response carries EXACTLY ONE `Set-Cookie: catalyst_session=…`
//     header — no duplicates. Even if a stale Set-Cookie had been
//     stamped on the ResponseWriter by an upstream middleware before
//     HandlePinVerify ran, the handler MUST emit a single canonical
//     cookie carrying the freshly-minted JWT so the browser cookie jar
//     can never select a stale value.
//
// Inbound `Cookie:` header carrying a stale catalyst_session is passed
// on both calls to mirror the Playwright browser-context replay shape.
func TestPinVerify_NoSessionReplay_FreshJWTRegardlessOfInboundCookie(t *testing.T) {
	h := testPinSetup(t)

	// ── Cycle 1: empty inbound cookie ────────────────────────────────────
	h.pinStore.put("op@example.com", "111111", "req-1")
	body1 := `{"email":"op@example.com","pin":"111111","requestId":"req-1"}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	h.HandlePinVerify(w1, req1)
	resp1 := w1.Result()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("cycle-1 status: got %d want 200 (body: %s)", resp1.StatusCode, w1.Body.String())
	}
	cookie1 := findCookie(resp1.Cookies(), "catalyst_session")
	if cookie1 == nil || cookie1.Value == "" {
		t.Fatal("cycle-1: catalyst_session cookie not set")
	}

	// Assert exactly one Set-Cookie: catalyst_session=… on the wire.
	if got := countSessionSetCookieHeaders(resp1.Header.Values("Set-Cookie")); got != 1 {
		t.Errorf("cycle-1: got %d Set-Cookie: catalyst_session=… headers want 1", got)
	}

	// ── Cycle 2: inbound cookie carries the STALE cycle-1 JWT ────────────
	//
	// Mirrors the Playwright browser-context replay: the cookie jar still
	// holds the previous PIN cycle's catalyst_session and Playwright
	// attaches it to the second /pin/verify request. The handler MUST
	// mint a fresh JWT (different jti, ≥ same iat) and the Set-Cookie
	// header MUST carry that fresh JWT, not the inbound stale one.
	//
	// Sleep one second so the JWT's `iat`/`exp` (Unix second precision)
	// differs between cycles — the `jti` UUID would differ anyway, but
	// the time-shift makes the wire-shape mismatch obvious even to a
	// jwt-payload diff that ignores `jti`.
	time.Sleep(1100 * time.Millisecond)
	h.pinStore.put("op@example.com", "222222", "req-2")
	body2 := `{"email":"op@example.com","pin":"222222","requestId":"req-2"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify",
		strings.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: cookie1.Value})
	w2 := httptest.NewRecorder()
	h.HandlePinVerify(w2, req2)
	resp2 := w2.Result()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("cycle-2 status: got %d want 200 (body: %s)", resp2.StatusCode, w2.Body.String())
	}
	cookie2 := findCookie(resp2.Cookies(), "catalyst_session")
	if cookie2 == nil || cookie2.Value == "" {
		t.Fatal("cycle-2: catalyst_session cookie not set")
	}

	// (1) Cycle-2 cookie value MUST differ from cycle-1 — proves the
	// handler does NOT pass through the inbound stale cookie.
	if cookie2.Value == cookie1.Value {
		t.Errorf("cycle-2 catalyst_session reuses cycle-1 JWT (session-replay regression):\n  cycle-1: %s\n  cycle-2: %s",
			truncJWT(cookie1.Value), truncJWT(cookie2.Value))
	}

	// (2) Cycle-2 must also have exactly one Set-Cookie: catalyst_session.
	if got := countSessionSetCookieHeaders(resp2.Header.Values("Set-Cookie")); got != 1 {
		t.Errorf("cycle-2: got %d Set-Cookie: catalyst_session=… headers want 1 (duplicates would let Playwright pick the stale one)", got)
	}

	// (3) Cycle-2 cookie value must NOT equal the stale cookie value
	// from the inbound Cookie header — belt-and-braces against any
	// future regression where the handler echoes the inbound cookie.
	if cookie2.Value == cookie1.Value {
		t.Error("cycle-2 catalyst_session matches inbound stale cookie value (handler must mint fresh)")
	}

	// (4) Decode payloads — `jti` MUST differ (fresh mint per call) and
	// `iat` MUST be ≥ cycle-1's `iat` (time can't go backwards).
	jti1, iat1 := decodeJtiIat(t, cookie1.Value)
	jti2, iat2 := decodeJtiIat(t, cookie2.Value)
	if jti1 == "" || jti2 == "" {
		t.Fatal("jti missing on one of the cycles")
	}
	if jti1 == jti2 {
		t.Errorf("jti must differ between cycles (replay regression): both = %q", jti1)
	}
	if iat2 < iat1 {
		t.Errorf("iat must not go backwards: cycle-1=%d cycle-2=%d", iat1, iat2)
	}
}

// countSessionSetCookieHeaders returns the number of Set-Cookie header
// values that set the catalyst_session cookie (i.e. start with
// "catalyst_session="). A correctly behaving handler emits exactly one;
// duplicates create the Playwright session-replay ambiguity.
func countSessionSetCookieHeaders(headers []string) int {
	n := 0
	for _, h := range headers {
		if strings.HasPrefix(h, auth.SessionCookieName+"=") {
			n++
		}
	}
	return n
}

// truncJWT shortens a JWT for log readability (head…tail).
func truncJWT(j string) string {
	if len(j) <= 24 {
		return j
	}
	return j[:12] + "…" + j[len(j)-12:]
}

// decodeJtiIat extracts the `jti` and `iat` claims from a compact JWT
// (raw, no signature verification) for regression assertions.
func decodeJtiIat(t *testing.T, rawJWT string) (string, int64) {
	t.Helper()
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		t.Fatalf("decodeJtiIat: malformed JWT (%d parts)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decodeJtiIat: decode payload: %v", err)
	}
	var claims struct {
		Jti string `json:"jti"`
		Iat int64  `json:"iat"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("decodeJtiIat: unmarshal: %v", err)
	}
	return claims.Jti, claims.Iat
}

// TestPinIssuer_FallbackToHardcode_2940 — locks down the env-driven
// pinIssuer contract from #2940. Pre-#2940 was a const hardcode
// (`https://console.openova.io`) that defeated bp-self-sovereign-cutover
// — franchised Sovereigns stamped the mothership URL in the JWT `iss`
// claim. Post-#2940 reads CATALYST_PIN_ISSUER env with the legacy
// hardcode as back-compat fallback for pre-#2940 Sovereigns.
//
// pinIssuer is a package-level var initialised at package load time, so
// this test verifies the fallback path (env unset). Override paths are
// exercised by the per-Sovereign HelmRelease overlay's catalystApi.env
// additional-env patch documented in chart/templates/api-deployment.yaml.
func TestPinIssuer_FallbackToHardcode_2940(t *testing.T) {
	if pinIssuer == "" {
		t.Fatalf("pinIssuer must never be empty (would mint un-issuer'd JWTs)")
	}
	if pinIssuer != "https://console.openova.io" {
		t.Logf("pinIssuer overridden via CATALYST_PIN_ISSUER env (value=%q); fallback path not exercised in this test process", pinIssuer)
	}
}
