package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPutKVv2_OK(t *testing.T) {
	var (
		gotPath  string
		gotToken string
		gotBody  map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("X-Vault-Token")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	err := c.PutKVv2(context.Background(), "secret", "catalyst/tofu-phase0-archive", map[string]any{
		"archive": "AGVuY3J5cHRlZA==", // base64 placeholder
	})
	if err != nil {
		t.Fatalf("PutKVv2 returned error: %v", err)
	}
	if gotPath != "/v1/secret/data/catalyst/tofu-phase0-archive" {
		t.Errorf("wrong path; got %q", gotPath)
	}
	if gotToken != "test-token" {
		t.Errorf("wrong token header; got %q", gotToken)
	}
	dataMap, ok := gotBody["data"].(map[string]any)
	if !ok {
		t.Fatalf("body missing data wrapper: %v", gotBody)
	}
	if dataMap["archive"] != "AGVuY3J5cHRlZA==" {
		t.Errorf("payload not forwarded; got %v", dataMap)
	}
}

func TestPutKVv2_DefaultMount(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "tk")
	if err := c.PutKVv2(context.Background(), "", "x/y", map[string]any{"k": "v"}); err != nil {
		t.Fatalf("PutKVv2: %v", err)
	}
	if !strings.HasPrefix(gotPath, "/v1/secret/data/") {
		t.Errorf("default mount not 'secret'; got %q", gotPath)
	}
}

func TestPutKVv2_StatusErrorWraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errors":["permission denied"]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tk")
	err := c.PutKVv2(context.Background(), "secret", "p", map[string]any{"k": "v"})
	if err == nil {
		t.Fatal("expected error on 403; got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should include status code; got %v", err)
	}
}

func TestPutKVv2_RequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		c     *Client
		path  string
		match string
	}{
		{"nil-client", (*Client)(nil), "p", "client is nil"},
		{"missing-addr", &Client{Token: "tk"}, "p", "address is required"},
		{"missing-token", &Client{Addr: "http://x"}, "p", "token is required"},
		{"missing-path", &Client{Addr: "http://x", Token: "tk"}, "", "secret path is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.c.PutKVv2(context.Background(), "secret", tc.path, map[string]any{"k": "v"})
			if err == nil {
				t.Fatal("expected error; got nil")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Errorf("error message mismatch; got %v", err)
			}
		})
	}
}

// ── GetKVv2 coverage (G117.3b #2765) ─────────────────────────────────

// TestGetKVv2_OK — happy path: 200 with canonical KV-v2 envelope shape
// returns the inner `data.data` map verbatim.
func TestGetKVv2_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", r.Method)
		}
		if got := r.Header.Get("X-Vault-Token"); got != "test-token" {
			t.Errorf("X-Vault-Token = %q, want test-token", got)
		}
		if r.URL.Path != "/v1/secret/data/org/acme/iac-bot-token" {
			t.Errorf("path = %q, want /v1/secret/data/org/acme/iac-bot-token", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"data":     map[string]any{"token": "robot-token-abc"},
				"metadata": map[string]any{"version": 1},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	got, err := c.GetKVv2(context.Background(), "secret", "org/acme/iac-bot-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["token"] != "robot-token-abc" {
		t.Errorf("data[token] = %v, want robot-token-abc", got["token"])
	}
}

// TestGetKVv2_NotFound — 404 maps to ErrSecretNotFound so callers can
// distinguish "no per-Org token yet" from transport error.
func TestGetKVv2_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []string{"not found"}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.GetKVv2(context.Background(), "secret", "org/missing/iac-bot-token")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("err = %v, want ErrSecretNotFound", err)
	}
}

// TestGetKVv2_StatusErrorWraps — non-200, non-404 surfaces the upstream
// status code + body in a wrapped error.
func TestGetKVv2_StatusErrorWraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "vault is sealed")
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.GetKVv2(context.Background(), "secret", "org/acme/iac-bot-token")
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error should include status code; got %v", err)
	}
	if errors.Is(err, ErrSecretNotFound) {
		t.Errorf("503 must NOT classify as not-found; got ErrSecretNotFound")
	}
}

// TestGetKVv2_EmptyEnvelopeIsNotFound — 200 with empty data.data is
// treated as not-found so callers' fallback path triggers rather than
// receiving nil.
func TestGetKVv2_EmptyEnvelopeIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.GetKVv2(context.Background(), "secret", "org/acme/iac-bot-token")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("err = %v, want ErrSecretNotFound", err)
	}
}

// TestGetKVv2_RequiredFields — pre-flight argument validation mirrors
// PutKVv2's contract.
func TestGetKVv2_RequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		c     *Client
		path  string
		match string
	}{
		{"nil-client", (*Client)(nil), "p", "client is nil"},
		{"missing-addr", &Client{Token: "tk"}, "p", "address is required"},
		{"missing-token", &Client{Addr: "http://x"}, "p", "token is required"},
		{"missing-path", &Client{Addr: "http://x", Token: "tk"}, "", "secret path is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.c.GetKVv2(context.Background(), "secret", tc.path)
			if err == nil {
				t.Fatal("expected error; got nil")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Errorf("error message mismatch; got %v", err)
			}
		})
	}
}

// ── OIDCAuthURL coverage (#3226 — server-side zero-click SSO shim) ────

