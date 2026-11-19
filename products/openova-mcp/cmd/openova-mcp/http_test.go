package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

// newTestTransport builds an httpTransport around an insecure resolver (no
// signature verification — the transport under test is the HTTP layer; the
// resolver's verify modes are covered in internal/identity).
func newTestTransport(t *testing.T) *httpTransport {
	t.Helper()
	return &httpTransport{
		reg:      tools.NewRegistry(nil),
		resolver: identity.NewInsecureResolver(""),
	}
}

// mintUnsigned mints an alg=none-style HS256 token the insecure resolver
// parses without verifying. Claims mirror a catalyst-api Org session.
func mintUnsigned(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte("test-only"))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return s
}

func postMCP(t *testing.T, tr *httpTransport, bearer string, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(payload))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	tr.handleMCP(rec, req)
	return rec
}

func TestHTTP_Healthz(t *testing.T) {
	rec := httptest.NewRecorder()
	handleHealth(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("healthz: got %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestHTTP_Initialize(t *testing.T) {
	tr := newTestTransport(t)
	rec := postMCP(t, tr, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("initialize: status %d body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("initialize: content-type %q", ct)
	}
	var resp struct {
		Result struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("initialize: bad JSON: %v", err)
	}
	if resp.Result.ServerInfo.Name != serverName {
		t.Fatalf("initialize: serverInfo.name %q, want %q", resp.Result.ServerInfo.Name, serverName)
	}
}

// An unauthenticated tools/list is an EMPTY surface (layer-1 RBAC: no
// identity sees nothing) — not an error.
func TestHTTP_ToolsList_UnauthenticatedIsEmpty(t *testing.T) {
	tr := newTestTransport(t)
	rec := postMCP(t, tr, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list: status %d", rec.Code)
	}
	var resp struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("tools/list: bad JSON: %v", err)
	}
	if len(resp.Result.Tools) != 0 {
		t.Fatalf("unauthenticated tools/list returned %d tools, want 0", len(resp.Result.Tools))
	}
}

// The Authorization header is the HTTP-transport bearer channel: an
// org-admin token sees the Org surface (whoami + reads + create_application)
// but NEVER a Sovereign-only surface.
func TestHTTP_ToolsList_BearerHeaderScopesSurface(t *testing.T) {
	tr := newTestTransport(t)
	bearer := mintUnsigned(t, jwt.MapClaims{
		"sub": "user-1", "org_id": "demo", "tier": "org-admin", "typ": "session",
	})
	rec := postMCP(t, tr, bearer, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list: status %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"whoami"`) || !strings.Contains(body, `"create_application"`) {
		t.Fatalf("org-admin surface missing expected tools: %s", body)
	}
}

// Notifications produce no reply frame → 202 Accepted, empty body.
func TestHTTP_NotificationIs202(t *testing.T) {
	tr := newTestTransport(t)
	rec := postMCP(t, tr, "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification: status %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("notification: body %q, want empty", rec.Body.String())
	}
}

func TestHTTP_GetMCPIs405(t *testing.T) {
	tr := newTestTransport(t)
	rec := httptest.NewRecorder()
	tr.handleMCP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /mcp: status %d, want 405", rec.Code)
	}
}

func TestHTTP_EmptyBodyIs400(t *testing.T) {
	tr := newTestTransport(t)
	rec := postMCP(t, tr, "", "   ")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body: status %d, want 400", rec.Code)
	}
}

// The HTTP transport reads the bearer from the Authorization header and from
// nothing else: no header, no bearer.
func TestBearerFromRequest_HeaderOnly(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(nil))
	if got := bearerFromRequest(r); got != "" {
		t.Fatalf("no header: got %q, want no bearer", got)
	}
	r.Header.Set("Authorization", "Bearer header-token")
	if got := bearerFromRequest(r); got != "header-token" {
		t.Fatalf("header: got %q", got)
	}
	r.Header.Set("Authorization", "raw-token-no-scheme")
	if got := bearerFromRequest(r); got != "raw-token-no-scheme" {
		t.Fatalf("raw header: got %q", got)
	}
}

// An agent token never takes the /whoami fallback: when the resolver cannot
// accept it, catalyst-api is not asked, even though it would vouch for it.
func TestResolveBearer_AgentTokenNeverTakesWhoamiFallback(t *testing.T) {
	calls := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"email":"probe@acme.agents.invalid","verified":true,"mode":"organization","tier":"admin","org":"acme","orgScoped":true}`))
	}))
	defer ts.Close()

	srv := &server{
		reg:      tools.NewRegistry(catalystapi.New(ts.URL)),
		resolver: identity.NewDegradedResolver("no session pubkey", ""),
	}
	agent := mintUnsigned(t, jwt.MapClaims{
		"sub": "agent:acme/probe", "typ": "agent", "agent": "probe", "org_id": "acme", "tier": "viewer",
		"aud": []string{"model-gateway", "platform-tools", "catalyst-api"},
	})
	if _, err := srv.resolveBearer(agent); err == nil {
		t.Fatal("an agent token the resolver refused was accepted")
	}
	if calls != 0 {
		t.Fatalf("an agent token took the /whoami fallback (%d calls)", calls)
	}

	// Control: a session token the resolver refuses still takes the fallback.
	session := mintUnsigned(t, jwt.MapClaims{"sub": "user-1", "org_id": "acme", "tier": "org-admin", "typ": "session"})
	if _, err := srv.resolveBearer(session); err != nil {
		t.Fatalf("session token: the /whoami fallback no longer works: %v", err)
	}
	if calls != 1 {
		t.Fatalf("session token: /whoami called %d times, want 1", calls)
	}
}
