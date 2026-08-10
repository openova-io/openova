// reconciler_bridge.go — projects the GENERIC §5a reconciler observations
// (Flux Kustomizations, recurring CronJobs, standalone batch Jobs, and
// reconciler-marked Deployments) into the same Job + Execution model the
// HelmRelease and activity bridges use (issue #3646 §5a/§5b).
//
// # Why this exists
//
// The helmwatch bridge surfaces ONLY HelmRelease installs, so a green
// "install-openbao" row masked a Failed openbao-snapshot-save CronJob.
// This file closes that gap WITHOUT new per-app code: ONE method,
// OnReconcilerObservation, dispatches on the observation's Kind and
// upserts the correct leaf:
//
//   - reconcile  → "reconcile-<name>" leaf, status from the Kustomization
//     Ready condition.
//   - cron       → "cron-<name>" leaf; each spawned run is an Execution
//     (the model already supports this — types.go:259), so
//     the cron's status is its latest run.
//   - task       → "task-<name>" leaf for a standalone Job, OR — when the
//     Job is an HR install-hook — an Execution attached under
//     the owning "install-<chart>" leaf, never a duplicate
//     task row (the §5a ownership de-dup).
//   - reconciler → "reconciler-<name>" leaf carrying a HEALTH status
//     (healthy/degraded/failing), never a one-shot
//     "succeeded".
//
// All non-install reconcilers hang under the top-level GroupReconcilers
// group so a failing recurring activity surfaces RED at the top even when
// its install leaf is green.
package jobs

import (
	"strings"
	"time"
)

// ReconcilerObservation is the jobs-local mirror of
// helmwatch.ReconcilerObservation (the handler translates between them so
// this package never imports helmwatch — same pattern as InformerSeed vs
// helmwatch.ComponentSnapshot).
type ReconcilerObservation struct {
	Kind              string // ReconcilerKind* tag (reconcile|cron|task|reconciler)
	Name              string
	Namespace         string
	Status            string // helmwatch state vocab OR health vocab
	Health            bool   // true ⇒ Status is a health-axis value
	Message           string
	ObservedAt        time.Time
	OwnerInstallChart string // non-empty ⇒ attach as Execution under install-<chart>
	Executions        []ReconcilerExecutionObservation
}

// ReconcilerExecutionObservation is one spawned CronJob run.
type ReconcilerExecutionObservation struct {
	Name       string
	Status     string // helmwatch state vocab
	StartedAt  time.Time
	FinishedAt time.Time
	Message    string
}

// Reconciler observation Kind tags (mirror helmwatch.ReconcilerKind*).
const (
	ObsKindReconcile  = "reconcile"
	ObsKindCron       = "cron"
	ObsKindTask       = "task"
	ObsKindReconciler = "reconciler"
)

// reconcilerLeafJobName returns the canonical leaf JobName for a given
// observation kind + object name.
func reconcilerLeafJobName(kind, name string) string {
	switch kind {
	case ObsKindReconcile:
		return ReconcileJobPrefix + name
	case ObsKindCron:
		return CronJobPrefix + name
	case ObsKindReconciler:
		return ReconcilerJobPrefix + name
	default:
		return TaskJobPrefix + name
	}
}

// reconcilerLeafKind maps an observation kind onto the typed Job Kind.
func reconcilerLeafKind(obsKind string) string {
	switch obsKind {
	case ObsKindReconcile:
		return KindReconcile
	case ObsKindCron:
		return KindCron
	case ObsKindReconciler:
		return KindReconciler
	default:
		return KindTask
	}
}

// SeedReconcilerObservations applies a snapshot of §5a observations,
// returning the count of leaf Jobs written + Executions seeded. Idempotent
// — a re-read monotonic-merges (UpsertJob) and StartExecution reuse
// prevents duplicate runs. Best-effort per observation: one bad entry
// never aborts the batch (errors are returned aggregated as the first
// failure for the caller to log).
func (b *Bridge) SeedReconcilerObservations(obs []ReconcilerObservation) (jobsWritten, execsSeeded int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i := range obs {
		o := obs[i]
		o.Name = strings.TrimSpace(o.Name)
		if o.Name == "" {
			continue
		}
		jw, es, oerr := b.onReconcilerObservationLocked(o)
		jobsWritten += jw
		execsSeeded += es
		if oerr != nil && err == nil {
			err = oerr
		}
	}
	return jobsWritten, execsSeeded, err
}

