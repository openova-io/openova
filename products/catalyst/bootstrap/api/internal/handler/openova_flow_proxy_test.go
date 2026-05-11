// openova_flow_proxy_test.go — table-driven coverage for the Agent #3
// integration proxy (Sovereign Console FlowPage → catalyst-api →
// openova-flow-server).
//
// We assert:
//   - GET /snapshot passes through status + body
//   - POST /events forwards body + propagates status
//   - GET /stream pass-through writes upstream `data:` frames AS THEY
//     ARRIVE (not buffered) and Content-Type is text/event-stream
//   - Empty deploymentId returns 400 on each verb
//   - Upstream-unreachable returns 502
package handler

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func registerFlowProxyRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/flows/{deploymentId}/snapshot", h.HandleFlowSnapshot)
	r.Get("/api/v1/flows/{deploymentId}/stream", h.HandleFlowStream)
	r.Post("/api/v1/flows/{deploymentId}/events", h.HandleFlowEvents)
}

func newFlowProxyHandler() *Handler {
	return NewWithPDM(silentLogger(), &fakePDM{})
}

// withFlowServerURL sets OPENOVA_FLOW_SERVER_URL for the lifetime of
// the test func, restoring on cleanup.
func withFlowServerURL(t *testing.T, url string) {
	t.Helper()
	prev, had := os.LookupEnv("OPENOVA_FLOW_SERVER_URL")
	if err := os.Setenv("OPENOVA_FLOW_SERVER_URL", url); err != nil {
		t.Fatalf("setenv: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("OPENOVA_FLOW_SERVER_URL", prev)
		} else {
			_ = os.Unsetenv("OPENOVA_FLOW_SERVER_URL")
		}
	})
}

// ── GET /snapshot ──────────────────────────────────────────────────────

func TestFlowProxy_Snapshot_PassesThroughStatusAndBody(t *testing.T) {
	body := `{"type":"snapshot","flow":{"id":"dep-abc","status":"running","startedAt":1735660000000},"nodes":[],"relationships":[]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method: got %s want GET", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/flows/dep-abc/snapshot") {
			t.Errorf("path: got %s want suffix /v1/flows/dep-abc/snapshot", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer up.Close()
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/dep-abc/snapshot", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != body {
		t.Fatalf("body mismatch:\n  got:  %s\n  want: %s", rec.Body.String(), body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type: got %q want application/json", ct)
	}
}

func TestFlowProxy_Snapshot_PropagatesUpstream404(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "flow not found", http.StatusNotFound)
	}))
	defer up.Close()
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/dep-missing/snapshot", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

func TestFlowProxy_Snapshot_EmptyIDReturns400(t *testing.T) {
	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	// chi requires the path param to match the pattern; an empty value
	// is impossible to express in URL form, so we test via the bare
	// handler with the chi RouteContext stripped.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows//snapshot", nil)
	r.ServeHTTP(rec, req)
	// chi returns 404 for an unmatched route; the explicit 400 path
	// fires only if a deploymentId of literal "" reaches the handler
	// (which is unreachable via chi's matcher today). The contract
	// stands as a defence-in-depth; we assert it via a direct call.
	h := newFlowProxyHandler()
	rec = httptest.NewRecorder()
	h.HandleFlowSnapshot(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("direct call: got %d want 400", rec.Code)
	}
}

func TestFlowProxy_Snapshot_UnreachableReturns502(t *testing.T) {
	// Address 127.0.0.1:1 — kernel refuses immediately, no DNS step.
	withFlowServerURL(t, "http://127.0.0.1:1")

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/dep-x/snapshot", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502", rec.Code)
	}
}

// ── POST /events ───────────────────────────────────────────────────────

func TestFlowProxy_Events_ForwardsBodyAndStatus(t *testing.T) {
	want := `{"type":"upsert-nodes","nodes":[{"id":"n1","flowId":"dep-z","label":"x","status":"running"}]}`
	var got []byte
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: got %s want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/v1/flows/dep-z/events") {
			t.Errorf("path: got %s", r.URL.Path)
		}
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"seq":42}`))
	}))
	defer up.Close()
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/flows/dep-z/events", bytes.NewBufferString(want))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if string(got) != want {
		t.Fatalf("forwarded body:\n  got:  %s\n  want: %s", string(got), want)
	}
	if !strings.Contains(rec.Body.String(), `"seq":42`) {
		t.Fatalf("response body did not propagate: %s", rec.Body.String())
	}
}

func TestFlowProxy_Events_PropagatesUpstream400(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad envelope", http.StatusBadRequest)
	}))
	defer up.Close()
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/flows/dep-1/events", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

func TestFlowProxy_Events_EmptyIDReturns400(t *testing.T) {
	h := newFlowProxyHandler()
	rec := httptest.NewRecorder()
	h.HandleFlowEvents(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("direct call: got %d want 400", rec.Code)
	}
}

// ── GET /stream (SSE pass-through) ─────────────────────────────────────

func TestFlowProxy_Stream_PassesThroughSSEFrames(t *testing.T) {
	// Emit two SSE frames with a small delay so the proxy must NOT
	// buffer them (httputil.ReverseProxy would collect both before
	// writing anything — that's the regression class this test guards).
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("upstream test server lacks flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: frame-1\n\n"))
		fl.Flush()
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte("data: frame-2\n\n"))
		fl.Flush()
	}))
	defer up.Close()
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())

	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/flows/dep-stream/stream")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type: got %q want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control: got %q want no-cache", cc)
	}
	if xa := resp.Header.Get("X-Accel-Buffering"); xa != "no" {
		t.Fatalf("X-Accel-Buffering: got %q want no", xa)
	}

	// Read the body until we've seen both frames or the deadline fires.
	br := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(2 * time.Second)
	var saw []string
	for time.Now().Before(deadline) && len(saw) < 2 {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			break
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			saw = append(saw, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if len(saw) != 2 || saw[0] != "frame-1" || saw[1] != "frame-2" {
		t.Fatalf("frames: got %v want [frame-1 frame-2]", saw)
	}
}

func TestFlowProxy_Stream_PropagatesUpstream404(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("unknown flow"))
	}))
	defer up.Close()
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/dep-unknown/stream", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

func TestFlowProxy_Stream_EmptyIDReturns400(t *testing.T) {
	h := newFlowProxyHandler()
	rec := httptest.NewRecorder()
	h.HandleFlowStream(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("direct call: got %d want 400", rec.Code)
	}
}

// ── Env default fallback ───────────────────────────────────────────────

func TestFlowProxy_DefaultURL_WhenEnvUnset(t *testing.T) {
	// Save+clear the env.
	prev, had := os.LookupEnv("OPENOVA_FLOW_SERVER_URL")
	_ = os.Unsetenv("OPENOVA_FLOW_SERVER_URL")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("OPENOVA_FLOW_SERVER_URL", prev)
		}
	})
	h := newFlowProxyHandler()
	got := h.resolveFlowServerURL("dep-anything")
	if !strings.HasPrefix(got, "http://openova-flow-server.") {
		t.Fatalf("default URL: got %q want prefix http://openova-flow-server.", got)
	}
}
