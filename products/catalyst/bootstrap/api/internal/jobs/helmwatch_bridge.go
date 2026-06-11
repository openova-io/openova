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
	// Phase-0 stdout/stderr stream tag. provisioner.go's runTofu uses
	// Phase="tofu" for every line of tofu's stdout/stderr; the bridge
	// appends them to the currently-active lifecycle phase's Execution
	// so /jobs/tofu-<step> exec logs render the full transcript.
	phaseTofuStream = "tofu"
	// Legacy alias the provisioner emits at the end of Phase-0; treated
	// as cluster-bootstrap (it's the human-readable tail of the phase).
	phaseFluxBootstrap = "flux-bootstrap"
)

// provisionerLifecyclePhases — ordered Phase-0 jobs the bridge
// pre-seeds + transitions through. The order is the canonical
// dependency chain (init → plan → apply → output → cluster-bootstrap)
// and feeds DependsOn for graph rendering.
var provisionerLifecyclePhases = []string{
	PhaseTofuInit,
	PhaseTofuPlan,
	PhaseTofuApply,
	PhaseTofuOutput,
	PhaseClusterBootstrap,
}

// provisionerLifecycleDisplay — user-visible display names per phase.
var provisionerLifecycleDisplay = map[string]string{
	PhaseTofuInit:         "Terraform Init",
	PhaseTofuPlan:         "Terraform Plan",
	PhaseTofuApply:        "Terraform Apply",
	PhaseTofuOutput:       "Terraform Output",
	PhaseClusterBootstrap: "Bootstrap Cluster",
}

// lifecyclePhaseIndex returns the position of phase in
// provisionerLifecyclePhases, or -1 if unknown. The legacy
// "flux-bootstrap" emit alias maps onto the cluster-bootstrap slot.
func lifecyclePhaseIndex(phase string) int {
	if phase == phaseFluxBootstrap {
		phase = PhaseClusterBootstrap
	}
	for i, p := range provisionerLifecyclePhases {
		if p == phase {
			return i
		}
	}
	return -1
}

// dependsOnForLifecyclePhase returns the previous-phase Job ID list
// (length 0 for the first phase, length 1 thereafter) so the graph
// view's edges render the linear chain.
func dependsOnForLifecyclePhase(deploymentID, phase string) []string {
	idx := lifecyclePhaseIndex(phase)
	if idx <= 0 {
		return []string{}
	}
	return []string{JobID(deploymentID, provisionerLifecyclePhases[idx-1])}
}

// lifecycleCursorKey — namespaces the in-memory activeExecID map so
// lifecycle cursors don't collide with helmwatch component cursors
// (which key off the bare component id like "cilium").
func lifecycleCursorKey(phase string) string {
	return "lifecycle:" + phase
}

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

