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

// TestSeedJobsFromInformerList_idempotent proves the load-bearing
// property of the bridge's backfill path: calling
// SeedJobsFromInformerList twice with the SAME cache contents writes
// each Job + Execution + LogLine exactly once. This is what makes it
// safe for the handler to call the seed hook on every Watcher start
// (resume-after-restart, /refresh-watch).
func TestSeedJobsFromInformerList_idempotent(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	now := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)

	seeds := []InformerSeed{
		{Component: "cilium", State: HelmStateInstalled, Message: "Helm install succeeded", ObservedAt: now},
		{Component: "cert-manager", State: HelmStateInstalled, Message: "Helm install succeeded", ObservedAt: now.Add(time.Minute)},
		{Component: "flux", State: HelmStateInstalling, Message: "first reconcile", ObservedAt: now.Add(2 * time.Minute)},
		{Component: "crossplane", State: HelmStateFailed, Message: "InstallFailed: timed out", ObservedAt: now.Add(3 * time.Minute)},
	}

	jobsWritten1, execs1, err := br.SeedJobsFromInformerList(seeds)
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if jobsWritten1 != 4 {
		t.Errorf("first seed jobsWritten: want 4, got %d", jobsWritten1)
	}
	// All four seeds allocate an Execution: 3 terminal (cilium /
	// cert-manager installed, crossplane failed) + 1 non-terminal
	// (flux installing). The non-terminal Execution stays open so raw
	// helm-controller log lines can append to it; terminal Executions
	// are stamped finished by the seed itself. See issue #305.
	if execs1 != 4 {
		t.Errorf("first seed executionsSeeded: want 4, got %d", execs1)
	}

	gotAfterFirst, err := st.ListJobs(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotAfterFirst) != 4 {
		t.Fatalf("after first seed: want 4 jobs, got %d", len(gotAfterFirst))
	}

	// Snapshot per-Job content for the idempotency comparison.
	beforeByName := map[string]Job{}
	for _, j := range gotAfterFirst {
		beforeByName[j.JobName] = j
	}

	// Second seed with identical input MUST be a no-op for terminal
	// states (no second Execution allocated, no second LogLine
	// appended). Non-terminal states (the "installing" flux row) are
	// cheap to re-Upsert so the bridge does, but the Status doesn't
	// change.
	jobsWritten2, execs2, err := br.SeedJobsFromInformerList(seeds)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if jobsWritten2 != 4 {
		t.Errorf("second seed jobsWritten: want 4 (re-upsert is idempotent), got %d", jobsWritten2)
	}
	if execs2 != 0 {
		t.Errorf("second seed executionsSeeded: want 0 (idempotent), got %d", execs2)
	}

	gotAfterSecond, err := st.ListJobs(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotAfterSecond) != 4 {
		t.Fatalf("after second seed: want 4 jobs (no duplicates), got %d", len(gotAfterSecond))
	}
	for _, j := range gotAfterSecond {
		prev, ok := beforeByName[j.JobName]
		if !ok {
			t.Errorf("second seed introduced unexpected job %q", j.JobName)
			continue
		}
		// Status + LatestExecutionID must be stable across the two
		// seeds; a non-stable LatestExecutionID would mean the
		// bridge allocated a fresh Execution (the bug we're guarding).
		if j.Status != prev.Status {
			t.Errorf("status drift for %q: was %q now %q", j.JobName, prev.Status, j.Status)
		}
		if j.LatestExecutionID != prev.LatestExecutionID {
			t.Errorf("LatestExecutionID drift for %q: was %q now %q", j.JobName, prev.LatestExecutionID, j.LatestExecutionID)
		}
	}

	// Per-Job execution count must be exactly 1 for terminal jobs and
	// 0 for the installing job, both before AND after the duplicate
	// seed. This is the strongest idempotency invariant.
	for _, name := range []string{"install-cilium", "install-cert-manager", "install-crossplane"} {
		_, execs, err := st.GetJob(depID, JobID(depID, name))
		if err != nil {
			t.Fatalf("GetJob %q: %v", name, err)
		}
		if len(execs) != 1 {
			t.Errorf("%s: want 1 execution after dup seed, got %d", name, len(execs))
		}
	}
	// install-flux was seeded as "installing" — a single open
	// Execution must exist so the FE log viewer has a row to deep-link
	// to and the logtailer's raw helm-controller lines can append.
	_, fluxExecs, err := st.GetJob(depID, JobID(depID, "install-flux"))
	if err != nil {
		t.Fatalf("GetJob install-flux: %v", err)
	}
	if len(fluxExecs) != 1 {
		t.Errorf("install-flux: want 1 open execution for installing seed, got %d", len(fluxExecs))
	}
	if len(fluxExecs) == 1 && fluxExecs[0].FinishedAt != nil {
		t.Errorf("install-flux execution must remain open (no FinishedAt), got %+v", fluxExecs[0].FinishedAt)
	}
}

