// jobs_test.go — httptest-driven handler tests for the 4 Jobs/
// Executions endpoints. Each test seeds a fresh in-memory store via
// NewWithJobsStore(t.TempDir()), wires the chi router the production
// main.go does, then asserts on the JSON shape end-to-end.
package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// newJobsAPIRouter wires the same chi routes main.go does, but only
// for the 4 endpoints under test. Avoids spinning up the full HTTP
// surface for these unit tests.
func newJobsAPIRouter(t *testing.T) (*chi.Mux, *jobs.Store, *Handler) {
	t.Helper()
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), js)
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
	r.Get("/api/v1/deployments/{depId}/jobs/batches", h.ListBatches)
	r.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
	r.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)
	return r, js, h
}

func decodeJSON(t *testing.T, body io.Reader, into any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestHandler_ListJobs_Empty(t *testing.T) {
	r, _, _ := newJobsAPIRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/dep-empty/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if resp.Jobs == nil {
		t.Fatal("jobs must be empty slice not null")
	}
	if len(resp.Jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(resp.Jobs))
	}
	// Verify the `jobs` key is `[]` not `null` in the raw body.
	if !strings.Contains(body, `"jobs"`) {
		t.Errorf("missing jobs key: %s", body)
	}
	if !strings.Contains(body, `[]`) {
		t.Errorf("expected empty array `[]`: %s", body)
	}
}

func TestHandler_ListJobs_Populated(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-populated"

	t0 := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	jobsToSeed := []jobs.Job{
		{DeploymentID: depID, JobName: "install-cilium", AppID: "cilium", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(20 * time.Second))},
		{DeploymentID: depID, JobName: "install-flux", AppID: "flux", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusRunning, StartedAt: ptrTime(t0.Add(time.Minute))},
		{DeploymentID: depID, JobName: "install-keycloak", AppID: "keycloak", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusPending},
	}
	for _, j := range jobsToSeed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID+"/jobs", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	decodeJSON(t, rec.Body, &resp)
	if len(resp.Jobs) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(resp.Jobs))
	}
	// Started DESC: install-flux (t0+1m) first, install-cilium (t0)
	// second, install-keycloak (pending) last.
	wantOrder := []string{"install-flux", "install-cilium", "install-keycloak"}
	for i, w := range wantOrder {
		if resp.Jobs[i].JobName != w {
			t.Errorf("position %d: got %q want %q", i, resp.Jobs[i].JobName, w)
		}
	}
}

func TestHandler_GetJob_FoundAndNotFound(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-getjob"

	if err := st.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		BatchID:      jobs.BatchBootstrapKit,
		Status:       jobs.StatusPending,
		DependsOn:    []string{"install-flux"},
	}); err != nil {
		t.Fatal(err)
	}
	exec, err := st.StartExecution(depID, "install-cilium", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	url := "/api/v1/deployments/" + depID + "/jobs/" + jobs.JobID(depID, "install-cilium")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != 200 {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Job        jobs.Job         `json:"job"`
		Executions []jobs.Execution `json:"executions"`
	}
	decodeJSON(t, rec.Body, &resp)
	if resp.Job.AppID != "cilium" {
		t.Errorf("appId: %q", resp.Job.AppID)
	}
	if len(resp.Job.DependsOn) != 1 || resp.Job.DependsOn[0] != "install-flux" {
		t.Errorf("dependsOn: %+v", resp.Job.DependsOn)
	}
	if len(resp.Executions) != 1 || resp.Executions[0].ID != exec.ID {
		t.Errorf("executions: %+v", resp.Executions)
	}

	// 404 path
	rec404 := httptest.NewRecorder()
	r.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID+"/jobs/"+jobs.JobID(depID, "install-missing"), nil))
	if rec404.Code != http.StatusNotFound {
		t.Errorf("404 expected, got %d", rec404.Code)
	}
}

func TestHandler_GetExecutionLogs_Pagination(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-logs"
	if err := st.UpsertJob(jobs.Job{DeploymentID: depID, JobName: "install-x"}); err != nil {
		t.Fatal(err)
	}
	exec, err := st.StartExecution(depID, "install-x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]jobs.LogLine, 50)
	for i := range lines {
		lines[i] = jobs.LogLine{Level: jobs.LevelInfo, Message: "log"}
	}
	if err := st.AppendLogLines(depID, exec.ID, lines); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/actions/executions/"+exec.ID+"/logs?fromLine=10&limit=5", nil))
	if rec.Code != 200 {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp jobs.LogPage
	decodeJSON(t, rec.Body, &resp)
	if len(resp.Lines) != 5 {
		t.Fatalf("lines: %d", len(resp.Lines))
	}
	if resp.Lines[0].LineNumber != 10 || resp.Lines[4].LineNumber != 14 {
		t.Errorf("LineNumbers: %+v", resp.Lines)
	}
	if resp.Total != 50 {
		t.Errorf("total: %d", resp.Total)
	}
	if resp.ExecutionFinished {
		t.Errorf("ExecutionFinished should be false while running")
	}
}

func TestHandler_GetExecutionLogs_NotFound(t *testing.T) {
	r, _, _ := newJobsAPIRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/actions/executions/no-such/logs", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_ListBatches(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-batches"

	now := time.Now().UTC()
	seeds := []jobs.Job{
		{DeploymentID: depID, JobName: "install-a", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusSucceeded, StartedAt: &now},
		{DeploymentID: depID, JobName: "install-b", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusFailed, StartedAt: &now},
		{DeploymentID: depID, JobName: "install-c", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusRunning, StartedAt: &now},
		{DeploymentID: depID, JobName: "install-d", BatchID: jobs.BatchBootstrapKit, Status: jobs.StatusPending},
	}
	for _, j := range seeds {
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID+"/jobs/batches", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	var resp struct {
		Batches []jobs.BatchSummary `json:"batches"`
	}
	decodeJSON(t, rec.Body, &resp)
	if len(resp.Batches) != 1 {
		t.Fatalf("batches: %+v", resp.Batches)
	}
	bs := resp.Batches[0]
	if bs.BatchID != jobs.BatchBootstrapKit ||
		bs.Total != 4 ||
		bs.Succeeded != 1 ||
		bs.Failed != 1 ||
		bs.Running != 1 ||
		bs.Pending != 1 ||
		bs.Finished != 2 {
		t.Errorf("batch summary: %+v", bs)
	}
}

func TestHandler_ListBatches_EmptySliceNotNull(t *testing.T) {
	r, _, _ := newJobsAPIRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/dep-x/jobs/batches", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"batches":[]`) {
		t.Errorf("expected `\"batches\":[]`, got %s", body)
	}
}

func TestHandler_NoStore_503(t *testing.T) {
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), nil)
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/d/jobs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}
