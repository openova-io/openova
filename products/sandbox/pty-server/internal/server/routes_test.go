// routes_test.go — Wave 10 idle-tracking coverage for pty-server.
//
// Verifies:
//
//   - GET /idle returns the manager's lastActivity timestamp and
//     activeSessions count in the documented JSON shape.
//   - Touch() bumps the timestamp.
//   - GET /healthz still works (regression).
//
// We deliberately do NOT spawn a real PTY session in unit tests
// (those need /bin/sh + cgroups + a TTY) — the idle endpoint is
// session-agnostic and only reads the manager-level counters.
package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/sandbox/pty-server/internal/agentcatalog"
	"github.com/openova-io/openova/products/sandbox/pty-server/internal/session"
)

func resetAgentCatalogCache() { agentcatalog.ResetCache() }

// pointSovereignShellAtTempDir writes a JSON override that re-points
// the `sovereign-shell` slug's DefaultCwd to a temp directory (the
// builtin DefaultCwd is /workspace, which doesn't exist on the test
// host). Returns nothing — relies on t.Setenv + t.Cleanup to flush.
func pointSovereignShellAtTempDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	override := []map[string]any{{
		"slug":       "sovereign-shell",
		"binary":     "/bin/sh",
		"defaultCwd": dir,
	}}
	body, _ := json.Marshal(override)
	path := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	t.Setenv("OPENOVA_SANDBOX_AGENTS_PATH", path)
	// The agentcatalog cache may already be populated from a prior
	// test in this package; force a re-read.
	resetAgentCatalogCache()
	t.Cleanup(resetAgentCatalogCache)
}

func TestIdleEndpoint_Shape(t *testing.T) {
	t.Parallel()
	mgr := session.NewManager()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	t0 := mgr.LastActivity()

	resp, err := http.Get(srv.URL + "/idle")
	if err != nil {
		t.Fatalf("GET /idle: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /idle: status=%d", resp.StatusCode)
	}
	var got struct {
		LastActivityAt time.Time `json:"lastActivityAt"`
		ActiveSessions int       `json:"activeSessions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode /idle body: %v", err)
	}
	if got.ActiveSessions != 0 {
		t.Errorf("activeSessions: got %d want 0", got.ActiveSessions)
	}
	if got.LastActivityAt.IsZero() {
		t.Errorf("lastActivityAt: got zero, want %v", t0)
	}
	if !got.LastActivityAt.Equal(t0) && got.LastActivityAt.Before(t0) {
		t.Errorf("lastActivityAt: got %v want >= %v", got.LastActivityAt, t0)
	}
}

func TestIdleEndpoint_TouchBumpsTimestamp(t *testing.T) {
	t.Parallel()
	mgr := session.NewManager()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	read := func() time.Time {
		t.Helper()
		resp, err := http.Get(srv.URL + "/idle")
		if err != nil {
			t.Fatalf("GET /idle: %v", err)
		}
		defer resp.Body.Close()
		var got struct {
			LastActivityAt time.Time `json:"lastActivityAt"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return got.LastActivityAt
	}

	before := read()
	// time.Now() resolution on Linux is ~1µs but the JSON marshal
	// rounds to nanoseconds; sleep enough that the second sample is
	// strictly greater than the first.
	time.Sleep(2 * time.Millisecond)
	mgr.Touch()
	after := read()

	if !after.After(before) {
		t.Errorf("Touch did not advance lastActivity: before=%v after=%v", before, after)
	}
}

func TestHealthz_StillWorks(t *testing.T) {
	t.Parallel()
	mgr := session.NewManager()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz: status=%d want 200", resp.StatusCode)
	}
}