// ensureGroupJob idempotently materialises a synthesised parent group
// Job. Called by every leaf-write path so the recursive Job tree is
// always rooted in a real on-disk row — Store.GetJob(parentId) must
// resolve. UpsertJob's merge is idempotent: a second call with the
// same row preserves prior StartedAt/FinishedAt and is effectively a
// no-op for an already-materialised group.
//
// Group jobs persist a placeholder Status (StatusPending); the runtime
// status is derived from descendants by Store.deriveTreeView at read
// time, so the persisted value never reaches the wire.
func (b *Bridge) ensureGroupJob(slug, displayName string) error {
	return b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      slug,
		DisplayName:  displayName,
		Type:         JobTypeGroup,
		ParentID:     "",
		DependsOn:    []string{},
		Status:       StatusPending,
	})
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
	if len(specs) > 0 {
		if err := b.ensureGroupJob(GroupBootstrapKit, GroupBootstrapKitDisplay); err != nil {
			return err
		}
	}
	for _, sp := range specs {
		j := Job{
			DeploymentID: b.deploymentID,
			JobName:      JobNamePrefix + sp.Chart,
			AppID:        sp.Chart,
			Type:         JobTypeInstall,
			ParentID:     JobID(b.deploymentID, GroupBootstrapKit),
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

	if len(seeds) > 0 {
		if gErr := b.ensureGroupJob(GroupBootstrapKit, GroupBootstrapKitDisplay); gErr != nil {
			return 0, 0, gErr
		}
	}

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
			Type:         JobTypeInstall,
			ParentID:     JobID(b.deploymentID, GroupBootstrapKit),
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
			// states we clear the cursor.
			//
			// Stale-state class bug (TBD-B6/B7, t131 sin-2 install Jobs
			// stuck "running"): a Pod restart between OnHelmReleaseEvent
			// allocating an Execution (non-terminal) and the HR reaching
			// Ready=True wipes activeExecID + lastState but leaves the
			// prior Execution in StatusRunning on disk. On resume, the
			// seed observes the HR's terminal state and lands here. Prior
			// to this fix the branch ONLY cleared the cursor — the
			// orphan Execution was never FinishExecution'd, so its row
			// stayed `running` forever (and Job.FinishedAt / DurationMs
			// stayed nil/0 because the UpsertJob above does not stamp
			// finishedAt). FinishExecution is the canonical terminal
			// transition: it stamps Execution.Status + FinishedAt AND
			// Job.Status + Job.FinishedAt + DurationMs in one call. We
			// check the prior Execution's current status before calling
			// so an already-finished Execution is left alone (idempotency
			// for the seed-twice path covered by
			// TestSeedJobsFromInformerList_idempotent).
			if isTerminalHelmState(s.State) {
				priorExec, findErr := b.store.FindExecution(b.deploymentID, job.LatestExecutionID)
				if findErr == nil && !IsTerminal(priorExec.Status) {
					final := StatusSucceeded
					if s.State == HelmStateFailed {
						final = StatusFailed
					}
					t := s.ObservedAt
					if t.IsZero() {
						t = time.Now().UTC()
					} else {
						t = t.UTC()
					}
					if finishErr := b.store.FinishExecution(b.deploymentID, job.LatestExecutionID, final, t); finishErr != nil {
						return jobsWritten, executionsSeeded, finishErr
					}
				}
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
//
// dependsOn carries the HelmRelease's spec.dependsOn[].name list (or
// nil/empty when the watcher couldn't extract it). Every event writes
// dependsOn through to the Job row via UpsertJob — mergeJob preserves
// a non-empty prior value if the current event arrives empty, so the
// seed's prior writes are never erased. Without this plumbing, the
// bridge was always writing DependsOn=[]string{} and relying on a
// pre-existing seed to have populated deps — which failed on every
// fresh provision because the seed ran on an empty initial-list
// snapshot.
func (b *Bridge) OnHelmReleaseEvent(componentID, state, level, message string, t time.Time, dependsOn []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	jobName := JobNamePrefix + componentID

	prev := b.lastState[componentID]
	if prev == state {
		return nil
	}
	b.lastState[componentID] = state

	// Canonicalise dependsOn so downstream UpsertJob mergeJob treats
	// the value consistently — nil and []string{} both serialise to
	// the same JSON but mergeJob compares by length, so prefer an
	// empty slice over nil for predictable behaviour.
	if dependsOn == nil {
		dependsOn = []string{}
	}

	// Prepend the canonical JobNamePrefix ("install-") to every dep so
	// the stored Job.DependsOn matches the format other Jobs use as
	// their JobName. Without this, the openova-flow snapshot composer
	// emits finish-to-start relationships with fromIds like
	// "<dep>:hel1-2:seaweedfs" (missing "install-"), which match no
	// FlowNode and the canvas renders every sibling-edge invisible.
	// Caught on prov t103.omani.works (005080699326a7ac, 2026-05-15):
	// 224 finish-to-start rels emitted, every fromId malformed.
	//
	// Three input shapes the watcher may emit:
	//   "cilium"               (primary, bare chart)        → "install-cilium"
	//   "hel1-2:cilium"        (secondary, region-prefixed) → "install-hel1-2:cilium"
	//   "install-hel1-2:cilium"(already canonical, idempotent) → same
	if len(dependsOn) > 0 {
		canon := make([]string, 0, len(dependsOn))
		for _, d := range dependsOn {
			if d == "" {
				continue
			}
			if !strings.HasPrefix(d, JobNamePrefix) {
				d = JobNamePrefix + d
			}
			canon = append(canon, d)
		}
		dependsOn = canon
	}

	// Ensure the bootstrap-kit parent group exists before any leaf is
	// written underneath it (idempotent — UpsertJob's merge preserves
	// any prior row).
	if err := b.ensureGroupJob(GroupBootstrapKit, GroupBootstrapKitDisplay); err != nil {
		return err
	}

	// Status mapping with monotonic guard: once a Job has an active
	// Execution (i.e. helmwatch ever observed a non-pending state for
	// this component), DO NOT regress back to "pending" if the next
	// event maps to HelmStatePending. Flux retries reconciliation while
	// dependencies aren't ready, oscillating the HR's Ready condition
	// between DependencyNotReady ("pending") and Reconciling
	// ("installing"). Without the guard the Jobs page flips between
	// "pending" and "running" while the Exec Log accumulates lines —
	// the founder caught this on otech34 (install-external-secrets:
	// status=pending despite startedAt + latestExecutionId set, blocked
	// on bp-openbao). Treat ongoing-after-start as Running so the row
	// reflects the live exec stream.
	nextStatus := jobStatusFromHelmState(state)
	if nextStatus == StatusPending && b.activeExecID[componentID] != "" {
		nextStatus = StatusRunning
	}

	// Ensure the Job row exists. If SeedJobs was called this is a
	// no-op merge; if it wasn't (e.g. the bootstrap-kit hot-shipped a
	// new chart helmwatch wasn't seeded with) the bridge still
	// auto-creates a row so no event is dropped.
	//
	// IMPORTANT: explicitly carry forward the existing Job's DependsOn
	// rather than passing []string{}. The canvas snapshot's Layer-1
	// dep-edge derivation reads Job.DependsOn from the store, and the
	// catalyst-api Pod's in-memory hrDeps cache is RESET on every
	// restart — so the persisted DependsOn is the only durable source
	// of HR-graph wiring once the live informer is gone. Relying on
	// mergeJob's prev.DependsOn preservation is correct on paper but
	// fragile in practice (caught on prov #75 2026-05-14 after a PR
	// #1469 pod restart wiped Layer-2 → all 135 install Jobs ended up
	// with `dependsOn: []` in the store → canvas showed flat fan-out).
	// Same pattern as OnRawComponentLog at line ~939: query then
	// preserve.
	// Resolve DependsOn with a 3-tier preference: prior store value >
	// event-carried (live HR spec.dependsOn) > empty. The prior-store
	// branch handles pod restarts (per the comment above). The
	// event-carried branch fires on FRESH provisions where the seed
	// observed an empty initial-list snapshot but subsequent events
	// carry the now-installed HR's spec.dependsOn (PR XXXX,
	// 2026-05-15).
	resolvedDeps := []string{}
	if existing, _, err := b.store.GetJob(b.deploymentID, JobID(b.deploymentID, jobName)); err == nil && len(existing.DependsOn) > 0 {
		resolvedDeps = existing.DependsOn
	} else if len(dependsOn) > 0 {
		resolvedDeps = dependsOn
	}
	// Also canonicalise any malformed entries inherited from prior
	// writes (pre-PR-1500 stored "hel1-2:gitea" without "install-"
	// prefix; the snapshot composer emits a FromId that matches no
	// FlowNode, edge invisible). The canon block above only ran on
	// the event-carried dependsOn arg; this final pass guarantees
	// the persisted Job.DependsOn is canonical regardless of where
	// the entries came from.
	if len(resolvedDeps) > 0 {
		canon := make([]string, 0, len(resolvedDeps))
		for _, d := range resolvedDeps {
			if d == "" {
				continue
			}
			if !strings.HasPrefix(d, JobNamePrefix) {
				d = JobNamePrefix + d
			}
			canon = append(canon, d)
		}
		resolvedDeps = canon
	}
	if err := b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      jobName,
		AppID:        componentID,
		Type:         JobTypeInstall,
		ParentID:     JobID(b.deploymentID, GroupBootstrapKit),
		DependsOn:    resolvedDeps,
		Status:       nextStatus,
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
// every provisioner.Event. Three phase classes have a Job/Execution
// analogue:
//
//   - Phase=="component"           — HelmRelease state transition.
//     Routes to OnHelmReleaseEvent which upserts the Job, allocates an
//     Execution on the first non-pending edge, and closes the Execution
//     on terminal transitions.
//   - Phase=="component-log"       — raw helm-controller log line
//     tagged with the bp-* HR it relates to. Routes to
//     OnRawComponentLog which appends the line verbatim to the active
//     Execution.
//   - Phase=="tofu-init|tofu-plan|tofu-apply|tofu-output|
//     cluster-bootstrap|flux-bootstrap" — Phase-0 lifecycle markers
//     emitted by the OpenTofu provisioner. Routes to
//     OnProvisionerLifecycleEvent which transitions the corresponding
//     pre-seeded Job into Running, marks earlier phases Succeeded, and
//     allocates an Execution so the per-phase Exec Log surface
//     resolves.
//   - Phase=="tofu"                — raw stdout/stderr line from the
//     in-flight tofu command. Routes to OnProvisionerStreamLog which
//     appends to the currently-active lifecycle phase's Execution.
func (b *Bridge) OnProvisionerEvent(ev provisioner.Event) error {
	t := parseEventTime(ev.Time)
	switch ev.Phase {
	case phaseComponent:
		if ev.Component == "" || ev.State == "" {
			return nil
		}
		return b.OnHelmReleaseEvent(ev.Component, ev.State, ev.Level, ev.Message, t, ev.DependsOn)
	case phaseComponentLog:
		if ev.Component == "" {
			return nil
		}
		return b.OnRawComponentLog(ev.Component, ev.Level, ev.Message, t)
	case PhaseTofuInit, PhaseTofuPlan, PhaseTofuApply, PhaseTofuOutput,
		PhaseClusterBootstrap, phaseFluxBootstrap:
		return b.OnProvisionerLifecycleEvent(ev.Phase, ev.Level, ev.Message, t)
	case phaseTofuStream:
		return b.OnProvisionerStreamLog(ev.Level, ev.Message, t)
	}
	return nil
}

// SeedProvisionerJobs registers the 5 Phase-0 lifecycle Jobs (under the
// "provisioner" parent group) in a pending state so deep links to
// /jobs/tofu-<step> resolve from the very first wizard render, and
// the JobsTable never drops the rows when bootstrap-kit children
// arrive later. Idempotent — UpsertJob's mergeJob preserves
// monotonic timestamps and any prior status.
func (b *Bridge) SeedProvisionerJobs() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureGroupJob(GroupProvisioner, GroupProvisionerDisplay); err != nil {
		return err
	}
	parent := JobID(b.deploymentID, GroupProvisioner)
	for _, phase := range provisionerLifecyclePhases {
		if err := b.store.UpsertJob(Job{
			DeploymentID: b.deploymentID,
			JobName:      phase,
			DisplayName:  provisionerLifecycleDisplay[phase],
			Type:         JobTypeInstall,
			ParentID:     parent,
			DependsOn:    dependsOnForLifecyclePhase(b.deploymentID, phase),
			Status:       StatusPending,
		}); err != nil {
			return err
		}
	}
	return nil
}

// OnProvisionerLifecycleEvent transitions the named Phase-0 phase
// into Running (allocating an Execution + StartedAt on the first
// edge), marks every earlier phase Succeeded if not already terminal,
// and appends an INFO LogLine for the human-readable phase header
// the provisioner emits. Idempotent — re-emitting the same phase is
// a no-op once it's in Running.
func (b *Bridge) OnProvisionerLifecycleEvent(phase, level, message string, t time.Time) error {
	idx := lifecyclePhaseIndex(phase)
	if idx < 0 {
		return nil
	}
	canonical := provisionerLifecyclePhases[idx]

	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.ensureGroupJob(GroupProvisioner, GroupProvisionerDisplay); err != nil {
		return err
	}

	// Lazy-seed every lifecycle Job (idempotent — UpsertJob's
	// mergeJob preserves any prior status). This is the "no job
	// should disappear" guard: once a single Phase-0 emit lands, the
	// JobsTable shows all 5 lifecycle rows regardless of which phase
	// fired first or whether SeedProvisionerJobs ran.
	for _, phase := range provisionerLifecyclePhases {
		if err := b.ensureLifecycleJobLocked(phase); err != nil {
			return err
		}
	}

	// Promote earlier phases to Succeeded — once a later phase has
	// emitted, every prior step has provably finished. The sentinel
	// "flux-bootstrap" emit lands at the END of Phase-0 so the four
	// tofu phases all roll up cleanly.
	for i := 0; i < idx; i++ {
		if err := b.markLifecyclePhaseTerminalLocked(provisionerLifecyclePhases[i], StatusSucceeded, t, ""); err != nil {
			return err
		}
	}

	// Allocate a fresh Execution if the cursor is empty for this
	// phase. StartExecution stamps the Job's StartedAt + Status=running
	// + LatestExecutionID inside the same persistIndex.
	cursor := lifecycleCursorKey(canonical)
	execID := b.activeExecID[cursor]
	if execID == "" {
		exec, err := b.store.StartExecution(b.deploymentID, canonical, t)
		if err != nil {
			return err
		}
		execID = exec.ID
		b.activeExecID[cursor] = execID
	}

	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "[" + canonical + "] starting"
	}
	return b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
		Timestamp: t.UTC(),
		Level:     mapLevel(level),
		Message:   msg,
	}})
}

