package handler

// Tests for the flow endpoints answering from the jobs store alone: the
// snapshot and the stream never call a flow server, a deployment without
// rows gets an empty snapshot, and the stream stays open on the store's
// subscription until a row arrives.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// flowLocalServer serves the two flow routes of a Handler with the given
// jobs store (nil: no store). Every request to a flow server is counted by
// the returned counter: OPENOVA_FLOW_SERVER_URL points at a server that
// answers 418 and must never be called.
func flowLocalServer(t *testing.T, st *jobs.Store) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(trap.Close)
	t.Setenv("OPENOVA_FLOW_SERVER_URL", trap.URL)
	t.Setenv("SOVEREIGN_FQDN", "")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.jobs = st
	r := chi.NewRouter()
	r.Get("/api/v1/flows/{deploymentId}/snapshot", h.HandleFlowSnapshot)
	r.Get("/api/v1/flows/{deploymentId}/stream", h.HandleFlowStream)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func flowLocalStore(t *testing.T) *jobs.Store {
	t.Helper()
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// flowLocalGet returns the status and the body of a GET.
func flowLocalGet(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestFlowLocalSnapshotAnswersFromTheStore(t *testing.T) {
	st := flowLocalStore(t)
	if err := st.UpsertJob(jobs.Job{DeploymentID: "dep-one", JobName: "install-cilium", Type: jobs.JobTypeInstall, Status: jobs.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	srv, calls := flowLocalServer(t, st)
	code, body := flowLocalGet(t, srv.URL+"/api/v1/flows/dep-one/snapshot")
	if code != http.StatusOK {
		t.Fatalf("status %d, body %s", code, body)
	}
	var msg flowSnapshotLocalMessage
	if err := json.Unmarshal([]byte(body), &msg); err != nil {
		t.Fatalf("body is not a snapshot: %v: %s", err, body)
	}
	if msg.Type != "snapshot" || msg.Flow == nil || msg.Flow.ID != "dep-one" {
		t.Fatalf("got %s", body)
	}
	found := false
	for _, n := range msg.Nodes {
		found = found || n.ID == jobs.JobID("dep-one", "install-cilium")
	}
	if !found {
		t.Fatalf("the job of the store is not a node: %s", body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the flow server was called %d times", n)
	}
}

func TestFlowLocalSnapshotOfAnEmptyStoreIsEmpty(t *testing.T) {
	srv, calls := flowLocalServer(t, flowLocalStore(t))
	code, body := flowLocalGet(t, srv.URL+"/api/v1/flows/dep-none/snapshot")
	if code != http.StatusOK || strings.TrimSpace(body) != `{"type":"snapshot"}` {
		t.Fatalf("status %d, body %s; want 200 and an empty snapshot", code, body)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the flow server was called %d times", n)
	}
}

func TestFlowLocalStreamOfAnEmptyStoreWaitsForARow(t *testing.T) {
	st := flowLocalStore(t)
	srv, calls := flowLocalServer(t, st)
	code, lines := sseLines(t, srv.URL+"/api/v1/flows/dep-late/stream")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	waitForLine(t, lines, "event: snapshot", 2*time.Second)
	if l := waitForLine(t, lines, "data: ", time.Second); l != `data: {"type":"snapshot"}` {
		t.Fatalf("first frame %q; want an empty snapshot", l)
	}
	if err := st.UpsertJob(jobs.Job{DeploymentID: "dep-late", JobName: "install-cilium", Type: jobs.JobTypeInstall, Status: jobs.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	waitForLine(t, lines, "event: snapshot", 2*time.Second)
	l := waitForLine(t, lines, "data: ", time.Second)
	if !strings.Contains(l, `"id":"`+jobs.JobID("dep-late", "install-cilium")+`"`) {
		t.Fatalf("frame after the row %q; want the job as a node", l)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the flow server was called %d times", n)
	}
}

func TestFlowLocalWithoutAStoreIs503(t *testing.T) {
	srv, calls := flowLocalServer(t, nil)
	for _, p := range []string{"snapshot", "stream"} {
		code, body := flowLocalGet(t, srv.URL+"/api/v1/flows/dep-one/"+p)
		if code != http.StatusServiceUnavailable || !strings.Contains(body, "jobs-store-unavailable") {
			t.Fatalf("%s: status %d, body %s; want 503 jobs-store-unavailable", p, code, body)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the flow server was called %d times", n)
	}
}
