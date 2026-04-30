// mutation_bridge.go — Day-2 mutation Job audit trail.
//
// Every infrastructure CRUD endpoint on the catalyst-api goes through
// this surface BEFORE writing the Crossplane XRC, so an operator
// browsing /api/v1/deployments/{id}/jobs sees a complete record of
// every Day-2 action with the diff that was applied + the XRC that
// implements it.
//
// # Audit trail shape
//
// Per mutation, the bridge writes:
//
//   - One Job (jobName="mutation-<verb>-<kind>", batchId="day-2-mutations")
//     with deterministic id JobID(deploymentID, jobName).
//   - One Execution scoped to the same Job, in `running` state.
//   - One INFO LogLine "[mutation-request] action=<action> diff=...".
//
// After the XRC is submitted by the caller, AppendXRCSubmittedLog
// stamps a follow-up INFO LogLine "[xrc-submitted] kind=<kind>
// name=<name>" — and Crossplane's Composition controller, when
// reconciling the claim, appends further LogLines via the existing
// helmwatch bridge path (for component-typed claims like Cluster /
// VCluster Claims) or via per-claim status watchers (TBD by the
// third-sibling chart's audit shape).
//
// # Why a separate batch
//
// The bootstrap-kit Phase-1 install Jobs go into batchId="bootstrap-kit".
// Day-2 mutations go into batchId="day-2-mutations" so the FE Jobs
// surface can render them as a separate column. The same Bridge
// instance handles both via UpsertJob's batch field.
package jobs

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// BatchDay2Mutations — every mutation Job lands in this batch so the
// FE can group them separately from the Phase-1 install Jobs.
const BatchDay2Mutations = "day-2-mutations"

// MutationJobNamePrefix — every mutation Job's name starts with this.
// JobName format: "mutation-<verb>-<kind>" (e.g.
// "mutation-add-region"). The verb is operator-supplied; the kind is
// the XRC kind (RegionClaim → "region").
const MutationJobNamePrefix = "mutation-"

// MutationRecord — the typed payload one CRUD endpoint passes to
// RegisterMutationJob. Free-form Diff is rendered verbatim into the
// first LogLine; a unified-diff format is the convention.
type MutationRecord struct {
	// Verb — short action label (add | remove | update | scale |
	// cordon | drain | replace). Used as the second segment of the
	// JobName.
	Verb string

	// Kind — XRC kind without the "Claim" suffix, lowercase. E.g.
	// RegionClaim → "region", NodePoolClaim → "node-pool".
	Kind string

	// Slug — short identifier of the target resource (region id,
	// pool name, node id). Used to disambiguate concurrent mutations
	// on the same kind.
	Slug string

	// Action — full operator-readable action string for the log line
	// (e.g. "add-region region=hel1 sku=cpx32 workers=2"). Persisted
	// verbatim into the LogLine message.
	Action string

	// Diff — the desired change, in unified-diff or compact ASCII
	// form. Rendered into the first LogLine and onto the XRC's
	// catalyst.openova.io/diff annotation by the caller.
	Diff string

	// XRCKind — the Crossplane Composite Resource Claim kind the
	// CRUD handler will write next (e.g. "RegionClaim"). Stamped
	// into a second LogLine via AppendXRCSubmittedLog after the
	// create() call.
	XRCKind string

	// At — wall-clock instant the mutation request landed. Defaults
	// to time.Now() inside the helper.
	At time.Time
}

// MutationResult — what RegisterMutationJob returns to the caller.
// The CRUD handler funnels these into the 202 response so the FE
// can deep-link to the GitLab-style log viewer.
type MutationResult struct {
	JobID       string
	JobName     string
	ExecutionID string
}

// ErrInvalidMutation — returned when the supplied MutationRecord is
// missing required fields. CRUD handlers map this onto HTTP 500
// (this would be a programmer error since the wizard shape is
// validated before the handler reaches RegisterMutationJob).
var ErrInvalidMutation = errors.New("jobs: invalid mutation record")

