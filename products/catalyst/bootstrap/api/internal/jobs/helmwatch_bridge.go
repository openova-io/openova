// helmwatch_bridge.go — translates helmwatch.Watcher events into Job +
// Execution + LogLine writes against the Store.
//
// The bridge is a goroutine-safe object (every method takes the
// store's mutex implicitly through the Store API). The catalyst-api
// constructs one Bridge per deployment and feeds it via OnEvent on the
// same path runProvisioning already feeds the SSE buffer. The two
// feeds (jobs/executions REST + the existing SSE stream) live in
// parallel — neither is ever the source of truth for the other.
//
// Mapping
//
//   - Each bp-<chart> HelmRelease maps to exactly one Job whose
//     jobName="install-<chart>".
//   - The Bridge calls UpsertJob on every component event so a new
//     HelmRelease (the first time helmwatch emits it) materialises a
//     pending Job row.
//   - On the first transition out of helmwatch.StatePending the
//     Bridge calls StartExecution to allocate a new Execution row
//     and stamp the Job's StartedAt + LatestExecutionID.
//   - On every component event the Bridge appends a LogLine to the
//     active Execution's NDJSON file (level mapped via levelFor). The
//     bridge keeps no in-memory line buffer — append is the canonical
//     persistence path.
//   - On a terminal helmwatch state (Installed / Failed) the Bridge
//     calls FinishExecution which flips the Job's terminal status +
//     stamps DurationMs.
//
// DependsOn — the bridge does not derive dependsOn from helmwatch
// events (helmwatch.Event carries only conditions). The catalyst-api
// is expected to call SeedJobs once at watch start with the bp-*
// HelmRelease specs (the bootstrap-kit YAMLs ship dependsOn metadata
// the helmwatch reader can stamp directly via the same dynamic
// informer it already runs). This file exposes the SeedJobs entry
// point; the actual wiring lives in
// internal/handler/phase1_watch.go.
package jobs

