// sandbox_token_test.go — unit tests for the real /admin/tokens/sandbox
// mint impl (PR #1638; replaces the PR #1619 stub). Each test exercises
// one branch of the handler so the documented failure modes regress
// loudly.
//
// Coverage:
//   - happy path (mint + claim shape + signature round-trip)
//   - de-dup + empty-channel scrub
//   - admin-secret mismatch → 401
//   - missing/malformed bearer → 401
//   - missing required body fields → 400
//   - non-POST → 405
//   - unset signing key or admin secret → 503
//   - mintSandboxToken empty-secret guard

package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// newTestHandler wires a Handler with deterministic clock + safe defaults.
func newTestHandler() *Handler {
	return &Handler{
		SigningKey:  []byte("test-signing-key-bytes-32+chars-ok"),
		AdminSecret: []byte("test-admin-secret-bytes-do-not-reuse"),
		Log:         nil, // silence — logSafe handles nil
		Now: func() time.Time {
			// Frozen epoch for deterministic exp. Anchored in the
			// future so jwt's built-in exp check doesn't fire on the
			// 7-day TTL when this test runs months from now.
			return time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
		},
	}
}

func doRequest(t *testing.T, h *Handler, method, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/admin/tokens/sandbox", strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.SandboxToken(rr, req)
	return rr
}

func TestSandboxToken_HappyPath(t *testing.T) {
	h := newTestHandler()
	body := `{
		"org_id": "bankdhofar",
		"user_id": "alice@bankdhofar.com",
		"sandbox_id": "sb-uid-12345",
		"allowed_channels": ["qwen3.6-bankdhofar"]
	}`
	rr := doRequest(t, h, http.MethodPost, body, string(h.AdminSecret))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp sandboxTokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("response token empty")
	}
	// Verify expires_at = iat + 7d (deterministic clock).
	wantExp := time.Date(2099, 1, 8, 0, 0, 0, 0, time.UTC)
	if !resp.ExpiresAt.Equal(wantExp) {
		t.Errorf("expires_at: got %s want %s", resp.ExpiresAt, wantExp)
	}
	// Parse the token with the same signing key and assert claim shape.
	parsed, err := jwt.Parse(resp.Token, func(tok *jwt.Token) (any, error) {
		if _, ok := tok.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return h.SigningKey, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("token parse: err=%v valid=%v", err, parsed != nil && parsed.Valid)
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		t.Fatalf("claims wrong type: %T", parsed.Claims)
	}
	if got, _ := claims["sub"].(string); got != "sb-uid-12345" {
		t.Errorf("sub: got %q want sb-uid-12345", got)
	}
	if got, _ := claims["org"].(string); got != "bankdhofar" {
		t.Errorf("org: got %q want bankdhofar", got)
	}
	if got, _ := claims["user"].(string); got != "alice@bankdhofar.com" {
		t.Errorf("user: got %q want alice@bankdhofar.com", got)
	}
	if got, _ := claims["typ"].(string); got != SandboxTokenType {
		t.Errorf("typ: got %q want %q", got, SandboxTokenType)
	}
	chs, _ := claims["channels"].([]any)
	if len(chs) != 1 || chs[0] != "qwen3.6-bankdhofar" {
		t.Errorf("channels: got %v want [qwen3.6-bankdhofar]", chs)
	}
}

