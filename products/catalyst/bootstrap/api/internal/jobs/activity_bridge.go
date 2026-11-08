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
//     "<group>-step-<slug>" whose DependsOn is the step's DECLARED
//     upstream set (ActivityStep.DependsOn — the dependsOn DAG contract,
//     epic #6703 P4), or, for a step that declares nothing, the PRIOR
//     step in Order (the linear chain the Phase-0 tofu lifecycle uses).
//     The canvas renders the edges from these DependsOn entries; the
//     flow snapshot turns every entry into a finish-to-start arrow.
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
//
// That DependsOn preservation has one deliberate exception: SeedSteps
// marks its writes DependsOnAuthoritative because it has just been
// handed the complete ordered step set, so its EMPTY list means "this
// step is the root", not "no edge information" (issue #6131). Every
// other write path here leaves the flag false and keeps inheriting —
// including ensureStepLocked, which may be registering a step against a
// partial in-memory set and therefore cannot be believed about an
// absent edge.
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
// EXECUTION order the source runs the steps in. DependsOn is the
// optional DECLARED dependency set; when absent the bridge derives the
// linear chain from Order. DisplayName is the optional user-visible
// label (falls back to Slug when empty).
//
// # Order vs DependsOn (the step contract, epic #6703 P4)
//
// A linear Order says "runs after"; a DependsOn set says "needs the
// result of". The two coincide only for a strictly serial activity whose
// every step consumes its predecessor. The cutover is NOT that — Harbor
// projects do not need the Gitea mirror — and the linear chain the
// bridge used to fabricate from Order alone drew eleven edges of which
// several asserted a dependency no step actually has (the same class
// #6093 killed for the alphabetical sort: a plausible tree that is wrong
// at the edge level). So a source that knows its real DAG declares it
// per step and the bridge projects exactly that; a source that only
// knows its sequence leaves DependsOn nil and keeps the chain.
//
// Order stays authoritative for execution. A declared dependency may only
// name a step that sorts BEFORE this one — the sequential schedule must
// be a topological order of the declared DAG, otherwise the engine would
// start a step before its input exists. dependsOnForStepLocked drops any
// edge that violates that (and any unknown or self reference), so the
// projected graph is acyclic by construction whatever a source declares.
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
	// (stable secondary sort on Slug). A step with no declared DependsOn
	// depends on the step immediately before it in that sorted order;
	// the first such step has no dependency.
	Order int

	// DependsOn — the DECLARED upstream step slugs (epic #6703 P4).
	//
	//   nil           → not declared: derive the linear predecessor from
	//                   Order (the pre-P4 contract, kept for sources that
	//                   only know a sequence).
	//   []string{}    → declared EMPTY: this step is a root, no edge.
	//   [a, b]        → declared: edges from a and b (each must sort
	//                   before this step; others are dropped).
	//
	// The nil-vs-empty distinction is load-bearing and mirrors the
	// store's DependsOnAuthoritative rule (#6131): absence of information
	// and an asserted absence are different facts.
	DependsOn []string

	// Kind — optional typed discriminator for the step's leaf Job
	// (ADR-0015 D7: KindApplication, KindTopologyTransition). Empty keeps
	// the default KindStep.
	Kind string
}

func (s ActivityStep) kind() string {
	if strings.TrimSpace(s.Kind) != "" {
		return s.Kind
	}
	return KindStep
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

// dependsOnForStepLocked returns the DependsOn list for a step — the
// upstream steps' JobNames, in the format the helmwatch leaf convention
// uses (Job.DependsOn carries the dependency's JobName, resolved by
// deriveTreeView's byJobName map for the cascading-failure sweep and by
// the flow snapshot for edge rendering). Caller MUST hold a.mu.
//
// Declared DependsOn (non-nil) wins: every named slug that is a known
// step sorting STRICTLY BEFORE this one becomes an edge, in declaration
// order, de-duplicated; unknown slugs, self references and forward
// references are dropped (see ActivityStep). A declared-empty set yields
// no edge — the step is a root. An undeclared (nil) set yields the
// linear predecessor: the immediately-prior step in sorted order, or no
// edge for the first step.
func (a *ActivityBridge) dependsOnForStepLocked(slug string) []string {
	sorted := a.sortedSteps()
	pos := -1
	for i, s := range sorted {
		if s.Slug == slug {
			pos = i
			break
		}
	}
	if pos < 0 {
		return []string{}
	}
	step := sorted[pos]
	if step.DependsOn == nil {
		if pos == 0 {
			return []string{}
		}
		return []string{ActivityStepJobName(a.groupSlug, sorted[pos-1].Slug)}
	}
	// Declared DAG — resolve each slug against the steps that precede
	// this one in execution order; anything else cannot be an edge the
	// sequential engine honours, so it is not drawn either.
	before := make(map[string]struct{}, pos)
	for _, s := range sorted[:pos] {
		before[s.Slug] = struct{}{}
	}
	out := make([]string, 0, len(step.DependsOn))
	seen := make(map[string]struct{}, len(step.DependsOn))
	for _, dep := range step.DependsOn {
		dep = strings.TrimSpace(dep)
		if dep == "" || dep == slug {
			continue
		}
		if _, ok := before[dep]; !ok {
			continue
		}
		if _, dup := seen[dep]; dup {
			continue
		}
		seen[dep] = struct{}{}
		out = append(out, ActivityStepJobName(a.groupSlug, dep))
	}
	return out
}

// SeedSteps registers (or re-registers) the activity's full step set and
// writes a pending leaf Job per step under the activity group, wired
// with the linear DependsOn chain. Idempotent: re-seeding preserves any
// step that has already started (UpsertJob's monotonic merge keeps
// StartedAt/Status), so a source may seed lazily once its step list is
// known and again on resume without regressing live rows.
//
// Re-seeding also CORRECTS the edges, including the root's. The caller
// hands over the complete ordered set, so these writes are marked
// DependsOnAuthoritative and an empty DependsOn is written through as
// "this step has no predecessor" rather than inheriting the stored
// value (#6131). Without that, a store seeded once under a WRONG order
// could never have its root repaired: every non-root step recomputes to
// a non-empty edge that overwrites cleanly, while the root recomputes to
// the one value the preservation rule refuses to write, so it keeps the
// stale edge forever and the tree stays cyclic.
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
			Kind:         s.kind(),
			ParentID:     parent,
			DependsOn:    a.dependsOnForStepLocked(s.Slug),
			// SeedSteps was just handed the COMPLETE ordered step set, so
			// it is the one writer entitled to assert an absence: an empty
			// DependsOn here means "no predecessor", i.e. this is the
			// root, not "I don't know the edges". mergeJob inherits the
			// stored list from every other writer's empty value, which is
			// what stranded cutover-step-gitea-mirror on its pre-#6099
			// alphabetical edge (#6131). The lazy ensureStepLocked path
			// below deliberately does NOT set this: it may be registering
			// a step against a partial in-memory set, where empty really
			// does mean "no information".
			DependsOnAuthoritative: true,
			Status:                 StatusPending,
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
		Kind:         step.kind(),
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
	return a.startStep(step, message, t, true)
}