// RegisterMutationJob writes the audit-trail Job + Execution +
// initial LogLine for one Day-2 mutation. Caller MUST call this
// BEFORE submitting the XRC; the handler then calls
// AppendXRCSubmittedLog with the create() result so the Job's log
// trail captures both the request and the submission.
//
// The store-level writes are serialised under Store.mu; the bridge's
// own state (b.activeExecID + b.lastState maps) is taken under b.mu
// so concurrent mutation registrations on the same deployment can't
// tear the cursor.
func (b *Bridge) RegisterMutationJob(rec MutationRecord) (MutationResult, error) {
	if b == nil {
		return MutationResult{}, errors.New("jobs: bridge is nil")
	}
	if strings.TrimSpace(rec.Verb) == "" {
		return MutationResult{}, fmt.Errorf("%w: verb is required", ErrInvalidMutation)
	}
	if strings.TrimSpace(rec.Kind) == "" {
		return MutationResult{}, fmt.Errorf("%w: kind is required", ErrInvalidMutation)
	}

	at := rec.At
	if at.IsZero() {
		at = time.Now().UTC()
	} else {
		at = at.UTC()
	}

	jobName := mutationJobName(rec.Verb, rec.Kind, rec.Slug)
	jobID := JobID(b.deploymentID, jobName)

	// Upsert the Job in `running` so the FE table renders the row
	// with a spinner the moment the API returns 202. We don't enter
	// `pending` here because the catalyst-api side has already
	// committed to writing the XRC — pending would be misleading.
	job := Job{
		DeploymentID: b.deploymentID,
		JobName:      jobName,
		AppID:        rec.Kind,
		BatchID:      BatchDay2Mutations,
		DependsOn:    []string{},
		Status:       StatusRunning,
	}
	if err := b.store.UpsertJob(job); err != nil {
		return MutationResult{}, fmt.Errorf("jobs: upsert mutation job: %w", err)
	}

	// Allocate the Execution. StartExecution stamps the Job's
	// LatestExecutionID + StartedAt; we don't have to UpsertJob
	// again post-allocation.
	exec, err := b.store.StartExecution(b.deploymentID, jobName, at)
	if err != nil {
		return MutationResult{}, fmt.Errorf("jobs: start mutation execution: %w", err)
	}

	// Take b.mu to record the active execution cursor.
	b.mu.Lock()
	b.activeExecID[jobName] = exec.ID
	b.lastState[jobName] = StatusRunning
	b.mu.Unlock()

	// Initial LogLine — "[mutation-request] ...".
	msg := "[mutation-request] " + strings.TrimSpace(rec.Action)
	if rec.Diff != "" {
		msg += " diff=" + strings.ReplaceAll(rec.Diff, "\n", " ")
	}
	if err := b.store.AppendLogLines(b.deploymentID, exec.ID, []LogLine{{
		Timestamp: at,
		Level:     LevelInfo,
		Message:   strings.TrimSpace(msg),
	}}); err != nil {
		return MutationResult{}, fmt.Errorf("jobs: append mutation request log: %w", err)
	}

	return MutationResult{
		JobID:       jobID,
		JobName:     jobName,
		ExecutionID: exec.ID,
	}, nil
}

// AppendXRCSubmittedLog appends an INFO LogLine recording the XRC
// the catalyst-api just submitted. Called after SubmitXRC succeeds.
// The CRUD handler passes the same MutationResult RegisterMutationJob
// returned so the helper finds the right Execution.
//
// When the XRC create errors (e.g. AlreadyExists), the handler
// instead calls FinishMutationJob with status=failed; this helper
// is for the success path.
func (b *Bridge) AppendXRCSubmittedLog(res MutationResult, xrcKind, xrcName, note string) error {
	if b == nil {
		return errors.New("jobs: bridge is nil")
	}
	if strings.TrimSpace(res.ExecutionID) == "" {
		return errors.New("jobs: AppendXRCSubmittedLog: ExecutionID is required")
	}
	msg := "[xrc-submitted] kind=" + xrcKind + " name=" + xrcName
	if note != "" {
		msg += " note=" + note
	}
	return b.store.AppendLogLines(b.deploymentID, res.ExecutionID, []LogLine{{
		Timestamp: time.Now().UTC(),
		Level:     LevelInfo,
		Message:   msg,
	}})
}

// FinishMutationJob flips the mutation Job into a terminal state
// AFTER the XRC submission completes. status MUST be StatusSucceeded
// or StatusFailed; the handler decides which based on the
// SubmitXRC outcome.
//
// For a successful submission the Job is technically still "running"
// from Crossplane's POV (the Composition is reconciling), but the
// API-side audit job is done — Crossplane's own status feed is the
// continuing log surface. Treating the API-side job as Succeeded on
// "submission accepted" matches the FE expectation: the row turns
// green when 202 lands, and Crossplane's downstream reconciliation
// surfaces as additional LogLines on the same Execution OR via the
// helmwatch bridge (for component claims).
func (b *Bridge) FinishMutationJob(res MutationResult, status string, errMsg string) error {
	if b == nil {
		return errors.New("jobs: bridge is nil")
	}
	if strings.TrimSpace(res.ExecutionID) == "" {
		return errors.New("jobs: FinishMutationJob: ExecutionID is required")
	}
	if status == "" {
		status = StatusSucceeded
	}
	if !IsTerminal(status) {
		return fmt.Errorf("jobs: FinishMutationJob: status must be terminal, got %q", status)
	}
	if errMsg != "" {
		_ = b.store.AppendLogLines(b.deploymentID, res.ExecutionID, []LogLine{{
			Timestamp: time.Now().UTC(),
			Level:     LevelError,
			Message:   "[xrc-submission-failed] " + errMsg,
		}})
	}
	if err := b.store.FinishExecution(b.deploymentID, res.ExecutionID, status, time.Now().UTC()); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.activeExecID, res.JobName)
	delete(b.lastState, res.JobName)
	b.mu.Unlock()
	return nil
}

// mutationJobName composes the JobName from the request fields. The
// shape is "mutation-<verb>-<kind>[-<slug>]" so concurrent mutations
// on the same kind don't collide on a single Job row.
func mutationJobName(verb, kind, slug string) string {
	v := strings.ToLower(strings.TrimSpace(verb))
	k := strings.ToLower(strings.TrimSpace(kind))
	s := strings.ToLower(strings.TrimSpace(slug))
	parts := []string{MutationJobNamePrefix + v, k}
	if s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "-")
}