// OnProvisionerStreamLog appends a raw "tofu" stdout/stderr line to
// the currently-active lifecycle phase's Execution. The stream tag
// ("tofu") covers every line of init/plan/apply/output stdout +
// stderr — we route to whichever phase is currently in flight by
// scanning the cursor map newest-first.
func (b *Bridge) OnProvisionerStreamLog(level, message string, t time.Time) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	for i := len(provisionerLifecyclePhases) - 1; i >= 0; i-- {
		execID := b.activeExecID[lifecycleCursorKey(provisionerLifecyclePhases[i])]
		if execID != "" {
			return b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
				Timestamp: t.UTC(),
				Level:     mapLevel(level),
				Message:   message,
			}})
		}
	}
	// No active lifecycle execution — drop the line. The provisioner
	// only emits "tofu" streams between an init/plan/apply marker and
	// the next phase marker, so this branch is unexpected; logging it
	// would risk amplifying noise.
	return nil
}

// MarkProvisionerComplete is called from runProvisioning after
// prov.Provision returns successfully. Every lifecycle phase that
// hasn't reached terminal yet is stamped Succeeded — the cluster
// bootstrap is the last emit before Provision returns, and Phase-1
// HR events take over the Job feed thereafter.
func (b *Bridge) MarkProvisionerComplete(t time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, phase := range provisionerLifecyclePhases {
		if err := b.markLifecyclePhaseTerminalLocked(phase, StatusSucceeded, t, ""); err != nil {
			return err
		}
	}
	return nil
}