// OnReconcilerObservation projects ONE observation. Public entry for a
// runtime (single-object) update; SeedReconcilerObservations batches it.
func (b *Bridge) OnReconcilerObservation(o ReconcilerObservation) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _, err := b.onReconcilerObservationLocked(o)
	return err
}

// onReconcilerObservationLocked is the core dispatch. Caller holds b.mu.
func (b *Bridge) onReconcilerObservationLocked(o ReconcilerObservation) (jobsWritten, execsSeeded int, err error) {
	// (a) HR install-hook Job → Execution under the install leaf, never a
	// duplicate task row (§5a ownership de-dup). The install leaf is
	// already minted by the helmwatch seed; we only append the run as an
	// Execution + log line so the operator can see the hook's outcome.
	if o.Kind == ObsKindTask && strings.TrimSpace(o.OwnerInstallChart) != "" {
		es, e := b.attachExecutionToInstallLeafLocked(o)
		return 0, es, e
	}

	jobName := reconcilerLeafJobName(o.Kind, o.Name)
	kind := reconcilerLeafKind(o.Kind)

	// Ensure the parent Reconcilers group exists so the leaf roots in a
	// real on-disk row (deriveTreeView resolves ParentID).
	if err := b.ensureGroupJob(GroupReconcilers, GroupReconcilersDisplay); err != nil {
		return 0, 0, err
	}

	// Status mapping: HEALTH-axis observations (reconciler kind) write the
	// health status verbatim; one-shot kinds translate through
	// jobStatusFromHelmState so they share the install vocabulary.
	var status string
	if o.Health {
		status = healthStatusFromObs(o.Status)
	} else {
		status = jobStatusFromHelmState(o.Status)
	}

	at := o.ObservedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}

	// Upsert the leaf. For the reconciler (health) kind the Job's Status
	// carries the health value directly — deriveTreeView's rollup treats a
	// degraded/failing leaf as a surfaced failure on the group.
	//
	// writeLeaf is reused for the post-Execution RE-ASSERT below. Seeding an
	// Execution calls StartExecution, which stamps Job.Status=running on
	// allocation, so any branch that seeds Executions MUST re-assert the
	// producer's recency-resolved status afterwards or the last-processed run
	// leaves the headline. That re-assert used to be spelled out inline in the
	// task + reconciler branches and was simply MISSING from the cron branch —
	// three copies of one rule, one of which was absent (UAT row 164). One
	// closure, called from every seeding branch, is why that can no longer
	// drift apart.
	writeLeaf := func(st string) error {
		return b.store.UpsertJob(Job{
			DeploymentID: b.deploymentID,
			JobName:      jobName,
			DisplayName:  reconcilerDisplay(o.Kind, o.Name),
			AppID:        o.Name,
			Type:         JobTypeInstall,
			Kind:         kind,
			ParentID:     JobID(b.deploymentID, GroupReconcilers),
			DependsOn:    []string{},
			Status:       st,
		})
	}
	if err := writeLeaf(status); err != nil {
		return 0, 0, err
	}
	jobsWritten++

	switch o.Kind {
	case ObsKindCron:
		// Each spawned run is an Execution on the cron leaf.
		es, e := b.seedCronExecutionsLocked(jobName, o)
		execsSeeded += es
		if e != nil {
			return jobsWritten, execsSeeded, e
		}
		// Re-resolve the headline FROM THE RUNS (UAT row 164). A cron leaf's
		// status is its latest run — that is this file's stated contract and
		// what TestReconcilerBridge_FailingCron_RedWhileInstallGreen pins, so
		// the producer's own o.Status must NOT be re-asserted here: it is
		// documented as "refined by the run below" and re-asserting it turns a
		// failed run green. But seeding walks the runs in LIST order and
		// StartExecution stamps running on each allocation, so whichever run
		// happens to be processed LAST leaves the headline. resolveRunHeadline
		// applies the same verdict rules the producer uses, order-independently.
		if st, ok := resolveRunHeadline(o.Executions); ok {
			if err := writeLeaf(st); err != nil {
				return jobsWritten, execsSeeded, err
			}
		}
	case ObsKindTask:
		// A task leaf is ONE STABLE entry keyed by its base identity
		// (issue #3916). When the producer carried per-run instances
		// (multiple Jobs sharing a generateName / run-suffix), each becomes
		// an Execution on this single leaf — deduped by run name across
		// re-reads, exactly like the cron leaf — so N reruns surface as 1
		// row + N runs, never N rows. When no per-run list is present, fall
		// back to a single status-carrying Execution.
		if len(o.Executions) > 0 {
			es, e := b.seedCronExecutionsLocked(jobName, o)
			execsSeeded += es
			if e != nil {
				return jobsWritten, execsSeeded, e
			}
			// seedCronExecutionsLocked finishes each run in list order, so the
			// LAST-processed run (not necessarily the most recent) would
			// otherwise leave the headline. Re-assert the producer's
			// recency-resolved status so the leaf reflects the latest run.
			if err := writeLeaf(status); err != nil {
				return jobsWritten, execsSeeded, err
			}
		} else if isTerminalOrRunningObs(status) {
			es, e := b.seedSingleExecutionLocked(jobName, status, o.Message, at)
			execsSeeded += es
			if e != nil {
				return jobsWritten, execsSeeded, e
			}
		}
	case ObsKindReconcile:
		// One-shot reconcile leaf gets a single Execution carrying the
		// status + message so the JobDetail log surface is non-empty and the
		// row renders a duration cell rather than an em-dash. Pending leaves
		// stay Job-only (no empty NDJSON file).
		if isTerminalOrRunningObs(status) {
			es, e := b.seedSingleExecutionLocked(jobName, status, o.Message, at)
			execsSeeded += es
			if e != nil {
				return jobsWritten, execsSeeded, e
			}
		}
	case ObsKindReconciler:
		// A health leaf is never "terminal" — it reports an ongoing axis.
		// Seed one Execution so the row deep-links to a log line stating
		// the current health, refreshed on each observation.
		es, e := b.seedHealthExecutionLocked(jobName, status, o.Message, at)
		execsSeeded += es
		if e != nil {
			return jobsWritten, execsSeeded, e
		}
		// StartExecution stamps the Job Status=running on allocation — but
		// a reconciler leaf must carry its HEALTH status (healthy/degraded/
		// failing), never the one-shot "running". Re-assert it after the
		// execution write so the wire shows the true health axis.
		if err := writeLeaf(status); err != nil {
			return jobsWritten, execsSeeded, err
		}
	}

	return jobsWritten, execsSeeded, nil
}

