// activity_bridge.go — the GENERIC source-bridge that projects a
// declared platform "activity" (an ordered list of named steps with
// per-step state) into the same Job + Execution + LogLine model the
// helmwatch bridge uses for HelmReleases.
//
// # Why this exists (issue #3646)
//
// "Flux first" is the platform principle: every activity the operator
// can observe should appear on the catalyst jobs/activity canvas as a
// projection of a reconciling object — one unified model. The
// helmwatch bridge already realises this for HelmReleases (a
// HelmRelease IS a Flux object; its reconcile state becomes a leaf Job
// row under the bootstrap-kit group, with dependsOn edges derived from
// spec.dependsOn). But some platform activities run as raw batch/v1
// Jobs the helmwatch/mutation bridges never observe — most importantly
// the 11-step self-sovereign CUTOVER. Those activities are INVISIBLE on
// the jobs view, or — worse — the dormant chart-install row reads
// "Succeeded" while the real execution is mid-flight (issue #3646).
//
// ActivityBridge closes that gap GENERICALLY. It is NOT cutover-
// specific: any activity source (the cutover engine, a future DR
// switchover, a backup/restore run, …) constructs ONE ActivityBridge
// for its declared group and feeds it step transitions. The bridge owns
// the translation into the jobs store:
//
//   - The activity's group (e.g. GroupCutover) is materialised as a
//     top-level group Job, exactly like the bootstrap-kit group. Its
//     status/timing roll up from its step children at read time via
//     Store.deriveTreeView — the activity source never has to compute
//     aggregate state.
//   - Each declared step becomes a leaf Job named
//     "<group>-step-<slug>" with DependsOn pointing at the PRIOR step
//     (a linear chain, the same shape SeedProvisionerJobs uses for the
//     Phase-0 tofu lifecycle). The canvas renders the edges from these
//     DependsOn entries.
//   - Step state transitions (pending → running → succeeded/failed)
//     drive StartExecution / AppendLogLines / FinishExecution so the
//     per-step Exec Log surface resolves and the GitLab-CI log viewer
//     has a row to deep-link to — identical to how a HelmRelease leaf
//     behaves.
//
// # Relationship to the helmwatch bridge
//
// ActivityBridge is a sibling of Bridge, not a replacement. Both write
// into the same Store under the same deployment id, so a single /jobs
// read returns the bootstrap-kit installs, the Phase-0 lifecycle, AND
// every projected activity in one tree — the unified model issue #3646
// asks for. The two never contend: the helmwatch Bridge only ever
// writes "install-*" leaves + the bootstrap-kit/provisioner groups;
// ActivityBridge only ever writes "<group>-step-*" leaves + the
// activity's own group. Store.mu serialises the actual disk writes.
//
// # Idempotency
//
// Every method is idempotent and safe to call repeatedly — the cutover
// engine resumes across catalyst-api Pod restarts and re-emits the same
// step transitions, and a /jobs read may lazily re-seed. UpsertJob's
// monotonic merge (store.mergeJob) preserves StartedAt/FinishedAt and a
// non-empty prior DependsOn, so re-seeding a step never un-starts it or
// drops its edges. StartStep reuses an already-open Execution rather
// than allocating a second one.
package jobs

import (
	"strings"
	"sync"
	"time"
)

// ActivityStepResult enumerates the source-side per-step result values
// an activity reports. These mirror the cutover status ConfigMap's
// `step.<name>.result` vocabulary ("running" | "success" | "failed" |
// "skipped" | "" for not-yet-started) so a source can hand its native
// result string straight through without a translation table. The
// bridge maps each onto the Job Status enum via activityStatusFromResult.
const (
	ActivityResultPending = ""        // not yet attempted
	ActivityResultRunning = "running" // in flight
	ActivityResultSuccess = "success" // completed OK
	ActivityResultFailed  = "failed"  // terminal failure
	ActivityResultSkipped = "skipped" // already-satisfied / no-op
)

// ActivityStepPrefixSep is the separator between an activity group slug
// and a step slug in a step Job's JobName ("<group>-step-<slug>"). The
// "-step-" infix keeps the namespace disjoint from the helmwatch
// bridge's "install-" leaves so the two source bridges can never mint
// colliding Job ids under the same deployment.
const ActivityStepInfix = "-step-"

// ActivityStepJobName returns the canonical leaf JobName for a step of
// the given activity group. e.g. ActivityStepJobName("cutover",
// "harbor-prewarm") == "cutover-step-harbor-prewarm". Exported so a
// source (and its tests) can deep-link to GET /jobs/{jobId} for a step
// without re-deriving the format.
func ActivityStepJobName(groupSlug, stepSlug string) string {
	return groupSlug + ActivityStepInfix + stepSlug
}

