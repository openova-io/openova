// helmwatch_bridge_test.go — assert that helmwatch component events
// translate into the Job + Execution + LogLine writes the canvas + per-
// job detail page render.
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

// leafJobs filters a Job slice to leaf install jobs only. Group jobs
// (synthesised parents like bootstrap-kit) are excluded so tests can
// reason about installs in isolation.
func leafJobs(in []Job) []Job {
	out := make([]Job, 0, len(in))
	for _, j := range in {
		if j.Type != JobTypeGroup {
			out = append(out, j)
		}
	}
	return out
}

// leafByName returns the leaf install Job with the given JobName, or
// fails the test. Used by tests that previously relied on `got[0]`
// when only one leaf existed — that index is now ambiguous because the
// store also returns the synthesised parent group row.
func leafByName(t *testing.T, in []Job, name string) Job {
	t.Helper()
	for _, j := range in {
		if j.JobName == name {
			return j
		}
	}
	t.Fatalf("leaf %q not found in %+v", name, in)
	return Job{}
}

// mustList wraps Store.ListJobs with a t.Fatal on error. Reduces the
// boilerplate in the failure-paths above.
func mustList(t *testing.T, st *Store, depID string) []Job {
	t.Helper()
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return got
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
	// Two leaf jobs + the synthesised bootstrap-kit parent group.
	if len(got) != 3 {
		t.Fatalf("expected 2 leaf jobs + 1 group, got %d (%+v)", len(got), got)
	}
	var cm, group Job
	for _, j := range got {
		if j.JobName == "install-cert-manager" {
			cm = j
		}
		if j.JobName == GroupBootstrapKit && j.Type == JobTypeGroup {
			group = j
		}
	}
	if cm.JobName == "" {
		t.Fatal("no install-cert-manager job")
	}
	if group.JobName == "" {
		t.Fatal("no bootstrap-kit group materialised on first seed")
	}
	wantParent := JobID(depID, GroupBootstrapKit)
	if cm.AppID != "cert-manager" || cm.ParentID != wantParent || cm.Type != JobTypeInstall || cm.Status != StatusPending {
		t.Errorf("seed metadata: %+v", cm)
	}
	if group.DisplayName != GroupBootstrapKitDisplay {
		t.Errorf("group DisplayName: want %q, got %q", GroupBootstrapKitDisplay, group.DisplayName)
	}
	if len(group.ChildIDs) != 2 {
		t.Errorf("group ChildIDs: want 2, got %d (%v)", len(group.ChildIDs), group.ChildIDs)
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
	if err := br.OnHelmReleaseEvent("cilium", HelmStatePending, "info", "observed", t0, nil); err != nil {
		t.Fatal(err)
	}
	leaves := leafJobs(mustList(t, st, depID))
	if len(leaves) != 1 || leaves[0].Status != StatusPending {
		t.Fatalf("after pending: %+v", leaves)
	}
	if leaves[0].LatestExecutionID != "" {
		t.Fatalf("Pending must not allocate an execution: %+v", leaves[0])
	}

	// Transition into installing — allocates an Execution.
	t1 := t0.Add(2 * time.Second)
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalling, "info", "Helm install in progress", t1, nil); err != nil {
		t.Fatal(err)
	}
	leaves = leafJobs(mustList(t, st, depID))
	if leaves[0].Status != StatusRunning {
		t.Errorf("status: want running, got %q", leaves[0].Status)
	}
	if leaves[0].LatestExecutionID == "" {
		t.Fatalf("execution not allocated")
	}
	if leaves[0].StartedAt == nil || !leaves[0].StartedAt.Equal(t1) {
		t.Errorf("StartedAt: got %v want %v", leaves[0].StartedAt, t1)
	}

	// Terminal: installed.
	t2 := t1.Add(30 * time.Second)
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalled, "info", "Ready=True", t2, nil); err != nil {
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
	if err := br.OnHelmReleaseEvent("flux", HelmStateInstalling, "info", "first reconcile", t0, nil); err != nil {
		t.Fatal(err)
	}
	if err := br.OnHelmReleaseEvent("flux", HelmStateFailed, "error", "InstallFailed: chart not found", t0.Add(time.Second), nil); err != nil {
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
		if err := br.OnHelmReleaseEvent("foo", HelmStateInstalling, "info", "spinning", t0.Add(time.Duration(i)*time.Second), nil); err != nil {
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

func TestBridge_OnProvisionerEvent_Phase0Lifecycle(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	// Phase-0 lifecycle event creates a durable Job under the
	// "provisioner" group so deep-links to /jobs/tofu-apply resolve
	// AND the JobsTable doesn't drop the row when bootstrap-kit
	// children land later.
	if err := br.OnProvisionerEvent(provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   "tofu-apply",
		Level:   "info",
		Message: "Applying — this provisions real Hetzner resources, please wait",
	}); err != nil {
		t.Fatal(err)
	}
	all, _ := st.ListJobs(depID)
	leaves := leafJobs(all)
	// 5 lifecycle phases: tofu-init, tofu-plan, tofu-apply, tofu-output,
	// cluster-bootstrap. tofu-apply is running; init+plan promoted to
	// succeeded; output+cluster-bootstrap remain pending.
	if len(leaves) != 5 {
		t.Fatalf("expected 5 lifecycle leaves, got %d: %+v", len(leaves), leaves)
	}
	apply := leafByName(t, leaves, "tofu-apply")
	if apply.Status != StatusRunning {
		t.Errorf("tofu-apply: status=%q want %q", apply.Status, StatusRunning)
	}
	plan := leafByName(t, leaves, "tofu-plan")
	if plan.Status != StatusSucceeded {
		t.Errorf("tofu-plan: status=%q want %q (promoted on later phase emit)", plan.Status, StatusSucceeded)
	}
	output := leafByName(t, leaves, "tofu-output")
	if output.Status != StatusPending {
		t.Errorf("tofu-output: status=%q want %q (not yet emitted)", output.Status, StatusPending)
	}

	// Phase-1 component event creates a leaf Job under the
	// bootstrap-kit parent — the lifecycle leaves are unchanged.
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
	leaves = leafJobs(mustList(t, st, depID))
	if len(leaves) != 6 {
		t.Fatalf("expected 5 lifecycle + 1 install = 6 leaves, got %d: %+v", len(leaves), leaves)
	}
	leafByName(t, leaves, "install-cilium")
	leafByName(t, leaves, "tofu-apply") // still present — must not vanish
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

	gotAfterFirst := leafJobs(mustList(t, st, depID))
	if len(gotAfterFirst) != 4 {
		t.Fatalf("after first seed: want 4 leaf jobs, got %d", len(gotAfterFirst))
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

	gotAfterSecond := leafJobs(mustList(t, st, depID))
	if len(gotAfterSecond) != 4 {
		t.Fatalf("after second seed: want 4 leaf jobs (no duplicates), got %d", len(gotAfterSecond))
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
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalled, "info", "still ok", now.Add(time.Second), nil); err != nil {
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
	leaves := leafJobs(mustList(t, st, depID))
	if len(leaves) != 1 {
		t.Errorf("expected 1 leaf (empty components skipped), got %d (%+v)", len(leaves), leaves)
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
// truly unknown phases ("phase-0", empty). Named Phase-0 lifecycle
// phases (tofu-init / tofu-plan / tofu-apply / tofu-output /
// cluster-bootstrap) are now durable Jobs and have their own coverage
// in TestBridge_OnProvisionerEvent_Phase0Lifecycle.
func TestOnProvisionerEvent_dropsUnknownPhases(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	for _, ph := range []string{"phase-0", "", "ham-sandwich"} {
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
		t.Errorf("unknown phases must not allocate Jobs, got %d", len(got))
	}
}

// TestBridge_SeedThenRuntimeTransitions covers the issue #695 fix path:
// after seeding the bridge from the helmwatch informer's initial-list
// (3 HRs all Pending → 3 pending Jobs), subsequent runtime transition
// events (the Watcher.Subscribe runtime stream) must update the
// per-Job state map so the wizard's /jobs page advances past the
// initial snapshot.
//
// Wire-shape mirrors how attachBridgeSeederHook subscribes the bridge
// to the Watcher: SeedJobsFromInformerList for the initial-list seed,
// then OnHelmReleaseEvent (driven from Watcher.Subscribe → bridge.
// OnProvisionerEvent) for each subsequent transition.
//
// Acceptance from the issue:
//   - HR-1 → Ready=True   ⇒ 1 succeeded + 2 pending
//   - HR-2 → Ready=Unknown ⇒ 1 succeeded + 1 running + 1 pending
//
// The pre-fix bug rendered every row as the seed-time state because
// the bridge was never wired to the runtime event stream — only to
// the one-shot OnInitialListSynced hook.
func TestBridge_SeedThenRuntimeTransitions(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	now := time.Date(2026, 5, 3, 14, 40, 0, 0, time.UTC)

	// Seed three pending HelmReleases (the initial-list snapshot the
	// helmwatch informer hands the bridge via SeedJobsFromInformerList).
	seeds := []InformerSeed{
		{Component: "cilium", State: HelmStatePending, Message: "waiting on first reconcile", ObservedAt: now},
		{Component: "cert-manager", State: HelmStatePending, Message: "waiting on cilium", ObservedAt: now},
		{Component: "flux", State: HelmStatePending, Message: "waiting on cert-manager", ObservedAt: now},
	}
	if _, _, err := br.SeedJobsFromInformerList(seeds); err != nil {
		t.Fatalf("SeedJobsFromInformerList: %v", err)
	}

	leaves := leafJobs(mustList(t, st, depID))
	if len(leaves) != 3 {
		t.Fatalf("after seed: want 3 leaf jobs, got %d (%+v)", len(leaves), leaves)
	}
	for _, j := range leaves {
		if j.Status != StatusPending {
			t.Errorf("after seed: %s status=%q, want %q", j.JobName, j.Status, StatusPending)
		}
	}

	// HR-1 (cilium) goes Ready=True → bridge sees the runtime
	// transition through OnHelmReleaseEvent (Watcher.Subscribe path).
	// Job status flips to succeeded; siblings remain pending.
	if err := br.OnHelmReleaseEvent("cilium", HelmStateInstalled, "info", "Helm install succeeded", now.Add(30*time.Second), nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent cilium installed: %v", err)
	}

	leaves = leafJobs(mustList(t, st, depID))
	statusByName := map[string]string{}
	for _, j := range leaves {
		statusByName[j.JobName] = j.Status
	}
	if got := statusByName["install-cilium"]; got != StatusSucceeded {
		t.Errorf("after cilium ready: install-cilium status=%q, want %q", got, StatusSucceeded)
	}
	if got := statusByName["install-cert-manager"]; got != StatusPending {
		t.Errorf("after cilium ready: install-cert-manager status=%q, want %q", got, StatusPending)
	}
	if got := statusByName["install-flux"]; got != StatusPending {
		t.Errorf("after cilium ready: install-flux status=%q, want %q", got, StatusPending)
	}

	// Verify the cilium Job has the terminal-state stamps the wizard
	// renders (StartedAt / FinishedAt / DurationMs / LatestExecutionID).
	cilium, ciliumExecs, err := st.GetJob(depID, JobID(depID, "install-cilium"))
	if err != nil {
		t.Fatalf("GetJob install-cilium: %v", err)
	}
	if cilium.LatestExecutionID == "" {
		t.Errorf("install-cilium: LatestExecutionID must be set after terminal transition")
	}
	if cilium.StartedAt == nil {
		t.Errorf("install-cilium: StartedAt must be set after terminal transition")
	}
	if cilium.FinishedAt == nil {
		t.Errorf("install-cilium: FinishedAt must be set after terminal transition")
	}
	if len(ciliumExecs) != 1 || ciliumExecs[0].Status != StatusSucceeded {
		t.Errorf("install-cilium executions: %+v", ciliumExecs)
	}

	// HR-2 (cert-manager) goes Ready=Unknown → state="installing"
	// → Job status=running. Siblings keep their prior state.
	if err := br.OnHelmReleaseEvent("cert-manager", HelmStateInstalling, "info", "first reconcile in flight", now.Add(45*time.Second), nil); err != nil {
		t.Fatalf("OnHelmReleaseEvent cert-manager installing: %v", err)
	}

	leaves = leafJobs(mustList(t, st, depID))
	statusByName = map[string]string{}
	for _, j := range leaves {
		statusByName[j.JobName] = j.Status
	}
	if got := statusByName["install-cilium"]; got != StatusSucceeded {
		t.Errorf("after cert-manager installing: install-cilium status=%q, want %q", got, StatusSucceeded)
	}
	if got := statusByName["install-cert-manager"]; got != StatusRunning {
		t.Errorf("after cert-manager installing: install-cert-manager status=%q, want %q", got, StatusRunning)
	}
	if got := statusByName["install-flux"]; got != StatusPending {
		t.Errorf("after cert-manager installing: install-flux status=%q, want %q", got, StatusPending)
	}

	// And cert-manager should now have an open Execution (not finished
	// — it's still running).
	cm, cmExecs, err := st.GetJob(depID, JobID(depID, "install-cert-manager"))
	if err != nil {
		t.Fatalf("GetJob install-cert-manager: %v", err)
	}
	if cm.LatestExecutionID == "" {
		t.Errorf("install-cert-manager: LatestExecutionID must be set after non-pending transition")
	}
	if cm.FinishedAt != nil {
		t.Errorf("install-cert-manager: FinishedAt must be nil while still running, got %v", cm.FinishedAt)
	}
	if len(cmExecs) != 1 || cmExecs[0].Status != StatusRunning {
		t.Errorf("install-cert-manager executions: %+v", cmExecs)
	}
}

// TestSeedJobsFromInformerList_resumeFinishesOrphanedExecution covers the
// TBD-B6/B7 stale-state class bug. Sequence (matches the t131 sin-2
// "install Jobs stuck running" incident captured in MEMORY.md):
//
//  1. First seed observes the HR as "installing" — bridge writes a
//     Status=running Job and allocates an open Execution.
//  2. Pod restart (simulated by NewBridge against the same Store): the
//     in-memory activeExecID + lastState are wiped, but the persisted
//     Job + Execution stay on disk. The Execution is still
//     Status=running, the Job still has a non-nil LatestExecutionID.
//  3. Resume seed observes the HR as "installed" (Ready=True flipped on
//     the cluster while the Pod was down).
//
// Pre-fix: the resume branch in SeedJobsFromInformerList only cleared
// the cursor — Execution stayed Status=running forever, Job.FinishedAt
// remained nil, DurationMs stayed 0. The JobsTable showed status=succeeded
// (because UpsertJob wrote that field) but the per-job detail page
// rendered an Execution row with a running spinner that never resolved.
// Founder caught this on t131.
//
// Post-fix: the resume branch detects the orphan Execution + calls
// FinishExecution which stamps both Execution.Status and Job.FinishedAt
// + DurationMs from a single canonical transition.
func TestSeedJobsFromInformerList_resumeFinishesOrphanedExecution(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	// First seed: HR is mid-install. Bridge allocates a non-terminal
	// Execution that stays open.
	_, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "cilium", State: HelmStateInstalling, Message: "reconciling", ObservedAt: t0},
	})
	if err != nil {
		t.Fatalf("first seed: %v", err)
	}
	jobID := JobID(depID, JobNamePrefix+"cilium")
	job, execs, err := st.GetJob(depID, jobID)
	if err != nil {
		t.Fatalf("GetJob after first seed: %v", err)
	}
	if job.Status != StatusRunning {
		t.Fatalf("first-seed Job.Status: want %q, got %q", StatusRunning, job.Status)
	}
	if job.LatestExecutionID == "" {
		t.Fatalf("first-seed: LatestExecutionID must be set")
	}
	if len(execs) != 1 || execs[0].Status != StatusRunning {
		t.Fatalf("first-seed executions: %+v", execs)
	}
	orphanExecID := execs[0].ID

	// Simulate Pod restart: fresh Bridge against the same persisted
	// Store. activeExecID + lastState start empty.
	br2 := NewBridge(st, depID)

	// Resume seed: HR transitioned to installed while the Pod was down.
	t1 := t0.Add(45 * time.Second)
	_, _, err = br2.SeedJobsFromInformerList([]InformerSeed{
		{Component: "cilium", State: HelmStateInstalled, Message: "Ready=True", ObservedAt: t1},
	})
	if err != nil {
		t.Fatalf("resume seed: %v", err)
	}

	// Post-resume invariants — the bug fix makes ALL of these hold.
	job, execs, err = st.GetJob(depID, jobID)
	if err != nil {
		t.Fatalf("GetJob after resume seed: %v", err)
	}
	if job.Status != StatusSucceeded {
		t.Errorf("resume Job.Status: want %q, got %q", StatusSucceeded, job.Status)
	}
	if job.FinishedAt == nil {
		t.Errorf("resume Job.FinishedAt: want non-nil after terminal seed, got nil")
	} else if !job.FinishedAt.Equal(t1) {
		t.Errorf("resume Job.FinishedAt: want %v, got %v", t1, *job.FinishedAt)
	}
	if job.DurationMs <= 0 {
		t.Errorf("resume Job.DurationMs: want >0 after terminal seed, got %d", job.DurationMs)
	}
	// The original orphan Execution must now be finished — not a fresh
	// one. This is the load-bearing assertion: the fix MUST close the
	// orphan, not paper over by allocating a new Execution.
	if job.LatestExecutionID != orphanExecID {
		t.Errorf("resume Job.LatestExecutionID drifted: want %q (orphan), got %q",
			orphanExecID, job.LatestExecutionID)
	}
	if len(execs) != 1 {
		t.Errorf("resume executions: want 1 (the original, now finished), got %d", len(execs))
	}
	if execs[0].Status != StatusSucceeded {
		t.Errorf("resume Execution.Status: want %q, got %q", StatusSucceeded, execs[0].Status)
	}
	if execs[0].FinishedAt == nil {
		t.Errorf("resume Execution.FinishedAt: want non-nil, got nil")
	}

	// Re-running the resume seed with the same terminal state MUST be a
	// no-op (idempotency — covers the secondary-watcher restart-twice
	// path on a flaky chroot-side catalyst-api).
	_, _, err = br2.SeedJobsFromInformerList([]InformerSeed{
		{Component: "cilium", State: HelmStateInstalled, Message: "Ready=True", ObservedAt: t1.Add(time.Second)},
	})
	if err != nil {
		t.Fatalf("second resume seed: %v", err)
	}
	_, execs, _ = st.GetJob(depID, jobID)
	if len(execs) != 1 {
		t.Errorf("after idempotent re-resume: want 1 execution, got %d", len(execs))
	}
	if execs[0].Status != StatusSucceeded {
		t.Errorf("after idempotent re-resume Execution.Status: want %q, got %q",
			StatusSucceeded, execs[0].Status)
	}
}

// TestSeedJobsFromInformerList_resumeFailedTerminalFinishesOrphan covers
// the failed-terminal variant of the stale-state bug — same Pod-restart
// race, but the HR flipped to InstallFailed instead of Ready=True. The
// orphan Execution must end up Status=failed (not running, not
// succeeded).
func TestSeedJobsFromInformerList_resumeFailedTerminalFinishesOrphan(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)

	// First seed: installing.
	if _, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "crossplane", State: HelmStateInstalling, Message: "reconciling", ObservedAt: t0},
	}); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	jobID := JobID(depID, JobNamePrefix+"crossplane")
	_, execs, _ := st.GetJob(depID, jobID)
	orphanExecID := execs[0].ID

	// Pod restart + resume with failed terminal state.
	br2 := NewBridge(st, depID)
	t1 := t0.Add(30 * time.Second)
	if _, _, err := br2.SeedJobsFromInformerList([]InformerSeed{
		{Component: "crossplane", State: HelmStateFailed, Message: "InstallFailed: timeout", ObservedAt: t1},
	}); err != nil {
		t.Fatalf("resume seed: %v", err)
	}

	job, execs, err := st.GetJob(depID, jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != StatusFailed {
		t.Errorf("Job.Status: want %q, got %q", StatusFailed, job.Status)
	}
	if job.FinishedAt == nil {
		t.Errorf("Job.FinishedAt: want non-nil, got nil")
	}
	if job.LatestExecutionID != orphanExecID {
		t.Errorf("LatestExecutionID drifted: want orphan %q, got %q", orphanExecID, job.LatestExecutionID)
	}
	if len(execs) != 1 || execs[0].Status != StatusFailed {
		t.Errorf("Execution after resume: want 1×failed, got %+v", execs)
	}
}