// attachExecutionToInstallLeafLocked appends a run of an HR install-hook
// Job as an Execution under the owning install-<chart> leaf, deduping by
// the run's stable name so a re-read doesn't duplicate it.
func (b *Bridge) attachExecutionToInstallLeafLocked(o ReconcilerObservation) (execsSeeded int, err error) {
	leafJobName := JobNamePrefix + o.OwnerInstallChart
	// Only attach when the install leaf actually exists (it should, from
	// the helmwatch seed) — otherwise the hook would orphan a leaf with no
	// install context.
	if _, _, gerr := b.store.GetJob(b.deploymentID, leafJobName); gerr != nil {
		return 0, nil
	}
	status := jobStatusFromHelmState(o.Status)
	if !isTerminalOrRunningObs(status) {
		return 0, nil
	}
	at := o.ObservedAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return b.seedSingleExecutionLocked(leafJobName, status, "[install-hook] "+o.Name+": "+o.Message, at)
}

// seedCronExecutionsLocked writes one Execution per spawned CronJob run,
// deduped by run name so a re-read never duplicates a run. Returns the count of
// NEW Executions seeded.
//
// An ALREADY-RECORDED run is not simply skipped. A run first observed in flight
// is written by StartExecution as running with FinishedAt=nil; when that same
// run later reaches a terminal verdict, the dedupe must TERMINATE the existing
// Execution rather than leave it untouched. Skipping it outright is UAT row
// 164's live symptom: on hw292 the syft-grype run that ended Failed /
// BackoffLimitExceeded still read "running, finishedAt: null" days later,
// because the only poll that could have terminated it was deduped away. A
// terminal Execution, or a run still in flight, is left exactly as it is.
func (b *Bridge) seedCronExecutionsLocked(cronLeafJobName string, o ReconcilerObservation) (execsSeeded int, err error) {
	existing, _ := b.recordedRunsForJobLocked(cronLeafJobName)
	for _, run := range o.Executions {
		run.Name = strings.TrimSpace(run.Name)
		if run.Name == "" {
			continue
		}
		if prev, ok := existing[run.Name]; ok {
			st := jobStatusFromHelmState(run.Status)
			// Terminate only on a real transition: the run has a terminal
			// verdict and the stored Execution has not recorded one yet.
			if IsTerminal(st) && !IsTerminal(prev.Status) {
				fin := run.FinishedAt
				if fin.IsZero() {
					fin = prev.StartedAt
				}
				if e := b.store.FinishExecution(b.deploymentID, prev.ID, st, fin); e != nil {
					return execsSeeded, e
				}
			}
			continue
		}
		st := jobStatusFromHelmState(run.Status)
		start := run.StartedAt
		if start.IsZero() {
			start = time.Now().UTC()
		}
		exec, e := b.store.StartExecution(b.deploymentID, cronLeafJobName, start)
		if e != nil {
			return execsSeeded, e
		}
		// Tag the run name in the first log line so the operator can map
		// the Execution to a kubectl-visible Job, and so the dedupe above
		// recognises it on the next read.
		if e := b.store.AppendLogLines(b.deploymentID, exec.ID, []LogLine{{
			Timestamp: start,
			Level:     LevelInfo,
			Message:   "[run " + run.Name + "] " + run.Message,
		}}); e != nil {
			return execsSeeded, e
		}
		if IsTerminal(st) {
			fin := run.FinishedAt
			if fin.IsZero() {
				fin = start
			}
			if e := b.store.FinishExecution(b.deploymentID, exec.ID, st, fin); e != nil {
				return execsSeeded, e
			}
		}
		execsSeeded++
	}
	return execsSeeded, nil
}

