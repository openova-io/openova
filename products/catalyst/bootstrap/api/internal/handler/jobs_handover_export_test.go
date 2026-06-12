// Tests for the mother-side handover jobs export (Refs #3263 #3277).
//
// What this file proves:
//
//  1. buildJobsExportPayload serialises BOTH regions' Job rows with the
//     wire keys the chroot's HandleJobsImport decodes (deploymentId,
//     sovereignFqdn, jobs, executions), keeps the first-class region
//     field on secondary-region rows, strips the derived childIds, and
//     bounds the executions list to each Job's LATEST attempt — no log
//     lines ride along.
//  2. postJobsExportWithRetry delivers the payload to the chroot
//     endpoint (success path) and gives up immediately on a 4xx — no
//     retry storm against a child that rejected the import.
package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// seedTwoRegionJobsStore seeds the mother-side store shape: a primary
// region row + a secondary region row (region-prefixed jobName per
// snapshotsToSeedsForRegion's ":" convention) with two execution
// attempts on the secondary row so the latest-only filter is provable.
func seedTwoRegionJobsStore(t *testing.T, js *jobs.Store, depID string) (latestExecID string) {
	t.Helper()
	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		Type:         jobs.JobTypeInstall,
		ParentID:     jobs.JobID(depID, jobs.GroupBootstrapKit),
		DependsOn:    []string{},
		Status:       jobs.StatusPending,
	}); err != nil {
		t.Fatalf("UpsertJob region-a: %v", err)
	}
	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-me-east-215-b-1:cilium",
		AppID:        "me-east-215-b-1:cilium",
		Region:       "me-east-215-b-1",
		Type:         jobs.JobTypeInstall,
		ParentID:     jobs.JobID(depID, jobs.GroupBootstrapKit),
		DependsOn:    []string{},
		Status:       jobs.StatusPending,
	}); err != nil {
		t.Fatalf("UpsertJob region-b: %v", err)
	}

	// Two attempts on the region-b job: only the SECOND (latest) may
	// ride the export payload.
	stale, err := js.StartExecution(depID, "install-me-east-215-b-1:cilium", time.Now().Add(-10*time.Minute))
	if err != nil {
		t.Fatalf("StartExecution stale: %v", err)
	}
	if err := js.FinishExecution(depID, stale.ID, jobs.StatusFailed, time.Now().Add(-9*time.Minute)); err != nil {
		t.Fatalf("FinishExecution stale: %v", err)
	}
	latest, err := js.StartExecution(depID, "install-me-east-215-b-1:cilium", time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("StartExecution latest: %v", err)
	}
	if err := js.FinishExecution(depID, latest.ID, jobs.StatusSucceeded, time.Now().Add(-4*time.Minute)); err != nil {
		t.Fatalf("FinishExecution latest: %v", err)
	}
	return latest.ID
}

