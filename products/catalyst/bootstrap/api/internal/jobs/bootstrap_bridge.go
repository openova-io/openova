// bootstrap_bridge.go — projects the ~30-minute cluster-bootstrap window
// (between "Provision <provider>: Success" and the first bp-* HelmRelease)
// into the same Job + Execution + LogLine model the helmwatch + activity
// bridges use.
//
// # Why this exists (the provisioning-observability gap)
//
// The provisioning timeline (/sovereign/provision/<id>/jobs) used to show
// the "Provision <provider>" lifecycle group flip to Success the moment
// `tofu apply` returned — and then NOTHING for ~30 minutes while the real
// work happened cluster-side: cloud-init boots the nodes, k3s comes up,
// cloud-init PUTs the kubeconfig back, Flux installs the bootstrap-kit, and
// the HelmReleases reconcile. During that whole window the operator saw a
// static "Provision <provider>: Success" with no further motion and
// reasonably concluded the prov was stuck or dead.
//
// The DATA to fill that window already exists in catalyst-api's Phase-1
// watch — it just was not surfaced to the timeline. BootstrapBridge closes
// that gap by materialising a "Bootstrapping cluster" group with three live
// step children, each driven by a phase1-watch signal:
//
//	cluster-converge (group, "Bootstrapping cluster")
//	    ├── cluster-converge-step-nodes-booting        ("Nodes booting · cloud-init running")
//	    ├── cluster-converge-step-kubeconfig-received  ("k3s up · kubeconfig received")
//	    └── cluster-converge-step-flux-installing      ("Flux installing — HR X/Y ready")
//
// The group's status rolls up from its step children at read time
// (Store.deriveTreeView), so while ANY step is running the group reads
// "running" — continuous motion in the existing 5-second /jobs poll. The
// Flux step's DisplayName is rewritten live (SetFluxProgress) to carry the
// climbing "HR X/Y ready" counter so the operator literally watches the
// bootstrap-kit converge.
//
// # Relationship to the other bridges
//
// BootstrapBridge is a sibling of Bridge + ActivityBridge, not a
// replacement. All three write into the same Store under the same deployment
// id, so a single /jobs read returns the Phase-0 lifecycle, the bootstrap
// window, the bootstrap-kit installs, and every projected activity in one
// tree. The namespaces never collide: BootstrapBridge only ever writes the
// "cluster-converge" group + its "cluster-converge-step-*" leaves.
//
// # Idempotency + resume
//
// Every method is idempotent. runPhase1Watch re-enters cleanly across a
// catalyst-api Pod restart (a resumed watch re-seeds the steps + re-drives
// the transitions). UpsertJob's monotonic merge (store.mergeJob) preserves
// StartedAt/FinishedAt and a non-empty DependsOn, so re-seeding never
// un-starts a step or drops an edge. StartStep reuses an already-open
// Execution rather than allocating a second one — the same property
// ActivityBridge relies on.
package jobs

import (
	"fmt"
	"sync"
	"time"
)

// bootstrapSteps — the ordered step set the BootstrapBridge seeds. Order
// drives the linear DependsOn chain (nodes-booting → kubeconfig-received →
// flux-installing) so the canvas renders the bootstrap window as a chain
// hanging off the last Phase-0 lifecycle step.
var bootstrapSteps = []ActivityStep{
	{Slug: BootstrapStepNodesBooting, DisplayName: BootstrapStepNodesBootingDisplay, Order: 1},
	{Slug: BootstrapStepKubeconfig, DisplayName: BootstrapStepKubeconfigDisplay, Order: 2},
	{Slug: BootstrapStepFluxInstalling, DisplayName: BootstrapStepFluxInstallingDisplay, Order: 3},
}

// BootstrapBridge projects the cluster-bootstrap window into the jobs Store
// under a single deployment id. One bridge instance per deployment.
// Goroutine-safe: the internal mutex serialises the per-step Execution
// cursor; the Store serialises the actual writes.
type BootstrapBridge struct {
	store        *Store
	deploymentID string

	mu sync.Mutex

	// activeExecID — per-step-slug id of the in-flight Execution. Set on
	// the first StartStep, cleared on FinishStep. A resumed bridge
	// re-attaches via the persisted Job.LatestExecutionID so a Pod restart
	// never strands an Execution open.
	activeExecID map[string]string

	// fluxLabel — last DisplayName written for the flux-installing step, so
	// SetFluxProgress skips a redundant UpsertJob when the HR census is
	// unchanged between poll ticks (avoids log/persist churn at the 5s poll
	// cadence).
	fluxLabel string
}

