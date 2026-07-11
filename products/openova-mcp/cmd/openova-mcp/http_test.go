package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	"github.com/openova-io/openova/products/openova-mcp/internal/catalystapi"
	"github.com/openova-io/openova/products/openova-mcp/internal/identity"
	"github.com/openova-io/openova/products/openova-mcp/internal/tools"
)

// ── test fixtures (reuse the identity/tools test idioms) ─────────────────

// roundTripFunc stands in for the live catalyst-api (mirrors tools_test).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

// mintToken signs an alg=none JWT carrying claims — the same insecure-resolver
// path identity_test uses. The HTTP transport verifies it through the SAME
// resolver the stdio path uses.
func mintToken(t *testing.T, claims *sharedauth.Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return signed
}

func orgAdminToken(t *testing.T) string {
	return mintToken(t, &sharedauth.Claims{Email: "a@acme.test", OrgID: "acme", Role: "admin", DeploymentID: "dep1"})
}

func orgViewerToken(t *testing.T) string {
	return mintToken(t, &sharedauth.Claims{Email: "v@acme.test", OrgID: "acme", Role: "viewer", DeploymentID: "dep1"})
}

// newTestTransport builds an httpTransport over the insecure resolver (so
// alg=none test tokens resolve) wired to the supplied backend client (nil for
// tests that never reach the backend).
func newTestTransport(api *catalystapi.Client) *httpTransport {
	c := &core{
		reg:            tools.NewRegistry(api),
		resolver:       identity.NewInsecureResolver(""),
		fallbackBearer: "",
	}
	return &httpTransport{core: c, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func postMCP(t *testing.T, h http.Handler, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mcpEndpoint, strings.NewReader(body))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// toolNames pulls the tool names out of a tools/list JSON-RPC response.
func toolNames(t *testing.T, raw []byte) []string {
	t.Helper()
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode tools/list: %v (body=%s)", err, raw)
	}
	out := make([]string, 0, len(resp.Result.Tools))
	for _, tt := range resp.Result.Tools {
		out = append(out, tt.Name)
	}
	return out
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// ── tests ────────────────────────────────────────────────────────────────

// TestHTTPValidBearerDispatches — a valid-bearer tools/list POST dispatches
// through the shared core and returns the JSON-RPC result over HTTP.
func TestHTTPValidBearerDispatches(t *testing.T) {
	tr := newTestTransport(nil)
	rec := postMCP(t, tr.router(), orgAdminToken(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("want application/json, got %q", ct)
	}
	names := toolNames(t, rec.Body.Bytes())
	for _, want := range []string{"whoami", "list_applications", "get_application"} {
		if !hasName(names, want) {
			t.Errorf("tools/list missing %q (got %v)", want, names)
		}
	}
}

// TestHTTPAbsentAndInvalidBearer401 — a request with no Authorization header,
// and one with a non-JWT bearer, are both rejected at the transport with 401 +
// a WWW-Authenticate challenge, before any dispatch.
func TestHTTPAbsentAndInvalidBearer401(t *testing.T) {
	tr := newTestTransport(nil)

	// Absent bearer.
	rec := postMCP(t, tr.router(), "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("absent bearer: want 401, got %d", rec.Code)
	}
	if ch := rec.Header().Get("WWW-Authenticate"); !strings.Contains(ch, "Bearer") {
		t.Errorf("absent bearer: missing WWW-Authenticate challenge, got %q", ch)
	}

	// Invalid (non-JWT) bearer.
	rec = postMCP(t, tr.router(), "not-a-jwt", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer: want 401, got %d", rec.Code)
	}
}

// TestHTTPToolsListFiltersByScope — layer-1 RBAC over HTTP: a viewer does NOT
// see the admin-gated create_application; an admin does. Identical filtering to
// the stdio path (same core, same registry).
func TestHTTPToolsListFiltersByScope(t *testing.T) {
	tr := newTestTransport(nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

	viewer := toolNames(t, postMCP(t, tr.router(), orgViewerToken(t), body).Body.Bytes())
	if hasName(viewer, "create_application") {
		t.Error("viewer must NOT see create_application over HTTP (admin-gated)")
	}
	if !hasName(viewer, "list_applications") {
		t.Error("viewer should still see the read tools")
	}

	admin := toolNames(t, postMCP(t, tr.router(), orgAdminToken(t), body).Body.Bytes())
	if !hasName(admin, "create_application") {
		t.Error("admin should see create_application over HTTP")
	}
}

// TestHTTPCrossOrgToolsCallForbidden — layer-2 RBAC over HTTP: an acme-scoped
// caller creating in globex is denied with the MCP 403 (JSON-RPC error -32003,
// data.status 403) inside a 200 HTTP response, and the backend is NEVER hit.
// Exact parity with the stdio cross-Org denial.
func TestHTTPCrossOrgToolsCallForbidden(t *testing.T) {
	called := false
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return jsonResp(201, `{"kind":"Application"}`), nil
	})
	api := catalystapi.New("https://console.test").WithHTTPClient(&http.Client{Transport: rt})
	tr := newTestTransport(api)

	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"create_application","arguments":{"blueprint":"bp-gitea","version":"1.0.0","name":"leak","organization":"globex"}}}`
	rec := postMCP(t, tr.router(), orgAdminToken(t), body)

	if rec.Code != http.StatusOK {
		t.Fatalf("app-tier denial travels as JSON-RPC error inside a 200, got HTTP %d", rec.Code)
	}
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				Status int `json:"status"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Error == nil {
		t.Fatalf("cross-org create should return a JSON-RPC error, got %s", rec.Body.String())
	}
	if resp.Error.Code != -32003 || resp.Error.Data.Status != 403 {
		t.Fatalf("want MCP 403 (code -32003, status 403), got code=%d status=%d",
			resp.Error.Code, resp.Error.Data.Status)
	}
	if called {
		t.Fatal("cross-org create must NOT reach the backend (denied at the facade)")
	}
}