// TestSeedJobsFromInformerList_reseedRecoversFailedToSucceeded is the
// bridge-side acceptance for the false-FAILED-chip defect (issue #3687 /
// #910 tail). The finite Phase-1 bootstrap watch observed bp-catalyst-
// platform at a transient InstallFailed and stamped the Job + its
// Execution `failed`. The watch then RETURNED. Flux's remediation.retries
// later converged the HR to Ready=True — but nothing re-read the live HR,
// so the Job chip stuck on the stale `failed` snapshot and a UAT walk
// counted a false ❌.
//
// A subsequent live re-read (POST /refresh-watch, or the chroot
// list-and-seed path) feeds SeedJobsFromInformerList a seed with the
// component now at HelmStateInstalled. The bridge MUST heal the stale
// failure, driven by the live Ready condition (not a fabricated green):
//
//   - Job.Status flips failed → succeeded with FinishedAt + DurationMs set,
//   - a FRESH succeeded Execution is allocated at the recovery instant,
//   - the prior `failed` Execution is preserved as history,
//   - and a repeat re-seed at the same installed state is idempotent.
func TestSeedJobsFromInformerList_reseedRecoversFailedToSucceeded(t *testing.T) {
	st, br, depID := newBridgeFixture(t)
	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	// Bootstrap-window seed: the HR was observed at a terminal failure.
	if _, _, err := br.SeedJobsFromInformerList([]InformerSeed{
		{Component: "catalyst-platform", State: HelmStateFailed, Message: "InstallFailed: missing sme namespace", ObservedAt: t0},
	}); err != nil {
		t.Fatalf("first (failed) seed: %v", err)
	}
	jobID := JobID(depID, JobNamePrefix+"catalyst-platform")
	job, execs, err := st.GetJob(depID, jobID)
	if err != nil {
		t.Fatalf("GetJob after failed seed: %v", err)
	}
	if job.Status != StatusFailed {
		t.Fatalf("post-failed-seed Job.Status: want %q, got %q", StatusFailed, job.Status)
	}
	if len(execs) != 1 || execs[0].Status != StatusFailed {
		t.Fatalf("post-failed-seed executions: want 1×failed, got %+v", execs)
	}
	failedExecID := execs[0].ID

	// Watch returned; later a live re-read (refresh-watch) sees Ready=True.
	// Fresh Bridge models the Pod-restart / new-watcher path that has no
	// in-memory cursors — the persisted store is the only state.
	br2 := NewBridge(st, depID)
	t1 := t0.Add(4 * time.Minute)
	if _, execsSeeded, err := br2.SeedJobsFromInformerList([]InformerSeed{
		{Component: "catalyst-platform", State: HelmStateInstalled, Message: "Helm install succeeded after retry", ObservedAt: t1},
	}); err != nil {
		t.Fatalf("recovery re-seed: %v", err)
	} else if execsSeeded != 1 {
		t.Errorf("recovery re-seed executionsSeeded = %d, want 1 (the fresh succeeded Execution)", execsSeeded)
	}

	job, execs, err = st.GetJob(depID, jobID)
	if err != nil {
		t.Fatalf("GetJob after recovery seed: %v", err)
	}
	// The load-bearing assertion: the stale FAILED chip is healed.
	if job.Status != StatusSucceeded {
		t.Errorf("recovered Job.Status: want %q, got %q (stale FAILED chip not healed)", StatusSucceeded, job.Status)
	}
	if job.FinishedAt == nil {
		t.Errorf("recovered Job.FinishedAt: want non-nil, got nil")
	} else if !job.FinishedAt.Equal(t1) {
		t.Errorf("recovered Job.FinishedAt: want %v, got %v", t1, *job.FinishedAt)
	}
	if job.DurationMs <= 0 {
		t.Errorf("recovered Job.DurationMs: want >0, got %d", job.DurationMs)
	}
	// History preserved: the original failed Execution survives, and a
	// fresh succeeded Execution is now the latest.
	if len(execs) != 2 {
		t.Fatalf("recovered executions: want 2 (failed history + fresh succeeded), got %d: %+v", len(execs), execs)
	}
	var sawFailedHistory, sawSucceeded bool
	for _, e := range execs {
		if e.ID == failedExecID && e.Status == StatusFailed {
			sawFailedHistory = true
		}
		if e.Status == StatusSucceeded {
			sawSucceeded = true
		}
	}
	if !sawFailedHistory {
		t.Errorf("recovered executions: the original failed Execution %q must be preserved as history; got %+v", failedExecID, execs)
	}
	if !sawSucceeded {
		t.Errorf("recovered executions: a fresh succeeded Execution must exist; got %+v", execs)
	}
	if job.LatestExecutionID == failedExecID {
		t.Errorf("recovered Job.LatestExecutionID still points at the failed Execution %q; want the fresh succeeded one", failedExecID)
	}

	// Idempotency: re-seeding the same installed state must NOT allocate
	// another Execution (a /refresh-watch poll loop must converge).
	if _, execsSeeded, err := br2.SeedJobsFromInformerList([]InformerSeed{
		{Component: "catalyst-platform", State: HelmStateInstalled, Message: "still Ready=True", ObservedAt: t1.Add(time.Second)},
	}); err != nil {
		t.Fatalf("idempotent re-seed: %v", err)
	} else if execsSeeded != 0 {
		t.Errorf("idempotent re-seed executionsSeeded = %d, want 0 (already converged)", execsSeeded)
	}
	_, execs, _ = st.GetJob(depID, jobID)
	if len(execs) != 2 {
		t.Errorf("after idempotent re-seed: want 2 executions, got %d", len(execs))
	}
}
