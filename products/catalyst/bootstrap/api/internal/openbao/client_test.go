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