// TestSeedJobsFromInformerList_writesSyntheticLogLine proves every
// terminal-state seed materialises exactly one INFO/ERROR log line of
// the form "[seeded] state=<state> at <ts>: <message>". The
// table-view UX deep-links to this Execution's logs even when no
// real helm-controller events were ever emitted (because the watch
// started AFTER Ready=True had already flipped).
func TestSeedJobsFromInformerList_writesSyntheticLogLine(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	now := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)
	seeds := []InformerSeed{
		{Component: "cilium", State: HelmStateInstalled, Message: "Helm install succeeded", ObservedAt: now},
		{Component: "crossplane", State: HelmStateFailed, Message: "InstallFailed: timed out", ObservedAt: now.Add(time.Minute)},
		{Component: "flux", State: HelmStateInstalling, Message: "first reconcile", ObservedAt: now.Add(2 * time.Minute)},
	}
	if _, _, err := br.SeedJobsFromInformerList(seeds); err != nil {
		t.Fatalf("SeedJobsFromInformerList: %v", err)
	}

	type logCheck struct {
		jobName     string
		wantLevel   string
		wantStateIn string
	}
	checks := []logCheck{
		{"install-cilium", LevelInfo, "state=installed"},
		{"install-crossplane", LevelError, "state=failed"},
	}
	for _, c := range checks {
		t.Run(c.jobName, func(t *testing.T) {
			job, execs, err := st.GetJob(depID, JobID(depID, c.jobName))
			if err != nil {
				t.Fatalf("GetJob: %v", err)
			}
			if len(execs) != 1 {
				t.Fatalf("expected exactly 1 Execution, got %d", len(execs))
			}
			page, err := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
			if err != nil {
				t.Fatalf("PageLogs: %v", err)
			}
			if page.Total != 1 {
				t.Fatalf("expected exactly 1 LogLine, got %d", page.Total)
			}
			ll := page.Lines[0]
			if ll.Level != c.wantLevel {
				t.Errorf("level: want %q, got %q", c.wantLevel, ll.Level)
			}
			if !strings.HasPrefix(ll.Message, "[seeded]") {
				t.Errorf("message must start with [seeded]: %q", ll.Message)
			}
			if !strings.Contains(ll.Message, c.wantStateIn) {
				t.Errorf("message must contain %q: %q", c.wantStateIn, ll.Message)
			}
		})
	}

	// The non-terminal install-flux row gets a single open Execution
	// with the synthetic anchor "[seeded] state=installing ..." line.
	// Subsequent raw helm-controller log lines append to this same
	// Execution; the FinishedAt remains nil until the HR transitions
	// to a terminal state.
	job, fluxExecs, err := st.GetJob(depID, JobID(depID, "install-flux"))
	if err != nil {
		t.Fatalf("GetJob install-flux: %v", err)
	}
	if len(fluxExecs) != 1 {
		t.Fatalf("install-flux: want 1 open execution, got %d", len(fluxExecs))
	}
	if fluxExecs[0].FinishedAt != nil {
		t.Errorf("install-flux execution must remain open (FinishedAt nil), got %v", fluxExecs[0].FinishedAt)
	}
	page, err := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	if err != nil {
		t.Fatalf("PageLogs install-flux: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("install-flux: want exactly 1 anchor LogLine, got %d", page.Total)
	}
	if !strings.HasPrefix(page.Lines[0].Message, "[seeded]") || !strings.Contains(page.Lines[0].Message, "state=installing") {
		t.Errorf("install-flux anchor line shape unexpected: %q", page.Lines[0].Message)
	}
}