// Wave 15 (PR #1674 follow-up) — GET /metrics serves Prometheus text
// format with the pty_server_websocket_connections gauge registered.
// The gauge value is 0 at process start (no WS connections yet); the
// Grafana panel "WebSocket Connections" sums it across the fleet.
func TestMetricsEndpoint_ExposesWebSocketGauge(t *testing.T) {
	t.Parallel()
	mgr := session.NewManager()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics: status=%d want 200", resp.StatusCode)
	}
	body := make([]byte, 64*1024)
	n, _ := resp.Body.Read(body)
	out := string(body[:n])
	if !strings.Contains(out, "pty_server_websocket_connections") {
		t.Errorf("GET /metrics body missing pty_server_websocket_connections gauge:\n%s", out)
	}
}

// --- TBD-P4 #1986 B3 — POST /sessions agent catalogue tests --------------
//
// We use the `sovereign-shell` slug (which maps to /bin/sh and has no
// RequiredEnv) for the happy-path test so we don't need the B1 agent
// bundle present. Real-agent slugs are exercised via the
// invalid-env / unknown-slug negative paths.

func postSessionsJSON(t *testing.T, srvURL string, body any) (*http.Response, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(srvURL+"/sessions", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST /sessions: %v", err)
	}
	defer resp.Body.Close()
	respBody := make([]byte, 8*1024)
	n, _ := resp.Body.Read(respBody)
	return resp, respBody[:n]
}

