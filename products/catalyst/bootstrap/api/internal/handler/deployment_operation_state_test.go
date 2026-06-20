// deployment_operation_state_test.go — unit tests for the
// `operationInProgress` projection that backs the #3925 surface-D
// readiness chip. Asserts the cutover-detection truth table:
//   - no jobs / nil store           → false
//   - only provision/install jobs   → false  (initial provision is `status`, not an operation)
//   - cutover group running         → true
//   - cutover step running          → true
//   - cutover DORMANT (all-pending) → false (installed, never triggered)
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

// A TRIGGERED cutover (group + a running step) flips operationInProgress
// true. A group's status derives from its step children at read time, so a
// "running" group is one with a running step — the realistic shape.
func TestOperationInProgress_CutoverGroupRunning(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-cutover-grp"
	t0 := time.Now().UTC()
	parentID := jobs.JobID(depID, jobs.GroupCutover)
	seed := []jobs.Job{
		{DeploymentID: depID, JobName: jobs.GroupCutover, DisplayName: jobs.GroupCutoverDisplay, Type: jobs.JobTypeGroup, Status: jobs.StatusRunning},
		{DeploymentID: depID, JobName: "cutover-step-01-mirror", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusRunning, StartedAt: &t0},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	if !h.operationInProgress(depID) {
		t.Fatal("a triggered cutover (group + running step) must yield operationInProgress=true")
	}
}

// 🛑 Regression guard for the false-positive fix: bp-self-sovereign-cutover
// installs DORMANT — its group + all 8 steps are seeded as `pending`
// placeholders, but the operator never triggered the cutover (no step has
// started). A dormant cutover must NOT flip operationInProgress, else a
// freshly-converged, QUIESCENT Sovereign falsely reads OPERATION-IN-PROGRESS
// and the console pulls navigation toward /jobs/cutover. Live repro: hw173
// 2026-06-20 ({install: succeeded, group: pending, 12 steps: pending}).
func TestOperationInProgress_CutoverDormant(t *testing.T) {
	h, st := newOpStateHandler(t)
	depID := "dep-cutover-dormant"
	parentID := jobs.JobID(depID, jobs.GroupCutover)
	seed := []jobs.Job{
		// install Job succeeded → the dormant cutover is INSTALLED…
		{DeploymentID: depID, JobName: "install-self-sovereign-cutover", Type: jobs.JobTypeInstall, Status: jobs.StatusSucceeded},
		// …but group + every step are still PENDING placeholders (never triggered).
		{DeploymentID: depID, JobName: jobs.GroupCutover, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "cutover-step-01-mirror", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "cutover-step-05-egress-block", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "cutover-step-08-verify", Kind: jobs.KindStep, ParentID: parentID, Status: jobs.StatusPending},
	}
	for _, j := range seed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatalf("UpsertJob: %v", err)
		}
	}
	if h.operationInProgress(depID) {
		t.Fatal("a DORMANT (never-triggered, all-pending) cutover must yield operationInProgress=false")
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