import (
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// Helmwatch state strings — kept here as untyped consts so the bridge
// does not import the helmwatch package and create an import cycle
// (helmwatch_test → handler → jobs would otherwise close back into
// jobs needing helmwatch types).
const (
	HelmStatePending    = "pending"
	HelmStateInstalling = "installing"
	HelmStateInstalled  = "installed"
	HelmStateDegraded   = "degraded"
	HelmStateFailed     = "failed"
)

// Phase strings for provisioner.Event routing — duplicated here to
// avoid the same import-cycle hazard as the HelmState* consts. Kept in
// lockstep with helmwatch.PhaseComponent / PhaseComponentLog.
const (
	phaseComponent    = "component"
	phaseComponentLog = "component-log"
)

// Bridge holds the per-deployment cursor the helmwatch consumer needs:
// which Execution is currently active for which Job. The cursor is
// memory-only and is discarded on Pod restart — a resumed Phase-1
// watch starts a fresh Execution row, which is the correct behaviour
// (an Execution is "one watch attempt"; a Pod restart legitimately
// counts as a new attempt).
type Bridge struct {
	store        *Store
	deploymentID string

	// mu serialises access to the in-memory cursors below.
	// SeedJobsFromInformerList runs from the helmwatch.Watcher's
	// post-Sync hook (fireOnSyncedHooks) while OnHelmReleaseEvent
	// runs from the informer's processEvent goroutine — without
	// this lock the two paths race on activeExecID + lastState.
	// Store-level writes are already serialised under Store.mu so
	// the lock here is purely for the bridge's own state.
	mu sync.Mutex

	// activeExecID — per-job map of the in-flight Execution.id. Set
	// on the first transition out of StatePending; cleared when the
	// Job reaches a terminal state. Mutated under b.mu.
	activeExecID map[string]string

	// lastState — per-job last-seen helmwatch state, so the bridge
	// can suppress duplicate appends when helmwatch refires UpdateFunc
	// at the informer's resync cadence without an actual transition.
	// Mutated under b.mu.
	lastState map[string]string
}

// NewBridge returns a fresh Bridge for the given deployment id.
// store must be non-nil; deploymentID must be non-empty.
func NewBridge(store *Store, deploymentID string) *Bridge {
	return &Bridge{
		store:        store,
		deploymentID: deploymentID,
		activeExecID: map[string]string{},
		lastState:    map[string]string{},
	}
}

// SeedJobs registers the supplied jobs against the deployment in a
// pending state. Used by the catalyst-api at Phase-1 watch start to
// pre-populate the table view with rows + dependsOn before any
// HelmRelease has reconciled.
//
// Each spec carries the chart name (without the bp- prefix) and the
// list of dependent chart names (also without the bp- prefix). The
// bridge translates them to JobName + DependsOn list using the
// install-<chart> convention.
func (b *Bridge) SeedJobs(specs []SeedSpec) error {
	now := time.Now().UTC()
	for _, sp := range specs {
		j := Job{
			DeploymentID: b.deploymentID,
			JobName:      JobNamePrefix + sp.Chart,
			AppID:        sp.Chart,
			BatchID:      BatchBootstrapKit,
			DependsOn:    dependsOnFromCharts(sp.DependsOn),
			Status:       StatusPending,
		}
		if err := b.store.UpsertJob(j); err != nil {
			return err
		}
		_ = now // reserved: future "createdAt" field, intentionally
		// unused for now — the wire spec in #205 doesn't include
		// it.
	}
	return nil
}

// SeedSpec is the per-Job seed input: the chart name (after the bp-
// prefix is stripped) plus the list of dependent chart names (also
// post-strip). The handler builds this list from the bootstrap-kit
// YAMLs it already reads via the helmwatch dynamic informer.
type SeedSpec struct {
	Chart     string
	DependsOn []string
}

// InformerSeed is one HelmRelease the helmwatch informer has in its
// local cache when SeedJobsFromInformerList runs. The bridge translates
// each entry into a Job (and, for terminal states, an Execution + a
// single synthetic LogLine) so a wizard that connects to the
// catalyst-api LONG after the watch terminated still sees the full
// table-view of Jobs.
//
// Spec source: helmwatch.Watcher.SnapshotComponents() walks the
// informer's local cache after HasSynced and produces one InformerSeed
// per bp-* HelmRelease. Component is the normalised id ("cilium"),
// State is the helmwatch state enum (HelmStatePending /
// HelmStateInstalling / HelmStateInstalled / HelmStateFailed /
// HelmStateDegraded), Message is the HelmRelease.status.conditions[Ready]
// message verbatim, ObservedAt is the LastTransitionTime when known
// (falls back to time.Now() inside the bridge if zero).
type InformerSeed struct {
	Component  string
	State      string
	Message    string
	ObservedAt time.Time
	// DependsOn — sibling AppIDs (bp-prefix stripped) this seed depends
	// on, sourced from the HelmRelease's spec.dependsOn[].name.
	// Translated to JobName form (install-<comp>) before being written
	// to the Job record so the Flow view's edge graph renders.
	DependsOn  []string
}

// SeedJobsFromInformerList takes a snapshot of the helmwatch informer's
// local cache (one entry per bp-* HelmRelease present at the moment
// HasSynced returns true) and writes a Job per entry — plus, for
// terminal states (succeeded | failed), a single Execution and a
// synthetic LogLine "[seeded] state=<state> at <ts>: <message>" so the
// table-view UX has a non-empty Executions list to deep-link to.
//
// Idempotency is the load-bearing property here: this method runs on
// every helmwatch start, including the resume-after-Pod-restart path
// AND the on-demand POST /refresh-watch. A second invocation with the
// SAME state for the SAME component MUST be a no-op (no duplicate
// Execution rows, no duplicate LogLine entries). Idempotency is
// enforced by:
//
//   - UpsertJob's monotonic merge in store.mergeJob — re-emitting an
//     existing terminal Job preserves its StartedAt/FinishedAt and does
//     not create a second Execution.
//   - The "skip when LatestExecutionID is already set" guard below —
//     the synthetic Execution + LogLine pair is allocated at most once
//     per (deployment, component) tuple.
//   - For non-terminal states (pending / installing) the method is a
//     plain UpsertJob (status reflected, no Execution allocated yet).
//     Subsequent transitions through OnHelmReleaseEvent allocate the
//     Execution at the first non-pending edge.
//
// Returns the count of (Jobs written, terminal Executions newly seeded)
// so the handler can log a one-line summary.
func (b *Bridge) SeedJobsFromInformerList(seeds []InformerSeed) (jobsWritten, executionsSeeded int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, s := range seeds {
		comp := strings.TrimSpace(s.Component)
		if comp == "" {
			continue
		}
		jobName := JobNamePrefix + comp
		jobID := JobID(b.deploymentID, jobName)

		// Reflect the cluster-current state into a Job row. The
		// translation matches OnHelmReleaseEvent so the seed and the
		// ongoing event stream agree on Status semantics — a
		// HelmStateInstalled HR observed during initial-list seeds a
		// Status=succeeded Job, exactly as if a transition had been
		// emitted.
		nextStatus := jobStatusFromHelmState(s.State)
		// Translate sibling AppIDs into the JobName form the Flow
		// view's edge graph keys off ("cilium" → "install-cilium").
		// This is the load-bearing line for issue #204's pipeline
		// view: without dependsOn, no edges render between Job rows.
		deps := make([]string, 0, len(s.DependsOn))
		for _, d := range s.DependsOn {
			d = strings.TrimSpace(d)
			if d == "" {
				continue
			}
			deps = append(deps, JobNamePrefix+d)
		}
		if err := b.store.UpsertJob(Job{
			DeploymentID: b.deploymentID,
			JobName:      jobName,
			AppID:        comp,
			BatchID:      BatchBootstrapKit,
			DependsOn:    deps,
			Status:       nextStatus,
		}); err != nil {
			return jobsWritten, executionsSeeded, err
		}
		jobsWritten++

		// Mirror the in-memory cursor so a follow-up
		// OnHelmReleaseEvent(comp, sameState, ...) is suppressed by
		// lastState — the seed is the canonical "first observation"
		// for every component it covers.
		b.lastState[comp] = s.State

		// "Pending" seeds remain Job-only: helm-controller has not yet
		// started reconciling the HR, so allocating an Execution would
		// leave an empty NDJSON file and confuse the GitLab-CI viewer.
		// The next transition fires through OnHelmReleaseEvent which
		// allocates the Execution at the pending → non-pending edge.
		if s.State == HelmStatePending {
			continue
		}

		// installing / degraded / installed / failed all need a
		// non-empty Execution so the FE log viewer has a row to
		// deep-link to. Idempotency: if the persisted Job already has
		// a LatestExecutionID, reuse it as the active cursor — the
		// terminal-finish branch below is gated on a fresh allocation.
		job, _, getErr := b.store.GetJob(b.deploymentID, jobID)
		if getErr == nil && job.LatestExecutionID != "" {
			// Already has an Execution from a prior seed or from a
			// transition emitted earlier in the same Pod's life. For
			// non-terminal states we keep the cursor live so subsequent
			// raw-log forwards land on the same Execution; for terminal
			// states we clear the cursor (the Execution has already
			// been finished by the prior call).
			if isTerminalHelmState(s.State) {
				b.activeExecID[comp] = ""
			} else {
				b.activeExecID[comp] = job.LatestExecutionID
			}
			continue
		}

		t := s.ObservedAt
		if t.IsZero() {
			t = time.Now().UTC()
		} else {
			t = t.UTC()
		}

		exec, startErr := b.store.StartExecution(b.deploymentID, jobName, t)
		if startErr != nil {
			return jobsWritten, executionsSeeded, startErr
		}
		executionsSeeded++

		// Synthetic anchor line so the viewer renders a non-empty
		// Executions tab even when no helm-controller log lines have
		// been forwarded yet. The raw lines from the logtailer append
		// after this anchor as they arrive.
		message := strings.TrimSpace(s.Message)
		line := "[seeded] state=" + s.State + " at " + t.Format(time.RFC3339)
		if message != "" {
			line = line + ": " + message
		}
		level := LevelInfo
		if s.State == HelmStateFailed {
			level = LevelError
		} else if s.State == HelmStateDegraded {
			level = LevelWarn
		}
		if appendErr := b.store.AppendLogLines(b.deploymentID, exec.ID, []LogLine{{
			Timestamp: t,
			Level:     level,
			Message:   line,
		}}); appendErr != nil {
			return jobsWritten, executionsSeeded, appendErr
		}

		// Terminal seeds: stamp Execution + Job with the terminal
		// status. Non-terminal seeds (installing / degraded) leave
		// the Execution open so OnHelmReleaseEvent + OnRawComponentLog
		// can keep appending until the HR reaches a terminal state.
		if isTerminalHelmState(s.State) {
			final := StatusSucceeded
			if s.State == HelmStateFailed {
				final = StatusFailed
			}
			if finishErr := b.store.FinishExecution(b.deploymentID, exec.ID, final, t); finishErr != nil {
				return jobsWritten, executionsSeeded, finishErr
			}
			b.activeExecID[comp] = ""
		} else {
			// Keep the cursor pointing at the open Execution so
			// downstream raw-log lines append here. OnHelmReleaseEvent
			// will close it when the terminal transition arrives.
			b.activeExecID[comp] = exec.ID
		}
	}
	return jobsWritten, executionsSeeded, nil
}

// isTerminalHelmState reports whether a helmwatch state string maps
// onto a terminal Job status (succeeded | failed). Pulled out so the
// seed path and OnHelmReleaseEvent agree on the boundary.
func isTerminalHelmState(state string) bool {
	switch state {
	case HelmStateInstalled, HelmStateFailed:
		return true
	}
	return false
}

// OnHelmReleaseEvent is the single entry point the helmwatch
// consumer calls. componentID is helmwatch.ComponentIDFromHelmRelease
// (the chart name with bp- stripped); state is one of the HelmState*
// constants; level + message map onto the LogLine. Time is the wall-
// clock instant of the event — the LogLine inherits it directly.
//
// The function is a no-op when state == previous state for the same
// componentID — helmwatch's UpdateFunc fires on every status sub-
// resource patch, including helm-controller's own observedGeneration
// touches, and we don't want to persist a fresh LogLine for each.
//
// The bridge tolerates store errors (returns them) but does not abort
// the helmwatch event loop — the handler's emit path treats this as
// a non-fatal best-effort write.
func (b *Bridge) OnHelmReleaseEvent(componentID, state, level, message string, t time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	jobName := JobNamePrefix + componentID

	prev := b.lastState[componentID]
	if prev == state {
		return nil
	}
	b.lastState[componentID] = state

	// Ensure the Job row exists. If SeedJobs was called this is a
	// no-op merge; if it wasn't (e.g. the bootstrap-kit hot-shipped a
	// new chart helmwatch wasn't seeded with) the bridge still
	// auto-creates a row so no event is dropped.
	if err := b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      jobName,
		AppID:        componentID,
		BatchID:      BatchBootstrapKit,
		DependsOn:    []string{},
		Status:       jobStatusFromHelmState(state),
	}); err != nil {
		return err
	}

	// Allocate an Execution if the Job has just become non-pending and
	// no Execution is active.
	execID := b.activeExecID[componentID]
	if execID == "" && state != HelmStatePending {
		exec, err := b.store.StartExecution(b.deploymentID, jobName, t)
		if err != nil {
			return err
		}
		execID = exec.ID
		b.activeExecID[componentID] = execID
	}

	// Append a LogLine for every observed transition. The bridge
	// drops the line if there is no active execution (the Job is
	// still pending) — the table view's "started" column is
	// authoritative for pending rows; a LogLine without an Execution
	// has no display surface.
	if execID != "" {
		ll := LogLine{
			Timestamp: t.UTC(),
			Level:     mapLevel(level),
			Message:   buildLogMessage(componentID, state, message),
		}
		if err := b.store.AppendLogLines(b.deploymentID, execID, []LogLine{ll}); err != nil {
			return err
		}
	}

	// Terminal-state Job: finish the Execution + clear the cursor so
	// a future re-run (Day-2 retry) gets a fresh Execution row.
	if execID != "" && (state == HelmStateInstalled || state == HelmStateFailed) {
		final := StatusSucceeded
		if state == HelmStateFailed {
			final = StatusFailed
		}
		if err := b.store.FinishExecution(b.deploymentID, execID, final, t); err != nil {
			return err
		}
		delete(b.activeExecID, componentID)
	}

	return nil
}

