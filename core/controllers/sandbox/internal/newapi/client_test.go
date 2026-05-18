package newapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMintSandboxToken_HappyPath(t *testing.T) {
	t.Parallel()

	var captured MintRequest
	var capturedAuth string
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/admin/tokens/sandbox" {
			http.Error(w, "path", http.StatusNotFound)
			return
		}
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(MintResponse{Token: "tok-abc", ExpiresAt: exp})
	}))
	defer srv.Close()

	c, err := New(srv.URL, "admin-bytes", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := c.MintSandboxToken(context.Background(), MintRequest{
		OrgID:           "acme",
		UserID:          "ceo@acme.com",
		SandboxID:       "uid-1",
		AllowedChannels: []string{"qwen"},
	})
	if err != nil {
		t.Fatalf("MintSandboxToken: %v", err)
	}
	if got.Token != "tok-abc" {
		t.Errorf("token: got %q", got.Token)
	}
	if !got.ExpiresAt.Equal(exp) {
		t.Errorf("expires_at: got %v want %v", got.ExpiresAt, exp)
	}
	if capturedAuth != "Bearer admin-bytes" {
		t.Errorf("auth header: got %q", capturedAuth)
	}
	if captured.OrgID != "acme" || captured.UserID != "ceo@acme.com" ||
		captured.SandboxID != "uid-1" || len(captured.AllowedChannels) != 1 ||
		captured.AllowedChannels[0] != "qwen" {
		t.Errorf("request body: got %+v", captured)
	}
}

func TestMintSandboxToken_Non2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid admin credentials"}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "wrong", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.MintSandboxToken(context.Background(), MintRequest{
		OrgID: "a", UserID: "u", SandboxID: "s", AllowedChannels: []string{"q"},
	})
	if err == nil {
		t.Fatalf("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should surface status code: %v", err)
	}
}

func TestNew_InputValidation(t *testing.T) {
	t.Parallel()
	if _, err := New("", "x", nil); err == nil {
		t.Errorf("expected error on empty baseURL")
	}
	if _, err := New("http://x", "  ", nil); err == nil {
		t.Errorf("expected error on empty adminSecret")
	}
}

func TestMintSandboxToken_RequestValidation(t *testing.T) {
	t.Parallel()
	c, err := New("http://x", "s", nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []MintRequest{
		{OrgID: "", UserID: "u", SandboxID: "s", AllowedChannels: []string{"q"}},
		{OrgID: "o", UserID: "", SandboxID: "s", AllowedChannels: []string{"q"}},
		{OrgID: "o", UserID: "u", SandboxID: "", AllowedChannels: []string{"q"}},
		{OrgID: "o", UserID: "u", SandboxID: "s", AllowedChannels: nil},
	}
	for i, tc := range cases {
		if _, err := c.MintSandboxToken(context.Background(), tc); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}