// TestSeedJobsFromInformerList_subsequentTransitionSuppressed proves
// the bridge's lastState cursor is primed by the seed: after seeding
// `cilium` as installed, a follow-up OnHelmReleaseEvent with
// HelmStateInstalled MUST be a no-op (no second Job upsert, no second
// log line). This is the load-bearing property that keeps the seed +
// emit paths from double-counting on a HR that has been Ready=True
// for an hour.
func TestSeedJobsFromInformerList_subsequentTransitionSuppressed(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	now := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)

	if _, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "cilium", State: HelmStateInstalled, Message: "ok", ObservedAt: now},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// First the watch's processEvent fires AddFunc → bridge sees the
	// installed state. Because lastState[cilium] is already
	// "installed" from the seed, the bridge must short-circuit and
	// NOT allocate a second Execution.
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalled, "info", "still ok", now.Add(time.Second)); err != nil {
		t.Fatalf("OnHelmReleaseEvent: %v", err)
	}
	_, execs, err := st.GetJob(depID, JobID(depID, "install-cilium"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if len(execs) != 1 {
		t.Errorf("seed + dup transition: want 1 execution, got %d", len(execs))
	}
}

// TestSeedJobsFromInformerList_skipsEmptyComponent guards against a
// future helmwatch.SnapshotComponents bug that returned a row with an
// empty AppID — the bridge must skip those rather than synthesise a
// "install-" Job with no chart name.
func TestSeedJobsFromInformerList_skipsEmptyComponent(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	if _, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "", State: HelmStateInstalled},
		{Component: "  ", State: HelmStateInstalled},
		{Component: "cilium", State: HelmStateInstalled},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, _ := st.ListJobs(depID)
	if len(got) != 1 {
		t.Errorf("expected 1 job (empty components skipped), got %d", len(got))
	}
}

// TestOnRawComponentLog_appendsToActiveExecution proves a raw
// helm-controller log line forwarded by OnProvisionerEvent (Phase=
// component-log) lands verbatim on the active Execution allocated by
// the seed, with the level surfaced via mapLevel.
func TestOnRawComponentLog_appendsToActiveExecution(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	now := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)

	// Seed a non-terminal "installing" component → allocates an open
	// Execution + writes the synthetic [seeded] anchor line.
	if _, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "seaweedfs", State: HelmStateInstalling, Message: "first reconcile", ObservedAt: now},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rawLines := []struct {
		ts    time.Time
		level string
		msg   string
	}{
		{now.Add(1 * time.Second), "info", `{"ts":"...","level":"info","msg":"reconciling helmrelease","helmrelease":"flux-system/bp-seaweedfs"}`},
		{now.Add(2 * time.Second), "warn", `level=warn helmrelease="flux-system/bp-seaweedfs" msg="retrying chart pull"`},
		{now.Add(3 * time.Second), "error", `level=error helmrelease="flux-system/bp-seaweedfs" msg="post-install hook timed out"`},
	}
	for _, r := range rawLines {
		if err := br.OnProvisionerEvent(provisioner.Event{
			Time:      r.ts.Format(time.RFC3339),
			Phase:     phaseComponentLog,
			Level:     r.level,
			Component: "seaweedfs",
			Message:   r.msg,
		}); err != nil {
			t.Fatalf("OnProvisionerEvent: %v", err)
		}
	}

	job, execs, err := st.GetJob(depID, JobID(depID, "install-seaweedfs"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("want 1 execution, got %d", len(execs))
	}
	if execs[0].FinishedAt != nil {
		t.Errorf("execution must remain open during installing state, got finished=%v", execs[0].FinishedAt)
	}
	page, err := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	if err != nil {
		t.Fatalf("PageLogs: %v", err)
	}
	// 1 anchor [seeded] line + 3 raw helm-controller lines.
	if page.Total != 4 {
		t.Fatalf("want 4 log lines (1 anchor + 3 raw), got %d", page.Total)
	}
	for i, want := range []string{LevelInfo, LevelInfo, LevelWarn, LevelError} {
		if got := page.Lines[i].Level; got != want {
			t.Errorf("line %d level: got %q, want %q", i, got, want)
		}
	}
	for i := 1; i <= 3; i++ {
		if page.Lines[i].Message != rawLines[i-1].msg {
			t.Errorf("line %d message must round-trip verbatim:\n  got  %q\n  want %q",
				i, page.Lines[i].Message, rawLines[i-1].msg)
		}
	}
}