// NewBootstrapBridge constructs a bridge for the cluster-bootstrap window.
// store must be non-nil; deploymentID must be non-empty.
func NewBootstrapBridge(store *Store, deploymentID string) *BootstrapBridge {
	return &BootstrapBridge{
		store:        store,
		deploymentID: deploymentID,
		activeExecID: map[string]string{},
	}
}

// GroupJobID returns the stable id of the "Bootstrapping cluster" group Job.
func (b *BootstrapBridge) GroupJobID() string {
	return JobID(b.deploymentID, GroupClusterConverge)
}

// stepJobName returns the canonical leaf JobName for a bootstrap step, e.g.
// "cluster-converge-step-nodes-booting".
func (b *BootstrapBridge) stepJobName(slug string) string {
	return ActivityStepJobName(GroupClusterConverge, slug)
}

// ensureGroupLocked idempotently materialises the "Bootstrapping cluster"
// group Job. Its runtime status rolls up from its step children at read
// time (deriveTreeView). Caller MUST hold b.mu.
func (b *BootstrapBridge) ensureGroupLocked() error {
	return b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      GroupClusterConverge,
		DisplayName:  GroupClusterConvergeDisplay,
		Type:         JobTypeGroup,
		Kind:         KindGroup,
		ParentID:     "",
		DependsOn:    []string{},
		Status:       StatusPending,
	})
}

// dependsOnForStepLocked returns the prior step's leaf JobName (length 0 for
// the first step) so the canvas renders the linear chain. Caller MUST hold
// b.mu.
func (b *BootstrapBridge) dependsOnForStepLocked(slug string) []string {
	for i, s := range bootstrapSteps {
		if s.Slug != slug {
			continue
		}
		if i == 0 {
			return []string{}
		}
		return []string{b.stepJobName(bootstrapSteps[i-1].Slug)}
	}
	return []string{}
}

// upsertStepLocked materialises one bootstrap step leaf at the given status
// (preserving any prior StartedAt via mergeJob). Caller MUST hold b.mu.
func (b *BootstrapBridge) upsertStepLocked(s ActivityStep, status string) error {
	return b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      b.stepJobName(s.Slug),
		DisplayName:  s.DisplayName,
		Type:         JobTypeInstall,
		Kind:         KindStep,
		ParentID:     b.GroupJobID(),
		DependsOn:    b.dependsOnForStepLocked(s.Slug),
		Status:       status,
	})
}

// Seed materialises the group + all three step leaves in pending state, wired
// with the linear DependsOn chain. Called once at Phase-1 watch start so the
// "Bootstrapping cluster" group appears (pending) the instant the Phase-0
// lifecycle group reaches Success — there is never a frame where the timeline
// is blank between provision and convergence.
//
// Idempotent + resume-safe: mergeJob lets next.Status win unconditionally, so
// a blind re-seed would REGRESS an already-running/terminal step back to
// pending after a Pod-restart resume. Seed therefore reads each step's current
// status first and never writes a status LESS advanced than what is on disk —
// a pending step is (re)seeded pending; a running/terminal one is preserved.
func (b *BootstrapBridge) Seed() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.ensureGroupLocked(); err != nil {
		return err
	}
	for _, s := range bootstrapSteps {
		status := StatusPending
		if job, _, err := b.store.GetJob(b.deploymentID, b.stepJobName(s.Slug)); err == nil && job.Status != "" {
			// Preserve whatever the row already reached (running/terminal);
			// only ever (re)assert pending for a not-yet-started step.
			status = job.Status
		}
		if err := b.upsertStepLocked(s, status); err != nil {
			return err
		}
	}
	return nil
}

// stepBySlug returns the declared step for a slug (with its display name), or
// a bare step carrying just the slug if it is unknown.
func stepBySlug(slug string) ActivityStep {
	for _, s := range bootstrapSteps {
		if s.Slug == slug {
			return s
		}
	}
	return ActivityStep{Slug: slug, DisplayName: slug}
}