func TestSandboxToken_DedupAndScrubChannels(t *testing.T) {
	h := newTestHandler()
	body := `{
		"org_id": "acme",
		"user_id": "u@acme.com",
		"sandbox_id": "sb-1",
		"allowed_channels": ["a", "", "b", "a", " "]
	}`
	rr := doRequest(t, h, http.MethodPost, body, string(h.AdminSecret))
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", rr.Code, rr.Body.String())
	}
	var resp sandboxTokenResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	parsed, _ := jwt.Parse(resp.Token, func(*jwt.Token) (any, error) { return h.SigningKey, nil })
	claims, _ := parsed.Claims.(jwt.MapClaims)
	chs, _ := claims["channels"].([]any)
	got := make([]string, len(chs))
	for i, v := range chs {
		got[i], _ = v.(string)
	}
	want := []string{"a", "b"}
	if len(got) != len(want) {
		t.Fatalf("channels len: got %d want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("channels[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestSandboxToken_AdminSecretMismatch(t *testing.T) {
	h := newTestHandler()
	body := `{"org_id":"o","user_id":"u","sandbox_id":"s","allowed_channels":["a"]}`
	rr := doRequest(t, h, http.MethodPost, body, "wrong-secret")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
}

func TestSandboxToken_MissingBearer(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/admin/tokens/sandbox",
		strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	h.SandboxToken(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401", rr.Code)
	}
}

func TestSandboxToken_RejectsMissingFields(t *testing.T) {
	h := newTestHandler()
	cases := []struct {
		name string
		body string
	}{
		{"missing org_id", `{"user_id":"u","sandbox_id":"s","allowed_channels":["a"]}`},
		{"missing user_id", `{"org_id":"o","sandbox_id":"s","allowed_channels":["a"]}`},
		{"missing sandbox_id", `{"org_id":"o","user_id":"u","allowed_channels":["a"]}`},
		{"empty channels list", `{"org_id":"o","user_id":"u","sandbox_id":"s","allowed_channels":[]}`},
		{"channels all empty", `{"org_id":"o","user_id":"u","sandbox_id":"s","allowed_channels":["",""]}`},
		{"malformed body", `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequest(t, h, http.MethodPost, tc.body, string(h.AdminSecret))
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status: got %d want 400, body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestSandboxToken_RejectsNonPOST(t *testing.T) {
	h := newTestHandler()
	rr := doRequest(t, h, http.MethodGet, "", string(h.AdminSecret))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status: got %d want 405", rr.Code)
	}
}

func TestSandboxToken_UnsetEnvReturns503(t *testing.T) {
	h := &Handler{} // SigningKey + AdminSecret both empty
	rr := doRequest(t, h, http.MethodPost,
		`{"org_id":"o","user_id":"u","sandbox_id":"s","allowed_channels":["a"]}`,
		"any")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503", rr.Code)
	}
}

func TestMintSandboxToken_EmptySecret(t *testing.T) {
	_, err := mintSandboxToken(nil, sandboxClaims{
		Sub: "x", Org: "o", User: "u",
		Channels: []string{"a"},
		IssuedAt: time.Now(),
		ExpireAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected error for empty secret, got nil")
	}
}

func TestMintSandboxToken_RoundTrip(t *testing.T) {
	secret := []byte("ks-32bytes-test-secret-for-mint-rt")
	now := time.Now()
	exp := now.Add(SandboxTokenTTL)
	tok, err := mintSandboxToken(secret, sandboxClaims{
		Sub:      "sb-uid-xyz",
		Org:      "acme",
		User:     "ceo@acme.com",
		Channels: []string{"qwen3.6-bankdhofar", "vllm-cheap"},
		IssuedAt: now,
		ExpireAt: exp,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	parsed, err := jwt.Parse(tok, func(*jwt.Token) (any, error) { return secret, nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse: err=%v valid=%v", err, parsed != nil && parsed.Valid)
	}
	claims, _ := parsed.Claims.(jwt.MapClaims)
	expF, _ := claims["exp"].(float64)
	if int64(expF) != exp.Unix() {
		t.Errorf("exp: got %d want %d", int64(expF), exp.Unix())
	}
	iatF, _ := claims["iat"].(float64)
	if int64(iatF) != now.Unix() {
		t.Errorf("iat: got %d want %d", int64(iatF), now.Unix())
	}
}

// Sanity check that we don't leak the admin secret in the response on a
// successful mint — the response should ONLY carry the minted JWT, not
// echo any inbound credential.
func TestSandboxToken_DoesNotEchoSecrets(t *testing.T) {
	h := newTestHandler()
	body := `{"org_id":"o","user_id":"u","sandbox_id":"s","allowed_channels":["a"]}`
	rr := doRequest(t, h, http.MethodPost, body, string(h.AdminSecret))
	raw, _ := io.ReadAll(bytes.NewReader(rr.Body.Bytes()))
	if strings.Contains(string(raw), string(h.AdminSecret)) {
		t.Fatal("response leaked admin secret in body")
	}
	if strings.Contains(string(raw), string(h.SigningKey)) {
		t.Fatal("response leaked signing key in body")
	}
}
