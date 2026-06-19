// deployment_operation_state_test.go — unit tests for the
// `operationInProgress` projection that backs the #3925 surface-D
// readiness chip. Asserts the cutover-detection truth table:
//   - no jobs / nil store           → false
//   - only provision/install jobs   → false  (initial provision is `status`, not an operation)
//   - cutover group running         → true
//   - cutover step running          → true
//   - cutover group all-terminal    → false (operation finished)
package handler

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

func newOpStateHandler(t *testing.T) (*Handler, *jobs.Store) {
	t.Helper()
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), js)
	return h, js
}

func TestOperationInProgress_NilStore(t *testing.T) {
	h := New(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if h.operationInProgress("dep-x") {
		t.Fatal("nil jobs store must yield operationInProgress=false")
	}
}

func TestOperationInProgress_NoJobs(t *testing.T) {
	h, _ := newOpStateHandler(t)
	if h.operationInProgress("dep-empty") {
		t.Fatal("no jobs must yield operationInProgress=false")
	}
}

// Only initial-provision jobs (install-* under bootstrap-kit) — even
// while RUNNING — must NOT flip operationInProgress. The provision is
// tracked by `status`, not by this boolean.
func TestOperationInProgress_ProvisionJobsOnly(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-prov"
	t0 := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	parentID := jobs.JobID(depID, jobs.GroupBootstrapKit)
	seed := []jobs.Job{
		{DeploymentID: depID, JobName: jobs.GroupBootstrapKit, Type: jobs.JobTypeGroup, Status: jobs.StatusRunning},
		{DeploymentID: depID, JobName: "install-cilium", AppID: "cilium", Type: jobs.JobTypeInstall, ParentID: parentID, Status: jobs.StatusRunning, StartedAt: &t0},
		{DeploymentID: depID, JobName: "install-flux", AppID: "flux", Type: jobs.JobTypeInstall, ParentID: parentID, Status: jobs.StatusPending},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	if h.operationInProgress(depID) {
		t.Fatal("install/provision jobs must NOT count as an operation")
	}
}

// A running cutover GROUP job flips operationInProgress true.
func TestOperationInProgress_CutoverGroupRunning(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-cutover-grp"
	if err := st.UpsertJob(jobs.Job{
		DeploymentID: depID, JobName: jobs.GroupCutover, DisplayName: jobs.GroupCutoverDisplay,
		Type: jobs.JobTypeGroup, Status: jobs.StatusRunning,
	}); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if !h.operationInProgress(depID) {
		t.Fatal("a running cutover group must yield operationInProgress=true")
	}
}

// A running cutover STEP leaf (cutover-step-NN-…) flips it true even when
// the parent group row isn't separately present.
func TestOperationInProgress_CutoverStepRunning(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-cutover-step"
	t0 := time.Now().UTC()
	parentID := jobs.JobID(depID, jobs.GroupCutover)
	seed := []jobs.Job{
		{DeploymentID: depID, JobName: jobs.GroupCutover, Type: jobs.JobTypeGroup, Status: jobs.StatusRunning},
		{DeploymentID: depID, JobName: "cutover-step-01-mirror", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: &t0},
		{DeploymentID: depID, JobName: "cutover-step-05-egress-block", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusRunning, StartedAt: &t0},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	if !h.operationInProgress(depID) {
		t.Fatal("a running cutover step must yield operationInProgress=true")
	}
}

// Once every cutover step + the group are terminal (succeeded/failed),
// the operation is over — operationInProgress must be false again so the
// chip flips back to READY.
func TestOperationInProgress_CutoverAllTerminal(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-cutover-done"
	t0 := time.Now().UTC()
	parentID := jobs.JobID(depID, jobs.GroupCutover)
	seed := []jobs.Job{
		{DeploymentID: depID, JobName: jobs.GroupCutover, Type: jobs.JobTypeGroup, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: &t0},
		{DeploymentID: depID, JobName: "cutover-step-01-mirror", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: &t0},
		{DeploymentID: depID, JobName: "cutover-step-08-verify", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: &t0},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	if h.operationInProgress(depID) {
		t.Fatal("a fully-terminal cutover must yield operationInProgress=false")
	}
}

// A FAILED cutover step is terminal — operationInProgress is false (the
// operation stopped; the chip reflects READY/DEGRADED, and the Jobs page
// shows the failed step). This guards against treating `failed` as
// in-flight.
func TestOperationInProgress_CutoverStepFailedIsTerminal(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-cutover-failed"
	t0 := time.Now().UTC()
	parentID := jobs.JobID(depID, jobs.GroupCutover)
	seed := []jobs.Job{
		{DeploymentID: depID, JobName: jobs.GroupCutover, Type: jobs.JobTypeGroup, Status: jobs.StatusFailed, StartedAt: &t0, FinishedAt: &t0},
		{DeploymentID: depID, JobName: "cutover-step-05-egress-block", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusFailed, StartedAt: &t0, FinishedAt: &t0},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	if h.operationInProgress(depID) {
		t.Fatal("a failed (terminal) cutover step must yield operationInProgress=false")
	}
}