// TestOnRawComponentLog_allocatesExecutionWhenJobMissing proves a raw
// helm-controller log line for a component the bridge has never seen
// before still gets persisted: a Job + Execution are created on the
// fly. Covers the "Pod restart wiped both the cursor AND the index"
// edge case that used to drop every line.
func TestOnRawComponentLog_allocatesExecutionWhenJobMissing(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)

	if err := br.OnProvisionerEvent(provisioner.Event{
		Time:      t0.Format(time.RFC3339),
		Phase:     phaseComponentLog,
		Level:     "info",
		Component: "openbao",
		Message:   `helmrelease="flux-system/bp-openbao" msg="installing chart"`,
	}); err != nil {
		t.Fatalf("OnProvisionerEvent: %v", err)
	}

	job, execs, err := st.GetJob(depID, JobID(depID, "install-openbao"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != StatusRunning {
		t.Errorf("job status: want running, got %q", job.Status)
	}
	if len(execs) != 1 {
		t.Fatalf("want 1 execution, got %d", len(execs))
	}
	page, err := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	if err != nil {
		t.Fatalf("PageLogs: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("want 1 log line, got %d", page.Total)
	}
}

// TestOnRawComponentLog_dropsAfterTerminal proves helm-controller's
// post-install drift-check chatter does NOT extend a closed Execution.
// Once the Job has reached a terminal status the line is dropped.
func TestOnRawComponentLog_dropsAfterTerminal(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	now := time.Date(2026, 4, 30, 11, 0, 0, 0, time.UTC)

	// Seed cilium as installed (terminal) so the bridge has a closed
	// Execution + cleared cursor on record.
	if _, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "cilium", State: HelmStateInstalled, Message: "ok", ObservedAt: now},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := br.OnProvisionerEvent(provisioner.Event{
		Time:      now.Add(time.Hour).Format(time.RFC3339),
		Phase:     phaseComponentLog,
		Level:     "info",
		Component: "cilium",
		Message:   `helmrelease="flux-system/bp-cilium" msg="reconciliation drift check"`,
	}); err != nil {
		t.Fatalf("OnProvisionerEvent: %v", err)
	}

	job, _, err := st.GetJob(depID, JobID(depID, "install-cilium"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	page, err := st.PageLogs(depID, job.LatestExecutionID, 1, 100)
	if err != nil {
		t.Fatalf("PageLogs: %v", err)
	}
	// Only the 1 anchor [seeded] line — the post-terminal raw line
	// must be dropped.
	if page.Total != 1 {
		t.Fatalf("want 1 log line (post-terminal raw line dropped), got %d", page.Total)
	}
}

// TestOnProvisionerEvent_dropsUnknownPhases keeps the bridge inert for
// Phase-0 OpenTofu events (Phase="phase-0", Phase="tofu-init") that
// have no Job analogue — a regression guard against accidentally
// materialising Jobs for opentofu output.
func TestOnProvisionerEvent_dropsUnknownPhases(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	for _, ph := range []string{"phase-0", "tofu-init", "tofu-apply", ""} {
		if err := br.OnProvisionerEvent(provisioner.Event{
			Time:      time.Now().UTC().Format(time.RFC3339),
			Phase:     ph,
			Component: "anything",
			Message:   "noise",
		}); err != nil {
			t.Errorf("phase %q must not error: %v", ph, err)
		}
	}
	got, _ := st.ListJobs(depID)
	if len(got) != 0 {
		t.Errorf("non-component phases must not allocate Jobs, got %d", len(got))
	}
}
