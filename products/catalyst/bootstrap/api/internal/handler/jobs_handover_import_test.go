// Tests for the chroot-side handover jobs receive endpoint
// (Refs #3263 #3277) — POST /api/v1/internal/jobs/import.
//
// What this file proves:
//
//  1. A mother snapshot carrying both regions lands in the chroot's
//     local jobs store: region-b rows materialise with their Region
//     field intact, the chroot's own pre-existing region-a rows (and
//     rows absent from the payload entirely) are preserved.
//  2. Re-POSTing the same snapshot is idempotent — no duplicate rows.
//  3. A terminal local row is never regressed through the endpoint.
//  4. The cross-cluster poisoning guards hold: fqdn-mismatch → 403,
//     CATALYST_OTECH_FQDN unset → 503, nil jobs store → 503, malformed
//     deploymentId → 400.
package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

const jobsImportTestFQDN = "hw130.omani.works"

// newJobsImportTestHandler wires a Handler with a t.TempDir-rooted
// jobs store and stamps the Sovereign identity env the endpoint
// validates against.
func newJobsImportTestHandler(t *testing.T) (*Handler, *jobs.Store) {
	t.Helper()
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	t.Setenv("CATALYST_OTECH_FQDN", jobsImportTestFQDN)
	return NewWithJobsStore(silentLogger(), js), js
}

func postJobsImport(t *testing.T, h *Handler, payload jobsImportPayload) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/jobs/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleJobsImport(rec, req)
	return rec
}

func tsp(t *testing.T, s string) *time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	v = v.UTC()
	return &v
}

// motherJobsPayload builds the wire payload the mother's
// buildJobsExportPayload produces for a 2-region deployment.
func motherJobsPayload(t *testing.T, depID string) jobsImportPayload {
	t.Helper()
	return jobsImportPayload{
		DeploymentID:  depID,
		SovereignFQDN: jobsImportTestFQDN,
		Jobs: []jobs.Job{
			{
				DeploymentID: depID,
				JobName:      "install-cilium",
				AppID:        "cilium",
				Type:         jobs.JobTypeInstall,
				ParentID:     jobs.JobID(depID, jobs.GroupBootstrapKit),
				DependsOn:    []string{},
				Status:       jobs.StatusSucceeded,
				StartedAt:    tsp(t, "2026-06-11T10:00:00Z"),
				FinishedAt:   tsp(t, "2026-06-11T10:02:00Z"),
			},
			{
				DeploymentID:      depID,
				JobName:           "install-me-east-215-b-1:cilium",
				AppID:             "me-east-215-b-1:cilium",
				Region:            "me-east-215-b-1",
				Type:              jobs.JobTypeInstall,
				ParentID:          jobs.JobID(depID, jobs.GroupBootstrapKit),
				DependsOn:         []string{},
				Status:            jobs.StatusSucceeded,
				StartedAt:         tsp(t, "2026-06-11T10:05:00Z"),
				FinishedAt:        tsp(t, "2026-06-11T10:08:00Z"),
				LatestExecutionID: "exec-b-cilium",
			},
		},
		Executions: []jobs.Execution{
			{
				ID:           "exec-b-cilium",
				JobID:        jobs.JobID(depID, "install-me-east-215-b-1:cilium"),
				DeploymentID: depID,
				Status:       jobs.StatusSucceeded,
				StartedAt:    *tsp(t, "2026-06-11T10:05:00Z"),
				FinishedAt:   tsp(t, "2026-06-11T10:08:00Z"),
				LineCount:    12,
			},
		},
	}
}

func jobByID(t *testing.T, js *jobs.Store, depID, id string) *jobs.Job {
	t.Helper()
	rows, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

func TestHandleJobsImport_UpsertsBothRegionsPreservesLocalRows(t *testing.T) {
	h, js := newJobsImportTestHandler(t)
	depID := "hw130dep"

	// Chroot pre-state: its own bootstrap watch already recorded the
	// region-a install AND a chroot-only row the mother knows nothing
	// about (e.g. a Day-2 mutation fired locally post-handover).
	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		Type:         jobs.JobTypeInstall,
		Status:       jobs.StatusSucceeded,
		StartedAt:    tsp(t, "2026-06-11T10:00:00Z"),
		FinishedAt:   tsp(t, "2026-06-11T10:02:00Z"),
	}); err != nil {
		t.Fatalf("seed region-a: %v", err)
	}
	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "chroot-local-mutation",
		Type:         jobs.JobTypeInstall,
		Status:       jobs.StatusSucceeded,
		StartedAt:    tsp(t, "2026-06-11T11:00:00Z"),
		FinishedAt:   tsp(t, "2026-06-11T11:01:00Z"),
	}); err != nil {
		t.Fatalf("seed chroot-only row: %v", err)
	}

	rec := postJobsImport(t, h, motherJobsPayload(t, depID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response decode: %v", err)
	}
	if resp["ok"] != true || resp["deploymentId"] != depID {
		t.Errorf("response = %v, want ok=true + deploymentId=%q", resp, depID)
	}
	if resp["jobsApplied"] != float64(2) {
		t.Errorf("jobsApplied = %v, want 2", resp["jobsApplied"])
	}

	// Region-b row materialised with its Region field — the chroot's
	// /jobs Region column/filter (#3278 #3352) keys off 2+ buckets.
	b := jobByID(t, js, depID, jobs.JobID(depID, "install-me-east-215-b-1:cilium"))
	if b == nil {
		t.Fatalf("region-b row missing after import")
	}
	if b.Region != "me-east-215-b-1" || b.Status != jobs.StatusSucceeded {
		t.Errorf("region-b row = %+v, want region=me-east-215-b-1 succeeded", b)
	}
	// Pre-existing rows preserved.
	if a := jobByID(t, js, depID, jobs.JobID(depID, "install-cilium")); a == nil || a.Status != jobs.StatusSucceeded {
		t.Errorf("region-a row lost or regressed: %+v", a)
	}
	if local := jobByID(t, js, depID, jobs.JobID(depID, "chroot-local-mutation")); local == nil {
		t.Errorf("chroot-only row deleted by import — import must only ADD information")
	}
	// Execution summary resolvable for the /logs deep-link.
	if _, err := js.FindExecution(depID, "exec-b-cilium"); err != nil {
		t.Errorf("imported execution not found: %v", err)
	}
}

