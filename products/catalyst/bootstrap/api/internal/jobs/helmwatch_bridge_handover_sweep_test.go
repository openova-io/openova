// helmwatch_bridge_handover_sweep_test.go — lock-in for issue #3277:
// at a SUCCESSFUL Phase-1 handover the mother must sweep every
// still-reconciling bp-* install Job row terminal, while NEVER masking
// a real per-component failure as handed-over.
package jobs

import (
	"strings"
	"testing"
	"time"
)

// statusByName returns the status of the leaf install Job with the
// given JobName, or fails the test.
func statusByName(t *testing.T, in []Job, name string) string {
	t.Helper()
	for _, j := range in {
		if j.JobName == name {
			return j.Status
		}
	}
	t.Fatalf("leaf %q not found in %+v", name, in)
	return ""
}

// TestBridge_SweepHandoverInstallJobs_StampsNonTerminalRows asserts the
// core #3277 fix: given a finalStates set with some HRs non-terminal at
// a successful handover, the sweep stamps every install row terminal so
// no row is left "running"/"pending", AND a row that genuinely FAILED is
// preserved as failed (never masked as handed-over).
func TestBridge_SweepHandoverInstallJobs_StampsNonTerminalRows(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)

	// cilium — reached Installed (terminal succeeded) before handover.
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalled, "info", "Ready=True", t0, nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent cilium: %v", err)
	}
	// gitea — stuck Installing (running) at handover: this is the frozen
	// "running forever" row #3277 describes.
	if err := br.OnHelmReleaseEvent("gitea", HelmStateInstalling, "info", "Helm install in progress", t0, nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent gitea: %v", err)
	}
	// harbor — never started reconciling: a pending Job-only row with no
	// Execution allocated yet.
	if err := br.OnHelmReleaseEvent("harbor", HelmStatePending, "info", "DependencyNotReady", t0, nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent harbor: %v", err)
	}
	// crossplane — genuinely FAILED before handover. Must stay failed.
	if err := br.OnHelmReleaseEvent("crossplane", HelmStateInstalling, "info", "first reconcile", t0, nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent crossplane installing: %v", err)
	}
	if err := br.OnHelmReleaseEvent("crossplane", HelmStateFailed, "error", "InstallFailed: timed out", t0.Add(time.Second), nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent crossplane failed: %v", err)
	}

	// Pre-sweep sanity: gitea running, harbor pending, crossplane failed.
	pre := mustList(t, st, depID)
	if got := statusByName(t, pre, "install-gitea"); got != StatusRunning {
		t.Fatalf("pre-sweep install-gitea = %q, want %q", got, StatusRunning)
	}
	if got := statusByName(t, pre, "install-harbor"); got != StatusPending {
		t.Fatalf("pre-sweep install-harbor = %q, want %q", got, StatusPending)
	}
	if got := statusByName(t, pre, "install-crossplane"); got != StatusFailed {
		t.Fatalf("pre-sweep install-crossplane = %q, want %q", got, StatusFailed)
	}

	swept, err := br.SweepHandoverInstallJobs(t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("SweepHandoverInstallJobs: %v", err)
	}
	// gitea (running) + harbor (pending) swept = 2. cilium already
	// succeeded, crossplane already failed → both skipped.
	if swept != 2 {
		t.Fatalf("swept = %d, want 2 (gitea + harbor)", swept)
	}

	post := mustList(t, st, depID)
	// No leaf install row may be left non-terminal after the sweep.
	for _, j := range leafJobs(post) {
		if !strings.HasPrefix(j.JobName, JobNamePrefix) {
			continue
		}
		if !IsTerminal(j.Status) {
			t.Fatalf("install row %q left non-terminal after sweep: %q", j.JobName, j.Status)
		}
	}
	// The frozen rows are now Succeeded (handed over).
	if got := statusByName(t, post, "install-gitea"); got != StatusSucceeded {
		t.Fatalf("post-sweep install-gitea = %q, want %q", got, StatusSucceeded)
	}
	if got := statusByName(t, post, "install-harbor"); got != StatusSucceeded {
		t.Fatalf("post-sweep install-harbor = %q, want %q", got, StatusSucceeded)
	}
	// cilium stays succeeded (untouched), crossplane stays FAILED (a real
	// failure is never masked as handed-over).
	if got := statusByName(t, post, "install-cilium"); got != StatusSucceeded {
		t.Fatalf("post-sweep install-cilium = %q, want %q", got, StatusSucceeded)
	}
	if got := statusByName(t, post, "install-crossplane"); got != StatusFailed {
		t.Fatalf("post-sweep install-crossplane = %q, want %q (failure must be preserved)", got, StatusFailed)
	}

	// The swept rows carry an honest hand-over LogLine on their
	// Execution — not a silent fabricated success.
	gitea, execs, err := st.GetJob(depID, JobID(depID, "install-gitea"))
	if err != nil {
		t.Fatalf("GetJob install-gitea: %v", err)
	}
	if gitea.FinishedAt == nil {
		t.Fatal("post-sweep install-gitea has nil FinishedAt — terminal rows must stamp FinishedAt")
	}
	if len(execs) == 0 {
		t.Fatal("post-sweep install-gitea has no Execution")
	}
	page, err := st.PageLogs(depID, gitea.LatestExecutionID, 1, 100)
	if err != nil {
		t.Fatalf("PageLogs install-gitea: %v", err)
	}
	foundNote := false
	for _, ll := range page.Lines {
		if strings.Contains(ll.Message, "handed-over") {
			foundNote = true
			break
		}
	}
	if !foundNote {
		t.Fatalf("post-sweep install-gitea log has no hand-over note: %+v", page.Lines)
	}

	// A row stuck pending with NO Execution gets one allocated
	// retroactively so the JobsTable never renders an em-dash duration.
	harbor, harborExecs, err := st.GetJob(depID, JobID(depID, "install-harbor"))
	if err != nil {
		t.Fatalf("GetJob install-harbor: %v", err)
	}
	if harbor.FinishedAt == nil {
		t.Fatal("post-sweep install-harbor has nil FinishedAt")
	}
	if len(harborExecs) == 0 {
		t.Fatal("post-sweep install-harbor: expected a retroactively-allocated Execution")
	}
}