// resolveRunHeadline folds a leaf's runs into ONE headline status, applying the
// same verdict rules the producer applies at ingestion
// (helmwatch.adoptRunHeadline, #5931) so the two layers cannot disagree:
//
//  1. a real failure always displaces a no-verdict headline, whatever order the
//     apiserver listed the runs in;
//  2. an in-flight run never clears a recorded failure;
//  3. otherwise the most recent run wins, comparing like with like
//     (FinishedAt when the run terminated, else StartedAt).
//
// Returns ok=false when there are no runs, so the caller leaves the leaf's
// existing status alone rather than blanking it.
func resolveRunHeadline(runs []ReconcilerExecutionObservation) (string, bool) {
	headline := ""
	var at time.Time
	for _, r := range runs {
		st := jobStatusFromHelmState(r.Status)
		runAt := r.FinishedAt
		if runAt.IsZero() {
			runAt = r.StartedAt
		}
		if headline == "" {
			headline, at = st, runAt
			continue
		}
		switch {
		case !IsTerminal(st) && headline == StatusFailed:
			// (2) an in-flight run never clears a recorded failure.
			continue
		case st == StatusFailed && !IsTerminal(headline):
			// (1) a real failure displaces a no-verdict headline.
			headline, at = st, runAt
			continue
		}
		// (3) recency.
		if at.IsZero() || runAt.IsZero() || !runAt.Before(at) {
			headline, at = st, runAt
		}
	}
	return headline, headline != ""
}

// recordedRunsForJobLocked returns the Executions already recorded on a leaf,
// keyed by run name (read from each Execution's first log line's
// "[run <name>]" tag). Used to dedupe CronJob runs across re-reads — and,
// because it returns the Execution itself rather than a bare presence flag, to
// decide whether an already-recorded run still needs terminating.
func (b *Bridge) recordedRunsForJobLocked(jobName string) (map[string]Execution, error) {
	out := map[string]Execution{}
	_, execs, err := b.store.GetJob(b.deploymentID, jobName)
	if err != nil {
		return out, err
	}
	for _, e := range execs {
		page, perr := b.store.PageLogs(b.deploymentID, e.ID, 1, 1)
		if perr != nil || len(page.Lines) == 0 {
			continue
		}
		if name := runNameFromLogLine(page.Lines[0].Message); name != "" {
			out[name] = e
		}
	}
	return out, nil
}

// runNameFromLogLine extracts "<name>" from a "[run <name>] ..." log line.
func runNameFromLogLine(msg string) string {
	const pfx = "[run "
	if !strings.HasPrefix(msg, pfx) {
		return ""
	}
	rest := msg[len(pfx):]
	if i := strings.IndexByte(rest, ']'); i >= 0 {
		return strings.TrimSpace(rest[:i])
	}
	return ""
}

