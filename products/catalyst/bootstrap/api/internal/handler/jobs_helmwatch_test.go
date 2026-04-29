// jobs_helmwatch_test.go — integration test that proves a HelmRelease
// component event flowing through emitWatchEvent (the single emit
// path Phase 0 + Phase 1 share) materialises a Job + Execution +
// LogLine in the jobs store. Sibling SSE feed must stay intact.
package handler

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func TestEmitWatchEvent_PopulatesJobsStore(t *testing.T) {
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), js)

	// Build a minimal Deployment with the channels emitWatchEvent
	// expects. We don't run any goroutine; we just call the emit
	// method directly to assert the store-side projection.
	dep := &Deployment{
		ID:        "dep-emit",
		eventsCh:  make(chan provisioner.Event, 8),
		eventsBuf: nil,
		done:      make(chan struct{}),
	}

	// Phase-0 event must NOT create a Job (filtered by bridge).
	h.emitWatchEvent(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   "tofu-apply",
		Level:   "info",
		Message: "applying...",
	})

	got, err := js.ListJobs("dep-emit")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("Phase-0 event must not create jobs, got %+v", got)
	}

	// Phase-1 component event → Job + Execution + LogLine.
	t0 := time.Now().UTC()
	h.emitWatchEvent(dep, provisioner.Event{
		Time:      t0.Format(time.RFC3339),
		Phase:     "component",
		Level:     "info",
		Component: "cilium",
		State:     "installing",
		Message:   "Helm install in progress",
	})
	h.emitWatchEvent(dep, provisioner.Event{
		Time:      t0.Add(5 * time.Second).Format(time.RFC3339),
		Phase:     "component",
		Level:     "info",
		Component: "cilium",
		State:     "installed",
		Message:   "Ready=True",
	})

	got, err = js.ListJobs("dep-emit")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 job, got %d", len(got))
	}
	job := got[0]
	if job.JobName != "install-cilium" || job.AppID != "cilium" {
		t.Errorf("job metadata: %+v", job)
	}
	if job.Status != jobs.StatusSucceeded {
		t.Errorf("status: want succeeded, got %q", job.Status)
	}

	// Bridge must NOT break the SSE stream — eventsBuf still records
	// every emit.
	dep.mu.Lock()
	bufLen := len(dep.eventsBuf)
	dep.mu.Unlock()
	if bufLen != 3 {
		t.Errorf("eventsBuf length: want 3 (1 phase-0 + 2 phase-1), got %d", bufLen)
	}
}

func TestEmitWatchEvent_NoStore_NoCrash(t *testing.T) {
	// When the jobs store is nil (CI runner without write access)
	// emitWatchEvent must still record into the SSE buffer.
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), nil)
	dep := &Deployment{
		ID:       "dep-nostore",
		eventsCh: make(chan provisioner.Event, 4),
		done:     make(chan struct{}),
	}
	h.emitWatchEvent(dep, provisioner.Event{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Phase:     "component",
		Component: "cilium",
		State:     "installed",
		Level:     "info",
		Message:   "ok",
	})
	dep.mu.Lock()
	bufLen := len(dep.eventsBuf)
	dep.mu.Unlock()
	if bufLen != 1 {
		t.Errorf("expected 1 buffered event, got %d", bufLen)
	}
}

func TestRouter_AllFourEndpointsWired(t *testing.T) {
	// End-to-end smoke that the 4 endpoints can be routed and produce
	// well-shaped JSON. Mirrors the exact route patterns main.go uses.
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), js)
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
	r.Get("/api/v1/deployments/{depId}/jobs/batches", h.ListBatches)
	r.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
	r.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)

	// Seed a deployment with a finished job + a tail of log lines.
	depID := "dep-router"
	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		BatchID:      jobs.BatchBootstrapKit,
		DependsOn:    []string{},
		Status:       jobs.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	exec, err := js.StartExecution(depID, "install-cilium", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := js.AppendLogLines(depID, exec.ID, []jobs.LogLine{
		{Level: jobs.LevelInfo, Message: "first"},
		{Level: jobs.LevelInfo, Message: "second"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := js.FinishExecution(depID, exec.ID, jobs.StatusSucceeded, time.Now()); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
	}{
		{"list-jobs", "/api/v1/deployments/" + depID + "/jobs"},
		{"get-job", "/api/v1/deployments/" + depID + "/jobs/" + jobs.JobID(depID, "install-cilium")},
		{"batches", "/api/v1/deployments/" + depID + "/jobs/batches"},
		{"logs", "/api/v1/actions/executions/" + exec.ID + "/logs?fromLine=1&limit=10"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
			if rec.Code != 200 {
				t.Errorf("status %d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Body.Len() == 0 {
				t.Error("empty body")
			}
		})
	}
}