// TestBridge_SweepHandoverInstallJobs_Idempotent asserts a second sweep
// is a no-op (every row is already terminal), so a re-attached watch /
// re-fired handover does not churn the store.
func TestBridge_SweepHandoverInstallJobs_Idempotent(t *testing.T) {
	_, br, _ := newBridgeFixture(t)
	t0 := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)
	if err := br.OnHelmReleaseEvent("gitea", HelmStateInstalling, "info", "in progress", t0, nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent: %v", err)
	}

	first, err := br.SweepHandoverInstallJobs(t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("first SweepHandoverInstallJobs: %v", err)
	}
	if first != 1 {
		t.Fatalf("first sweep = %d, want 1", first)
	}
	second, err := br.SweepHandoverInstallJobs(t0.Add(2 * time.Minute))
	if err != nil {
		t.Fatalf("second SweepHandoverInstallJobs: %v", err)
	}
	if second != 0 {
		t.Fatalf("second sweep = %d, want 0 (idempotent)", second)
	}
}

// TestBridge_SweepHandoverInstallJobs_SkipsGroupsAndLifecycle asserts the
// sweep only touches leaf install rows: the synthesised bootstrap-kit
// group and the Phase-0 lifecycle Jobs (tofu-*, cluster-bootstrap) are
// not swept.
func TestBridge_SweepHandoverInstallJobs_SkipsGroupsAndLifecycle(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Date(2026, 6, 11, 9, 0, 0, 0, time.UTC)

	// Phase-0 lifecycle Jobs in a pending state (not install-* rows).
	if err := br.SeedProvisionerJobs(); err != nil {
		t.Fatalf("SeedProvisionerJobs: %v", err)
	}
	// One running install row.
	if err := br.OnHelmReleaseEvent("gitea", HelmStateInstalling, "info", "in progress", t0, nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent: %v", err)
	}

	swept, err := br.SweepHandoverInstallJobs(t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("SweepHandoverInstallJobs: %v", err)
	}
	// Only install-gitea is swept — the 5 lifecycle Jobs (which are
	// JobType install but NOT prefixed "install-") are left alone.
	if swept != 1 {
		t.Fatalf("swept = %d, want 1 (only the install-* leaf)", swept)
	}

	post := mustList(t, st, depID)
	// The lifecycle phase Jobs (e.g. tofu-init) are untouched / pending.
	if got := statusByName(t, post, PhaseTofuInit); got != StatusPending {
		t.Fatalf("lifecycle %q = %q after sweep, want %q (must not be swept)", PhaseTofuInit, got, StatusPending)
	}
}