// MarkProvisionerFailed is called from runProvisioning after
// prov.Provision returns an error. The most-recent non-terminal phase
// is stamped Failed (with the error message captured as a final
// LogLine); earlier phases are stamped Succeeded since their
// emission is the proof of completion.
func (b *Bridge) MarkProvisionerFailed(message string, t time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	failedSet := false
	for i := len(provisionerLifecyclePhases) - 1; i >= 0; i-- {
		phase := provisionerLifecyclePhases[i]
		jobID := JobID(b.deploymentID, phase)
		job, _, err := b.store.GetJob(b.deploymentID, jobID)
		if err != nil {
			continue
		}
		if IsTerminal(job.Status) {
			continue
		}
		if !failedSet {
			if err := b.markLifecyclePhaseTerminalLocked(phase, StatusFailed, t, message); err != nil {
				return err
			}
			failedSet = true
			continue
		}
		// Earlier non-terminal phases — their emission is implied by
		// the failed phase being downstream of them, but the failed
		// phase MAY have started without prior phases ever entering
		// running (e.g. validation failure pre tofu-init). Leave them
		// Pending in that case so the FE renders accurately.
	}
	return nil
}

// SweepHandoverInstallJobs stamps every still-reconciling bp-* install
// Job row terminal at a SUCCESSFUL Phase-1 handover (issue #3277).
//
// Why this exists: the mother's Phase-1 helmwatch derives one Job row
// per HelmRelease from live HR state, but at watch-end nothing swept
// the rows whose last-observed state was non-terminal. When the watch
// terminates by OutcomeReady, an individual install-* row can still be
// frozen at "running"/"pending" in the snapshot — informer-dedup loss
// (the seed observed the HR already at its terminal state, so no
// distinct transition event fired), a Pod restart that wiped the
// in-memory cursor before the terminal edge, or a secondary-region
// watcher whose stream stalled. The mother stops watching after
// handover, so that row's "running" never resolves and the operator's
// bootstrap view reads "stuck running forever".
//
// At a successful handover the Sovereign is now self-managing every
// component, so the mother's bootstrap view should read complete — not
// stuck running. This sweep walks every leaf install Job (Type ==
// JobTypeInstall named "install-<chart>") and stamps each NON-TERMINAL
// row Succeeded, finishing any open Execution and appending an honest
// hand-over LogLine so the record never claims silent install-success
// without explaining that the mother has handed the component off.
//
// Correctness guards:
//   - ONLY call this on a SUCCESSFUL handover (the caller gates on
//     helmwatch.OutcomeReady). A FAILED Phase-1 must keep its failed
//     rows visible — never call this for a failure.
//   - Rows already Failed are LEFT UNTOUCHED — a real per-component
//     failure is never masked as handed-over, even at OutcomeReady
//     (OutcomeReady is all-installed by definition, but the guard makes
//     the invariant explicit and defends a future caller).
//   - Rows already Succeeded are a no-op (IsTerminal short-circuit).
//   - Group rows derive their status from descendants at read time, so
//     they're skipped — sweeping the leaves is sufficient.
//
// The Job model exposes only pending|running|succeeded|failed; there is
// no distinct "handed-over" terminal value, and the FE JobStatus union
// is the same four buckets — so the truth about hand-over lives in the
// final LogLine, not a fabricated fifth status. Mirrors
// markLifecyclePhaseTerminalLocked's locking + FinishExecution pattern.
func (b *Bridge) SweepHandoverInstallJobs(t time.Time) (swept int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	all, listErr := b.store.ListJobs(b.deploymentID)
	if listErr != nil {
		return 0, listErr
	}
	t = t.UTC()
	const handoverNote = "[handed-over] Phase-1 handover complete — the Sovereign is now self-managing this component. The mother is no longer tracking its state; see the Sovereign console for live status."
	for _, j := range all {
		// Leaf install rows only. Group rows derive status from
		// descendants at read time; lifecycle (Phase-0) rows are already
		// stamped terminal by MarkProvisionerComplete before Phase-1.
		if j.Type != JobTypeInstall {
			continue
		}
		if !strings.HasPrefix(j.JobName, JobNamePrefix) {
			continue
		}
		// Never touch a terminal row: an already-Succeeded row is done,
		// and an already-Failed row must keep surfacing its failure — a
		// real per-component failure is never masked as handed-over.
		if IsTerminal(j.Status) {
			continue
		}

		// Finish any open Execution on this Job so the GitLab-CI viewer
		// shows the row terminal too. Reuse the persisted
		// LatestExecutionID; allocate one retroactively if the row never
		// started an Execution (a row stuck "pending" with no attempt),
		// matching markLifecyclePhaseTerminalLocked so the JobsTable
		// never renders an em-dash duration cell.
		execID := j.LatestExecutionID
		if execID == "" {
			exec, startErr := b.store.StartExecution(b.deploymentID, j.JobName, t)
			if startErr != nil {
				return swept, startErr
			}
			execID = exec.ID
		}
		if appendErr := b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
			Timestamp: t,
			Level:     LevelInfo,
			Message:   handoverNote,
		}}); appendErr != nil {
			return swept, appendErr
		}
		// FinishExecution is idempotent on an already-terminal Execution's
		// status and is the canonical terminal transition — it stamps the
		// Execution AND the Job (Status + FinishedAt + DurationMs) in one
		// call, converging a Job row left non-terminal even when its
		// Execution already finished.
		if finishErr := b.store.FinishExecution(b.deploymentID, execID, StatusSucceeded, t); finishErr != nil {
			return swept, finishErr
		}
		// Clear any live cursor so a late raw-log line doesn't re-open
		// the Execution. comp is the AppID (bare chart) the cursor keys
		// off; fall back to stripping the install- prefix.
		comp := j.AppID
		if comp == "" {
			comp = strings.TrimPrefix(j.JobName, JobNamePrefix)
		}
		delete(b.activeExecID, comp)
		swept++
	}
	return swept, nil
}