func TestBuildJobsExportPayload_WireShape(t *testing.T) {
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	depID := "hw130dep"
	fqdn := "hw130.omani.works"
	latestExecID := seedTwoRegionJobsStore(t, js, depID)

	h := NewWithJobsStore(silentLogger(), js)
	body, nJobs, nExecs, err := h.buildJobsExportPayload(depID, fqdn)
	if err != nil {
		t.Fatalf("buildJobsExportPayload: %v", err)
	}
	// 2 leaf rows + the synthesized bootstrap-kit group from
	// ListJobs's derived view.
	if nJobs != 3 {
		t.Errorf("nJobs = %d, want 3 (2 leaves + synthesized group)", nJobs)
	}
	if nExecs != 1 {
		t.Errorf("nExecs = %d, want 1 (latest attempt only — stale attempt stays mother-side)", nExecs)
	}

	// Decode as the typed wire shape the chroot's HandleJobsImport uses.
	var typed jobsImportPayload
	if err := json.Unmarshal(body, &typed); err != nil {
		t.Fatalf("payload does not round-trip the import shape: %v", err)
	}
	if typed.DeploymentID != depID {
		t.Errorf("deploymentId = %q, want %q", typed.DeploymentID, depID)
	}
	if typed.SovereignFQDN != fqdn {
		t.Errorf("sovereignFqdn = %q, want %q (the chroot's poisoning guard keys off this)", typed.SovereignFQDN, fqdn)
	}
	var regionB *jobs.Job
	for i := range typed.Jobs {
		if typed.Jobs[i].JobName == "install-me-east-215-b-1:cilium" {
			regionB = &typed.Jobs[i]
		}
	}
	if regionB == nil {
		t.Fatalf("region-b row missing from payload: %+v", typed.Jobs)
	}
	if regionB.Region != "me-east-215-b-1" {
		t.Errorf("region-b row region = %q, want me-east-215-b-1 (the chroot Region column keys off this)", regionB.Region)
	}
	if len(typed.Executions) != 1 || typed.Executions[0].ID != latestExecID {
		t.Errorf("executions = %+v, want exactly the latest attempt %q", typed.Executions, latestExecID)
	}

	// Raw-key assertions: childIds (derived) must be stripped from
	// every job; no log-line content rides the payload.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("raw unmarshal: %v", err)
	}
	for _, key := range []string{"deploymentId", "sovereignFqdn", "jobs", "executions"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("payload missing wire key %q", key)
		}
	}
	var rawJobs []map[string]json.RawMessage
	if err := json.Unmarshal(raw["jobs"], &rawJobs); err != nil {
		t.Fatalf("raw jobs unmarshal: %v", err)
	}
	for _, rj := range rawJobs {
		if _, ok := rj["childIds"]; ok {
			t.Errorf("job carries derived childIds — must be stripped to bound the payload: %v", rj)
		}
	}
	var rawExecs []map[string]json.RawMessage
	if err := json.Unmarshal(raw["executions"], &rawExecs); err != nil {
		t.Fatalf("raw executions unmarshal: %v", err)
	}
	for _, re := range rawExecs {
		if _, ok := re["lines"]; ok {
			t.Errorf("execution carries log lines — the export must skip them: %v", re)
		}
	}
}

// fakeJobsImportServer mimics the chroot's POST /api/v1/internal/jobs/
// import endpoint, recording every payload.
type fakeJobsImportServer struct {
	mu       sync.Mutex
	received []jobsImportPayload
	hits     atomic.Int32
	status   int
}

func (s *fakeJobsImportServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		var p jobsImportPayload
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &p)
		s.mu.Lock()
		s.received = append(s.received, p)
		s.mu.Unlock()
		code := s.status
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}
}

func TestPostJobsExport_DeliversBothRegions(t *testing.T) {
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	depID := "hw130dep"
	fqdn := "hw130.omani.works"
	seedTwoRegionJobsStore(t, js, depID)

	chroot := &fakeJobsImportServer{}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()

	h := NewWithJobsStore(silentLogger(), js)
	body, _, _, err := h.buildJobsExportPayload(depID, fqdn)
	if err != nil {
		t.Fatalf("buildJobsExportPayload: %v", err)
	}
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: newRoundTripperToServer(srv),
	}
	url := "https://api." + fqdn + "/api/v1/internal/jobs/import"
	h.postJobsExportWithRetry(client, url, body, depID)

	if got := chroot.hits.Load(); got != 1 {
		t.Fatalf("chroot hit count = %d, want 1", got)
	}
	chroot.mu.Lock()
	p := chroot.received[0]
	chroot.mu.Unlock()
	regions := map[string]bool{}
	for _, j := range p.Jobs {
		regions[j.Region] = true
	}
	if !regions[""] || !regions["me-east-215-b-1"] {
		t.Errorf("delivered payload region buckets = %v, want both primary (\"\") + me-east-215-b-1", regions)
	}
}

func TestPostJobsExport_GivesUpOn4xx(t *testing.T) {
	chroot := &fakeJobsImportServer{status: http.StatusForbidden}
	srv := httptest.NewServer(chroot.handler())
	defer srv.Close()

	h := NewWithPDM(silentLogger(), &fakePDM{})
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: newRoundTripperToServer(srv),
	}
	done := make(chan struct{})
	go func() {
		h.postJobsExportWithRetry(client, "https://api.x.example/api/v1/internal/jobs/import", []byte(`{}`), "dep")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("postJobsExportWithRetry kept retrying a 4xx — must give up immediately")
	}
	if got := chroot.hits.Load(); got != 1 {
		t.Errorf("chroot hit count = %d, want 1 (no retry on 4xx)", got)
	}
}
