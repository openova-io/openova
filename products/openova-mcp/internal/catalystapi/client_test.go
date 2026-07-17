package catalystapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Whoami parses catalyst-api's identity verdict — the #5175 authority the MCP
// falls back to when it cannot verify a session token's signature locally.
func TestWhoami_ParsesVerdict(t *testing.T) {
	var gotAuth, gotCookie string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/whoami" {
			t.Errorf("path = %q, want /api/v1/whoami", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotCookie = r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"emrah.baysal@openova.io","sub":"u1","verified":true,` +
			`"deploymentId":"b85cb3b3a565893a","sovereignFQDN":"hw266.omani.works",` +
			`"mode":"sovereign","tier":"owner"}`))
	}))
	defer ts.Close()

	w, err := New(ts.URL).Whoami(context.Background(), "sess.tok.xyz")
	if err != nil {
		t.Fatalf("Whoami: %v", err)
	}
	if !w.Verified || w.Mode != "sovereign" || w.Tier != "owner" {
		t.Fatalf("verdict = %+v", w)
	}
	if w.DeploymentID != "b85cb3b3a565893a" || w.SovereignFQDN != "hw266.omani.works" {
		t.Fatalf("verdict deployment fields = %+v", w)
	}
	// The facade forwards the caller's token both ways (gateway + RequireSession).
	if gotAuth != "Bearer sess.tok.xyz" || gotCookie != "catalyst_session=sess.tok.xyz" {
		t.Fatalf("forwarded auth=%q cookie=%q", gotAuth, gotCookie)
	}
}

// A bad/expired/tampered token 401s upstream → *APIError, so the MCP
// whoami-fallback fails closed (the caller keeps the original verify error).
func TestWhoami_Unauthorized_IsAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthenticated"}`))
	}))
	defer ts.Close()

	_, err := New(ts.URL).Whoami(context.Background(), "bad.tok")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want *APIError status 401", err)
	}
}
