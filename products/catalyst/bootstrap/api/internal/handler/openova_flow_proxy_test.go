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
//   - When OPENOVA_FLOW_SERVER_URL is UNSET, the upstream URL is
//     derived from the deployment record's sovereignFQDN as
//     `https://openova-flow.<fqdn>` (HTTPRoute pattern, Agent #8)
//   - When the deploymentId isn't in the store AND no env override is
//     set, the proxy returns 404 (deployment not found) rather than
//     silently falling through to an in-cluster Service URL the
//     mothership can't dial
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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
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

// ── URL resolution (Agent #8 — per-deployment + env override) ──────────

// withFlowServerURLUnset clears OPENOVA_FLOW_SERVER_URL for the test
// duration and restores on cleanup. Used by the per-deployment
// derivation tests below — when the env is set, resolveFlowServerURL
// returns it verbatim and the deployment lookup never runs, which
// would defeat the test.
func withFlowServerURLUnset(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("OPENOVA_FLOW_SERVER_URL")
	_ = os.Unsetenv("OPENOVA_FLOW_SERVER_URL")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("OPENOVA_FLOW_SERVER_URL", prev)
		}
	})
}

// TestFlowProxy_EnvOverride_TakesPrecedence — when
// OPENOVA_FLOW_SERVER_URL is set (chroot catalyst-api case), the
// resolver returns it verbatim and ignores any deployment record. This
// preserves the original Agent #3 behaviour.
func TestFlowProxy_EnvOverride_TakesPrecedence(t *testing.T) {
	withFlowServerURL(t, "http://override.local:9999")
	h := newFlowProxyHandler()
	// Stash a deployment with a different FQDN — must NOT be picked.
	h.deployments.Store("dep-x", &Deployment{
		ID:      "dep-x",
		Request: provisioner.Request{SovereignFQDN: "should-not-be-used.example"},
	})
	got, err := h.resolveFlowServerURL("dep-x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "http://override.local:9999" {
		t.Fatalf("env override: got %q want http://override.local:9999", got)
	}
}

// TestFlowProxy_DerivesURLFromDeploymentFQDN — the mothership path:
// no env override, look up the deployment by ID, build
// `https://openova-flow.<sovereignFQDN>` from the record. This is
// the canonical HTTPRoute hostname pattern documented in
// platform/openova-flow-server/chart/values.yaml + the bootstrap-kit
// overlay 56-bp-openova-flow-server.yaml.
func TestFlowProxy_DerivesURLFromDeploymentFQDN(t *testing.T) {
	withFlowServerURLUnset(t)
	h := newFlowProxyHandler()
	h.deployments.Store("5a175e0a88c99cec", &Deployment{
		ID:      "5a175e0a88c99cec",
		Request: provisioner.Request{SovereignFQDN: "tenant-7.example.org"},
	})
	got, err := h.resolveFlowServerURL("5a175e0a88c99cec")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := "https://openova-flow.tenant-7.example.org"
	if got != want {
		t.Fatalf("derived URL: got %q want %q", got, want)
	}
}

// TestFlowProxy_DerivedURL_NotFoundReturns404 — when the env isn't
// set and the deploymentId isn't in the store, the resolver returns
// an error and the proxy responds 404. Previously the code silently
// fell back to the in-cluster Service URL — on the mothership that
// produced a 502 with a confusing DNS-lookup error.
func TestFlowProxy_DerivedURL_NotFoundReturns404(t *testing.T) {
	withFlowServerURLUnset(t)
	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/dep-missing/snapshot", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not found") {
		t.Fatalf("body should mention 'not found'; got %q", rec.Body.String())
	}
}

// TestFlowProxy_DerivedURL_EmptyFQDNReturns404 — deployment exists
// but its Request.SovereignFQDN is empty (pre-FQDN-assignment, or a
// half-persisted record). Same 404 as missing-deployment so the
// browser surfaces a clear error rather than a silent 502.
func TestFlowProxy_DerivedURL_EmptyFQDNReturns404(t *testing.T) {
	withFlowServerURLUnset(t)
	h := newFlowProxyHandler()
	h.deployments.Store("dep-half", &Deployment{
		ID:      "dep-half",
		Request: provisioner.Request{SovereignFQDN: ""},
	})
	r := chi.NewRouter()
	registerFlowProxyRoutes(r, h)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/dep-half/snapshot", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestFlowProxy_DerivedURL_RoutesFullPath — end-to-end check that the
// snapshot path appends `/v1/flows/<id>/snapshot` onto the derived
// base URL. We point the derived hostname at a localhost httptest
// server by abusing OPENOVA_FLOW_SERVER_URL — but the assertion below
// proves the SECOND code path (per-deployment derivation) works in
// isolation via the resolveFlowServerURL unit test above; this
// end-to-end test re-validates the request path assembly.
func TestFlowProxy_DerivedURL_PathAssembly(t *testing.T) {
	// Use a fake upstream as a stand-in; we only check that the URL
	// the proxy dials carries the right suffix `/v1/flows/<id>/snapshot`.
	got := ""
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"snapshot"}`))
	}))
	defer up.Close()
	// Set the env so the dial actually reaches the test server; the
	// derivation path is unit-tested above. This test additionally
	// asserts the suffix is built correctly.
	withFlowServerURL(t, up.URL)

	r := chi.NewRouter()
	registerFlowProxyRoutes(r, newFlowProxyHandler())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/flows/5a175e0a88c99cec/snapshot", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	if !strings.HasSuffix(got, "/v1/flows/5a175e0a88c99cec/snapshot") {
		t.Fatalf("upstream path: got %q want suffix /v1/flows/5a175e0a88c99cec/snapshot", got)
	}
}