// OnProvisionerEvent is the adapter the handler's emit path calls with
// every provisioner.Event the helmwatch.Watcher emits. Two phase
// classes have a Job/Execution analogue:
//
//   - Phase=="component"     — HelmRelease state transition. Routes to
//     OnHelmReleaseEvent which upserts the Job, allocates an Execution
//     on the first non-pending edge, and closes the Execution on
//     terminal transitions.
//   - Phase=="component-log" — raw helm-controller log line tagged
//     with the bp-* HR it relates to. Routes to OnRawComponentLog
//     which appends the line verbatim to the active Execution so the
//     GitLab-CI-style viewer renders the full helm-controller stdout
//     for the install attempt.
//
// Phase-0 OpenTofu events ("phase-0", "tofu-init", etc.) have no
// component-Job analogue and fall through silently.
func (b *Bridge) OnProvisionerEvent(ev provisioner.Event) error {
	if ev.Component == "" {
		return nil
	}
	t := parseEventTime(ev.Time)
	switch ev.Phase {
	case phaseComponent:
		if ev.State == "" {
			return nil
		}
		return b.OnHelmReleaseEvent(ev.Component, ev.State, ev.Level, ev.Message, t)
	case phaseComponentLog:
		return b.OnRawComponentLog(ev.Component, ev.Level, ev.Message, t)
	}
	return nil
}