// TestHTTPOwnOrgToolsCallDispatches — a valid own-org create dispatches through
// the shared core, forwards the caller's bearer to the backend, and returns the
// install envelope over HTTP.
func TestHTTPOwnOrgToolsCallDispatches(t *testing.T) {
	var sawAuth, sawPath string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.Path
		return jsonResp(201, `{"kind":"Application","name":"shop","namespace":"acme","applied":true}`), nil
	})
	api := catalystapi.New("https://console.acme.omani.homes").
		WithTenantHost("console.acme.omani.homes").
		WithHTTPClient(&http.Client{Transport: rt})
	tr := newTestTransport(api)
	token := orgAdminToken(t)

	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"create_application","arguments":{"blueprint":"bp-wordpress","version":"1.2.3","name":"shop"}}}`
	rec := postMCP(t, tr.router(), token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}

	// The caller's bearer is forwarded verbatim to the catalyst-api (thin
	// facade) and the org-context create uses the dedicated own-org route.
	if sawAuth != "Bearer "+token {
		t.Errorf("bearer not forwarded to backend: %q", sawAuth)
	}
	if sawPath != "/api/v1/org/applications" {
		t.Errorf("org context must use the own-org route, got %q", sawPath)
	}

	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Result.IsError || len(resp.Result.Content) == 0 {
		t.Fatalf("unexpected result envelope: %s", rec.Body.String())
	}
	if !strings.Contains(resp.Result.Content[0].Text, `"applied":true`) {
		t.Errorf("install envelope not surfaced: %s", resp.Result.Content[0].Text)
	}
}

// TestHTTPNotificationAccepted — a JSON-RPC notification (no id) takes no reply
// and is acked at the transport with 202.
func TestHTTPNotificationAccepted(t *testing.T) {
	tr := newTestTransport(nil)
	rec := postMCP(t, tr.router(), orgAdminToken(t),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("notification: want 202, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification: want empty body, got %q", rec.Body.String())
	}
}

// TestHTTPHealthz — the chart probe target returns 200.
func TestHTTPHealthz(t *testing.T) {
	tr := newTestTransport(nil)
	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		tr.router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ok") {
			t.Errorf("%s: want ok body, got %q", path, rec.Body.String())
		}
	}
}

// TestHTTPGetSSEUnauthorized — the SSE stream enforces the same 401 gate.
func TestHTTPGetSSEUnauthorized(t *testing.T) {
	tr := newTestTransport(nil)
	req := httptest.NewRequest(http.MethodGet, mcpEndpoint, nil)
	rec := httptest.NewRecorder()
	tr.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SSE: want 401, got %d", rec.Code)
	}
}

// TestHTTPGetSSEOpensStream — an authenticated GET /mcp opens a
// text/event-stream and emits the open frame. Driven through a real server so
// the blocking stream handler can be canceled via the request context.
func TestHTTPGetSSEOpensStream(t *testing.T) {
	tr := newTestTransport(nil)
	srv := httptest.NewServer(tr.router())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+mcpEndpoint, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+orgAdminToken(t))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE open: want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("want text/event-stream, got %q", ct)
	}
	// The handler flushes an open comment immediately; read it, then cancel.
	buf := make([]byte, 64)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("read SSE open frame: %v", err)
	}
	if !strings.Contains(string(buf[:n]), "openova-mcp stream open") {
		t.Errorf("SSE open frame not seen, got %q", string(buf[:n]))
	}
	cancel()
}

// TestResolveHTTPAddr — the flag wins over the env; the env is the fallback; an
// unknown arg never breaks the (stdio) default.
func TestResolveHTTPAddr(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		if got := resolveHTTPAddr([]string{"--http", ":9090"}); got != ":9090" {
			t.Errorf("--http :9090 → %q", got)
		}
		if got := resolveHTTPAddr([]string{"--http=:7000"}); got != ":7000" {
			t.Errorf("--http=:7000 → %q", got)
		}
	})
	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("OPENOVA_MCP_HTTP_ADDR", ":8080")
		if got := resolveHTTPAddr(nil); got != ":8080" {
			t.Errorf("env → %q", got)
		}
	})
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv("OPENOVA_MCP_HTTP_ADDR", ":8080")
		if got := resolveHTTPAddr([]string{"--http", ":9090"}); got != ":9090" {
			t.Errorf("flag should win over env, got %q", got)
		}
	})
	t.Run("unknown arg is stdio (empty)", func(t *testing.T) {
		t.Setenv("OPENOVA_MCP_HTTP_ADDR", "")
		if got := resolveHTTPAddr([]string{"--unknown", "foo"}); got != "" {
			t.Errorf("unknown arg must not break stdio, got %q", got)
		}
	})
}