func TestHandleJobsImport_Idempotent(t *testing.T) {
	h, js := newJobsImportTestHandler(t)
	depID := "hw130dep"
	payload := motherJobsPayload(t, depID)

	if rec := postJobsImport(t, h, payload); rec.Code != http.StatusOK {
		t.Fatalf("first POST status = %d; body: %s", rec.Code, rec.Body.String())
	}
	first, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if rec := postJobsImport(t, h, payload); rec.Code != http.StatusOK {
		t.Fatalf("second POST status = %d; body: %s", rec.Code, rec.Body.String())
	}
	second, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(first) != len(second) {
		t.Errorf("row count changed on re-POST: %d → %d", len(first), len(second))
	}
	execs, err := js.ListExecutions(depID)
	if err != nil {
		t.Fatalf("ListExecutions: %v", err)
	}
	if len(execs) != 1 {
		t.Errorf("executions duplicated on re-POST: %d, want 1", len(execs))
	}
}

func TestHandleJobsImport_NeverRegressesTerminalRow(t *testing.T) {
	h, js := newJobsImportTestHandler(t)
	depID := "hw130dep"

	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		Type:         jobs.JobTypeInstall,
		Status:       jobs.StatusSucceeded,
		StartedAt:    tsp(t, "2026-06-11T10:00:00Z"),
		FinishedAt:   tsp(t, "2026-06-11T10:02:00Z"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	stale := jobsImportPayload{
		DeploymentID:  depID,
		SovereignFQDN: jobsImportTestFQDN,
		Jobs: []jobs.Job{{
			DeploymentID: depID,
			JobName:      "install-cilium",
			Type:         jobs.JobTypeInstall,
			Status:       jobs.StatusRunning, // stale mother view
			StartedAt:    tsp(t, "2026-06-11T10:00:00Z"),
		}},
	}
	if rec := postJobsImport(t, h, stale); rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rec.Code, rec.Body.String())
	}
	a := jobByID(t, js, depID, jobs.JobID(depID, "install-cilium"))
	if a == nil || a.Status != jobs.StatusSucceeded || a.FinishedAt == nil {
		t.Errorf("terminal row regressed through the endpoint: %+v", a)
	}
}

func TestHandleJobsImport_RejectsForeignFQDN(t *testing.T) {
	h, js := newJobsImportTestHandler(t)
	depID := "hw130dep"
	payload := motherJobsPayload(t, depID)
	payload.SovereignFQDN = "evil.example.com"

	rec := postJobsImport(t, h, payload)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", rec.Code, rec.Body.String())
	}
	rows, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("foreign payload landed %d rows — poisoning guard failed", len(rows))
	}
}

func TestHandleJobsImport_NotASovereign503(t *testing.T) {
	h, _ := newJobsImportTestHandler(t)
	t.Setenv("CATALYST_OTECH_FQDN", "")

	rec := postJobsImport(t, h, motherJobsPayload(t, "hw130dep"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (mothership must refuse the import); body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleJobsImport_NilStore503(t *testing.T) {
	t.Setenv("CATALYST_OTECH_FQDN", jobsImportTestFQDN)
	h := NewWithPDM(silentLogger(), &fakePDM{}) // no jobs store wired

	rec := postJobsImport(t, h, motherJobsPayload(t, "hw130dep"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleJobsImport_RejectsMalformedDeploymentID(t *testing.T) {
	h, _ := newJobsImportTestHandler(t)
	payload := motherJobsPayload(t, "hw130dep")
	payload.DeploymentID = "../../../etc/passwd"

	rec := postJobsImport(t, h, payload)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}