// StartStep transitions a bootstrap step into Running, allocating an
// Execution (and stamping StartedAt) on the first call. Re-calling for a step
// that already has an open Execution reuses it (no duplicate Execution row),
// the load-bearing idempotency property for the Pod-restart resume path. The
// message is appended as the first LogLine so the Exec Log surface is never
// empty.
func (b *BootstrapBridge) StartStep(slug, message string, t time.Time) error {
	t = t.UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startStepLocked(slug, message, t)
}

func (b *BootstrapBridge) startStepLocked(slug, message string, t time.Time) error {
	if err := b.ensureGroupLocked(); err != nil {
		return err
	}
	s := stepBySlug(slug)
	jobName := b.stepJobName(slug)

	// Reuse an open Execution: in-memory cursor first, then the persisted
	// LatestExecutionID while it is still running (resume). Only allocate a
	// fresh Execution when neither is live.
	execID := b.activeExecID[slug]
	if execID == "" {
		if job, _, err := b.store.GetJob(b.deploymentID, jobName); err == nil &&
			job.LatestExecutionID != "" && !IsTerminal(job.Status) {
			execID = job.LatestExecutionID
			b.activeExecID[slug] = execID
		}
	}
	if execID == "" {
		exec, err := b.store.StartExecution(b.deploymentID, jobName, t)
		if err != nil {
			return err
		}
		execID = exec.ID
		b.activeExecID[slug] = execID
	} else {
		// Execution already open — flip the Job to running (a resume that
		// re-enters a step the durable record left at pending). mergeJob
		// keeps the prior StartedAt.
		if err := b.upsertStepLocked(s, StatusRunning); err != nil {
			return err
		}
	}

	msg := message
	if msg == "" {
		msg = "[" + slug + "] started"
	}
	return b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
		Timestamp: t,
		Level:     LevelInfo,
		Message:   msg,
	}})
}

// Heartbeat appends a single live LogLine to a running step's Execution
// WITHOUT changing its status — this is what keeps the operator seeing motion
// during the long nodes-booting wait. The catalyst-api drives it from the
// kubeconfig-wait poll loop with a cloud-init log tail / numEvents counter so
// every tick produces a fresh line. A no-op if the step has no open
// Execution (it was never started or has already terminated).
func (b *BootstrapBridge) Heartbeat(slug, message string, t time.Time) error {
	if message == "" {
		return nil
	}
	t = t.UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	execID := b.activeExecID[slug]
	if execID == "" {
		// Resume path: re-attach to a persisted open Execution.
		if job, _, err := b.store.GetJob(b.deploymentID, b.stepJobName(slug)); err == nil &&
			job.LatestExecutionID != "" && !IsTerminal(job.Status) {
			execID = job.LatestExecutionID
			b.activeExecID[slug] = execID
		}
	}
	if execID == "" {
		return nil
	}
	return b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
		Timestamp: t,
		Level:     LevelInfo,
		Message:   message,
	}})
}

// SetFluxProgress rewrites the flux-installing step's DisplayName to carry the
// live "Flux installing — HR X/Y ready" counter and appends a progress
// LogLine. Driven each poll tick from the in-cluster HelmRelease census the
// helmwatch snapshot exposes, so the operator watches the bootstrap-kit
// converge in real time. The step must already be running (StartStep called);
// this only mutates the label + log, never the status. A no-op when the
// (ready,total) census is unchanged since the last call (avoids churn at the
// 5s poll cadence). total<=0 is treated as "not yet known" and renders the
// bare baseline label.
func (b *BootstrapBridge) SetFluxProgress(ready, total int, t time.Time) error {
	t = t.UTC()
	label := BootstrapStepFluxInstallingDisplay
	if total > 0 {
		label = fmt.Sprintf("%s — HR %d/%d ready", BootstrapStepFluxInstallingDisplay, ready, total)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if label == b.fluxLabel {
		return nil
	}
	b.fluxLabel = label

	// Keep the step running while progress streams; mergeJob preserves the
	// prior StartedAt and never regresses a terminal status, so a late tick
	// after FinishStep can't un-finish it.
	if err := b.store.UpsertJob(Job{
		DeploymentID: b.deploymentID,
		JobName:      b.stepJobName(BootstrapStepFluxInstalling),
		DisplayName:  label,
		Type:         JobTypeInstall,
		Kind:         KindStep,
		ParentID:     b.GroupJobID(),
		DependsOn:    b.dependsOnForStepLocked(BootstrapStepFluxInstalling),
		Status:       StatusRunning,
	}); err != nil {
		return err
	}

	execID := b.activeExecID[BootstrapStepFluxInstalling]
	if execID == "" {
		return nil
	}
	return b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
		Timestamp: t,
		Level:     LevelInfo,
		Message:   label,
	}})
}