// ResumeStep is StartStep for a REPLAY: it flips the step to running and
// reuses its open Execution exactly like StartStep, but writes the
// message only when it had to allocate a fresh Execution. The cutover
// resume seed runs on every /jobs read; with StartStep each read
// appended another "[in flight …]" line, so the operator's log panel
// filled with identical placeholders instead of the step's output.
func (a *ActivityBridge) ResumeStep(step ActivityStep, message string, t time.Time) error {
	return a.startStep(step, message, t, false)
}

// AppendStepLog appends lines to the step's open Execution — the live
// output of the batch/v1 Job (or other worker) the step runs. A step
// with no open Execution is a no-op: there is nothing running to attach
// the output to.
func (a *ActivityBridge) AppendStepLog(slug string, lines []LogLine) error {
	slug = strings.TrimSpace(slug)
	if slug == "" || len(lines) == 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	execID := a.activeExecID[slug]
	if execID == "" {
		job, _, err := a.store.GetJob(a.deploymentID, ActivityStepJobName(a.groupSlug, slug))
		if err != nil || job.LatestExecutionID == "" || IsTerminal(job.Status) {
			return nil
		}
		execID = job.LatestExecutionID
	}
	return a.store.AppendLogLines(a.deploymentID, execID, lines)
}

func (a *ActivityBridge) startStep(step ActivityStep, message string, t time.Time, logOnReuse bool) error {
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
	reused := execID != ""
	if execID == "" {
		if job, _, err := a.store.GetJob(a.deploymentID, jobName); err == nil &&
			job.LatestExecutionID != "" && !IsTerminal(job.Status) {
			execID = job.LatestExecutionID
			a.activeExecID[step.Slug] = execID
			reused = true
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

	if reused && !logOnReuse {
		return nil
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

// ProjectObservation is the LEVEL-TRIGGERED entry point for sources that
// observe an object's current state rather than emit transitions (ADR-0015
// D7: Application CRs, topology-transition activity entries). Called on
// every /jobs read with the object's current (result, reason, message); it
// writes ONLY when the projected status, reason or message differs from
// what the store already holds, so repeated reads never mint duplicate
// Executions or log lines.
//
//   - result running → StartStep (reused Execution, message logged once).
//   - result success/skipped/failed → FinishStep (closes the Execution;
//     message appended as the final log line).
//   - result pending ("") → the leaf is (re)materialised as pending.
//
// reason + message are stamped verbatim on the leaf Job (Job.Reason /
// Job.Message) so the row itself says why it is blocked.
func (a *ActivityBridge) ProjectObservation(step ActivityStep, result, reason, message string, t time.Time) error {
	step.Slug = strings.TrimSpace(step.Slug)
	if step.Slug == "" {
		return nil
	}
	want := activityStatusFromResult(result)
	jobName := ActivityStepJobName(a.groupSlug, step.Slug)
	if prev, _, err := a.store.GetJob(a.deploymentID, jobName); err == nil &&
		prev.Status == want && prev.Reason == reason && prev.Message == message {
		return nil
	}
	var err error
	switch want {
	case StatusRunning:
		err = a.ResumeStep(step, message, t)
	case StatusSucceeded, StatusFailed:
		err = a.FinishStep(step, result, message, t)
	default:
		a.mu.Lock()
		err = a.ensureStepLocked(step)
		a.mu.Unlock()
	}
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.store.UpsertJob(Job{
		DeploymentID: a.deploymentID,
		JobName:      jobName,
		DisplayName:  step.DisplayName,
		Type:         JobTypeInstall,
		Kind:         step.kind(),
		ParentID:     JobID(a.deploymentID, a.groupSlug),
		DependsOn:    a.dependsOnForStepLocked(step.Slug),
		Status:       want,
		Reason:       reason,
		Message:      message,
	})
}

// RegisterSteps declares the step set (for dependsOn derivation) WITHOUT
// writing anything to the store. Level-triggered sources (ProjectObservation)
// use it instead of SeedSteps, whose pending re-seed would reset every
// already-projected leaf on each read.
func (a *ActivityBridge) RegisterSteps(steps []ActivityStep) {
	a.mu.Lock()
	defer a.mu.Unlock()
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
}