// OnRawComponentLog appends a single raw helm-controller log line to
// the active Execution for the given component. The line is the
// helmwatch logtailer's stdout payload verbatim — typically a logr
// text or structured-JSON record from helm-controller pinned to the
// matching `helmrelease="flux-system/bp-<name>"` token.
//
// Resolution policy when no active Execution is recorded for the
// component:
//
//   1. If the persisted Job has a non-empty LatestExecutionID AND that
//      Execution is still running, the line lands there. Covers the
//      "Pod restart wiped the in-memory cursor" path.
//   2. If the persisted Job is non-terminal but has no Execution yet
//      (e.g. seed wrote a Job-only pending row), allocate a fresh
//      Execution on the fly so no helm-controller line is dropped.
//   3. If the Job is in a terminal state OR doesn't exist, the line
//      is dropped — helm-controller emits maintenance lines after the
//      install completes (drift checks, observed-generation patches)
//      that should not extend a closed Execution.
//
// The bridge tolerates store errors (returns them) but does not abort
// the helmwatch event loop — the handler's emit path treats this as
// a non-fatal best-effort write.
func (b *Bridge) OnRawComponentLog(componentID, level, message string, t time.Time) error {
	if strings.TrimSpace(componentID) == "" {
		return nil
	}
	message = strings.TrimRight(message, "\r\n")
	if message == "" {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	jobName := JobNamePrefix + componentID
	jobID := JobID(b.deploymentID, jobName)

	execID := b.activeExecID[componentID]
	if execID == "" {
		// Cursor missing — fall back to the persisted Job's state.
		job, _, getErr := b.store.GetJob(b.deploymentID, jobID)
		switch {
		case getErr != nil:
			// No persisted Job (yet). Allocate the Job + Execution so
			// the helm-controller line is captured. State is
			// approximated as "running" since something is logging
			// about it; the next OnHelmReleaseEvent fixes the canonical
			// status.
			if err := b.store.UpsertJob(Job{
				DeploymentID: b.deploymentID,
				JobName:      jobName,
				AppID:        componentID,
				BatchID:      BatchBootstrapKit,
				DependsOn:    []string{},
				Status:       StatusRunning,
			}); err != nil {
				return err
			}
			exec, err := b.store.StartExecution(b.deploymentID, jobName, t)
			if err != nil {
				return err
			}
			execID = exec.ID
			b.activeExecID[componentID] = execID
			b.lastState[componentID] = HelmStateInstalling
		case job.LatestExecutionID != "" && !IsTerminal(job.Status):
			// In-flight Execution exists but the in-memory cursor was
			// lost (Pod restart). Re-attach.
			execID = job.LatestExecutionID
			b.activeExecID[componentID] = execID
		case IsTerminal(job.Status):
			// Job has completed — drop late helm-controller chatter.
			return nil
		default:
			// Job exists but is pending and has no Execution. Allocate.
			exec, err := b.store.StartExecution(b.deploymentID, jobName, t)
			if err != nil {
				return err
			}
			execID = exec.ID
			b.activeExecID[componentID] = execID
			if err := b.store.UpsertJob(Job{
				DeploymentID: b.deploymentID,
				JobName:      jobName,
				AppID:        componentID,
				BatchID:      BatchBootstrapKit,
				DependsOn:    job.DependsOn,
				Status:       StatusRunning,
			}); err != nil {
				return err
			}
			b.lastState[componentID] = HelmStateInstalling
		}
	}

	return b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
		Timestamp: t.UTC(),
		Level:     mapLevel(level),
		Message:   message,
	}})
}

