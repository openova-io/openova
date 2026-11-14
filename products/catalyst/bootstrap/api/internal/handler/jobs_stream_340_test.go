package handler

// Tests for the Jobs push path: the access check of the jobs stream, the
// translation of the Job UIDs into the jobs bridge, and the local flow
// stream that emits on store changes instead of a 3-second ticker.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// pushTestHandler returns a Handler with a jobs store and one known
// deployment "dep-push" that holds one Job.
func pushTestHandler(t *testing.T) (*Handler, *jobs.Store) {
	t.Helper()
	t.Setenv("SOVEREIGN_FQDN", "")
	h := NewWithPDM(silentLogger(), &fakePDM{})
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h.jobs = st
	h.deployments.Store("dep-push", &Deployment{ID: "dep-push"})
	if err := st.UpsertJob(jobs.Job{DeploymentID: "dep-push", JobName: "install-cilium", Type: jobs.JobTypeInstall, Status: jobs.StatusRunning}); err != nil {
		t.Fatal(err)
	}
	return h, st
}

// sseLines streams the lines of a GET to url.
func sseLines(t *testing.T, url string) (int, <-chan string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()
	return resp.StatusCode, lines
}

// waitForLine returns the first line with the prefix, or fails after d.
func waitForLine(t *testing.T, lines <-chan string, prefix string, d time.Duration) string {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream closed before a line starting with %q", prefix)
			}
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-deadline:
			t.Fatalf("no line starting with %q within %v", prefix, d)
		}
	}
}

func TestJobsStreamRouteChecksAccess(t *testing.T) {
	h, st := pushTestHandler(t)
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs/stream", jobs.StreamHandler(h.JobsReadAccess))
	srv := httptest.NewServer(r)
	// Cleanups run last-in first-out: the stream bodies close before the server.
	t.Cleanup(srv.Close)

	code, _ := sseLines(t, srv.URL+"/api/v1/deployments/dep-unknown/jobs/stream")
	if code != http.StatusNotFound {
		t.Fatalf("unknown deployment: status %d, want 404", code)
	}

	code, lines := sseLines(t, srv.URL+"/api/v1/deployments/dep-push/jobs/stream")
	if code != http.StatusOK {
		t.Fatalf("known deployment: status %d, want 200", code)
	}
	if err := st.UpsertJob(jobs.Job{DeploymentID: "dep-push", JobName: "install-cilium", Type: jobs.JobTypeInstall, Status: jobs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	data := waitForLine(t, lines, "data: ", 2*time.Second)
	var f map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(data, "data: ")), &f); err != nil {
		t.Fatal(err)
	}
	if f["kind"] != "job-upserted" || f["jobId"] != "dep-push:install-cilium" || f["deploymentId"] != "dep-push" {
		t.Fatalf("frame %v", f)
	}

	h.jobs = nil
	code, _ = sseLines(t, srv.URL+"/api/v1/deployments/dep-push/jobs/stream")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("no store: status %d, want 503", code)
	}
}

func TestReconcilerObsToBridgeCarriesTheJobUIDs(t *testing.T) {
	out := reconcilerObsToBridge([]helmwatch.ReconcilerObservation{
		{Kind: helmwatch.ReconcilerKindTask, Name: "bp-openbao-init", OwnerInstallChart: "openbao", JobUID: "uid-hook"},
		{Kind: helmwatch.ReconcilerKindCron, Name: "snapshot", Executions: []helmwatch.ReconcilerExecution{{Name: "snapshot-1", UID: "uid-run"}}},
	})
	if out[0].JobUID != "uid-hook" || out[1].Executions[0].UID != "uid-run" {
		t.Fatalf("got %+v", out)
	}
}

func TestFlowStreamLocalEmitsOnAStoreChange(t *testing.T) {
	h, st := pushTestHandler(t)
	r := chi.NewRouter()
	r.Get("/api/v1/flows/{deploymentId}/stream", h.HandleFlowStream)
	srv := httptest.NewServer(r)
	// Cleanups run last-in first-out: the stream bodies close before the server.
	t.Cleanup(srv.Close)

	code, lines := sseLines(t, srv.URL+"/api/v1/flows/dep-push/stream")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	waitForLine(t, lines, "event: snapshot", 2*time.Second)
	start := time.Now()
	if err := st.UpsertJob(jobs.Job{DeploymentID: "dep-push", JobName: "install-cilium", Type: jobs.JobTypeInstall, Status: jobs.StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	// A 3-second ticker would emit the next snapshot after this deadline.
	waitForLine(t, lines, "event: snapshot", 2*time.Second)
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("snapshot after %v, sooner than the coalescing window", d)
	}
}