// ActivityStep is one declared step of an activity. Order is the
// 1-based (or any monotonically increasing) sequence position — the
// bridge sorts by it to build the linear DependsOn chain and to choose
// each step's predecessor. DisplayName is the optional user-visible
// label (falls back to Slug when empty).
type ActivityStep struct {
	// Slug — stable, URL-safe step identifier (e.g. "harbor-prewarm").
	// Used as the step's JobName suffix and as the map key the source
	// keys transitions off. Required; an empty slug is skipped.
	Slug string

	// DisplayName — friendly label rendered by the canvas/table. Empty
	// falls back to Slug on the leaf Job (matching the helmwatch leaf
	// convention where DisplayName is empty and the FE renders JobName).
	DisplayName string

	// Order — sequence position. Steps are sorted ascending by Order
	// (stable secondary sort on Slug) to derive the linear dependency
	// chain. A step depends on the step immediately before it in that
	// sorted order; the first step has no dependency.
	Order int
}

// ActivityBridge projects one declared activity (identified by a stable
// group slug) into the jobs Store under a single deployment id. One
// bridge instance per (deployment, activity-group). Goroutine-safe: the
// internal mutex serialises the bridge's own per-step cursor map; the
// Store serialises the actual writes.
type ActivityBridge struct {
	store        *Store
	deploymentID string

	// groupSlug / groupDisplay — the top-level group Job this activity's
	// steps hang under (e.g. GroupCutover / GroupCutoverDisplay).
	groupSlug    string
	groupDisplay string

	mu sync.Mutex

	// steps — the declared step set, in the order the source registered
	// them. Re-registering (SeedSteps) replaces this slice so a source
	// whose step list is only discovered lazily (the cutover engine
	// reads its steps from ConfigMaps at /start) can seed once the list
	// is known. Keyed lookups go through stepIndex.
	steps     []ActivityStep
	stepIndex map[string]int // slug → position in steps

	// activeExecID — per-step-slug id of the in-flight Execution. Set on
	// the first StartStep, cleared on FinishStep. Survives only within a
	// process lifetime; a resumed activity re-attaches via the persisted
	// Job.LatestExecutionID (see StartStep's reuse path) so a Pod restart
	// never strands an Execution open.
	activeExecID map[string]string
}

// NewActivityBridge constructs a bridge for the given activity group.
// store must be non-nil; deploymentID + groupSlug must be non-empty.
// groupDisplay falls back to groupSlug when empty.
func NewActivityBridge(store *Store, deploymentID, groupSlug, groupDisplay string) *ActivityBridge {
	if groupDisplay == "" {
		groupDisplay = groupSlug
	}
	return &ActivityBridge{
		store:        store,
		deploymentID: deploymentID,
		groupSlug:    groupSlug,
		groupDisplay: groupDisplay,
		stepIndex:    map[string]int{},
		activeExecID: map[string]string{},
	}
}

// GroupJobID returns the stable id of this activity's top-level group
// Job. Exported so a source can assert against it / deep-link.
func (a *ActivityBridge) GroupJobID() string {
	return JobID(a.deploymentID, a.groupSlug)
}

// ensureGroupLocked idempotently materialises the activity's top-level
// group Job. The group's persisted Status is a placeholder; the runtime
// status rolls up from its step children at read time (deriveTreeView).
// Caller MUST hold a.mu.
func (a *ActivityBridge) ensureGroupLocked() error {
	return a.store.UpsertJob(Job{
		DeploymentID: a.deploymentID,
		JobName:      a.groupSlug,
		DisplayName:  a.groupDisplay,
		Type:         JobTypeGroup,
		ParentID:     "",
		DependsOn:    []string{},
		Status:       StatusPending,
	})
}

