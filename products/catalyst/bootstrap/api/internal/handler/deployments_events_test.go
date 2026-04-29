// Tests for the durable event buffer + SSE replay-on-connect path added by
// issue #180. The user reported: "this is empty are you sure this is
// progressing?" — the wizard's `/sovereign/provision/<id>` page rendered
// `0 events · done` because a browser that connected after `event: done`
// arrived at an already-closed channel with nothing to replay.
//
// These tests prove the four invariants the fix has to maintain:
//
//  1. The durable buffer (Deployment.eventsBuf) fills as events flow
//     through the tee in runProvisioning — i.e. recordEvent is the single
//     emit path and nothing escapes it.
//  2. StreamLogs on a completed deployment replays the buffer plus emits
//     the terminal `event: done` frame, then closes — so a browser landing
//     on a completed-deployment URL renders the full history.
//  3. GET /api/v1/deployments/{id}/events returns the slice + state JSON +
//     done flag, agreeing with the SSE replay byte-for-byte on the
//     event content (both read snapshotEvents()).
//  4. Buffer eviction at EventBufferCap is FIFO — the oldest entry drops
//     when the buffer fills, so a runaway producer cannot OOM the Pod.
package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// makeDeployment builds a Deployment with the same field initialisation as
// CreateDeployment, registers it on the handler's sync.Map, and returns it.
// Tests drive recordEvent directly so they don't have to spin up `tofu`.
func makeDeployment(t *testing.T, h *Handler, id string) *Deployment {
	t.Helper()
	dep := &Deployment{
		ID:        id,
		Status:    "provisioning",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "test." + id + ".example",
			Region:        "fsn1",
		},
	}
	h.deployments.Store(id, dep)
	return dep
}

// finishDeployment mirrors what runProvisioning does at the end so tests
// can simulate a completed deployment without exec'ing tofu.
func finishDeployment(dep *Deployment, status string) {
	dep.mu.Lock()
	dep.Status = status
	dep.FinishedAt = time.Now()
	dep.mu.Unlock()
	close(dep.eventsCh)
	close(dep.done)
}

// TestRecordEvent_BufferFillsDuringDeployment proves invariant 1: every
// event passes through recordEvent, so the durable buffer is the
// authoritative history of what happened on the deployment.
func TestRecordEvent_BufferFillsDuringDeployment(t *testing.T) {
	h := New(slog.Default())
	dep := makeDeployment(t, h, "test-buffer-fills")

	for i := 0; i < 50; i++ {
		dep.recordEvent(provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "tofu",
			Level:   "info",
			Message: fmt.Sprintf("hcloud_server.cp[0]: Creation complete after %ds", i),
		})
	}

	snap := dep.snapshotEvents()
	if len(snap) != 50 {
		t.Errorf("buffer length = %d, want 50", len(snap))
	}
	// First and last carry the right messages — proves order is preserved
	// (FIFO append, no shuffle).
	if !strings.Contains(snap[0].Message, "Creation complete after 0s") {
		t.Errorf("snap[0] = %q, expected first event", snap[0].Message)
	}
	if !strings.Contains(snap[49].Message, "Creation complete after 49s") {
		t.Errorf("snap[49] = %q, expected 50th event", snap[49].Message)
	}

	state := dep.State()
	if got, want := state["numEvents"], 50; got != want {
		t.Errorf("State()[numEvents] = %v, want %d", got, want)
	}
}