// seedSingleExecutionLocked writes one Execution for a one-shot leaf,
// idempotent: if the leaf already has a terminal Execution at the same
// status it is left untouched (no duplicate row on re-read).
func (b *Bridge) seedSingleExecutionLocked(jobName, status, message string, at time.Time) (execsSeeded int, err error) {
	job, execs, gerr := b.store.GetJob(b.deploymentID, jobName)
	if gerr == nil && job.LatestExecutionID != "" {
		for _, e := range execs {
			if e.ID == job.LatestExecutionID && e.Status == status {
				// Already reflected — no-op.
				return 0, nil
			}
		}
	}
	exec, e := b.store.StartExecution(b.deploymentID, jobName, at)
	if e != nil {
		return 0, e
	}
	if e := b.store.AppendLogLines(b.deploymentID, exec.ID, []LogLine{{
		Timestamp: at,
		Level:     logLevelForStatus(status),
		Message:   message,
	}}); e != nil {
		return 0, e
	}
	if IsTerminal(status) {
		if e := b.store.FinishExecution(b.deploymentID, exec.ID, status, at); e != nil {
			return 0, e
		}
	}
	return 1, nil
}

// seedHealthExecutionLocked writes/refreshes the single Execution that
// carries a reconciler leaf's current health line. The leaf never reaches
// a one-shot terminal state — the Execution stays running and its log line
// states the live health. Idempotent: re-reads append a fresh line only
// when the health changed (keeps the NDJSON from growing on every poll).
func (b *Bridge) seedHealthExecutionLocked(jobName, health, message string, at time.Time) (execsSeeded int, err error) {
	job, _, gerr := b.store.GetJob(b.deploymentID, jobName)
	execID := ""
	if gerr == nil {
		execID = job.LatestExecutionID
	}
	if execID == "" {
		exec, e := b.store.StartExecution(b.deploymentID, jobName, at)
		if e != nil {
			return 0, e
		}
		execID = exec.ID
		execsSeeded = 1
	}
	return execsSeeded, b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
		Timestamp: at,
		Level:     logLevelForHealth(health),
		Message:   "[health=" + health + "] " + message,
	}})
}

// reconcilerDisplay returns a granular, human-readable label for a
// reconciler leaf — the bare K8s object name humanized + qualified by its
// kind so the /jobs Name column reads the way the operator says it out
// loud (issue #3646: "non granular naming"). It builds the SAME label
// Store.deriveTreeView would back-fill, so the source-set value and the
// read-time floor agree:
//
//   - cron       "openbao-snapshot-save" → "OpenBao Snapshot Save (cron)"
//   - reconcile  "flux-system"           → "Reconcile Flux System"
//   - task       "gitea-sso-configure"   → "Gitea SSO Configure (task)"
//   - reconciler "sso-bridge"            → "SSO Bridge (reconciler)"
//
// Previously this returned the bare lowercase-hyphenated object name,
// which the founder reads as non-granular.
func reconcilerDisplay(obsKind, name string) string {
	return DeriveLeafDisplayName(Job{
		JobName: reconcilerLeafJobName(obsKind, name),
		AppID:   name,
		Kind:    reconcilerLeafKind(obsKind),
		Type:    JobTypeInstall,
	})
}

// healthStatusFromObs maps a helmwatch.ObsHealth* string onto the jobs
// HEALTH-axis status constant.
func healthStatusFromObs(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "healthy":
		return StatusHealthy
	case "failing":
		return StatusFailing
	case "degraded":
		return StatusDegraded
	default:
		return StatusDegraded
	}
}

// isTerminalOrRunningObs reports whether a translated status warrants an
// Execution (terminal or running) vs a Job-only pending row.
func isTerminalOrRunningObs(status string) bool {
	return IsTerminal(status) || status == StatusRunning
}

// logLevelForStatus maps a one-shot status onto a log level.
func logLevelForStatus(status string) string {
	switch status {
	case StatusFailed:
		return LevelError
	case StatusRunning:
		return LevelInfo
	default:
		return LevelInfo
	}
}

// logLevelForHealth maps a health status onto a log level.
func logLevelForHealth(health string) string {
	switch health {
	case StatusFailing:
		return LevelError
	case StatusDegraded:
		return LevelWarn
	default:
		return LevelInfo
	}
}