// sortedSteps returns the declared steps ordered by Order ascending
// (stable secondary sort on Slug). Caller MUST hold a.mu.
func (a *ActivityBridge) sortedSteps() []ActivityStep {
	out := make([]ActivityStep, len(a.steps))
	copy(out, a.steps)
	// Insertion sort — step counts are tiny (the cutover has 11) so an
	// allocation-free stable sort by hand keeps the dependency edges
	// deterministic without pulling sort.Slice's closure overhead.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a0, b0 := out[j-1], out[j]
			less := a0.Order < b0.Order || (a0.Order == b0.Order && a0.Slug <= b0.Slug)
			if less {
				break
			}
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// dependsOnForStepLocked returns the DependsOn list (length 0 for the
// first step, length 1 thereafter) pointing at the immediately-prior
// step's JobName, so the canvas renders the linear chain. The format
// matches the helmwatch leaf convention (Job.DependsOn carries the
// dependency's JobName, resolved by deriveTreeView's byJobName map for
// the cascading-failure sweep and by the FE for edge rendering).
// Caller MUST hold a.mu.
func (a *ActivityBridge) dependsOnForStepLocked(slug string) []string {
	sorted := a.sortedSteps()
	for i, s := range sorted {
		if s.Slug != slug {
			continue
		}
		if i == 0 {
			return []string{}
		}
		return []string{ActivityStepJobName(a.groupSlug, sorted[i-1].Slug)}
	}
	return []string{}
}

// SeedSteps registers (or re-registers) the activity's full step set and
// writes a pending leaf Job per step under the activity group, wired
// with the linear DependsOn chain. Idempotent: re-seeding preserves any
// step that has already started (UpsertJob's monotonic merge keeps
// StartedAt/Status), so a source may seed lazily once its step list is
// known and again on resume without regressing live rows.
//
// Steps with an empty Slug are skipped (defence against a malformed
// source). An empty step list materialises just the group Job (so the
// group is visible as pending even before steps are discovered).
func (a *ActivityBridge) SeedSteps(steps []ActivityStep) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Replace the declared set + rebuild the index. We keep only
	// non-empty slugs so dependsOn derivation and transitions can't key
	// off a blank.
	a.steps = a.steps[:0]
	a.stepIndex = make(map[string]int, len(steps))
	for _, s := range steps {
		s.Slug = strings.TrimSpace(s.Slug)
		if s.Slug == "" {
			continue
		}
		a.stepIndex[s.Slug] = len(a.steps)
		a.steps = append(a.steps, s)
	}

	if err := a.ensureGroupLocked(); err != nil {
		return err
	}

	parent := JobID(a.deploymentID, a.groupSlug)
	for _, s := range a.sortedSteps() {
		display := s.DisplayName
		if err := a.store.UpsertJob(Job{
			DeploymentID: a.deploymentID,
			JobName:      ActivityStepJobName(a.groupSlug, s.Slug),
			DisplayName:  display,
			Type:         JobTypeInstall,
			Kind:         KindStep,
			ParentID:     parent,
			DependsOn:    a.dependsOnForStepLocked(s.Slug),
			Status:       StatusPending,
		}); err != nil {
			return err
		}
	}
	return nil
}

// ensureStepLocked idempotently materialises a single step's leaf Job
// (used when a transition arrives for a step SeedSteps never declared —
// e.g. a source that emits transitions without a prior seed). It also
// lazily registers the step in the in-memory set so dependsOn derivation
// has something to chain off. Caller MUST hold a.mu.
func (a *ActivityBridge) ensureStepLocked(step ActivityStep) error {
	step.Slug = strings.TrimSpace(step.Slug)
	if step.Slug == "" {
		return nil
	}
	if _, ok := a.stepIndex[step.Slug]; !ok {
		a.stepIndex[step.Slug] = len(a.steps)
		a.steps = append(a.steps, step)
	}
	if err := a.ensureGroupLocked(); err != nil {
		return err
	}
	return a.store.UpsertJob(Job{
		DeploymentID: a.deploymentID,
		JobName:      ActivityStepJobName(a.groupSlug, step.Slug),
		DisplayName:  step.DisplayName,
		Type:         JobTypeInstall,
		ParentID:     JobID(a.deploymentID, a.groupSlug),
		DependsOn:    a.dependsOnForStepLocked(step.Slug),
		Status:       StatusPending,
	})
}