// ensureLifecycleJobLocked idempotently materialises the durable Job
// row for a single Phase-0 lifecycle phase. Used by paths that may
// run before SeedProvisionerJobs (e.g. a fast emit, a test that
// constructs the Bridge without calling Seed). Caller MUST hold b.mu.
func (b *Bridge) ensureLifecycleJobLocked(phase string) error {
	if lifecyclePhaseIndex(phase) < 0 {
		return nil
	}
	return b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      phase,
		DisplayName:  provisionerLifecycleDisplay[phase],
		Type:         JobTypeInstall,
		ParentID:     JobID(b.deploymentID, GroupProvisioner),
		DependsOn:    dependsOnForLifecyclePhase(b.deploymentID, phase),
		Status:       StatusPending,
	})
}

// markLifecyclePhaseTerminalLocked stamps a single lifecycle phase as
// terminal (Succeeded or Failed). If the phase has an active
// Execution it is finished with the same status; an optional final
// LogLine is appended (used by MarkProvisionerFailed to capture the
// error). If the phase had no Execution yet (was still Pending), one
// is allocated retroactively so the Exec Log surface has at least
// one line — the JobsTable never renders an `—` duration cell.
//
// Caller MUST hold b.mu.
func (b *Bridge) markLifecyclePhaseTerminalLocked(phase, status string, t time.Time, finalMessage string) error {
	jobID := JobID(b.deploymentID, phase)
	job, _, err := b.store.GetJob(b.deploymentID, jobID)
	if err != nil {
		return nil // unknown phase — bridge is stateless about phases it didn't seed
	}
	if IsTerminal(job.Status) {
		return nil
	}

	cursor := lifecycleCursorKey(phase)
	execID := b.activeExecID[cursor]
	if execID == "" && job.LatestExecutionID != "" {
		execID = job.LatestExecutionID
	}
	if execID == "" {
		exec, startErr := b.store.StartExecution(b.deploymentID, phase, t)
		if startErr != nil {
			return startErr
		}
		execID = exec.ID
	}

	if msg := strings.TrimSpace(finalMessage); msg != "" {
		level := LevelInfo
		if status == StatusFailed {
			level = LevelError
		}
		_ = b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
			Timestamp: t.UTC(),
			Level:     level,
			Message:   msg,
		}})
	}

	if err := b.store.FinishExecution(b.deploymentID, execID, status, t); err != nil {
		return err
	}
	b.activeExecID[cursor] = ""
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
			if err := b.ensureGroupJob(GroupBootstrapKit, GroupBootstrapKitDisplay); err != nil {
				return err
			}
			if err := b.store.UpsertJob(Job{
				DeploymentID: b.deploymentID,
				JobName:      jobName,
				AppID:        componentID,
				Type:         JobTypeInstall,
			ParentID:     JobID(b.deploymentID, GroupBootstrapKit),
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
				Type:         JobTypeInstall,
			ParentID:     JobID(b.deploymentID, GroupBootstrapKit),
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