func TestCreate_Agent_HappyPath_SovereignShell(t *testing.T) {
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// /workspace doesn't exist in the test container; the request's
	// `cwd` field overrides the agent's DefaultCwd so we can land in
	// the per-test tempdir.
	resp, body := postSessionsJSON(t, srv.URL, map[string]any{
		"agent": "sovereign-shell",
		"cwd":   t.TempDir(),
		"rows":  40,
		"cols":  120,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s want 201", resp.StatusCode, body)
	}
	var got sessionDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if got.ID == "" {
		t.Errorf("empty session id in response: %s", body)
	}
	if mgr.Count() != 1 {
		t.Errorf("manager count=%d want 1", mgr.Count())
	}
}

func TestCreate_Agent_UnknownSlug_Returns400WithCatalogue(t *testing.T) {
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, body := postSessionsJSON(t, srv.URL, map[string]any{
		"agent": "goose",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if got["error"] != "invalid-agent" {
		t.Errorf("error=%q want invalid-agent (body=%s)", got["error"], body)
	}
	// The 400 detail must enumerate the canonical slugs so the operator
	// (or the FE error toast) knows what the right answer was.
	for _, want := range []string{"sovereign-shell", "claude-code", "qwen-code"} {
		if !strings.Contains(got["detail"], want) {
			t.Errorf("detail missing %q: %s", want, got["detail"])
		}
	}
	if mgr.Count() != 0 {
		t.Errorf("manager spawned a session despite invalid agent: count=%d", mgr.Count())
	}
}

func TestCreate_AgentAndCommand_BothSet_Returns400(t *testing.T) {
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, body := postSessionsJSON(t, srv.URL, map[string]any{
		"agent":   "sovereign-shell",
		"command": []string{"/bin/echo", "hi"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if got["error"] != "ambiguous-request" {
		t.Errorf("error=%q want ambiguous-request (body=%s)", got["error"], body)
	}
	if mgr.Count() != 0 {
		t.Errorf("manager spawned despite both-set rejection: count=%d", mgr.Count())
	}
}

func TestCreate_Neither_Returns400(t *testing.T) {
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, body := postSessionsJSON(t, srv.URL, map[string]any{
		"rows": 24, "cols": 80,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if got["error"] != "missing-spec" {
		t.Errorf("error=%q want missing-spec (body=%s)", got["error"], body)
	}
}

func TestCreate_Agent_MissingRequiredEnv_Returns400(t *testing.T) {
	// qwen-code requires OPENAI_API_KEY + OPENAI_BASE_URL. Force them
	// unset for this test.
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, body := postSessionsJSON(t, srv.URL, map[string]any{
		"agent": "qwen-code",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400", resp.StatusCode, body)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if got["error"] != "missing-env" {
		t.Errorf("error=%q want missing-env (body=%s)", got["error"], body)
	}
	if mgr.Count() != 0 {
		t.Errorf("manager spawned despite missing env: count=%d", mgr.Count())
	}
}

func TestCreate_BackwardCompatCommand_StillWorks(t *testing.T) {
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Raw command path must still work for curl-debug + test harness.
	resp, body := postSessionsJSON(t, srv.URL, map[string]any{
		"command": []string{"/bin/sh", "-l"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d body=%s want 201", resp.StatusCode, body)
	}
	if mgr.Count() != 1 {
		t.Errorf("manager count=%d want 1", mgr.Count())
	}
}

// --- TBD-P4 #1986 B3 — lazy-spawn on attach unit coverage ----------------
//
// We can't open a real WS in a unit test without a TTY-aware server, so
// we exercise the lazy-spawn decision via the buildSpecFromCreateRequest
// + lazySpawn helpers and the public manager API. The end-to-end WS
// path is covered by the smoke test in the design spec §3.

func TestLazySpawn_SandboxDefaultAgent_MintsSession(t *testing.T) {
	pointSovereignShellAtTempDir(t)
	t.Setenv("SANDBOX_DEFAULT_AGENT", "sovereign-shell")
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)

	// Synthesize the same request the attach() handler builds.
	req, err := http.NewRequest(http.MethodGet, "/sessions/sandbox-foo/attach", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	got, err := h.lazySpawn(req, "sandbox-foo")
	if err != nil {
		t.Fatalf("lazySpawn: %v", err)
	}
	if got.ID != "sandbox-foo" {
		t.Errorf("lazySpawn id=%q want sandbox-foo", got.ID)
	}
	if mgr.Count() != 1 {
		t.Errorf("manager count=%d want 1", mgr.Count())
	}
}

func TestLazySpawn_QueryAgentOverridesEnv(t *testing.T) {
	pointSovereignShellAtTempDir(t)
	t.Setenv("SANDBOX_DEFAULT_AGENT", "claude-code") // would fail missing-env
	t.Setenv("LLM_GATEWAY_URL", "")
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)

	// Query-param wins; sovereign-shell has no RequiredEnv so it
	// spawns even though claude-code would have rejected.
	u := url.URL{Path: "/sessions/sandbox-bar/attach", RawQuery: "agent=sovereign-shell"}
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	got, err := h.lazySpawn(req, "sandbox-bar")
	if err != nil {
		t.Fatalf("lazySpawn: %v", err)
	}
	if got.ID != "sandbox-bar" {
		t.Errorf("lazySpawn id=%q want sandbox-bar", got.ID)
	}
}

func TestLazySpawn_NeitherSet_ReturnsErrNotFound(t *testing.T) {
	t.Setenv("SANDBOX_DEFAULT_AGENT", "")
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)

	req, _ := http.NewRequest(http.MethodGet, "/sessions/sandbox-baz/attach", nil)
	_, err := h.lazySpawn(req, "sandbox-baz")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	// Preserve the historic 404 behaviour when no env / query is set.
	if err.Error() != session.ErrNotFound.Error() {
		t.Errorf("err=%v want %v", err, session.ErrNotFound)
	}
}

func TestLazySpawn_UnknownSlug_SurfacesInvalidAgent(t *testing.T) {
	t.Setenv("SANDBOX_DEFAULT_AGENT", "goose")
	mgr := session.NewManager()
	defer mgr.Shutdown()
	h := New(mgr)

	req, _ := http.NewRequest(http.MethodGet, "/sessions/sandbox-q/attach", nil)
	_, err := h.lazySpawn(req, "sandbox-q")
	if err == nil {
		t.Fatal("expected invalid-agent error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid-agent") {
		t.Errorf("err=%v should contain invalid-agent", err)
	}
}