// StartStep transitions a step into Running, allocating an Execution
// (and stamping StartedAt) on the first call. Re-calling for a step that
// already has an open Execution reuses it (no duplicate Execution row),
// which is the load-bearing idempotency property for the cutover
// engine's resume path. An optional message is appended as the first
// LogLine so the Exec Log surface is never empty.
//
// step carries the slug + (optional) display + order. If the step was
// already declared via SeedSteps the display/order there win for the
// persisted row's DependsOn chain; passing them again is harmless.
func (a *ActivityBridge) StartStep(step ActivityStep, message string, t time.Time) error {
	step.Slug = strings.TrimSpace(step.Slug)
	if step.Slug == "" {
		return nil
	}
	t = t.UTC()

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.ensureStepLocked(step); err != nil {
		return err
	}
	jobName := ActivityStepJobName(a.groupSlug, step.Slug)

	// Reuse an open Execution: prefer the in-memory cursor, then fall
	// back to the persisted LatestExecutionID when it's still running
	// (Pod-restart resume). Only allocate a fresh Execution when neither
	// is live — this is what prevents a resumed activity from minting a
	// second Execution for a step that's already in flight.
	execID := a.activeExecID[step.Slug]
	if execID == "" {
		if job, _, err := a.store.GetJob(a.deploymentID, jobName); err == nil &&
			job.LatestExecutionID != "" && !IsTerminal(job.Status) {
			execID = job.LatestExecutionID
			a.activeExecID[step.Slug] = execID
		}
	}
	if execID == "" {
		exec, err := a.store.StartExecution(a.deploymentID, jobName, t)
		if err != nil {
			return err
		}
		execID = exec.ID
		a.activeExecID[step.Slug] = execID
	} else {
		// Execution already open — StartExecution stamps Status=running
		// on allocation, but on a reuse we still want the Job flipped to
		// running (e.g. a resume that re-enters a step the durable record
		// left at pending). UpsertJob's merge keeps the prior StartedAt.
		if err := a.store.UpsertJob(Job{
			DeploymentID: a.deploymentID,
			JobName:      jobName,
			DisplayName:  step.DisplayName,
			Type:         JobTypeInstall,
			ParentID:     JobID(a.deploymentID, a.groupSlug),
			DependsOn:    a.dependsOnForStepLocked(step.Slug),
			Status:       StatusRunning,
		}); err != nil {
			return err
		}
	}

	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "[" + step.Slug + "] started"
	}
	return a.store.AppendLogLines(a.deploymentID, execID, []LogLine{{
		Timestamp: t,
		Level:     LevelInfo,
		Message:   msg,
	}})
}

// FinishStep transitions a step into its terminal state and closes its
// Execution. result is the source's native result string (one of the
// ActivityResult* consts); it maps onto the Job Status enum. A
// "skipped" / "success" result lands Succeeded; "failed" lands Failed.
// An optional message is appended as the final LogLine.
//
// Idempotent: FinishExecution is a no-op on an already-terminal
// Execution, and a step with no open Execution (e.g. a "skipped" step
// the source never started) gets one allocated retroactively so the
// JobsTable never renders an em-dash duration cell — matching the
// helmwatch bridge's markLifecyclePhaseTerminalLocked.
func (a *ActivityBridge) FinishStep(step ActivityStep, result, message string, t time.Time) error {
	step.Slug = strings.TrimSpace(step.Slug)
	if step.Slug == "" {
		return nil
	}
	t = t.UTC()
	status := activityStatusFromResult(result)
	if !IsTerminal(status) {
		// A non-terminal result handed to FinishStep is a source bug;
		// treat it as success so we never leave a step wedged, but the
		// caller really should use StartStep for "running".
		status = StatusSucceeded
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.ensureStepLocked(step); err != nil {
		return err
	}
	jobName := ActivityStepJobName(a.groupSlug, step.Slug)

	execID := a.activeExecID[step.Slug]
	if execID == "" {
		// Re-attach to a persisted open Execution (resume) before
		// allocating a new one.
		if job, _, err := a.store.GetJob(a.deploymentID, jobName); err == nil && job.LatestExecutionID != "" {
			if priorExec, ferr := a.store.FindExecution(a.deploymentID, job.LatestExecutionID); ferr == nil && !IsTerminal(priorExec.Status) {
				execID = job.LatestExecutionID
			}
		}
	}
	if execID == "" {
		exec, err := a.store.StartExecution(a.deploymentID, jobName, t)
		if err != nil {
			return err
		}
		execID = exec.ID
	}

	if msg := strings.TrimSpace(message); msg != "" {
		level := LevelInfo
		if status == StatusFailed {
			level = LevelError
		}
		if err := a.store.AppendLogLines(a.deploymentID, execID, []LogLine{{
			Timestamp: t,
			Level:     level,
			Message:   msg,
		}}); err != nil {
			return err
		}
	}

	if err := a.store.FinishExecution(a.deploymentID, execID, status, t); err != nil {
		return err
	}
	delete(a.activeExecID, step.Slug)
	return nil
}

// activityStatusFromResult maps an activity's native per-step result
// string onto the Job Status enum. Unknown/empty results map to
// pending so a not-yet-attempted step renders correctly.
func activityStatusFromResult(result string) string {
	switch strings.ToLower(strings.TrimSpace(result)) {
	case ActivityResultSuccess, ActivityResultSkipped:
		return StatusSucceeded
	case ActivityResultFailed:
		return StatusFailed
	case ActivityResultRunning:
		return StatusRunning
	default:
		return StatusPending
	}
}