// TestOIDCAuthURL_OK — happy path: POST /v1/auth/{mount}/oidc/auth_url
// with {role, redirect_uri} returns the inner data.auth_url verbatim.
func TestOIDCAuthURL_OK(t *testing.T) {
	const wantAuthURL = "https://auth.t01.omani.works/realms/sovereign/protocol/openid-connect/auth?client_id=openbao&redirect_uri=https%3A%2F%2Fbao.t01.omani.works%2Fui%2Fvault%2Fauth%2Foidc%2Foidc%2Fcallback&response_type=code&scope=openid&state=st&nonce=nc"
	var (
		gotPath string
		gotTok  string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		gotPath = r.URL.Path
		gotTok = r.Header.Get("X-Vault-Token")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"auth_url": wantAuthURL},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	got, err := c.OIDCAuthURL(context.Background(), "oidc", "default",
		"https://bao.t01.omani.works/ui/vault/auth/oidc/oidc/callback")
	if err != nil {
		t.Fatalf("OIDCAuthURL returned error: %v", err)
	}
	if got != wantAuthURL {
		t.Fatalf("auth_url mismatch:\n got %q\nwant %q", got, wantAuthURL)
	}
	if gotPath != "/v1/auth/oidc/oidc/auth_url" {
		t.Errorf("wrong path; got %q want /v1/auth/oidc/oidc/auth_url", gotPath)
	}
	if gotTok != "test-token" {
		t.Errorf("wrong token header; got %q", gotTok)
	}
	if gotBody["role"] != "default" {
		t.Errorf("role not forwarded; got %v", gotBody["role"])
	}
	if gotBody["redirect_uri"] != "https://bao.t01.omani.works/ui/vault/auth/oidc/oidc/callback" {
		t.Errorf("redirect_uri not forwarded; got %v", gotBody["redirect_uri"])
	}
}

// TestOIDCAuthURL_NoTokenOmitsHeader (#3374) — auth_url is an
// UNAUTHENTICATED login-flow endpoint; an addr-only client (the
// on-Sovereign shape: CATALYST_OPENBAO_ADDR set, no token) must succeed
// and must NOT send an empty X-Vault-Token header.
func TestOIDCAuthURL_NoTokenOmitsHeader(t *testing.T) {
	var (
		gotHasTok bool
		gotTok    string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, gotHasTok = r.Header["X-Vault-Token"]
		gotTok = r.Header.Get("X-Vault-Token")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"auth_url": "https://auth.t01.omani.works/x"},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "")
	got, err := c.OIDCAuthURL(context.Background(), "oidc", "operator", "https://bao.t01.omani.works/cb")
	if err != nil {
		t.Fatalf("OIDCAuthURL with no token must succeed (unauthenticated endpoint); got error: %v", err)
	}
	if got != "https://auth.t01.omani.works/x" {
		t.Fatalf("auth_url = %q", got)
	}
	if gotHasTok {
		t.Errorf("X-Vault-Token header must be omitted for a token-less client; got %q", gotTok)
	}
}

// TestOIDCAuthURL_DefaultMount — empty mount falls back to "oidc".
func TestOIDCAuthURL_DefaultMount(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"auth_url": "https://x/auth"},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "tk")
	if _, err := c.OIDCAuthURL(context.Background(), "", "default", "https://h/cb"); err != nil {
		t.Fatalf("OIDCAuthURL: %v", err)
	}
	if gotPath != "/v1/auth/oidc/oidc/auth_url" {
		t.Errorf("empty mount must default to 'oidc'; got %q", gotPath)
	}
}

// TestOIDCAuthURL_EmptyAuthURLErrors — a 200 with no auth_url is a
// failure (the shim must fall back to the deep-link, not 302 to "").
func TestOIDCAuthURL_EmptyAuthURLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{}})
	}))
	defer srv.Close()

	c := New(srv.URL, "tk")
	_, err := c.OIDCAuthURL(context.Background(), "oidc", "default", "https://h/cb")
	if err == nil {
		t.Fatal("expected error on empty auth_url; got nil")
	}
}

// TestOIDCAuthURL_StatusErrorWraps — non-200 surfaces the status code.
func TestOIDCAuthURL_StatusErrorWraps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errors":["role default not found"]}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tk")
	_, err := c.OIDCAuthURL(context.Background(), "oidc", "default", "https://h/cb")
	if err == nil {
		t.Fatal("expected error on 400; got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should include status; got %v", err)
	}
}

// TestOIDCAuthURL_RequiredFields — pre-flight validation mirrors the
// other client methods.
func TestOIDCAuthURL_RequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		c     *Client
		role  string
		rdr   string
		match string
	}{
		{"nil-client", (*Client)(nil), "default", "https://h/cb", "client is nil"},
		{"missing-addr", &Client{Token: "tk"}, "default", "https://h/cb", "address is required"},
		// NOTE (#3374): a missing TOKEN is no longer an error — auth_url
		// is an unauthenticated login-flow endpoint; see
		// TestOIDCAuthURL_NoTokenOmitsHeader.
		{"missing-role", &Client{Addr: "http://x", Token: "tk"}, "", "https://h/cb", "role is required"},
		{"missing-redirect", &Client{Addr: "http://x", Token: "tk"}, "default", "", "redirect_uri is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.c.OIDCAuthURL(context.Background(), "oidc", tc.role, tc.rdr)
			if err == nil {
				t.Fatal("expected error; got nil")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Errorf("error message mismatch; got %v", err)
			}
		})
	}
}