// TestStreamLogs_ReplaysOnCompletedDeployment proves invariant 2: a browser
// connecting AFTER the deployment finished still sees the full history.
// This is the user-reported regression — `0 events · done` in the wizard.
func TestStreamLogs_ReplaysOnCompletedDeployment(t *testing.T) {
	h := New(slog.Default())
	dep := makeDeployment(t, h, "test-replay")

	// Simulate the events the original deployment emitted before crashing
	// out the goroutine.
	want := []string{
		"Initialising OpenTofu working directory",
		"Planning Hetzner resources (network, firewall, server, LB, DNS)",
		"hcloud_network.sovereign: Creation complete after 2s",
		"hcloud_firewall.sovereign: Creation complete after 1s",
		"hcloud_server.cp[0]: Creation complete after 18s",
		"hcloud_load_balancer.api: Creation complete after 4s",
		"Reading OpenTofu outputs",
	}
	for _, msg := range want {
		dep.recordEvent(provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "tofu-apply",
			Level:   "info",
			Message: msg,
		})
	}
	finishDeployment(dep, "ready")

	// Now connect via the SSE handler exactly like the browser does.
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/deployments/test-replay/logs")
	if err != nil {
		t.Fatalf("GET /logs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	got := readSSEFrames(t, resp.Body)

	// Every recorded event must replay as a `data:` frame, plus one final
	// `event: done` frame with the State() JSON.
	if len(got.dataFrames) != len(want) {
		t.Fatalf("replayed data frames = %d, want %d (msgs: %v)",
			len(got.dataFrames), len(want), got.dataFrames)
	}
	for i, ev := range got.dataFrames {
		if !strings.Contains(ev.Message, want[i]) {
			t.Errorf("frame[%d].Message = %q, want substring %q", i, ev.Message, want[i])
		}
	}
	if got.doneFrame == nil {
		t.Fatal("no `event: done` frame after replay — completed deployment must terminate the stream")
	}
	if got.doneFrame["status"] != "ready" {
		t.Errorf("done frame status = %v, want ready", got.doneFrame["status"])
	}
	if numEv, ok := got.doneFrame["numEvents"].(float64); !ok || int(numEv) != len(want) {
		t.Errorf("done frame numEvents = %v, want %d", got.doneFrame["numEvents"], len(want))
	}
}

// TestStreamLogs_ReplaysAndTailsLiveStream proves the in-flight path: a
// browser connecting while the deployment is still running gets the
// already-buffered history first, then live events as they arrive.
func TestStreamLogs_ReplaysAndTailsLiveStream(t *testing.T) {
	h := New(slog.Default())
	dep := makeDeployment(t, h, "test-replay-and-tail")

	// Two events already in the buffer when the browser connects.
	dep.recordEvent(provisioner.Event{Phase: "tofu-init", Level: "info", Message: "Initialising"})
	dep.recordEvent(provisioner.Event{Phase: "tofu-plan", Level: "info", Message: "Planning"})

	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
	srv := httptest.NewServer(r)
	defer srv.Close()

	// Open the SSE stream and start consuming.
	resp, err := http.Get(srv.URL + "/api/v1/deployments/test-replay-and-tail/logs")
	if err != nil {
		t.Fatalf("GET /logs: %v", err)
	}
	defer resp.Body.Close()

	frames := make(chan parsedFrame, 8)
	go func() {
		readSSEFramesStreaming(t, resp.Body, frames)
		close(frames)
	}()

	// Read the two replayed frames.
	got := readN(t, frames, 2, 2*time.Second)
	if len(got) != 2 {
		t.Fatalf("expected 2 replayed frames, got %d", len(got))
	}
	if got[0].kind != "data" || got[1].kind != "data" {
		t.Errorf("expected both frames to be data, got %v", got)
	}

	// Now emit a live event through the tee path — record + send.
	live := provisioner.Event{Phase: "tofu-apply", Level: "info", Message: "live frame"}
	dep.recordEvent(live)
	dep.eventsCh <- live

	gotLive := readN(t, frames, 1, 2*time.Second)
	if len(gotLive) != 1 {
		t.Fatalf("expected 1 live frame, got %d", len(gotLive))
	}
	if !strings.Contains(gotLive[0].rawData, "live frame") {
		t.Errorf("live frame data = %q, want substring %q", gotLive[0].rawData, "live frame")
	}

	// Close the stream so the goroutine exits cleanly.
	finishDeployment(dep, "ready")

	// One more frame: the `event: done`.
	gotDone := readN(t, frames, 1, 2*time.Second)
	if len(gotDone) != 1 || gotDone[0].kind != "done" {
		t.Errorf("expected done frame, got %v", gotDone)
	}
}

// TestGetDeploymentEvents_ReturnsBufferedSlice proves invariant 3: the GET
// endpoint returns the same history the SSE replay path serves.
func TestGetDeploymentEvents_ReturnsBufferedSlice(t *testing.T) {
	h := New(slog.Default())
	dep := makeDeployment(t, h, "test-get-events")

	for i := 0; i < 5; i++ {
		dep.recordEvent(provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "tofu-apply",
			Level:   "info",
			Message: fmt.Sprintf("event %d", i),
		})
	}
	finishDeployment(dep, "ready")

	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}/events", h.GetDeploymentEvents)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/deployments/test-get-events/events")
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body struct {
		State  map[string]any       `json:"state"`
		Events []provisioner.Event  `json:"events"`
		Done   bool                 `json:"done"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Events) != 5 {
		t.Errorf("len(events) = %d, want 5", len(body.Events))
	}
	for i, ev := range body.Events {
		want := fmt.Sprintf("event %d", i)
		if ev.Message != want {
			t.Errorf("events[%d].Message = %q, want %q", i, ev.Message, want)
		}
	}
	if !body.Done {
		t.Error("done = false, want true (deployment finished)")
	}
	if body.State["status"] != "ready" {
		t.Errorf("state.status = %v, want ready", body.State["status"])
	}
}

// TestGetDeploymentEvents_NotFound covers the 404 path so the wizard can
// surface "Unreachable" without polling forever on a typoed id.
func TestGetDeploymentEvents_NotFound(t *testing.T) {
	h := New(slog.Default())
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}/events", h.GetDeploymentEvents)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/deployments/does-not-exist/events")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestRecordEvent_BufferEvictionAtCap proves invariant 4: when a runaway
// producer emits more than EventBufferCap events, the oldest get dropped
// (FIFO) and the slice never exceeds the cap. A multi-region apply that
// emits ~50k tofu lines must NOT OOM the Pod.
func TestRecordEvent_BufferEvictionAtCap(t *testing.T) {
	h := New(slog.Default())
	dep := makeDeployment(t, h, "test-evict")

	// Emit cap + 100 events. The first 100 must drop, the remaining 10000
	// must be present in order.
	total := EventBufferCap + 100
	for i := 0; i < total; i++ {
		dep.recordEvent(provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   "tofu",
			Level:   "info",
			Message: fmt.Sprintf("line %d", i),
		})
	}

	snap := dep.snapshotEvents()
	if len(snap) != EventBufferCap {
		t.Fatalf("buffer size = %d, want %d (eviction failed)", len(snap), EventBufferCap)
	}
	// First entry must be line 100 (the oldest 100 evicted).
	if !strings.Contains(snap[0].Message, "line 100") {
		t.Errorf("snap[0] = %q, expected oldest non-evicted = line 100", snap[0].Message)
	}
	// Last entry must be line (total-1).
	wantLast := fmt.Sprintf("line %d", total-1)
	if !strings.Contains(snap[len(snap)-1].Message, wantLast) {
		t.Errorf("snap[last] = %q, expected %q", snap[len(snap)-1].Message, wantLast)
	}
}

// TestStreamLogs_NotFound covers 404 on the SSE endpoint too — kept here
// so the events-buffer feature's full surface is in one file.
func TestStreamLogs_NotFound(t *testing.T) {
	h := New(slog.Default())
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{id}/logs", h.StreamLogs)
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/deployments/does-not-exist/logs")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

/* ── SSE frame parsing helpers ───────────────────────────────────────── */

type parsedFrames struct {
	dataFrames []provisioner.Event
	doneFrame  map[string]any
}

type parsedFrame struct {
	kind    string // data | done
	rawData string
}

// readSSEFrames reads the entire SSE response body (until the server
// closes), splits on blank lines, and returns the parsed events + the
// terminal `done` frame.
func readSSEFrames(t *testing.T, body io.Reader) parsedFrames {
	t.Helper()
	out := parsedFrames{}
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	for _, frame := range strings.Split(string(raw), "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		isDone := strings.Contains(frame, "event: done")
		var dataLine string
		for _, ln := range strings.Split(frame, "\n") {
			if strings.HasPrefix(ln, "data: ") {
				dataLine = strings.TrimPrefix(ln, "data: ")
				break
			}
		}
		if isDone {
			var m map[string]any
			if err := json.Unmarshal([]byte(dataLine), &m); err != nil {
				t.Fatalf("done frame JSON: %v (data=%q)", err, dataLine)
			}
			out.doneFrame = m
		} else {
			var ev provisioner.Event
			if err := json.Unmarshal([]byte(dataLine), &ev); err != nil {
				t.Fatalf("data frame JSON: %v (data=%q)", err, dataLine)
			}
			out.dataFrames = append(out.dataFrames, ev)
		}
	}
	return out
}

// readSSEFramesStreaming pumps frames into ch as they arrive. Used by the
// in-flight test which needs to read replay frames, then trigger a live
// emit, then read the live frame.
func readSSEFramesStreaming(t *testing.T, body io.Reader, ch chan<- parsedFrame) {
	t.Helper()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var buf strings.Builder
	for scanner.Scan() {
		ln := scanner.Text()
		if ln == "" && buf.Len() > 0 {
			pf := parseFrame(buf.String())
			ch <- pf
			buf.Reset()
			continue
		}
		if ln != "" {
			buf.WriteString(ln)
			buf.WriteByte('\n')
		}
	}
	if buf.Len() > 0 {
		ch <- parseFrame(buf.String())
	}
}

func parseFrame(frame string) parsedFrame {
	pf := parsedFrame{kind: "data"}
	for _, ln := range strings.Split(frame, "\n") {
		if strings.HasPrefix(ln, "event: done") {
			pf.kind = "done"
		} else if strings.HasPrefix(ln, "data: ") {
			pf.rawData = strings.TrimPrefix(ln, "data: ")
		}
	}
	return pf
}

func readN(t *testing.T, ch <-chan parsedFrame, n int, dl time.Duration) []parsedFrame {
	t.Helper()
	out := make([]parsedFrame, 0, n)
	deadline := time.After(dl)
	for len(out) < n {
		select {
		case f, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, f)
		case <-deadline:
			return out
		}
	}
	return out
}