// dependsOnFromCharts converts a list of dependent chart names (e.g.
// ["cilium", "cert-manager"]) into the install-<chart> jobName form
// the wire spec expects. Empty input yields an empty (non-nil) slice
// so the JSON shape is `[]` not `null`.
func dependsOnFromCharts(charts []string) []string {
	out := make([]string, 0, len(charts))
	for _, c := range charts {
		c = strings.TrimSpace(c)
		c = strings.TrimPrefix(c, "bp-")
		if c == "" {
			continue
		}
		out = append(out, JobNamePrefix+c)
	}
	return out
}

// jobStatusFromHelmState maps helmwatch's State enum onto the Job
// Status enum. The Bridge writes this through UpsertJob on every
// event so the table view reflects current state without waiting for
// a terminal transition.
func jobStatusFromHelmState(state string) string {
	switch state {
	case HelmStateInstalled:
		return StatusSucceeded
	case HelmStateFailed:
		return StatusFailed
	case HelmStateInstalling, HelmStateDegraded:
		return StatusRunning
	case HelmStatePending:
		return StatusPending
	}
	return StatusPending
}

// mapLevel translates the helmwatch event level (info|warn|error)
// onto the LogLine wire-format level (INFO|WARN|ERROR|DEBUG). The
// wire spec is uppercase per the GitLab-CI viewer convention (#204).
func mapLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error":
		return LevelError
	case "warn", "warning":
		return LevelWarn
	case "debug":
		return LevelDebug
	default:
		return LevelInfo
	}
}

// buildLogMessage formats the LogLine.Message text. We prepend a
// "[<state>]" tag so an operator scrolling the GitLab-style viewer
// can scan transitions without horizontal scanning. The original
// helm-controller message (HelmRelease.status.conditions[Ready].
// Message) is preserved unchanged after the tag.
func buildLogMessage(componentID, state, message string) string {
	state = strings.TrimSpace(state)
	message = strings.TrimSpace(message)
	if state == "" {
		return message
	}
	if message == "" {
		return "[" + state + "] " + componentID
	}
	return "[" + state + "] " + message
}

// parseEventTime parses the RFC3339 timestamp helmwatch stamps onto
// every Event. A bad parse falls back to time.Now() so a malformed
// timestamp doesn't drop the LogLine.
func parseEventTime(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339Nano, s)
	}
	if err != nil {
		return time.Now().UTC()
	}
	return t.UTC()
}
