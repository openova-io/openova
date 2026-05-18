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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/sandbox/pty-server/internal/session"
)

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
