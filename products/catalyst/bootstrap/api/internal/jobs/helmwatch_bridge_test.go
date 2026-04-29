// helmwatch_bridge_test.go — assert that helmwatch component events
// translate into the Job + Execution + LogLine writes the table-view
// UX renders.
package jobs

import (
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func newBridgeFixture(t *testing.T) (*Store, *Bridge, string) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	depID := "dep-bridge"
	return st, NewBridge(st, depID), depID
}

func TestBridge_SeedJobs_StripsBpPrefix(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	if err := br.SeedJobs([]SeedSpec{
		{Chart: "cilium"},
		{Chart: "cert-manager", DependsOn: []string{"bp-cilium", "cilium"}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(got))
	}
	var cm Job
	for _, j := range got {
		if j.JobName == "install-cert-manager" {
			cm = j
		}
	}
	if cm.JobName == "" {
		t.Fatal("no install-cert-manager job")
	}
	if cm.AppID != "cert-manager" || cm.BatchID != BatchBootstrapKit || cm.Status != StatusPending {
		t.Errorf("seed metadata: %+v", cm)
	}
	// dependsOn: bp- prefix must be stripped, then install- prepended.
	want := []string{"install-cilium", "install-cilium"}
	if len(cm.DependsOn) != len(want) {
		t.Fatalf("dependsOn len: %v", cm.DependsOn)
	}
	for i, w := range want {
		if cm.DependsOn[i] != w {
			t.Errorf("dependsOn[%d]: got %q, want %q", i, cm.DependsOn[i], w)
		}
	}
}

func TestBridge_OnHelmReleaseEvent_HappyPath(t *testing.T) {
	st, br, depID := newBridgeFixture(t)

	t0 := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	if err := br.OnHelmReleaseEvent("cilium", HelmStatePending, "info", "observed", t0); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListJobs(depID)
	if len(got) != 1 || got[0].Status != StatusPending {
		t.Fatalf("after pending: %+v", got)
	}
	if got[0].LatestExecutionID != "" {
		t.Fatalf("Pending must not allocate an execution: %+v", got[0])
	}

	// Transition into installing — allocates an Execution.
	t1 := t0.Add(2 * time.Second)
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalling, "info", "Helm install in progress", t1); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListJobs(depID)
	if got[0].Status != StatusRunning {
		t.Errorf("status: want running, got %q", got[0].Status)
	}
	if got[0].LatestExecutionID == "" {
		t.Fatalf("execution not allocated")
	}
	if got[0].StartedAt == nil || !got[0].StartedAt.Equal(t1) {
		t.Errorf("StartedAt: got %v want %v", got[0].StartedAt, t1)
	}

	// Terminal: installed.
	t2 := t1.Add(30 * time.Second)
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalled, "info", "Ready=True", t2); err != nil {
		t.Fatal(err)
	}
	job, execs, err := st.GetJob(depID, JobID(depID, "install-cilium"))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusSucceeded {
		t.Errorf("final status: want succeeded, got %q", job.Status)
	}
	if job.FinishedAt == nil || !job.FinishedAt.Equal(t2) {
		t.Errorf("FinishedAt: got %v want %v", job.FinishedAt, t2)
	}
	if job.DurationMs != 30000 {
		t.Errorf("DurationMs: got %d want 30000", job.DurationMs)
	}
	if len(execs) != 1 || execs[0].Status != StatusSucceeded {
		t.Errorf("executions: %+v", execs)
	}

	// Logs: 2 transitions (installing, installed) → 2 lines, prefixed.
	page, _ := st.PageLogs(depID, execs[0].ID, 1, 100)
	if page.Total != 2 || len(page.Lines) != 2 {
		t.Fatalf("logs: %+v", page)
	}
	if !strings.HasPrefix(page.Lines[0].Message, "[installing]") {
		t.Errorf("line0 message prefix: %q", page.Lines[0].Message)
	}
	if !strings.HasPrefix(page.Lines[1].Message, "[installed]") {
		t.Errorf("line1 message prefix: %q", page.Lines[1].Message)
	}
}

func TestBridge_OnHelmReleaseEvent_FailedTerminal(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Now().UTC()
	if err := br.OnHelmReleaseEvent("flux", HelmStateInstalling, "info", "first reconcile", t0); err != nil {
		t.Fatal(err)
	}
	if err := br.OnHelmReleaseEvent("flux", HelmStateFailed, "error", "InstallFailed: chart not found", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	job, _, _ := st.GetJob(depID, JobID(depID, "install-flux"))
	if job.Status != StatusFailed {
		t.Errorf("status: want failed, got %q", job.Status)
	}
	page, _ := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	hasError := false
	for _, ll := range page.Lines {
		if ll.Level == LevelError {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected at least one ERROR log line, got %+v", page.Lines)
	}
}

func TestBridge_DuplicateStateSuppressed(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if err := br.OnHelmReleaseEvent("foo", HelmStateInstalling, "info", "spinning", t0.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	job, _, _ := st.GetJob(depID, JobID(depID, "install-foo"))
	page, _ := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	// Only the first emit registers as a transition; the next four
	// repeats are suppressed by lastState.
	if page.Total != 1 {
		t.Errorf("expected 1 line for 5 dup events, got %d", page.Total)
	}
}

func TestBridge_OnProvisionerEvent_FiltersPhase0(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	// Phase-0 OpenTofu event has no Component/State — bridge must drop.
	if err := br.OnProvisionerEvent(provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   "tofu-apply",
		Level:   "info",
		Message: "Apply complete",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListJobs(depID)
	if len(got) != 0 {
		t.Errorf("Phase-0 event must not create jobs, got %+v", got)
	}

	// Phase-1 component event creates a Job.
	if err := br.OnProvisionerEvent(provisioner.Event{
		Time:      time.Now().UTC().Format(time.RFC3339),
		Phase:     "component",
		Level:     "info",
		Component: "cilium",
		State:     HelmStateInstalling,
		Message:   "in progress",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListJobs(depID)
	if len(got) != 1 || got[0].JobName != "install-cilium" {
		t.Errorf("expected install-cilium job, got %+v", got)
	}
}

func TestMapLevel(t *testing.T) {
	cases := map[string]string{
		"":        LevelInfo,
		"info":    LevelInfo,
		"warn":    LevelWarn,
		"warning": LevelWarn,
		"error":   LevelError,
		"debug":   LevelDebug,
		"WEIRD":   LevelInfo,
	}
	for in, want := range cases {
		if got := mapLevel(in); got != want {
			t.Errorf("mapLevel(%q): got %q, want %q", in, got, want)
		}
	}
}

func TestJobStatusFromHelmState(t *testing.T) {
	cases := map[string]string{
		HelmStateInstalled:  StatusSucceeded,
		HelmStateFailed:     StatusFailed,
		HelmStateInstalling: StatusRunning,
		HelmStateDegraded:   StatusRunning,
		HelmStatePending:    StatusPending,
		"":                  StatusPending,
		"unknown":           StatusPending,
	}
	for in, want := range cases {
		if got := jobStatusFromHelmState(in); got != want {
			t.Errorf("jobStatusFromHelmState(%q): got %q, want %q", in, got, want)
		}
	}
}