// FinishStep transitions a bootstrap step into a terminal state and closes
// its Execution. status must be StatusSucceeded or StatusFailed. The message
// is appended as the final LogLine. Idempotent: a step with no open Execution
// gets one allocated retroactively so the JobsTable never renders an em-dash
// duration cell (matching the activity + helmwatch bridges).
func (b *BootstrapBridge) FinishStep(slug, status, message string, t time.Time) error {
	if !IsTerminal(status) {
		status = StatusSucceeded
	}
	t = t.UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.finishStepLocked(slug, status, message, t)
}

func (b *BootstrapBridge) finishStepLocked(slug, status, message string, t time.Time) error {
	if err := b.ensureGroupLocked(); err != nil {
		return err
	}
	jobName := b.stepJobName(slug)

	execID := b.activeExecID[slug]
	if execID == "" {
		if job, _, err := b.store.GetJob(b.deploymentID, jobName); err == nil && job.LatestExecutionID != "" {
			if priorExec, ferr := b.store.FindExecution(b.deploymentID, job.LatestExecutionID); ferr == nil && !IsTerminal(priorExec.Status) {
				execID = job.LatestExecutionID
			}
		}
	}
	if execID == "" {
		exec, err := b.store.StartExecution(b.deploymentID, jobName, t)
		if err != nil {
			return err
		}
		execID = exec.ID
	}

	if message != "" {
		level := LevelInfo
		if status == StatusFailed {
			level = LevelError
		}
		if err := b.store.AppendLogLines(b.deploymentID, execID, []LogLine{{
			Timestamp: t,
			Level:     level,
			Message:   message,
		}}); err != nil {
			return err
		}
	}

	if err := b.store.FinishExecution(b.deploymentID, execID, status, t); err != nil {
		return err
	}
	delete(b.activeExecID, slug)
	return nil
}

// MarkKubeconfigReceived is the convenience transition the kubeconfig-PUT
// callback fires: it succeeds nodes-booting + kubeconfig-received and starts
// flux-installing in one atomic critical section, so the timeline advances
// from "Nodes booting" → "k3s up · kubeconfig received" → "Flux installing"
// the instant the PUT lands. Idempotent: re-firing is a no-op once the steps
// are terminal (FinishExecution is a no-op on an already-terminal Execution;
// StartStep reuses the open flux Execution).
func (b *BootstrapBridge) MarkKubeconfigReceived(message string, t time.Time) error {
	t = t.UTC()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.finishStepLocked(BootstrapStepNodesBooting, StatusSucceeded, "Nodes booted; cloud-init PUT the kubeconfig back.", t); err != nil {
		return err
	}
	msg := message
	if msg == "" {
		msg = "k3s up; kubeconfig received from cloud-init."
	}
	if err := b.finishStepLocked(BootstrapStepKubeconfig, StatusSucceeded, msg, t); err != nil {
		return err
	}
	return b.startStepLocked(BootstrapStepFluxInstalling, "Flux is reconciling the bootstrap-kit Kustomization; watching HelmReleases converge.", t)
}

// MarkConverged succeeds the flux-installing step at OutcomeReady — the
// bootstrap window is closed and the bootstrap-kit installs take over the
// timeline. Idempotent.
func (b *BootstrapBridge) MarkConverged(message string, t time.Time) error {
	msg := message
	if msg == "" {
		msg = "Bootstrap-kit converged; per-component HelmReleases now drive the timeline."
	}
	return b.FinishStep(BootstrapStepFluxInstalling, StatusSucceeded, msg, t)
}
