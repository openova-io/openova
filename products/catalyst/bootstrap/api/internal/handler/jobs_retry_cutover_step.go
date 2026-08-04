// jobs_retry_cutover_step.go — the KindStep leg of the ONE generic
// remediation endpoint (jobs_retry.go): the operator console's per-row
// **Re-run** control on a FAILED `cutover-step-*` row of the /jobs board.
//
// # Why this leg was missing (issue #3379, UAT row 165)
//
// The self-sovereign cutover engine already had the correct re-drive
// capability: `runCutover(..., operatorRetry=true)` DELETES a step whose
// prior Job failed genuinely (BackoffLimitExceeded etc.) and RE-RUNS it,
// rather than re-surfacing it as a terminal wedge (cutover.go:1405-1412).
// The auto-resume + in-cluster auto-trigger deliberately pass false so a
// broken step never auto-loops — the operator CTA is the intended escape
// hatch.
//
// The console side was the gap. `cutover_activity_bridge.go` projects each
// of the 11 steps into the jobs store as a leaf with `Kind = jobs.KindStep`
// and JobName `cutover-step-<slug>`, and JobsTable already renders the
// generic Retry control on any `failed` row — but `dispatchRetry` had no
// `KindStep` case, so the control fell through to `default:` and every
// click on a failed cutover step answered
//
//	422 kind "step" does not support retry
//
// i.e. a button the operator could see but that could never work. The only
// real recovery was a hand-crafted `POST /api/v1/sovereign/cutover/start`.
//
// # What this leg does
//
// Reuses the existing engine rather than inventing a second cutover write
// path: it discovers the step ConfigMaps, verifies the clicked row's slug
// is one of them, then spawns the SAME `runCutover(..., operatorRetry=true)`
// goroutine `HandleCutoverStart` spawns. Already-succeeded steps are skipped
// from the durable per-step status (cutover.go's skip-success basis), so the
// engine resumes exactly at the failed step, deletes its stale Job, re-runs
// it, and carries the chain onward. Because the chain is sequential and
// halts at its first failure, at most ONE step is `failed` at a time — so
// "re-run this row" and "resume the chain" are the same operation.
//
// Every guarantee of the generic endpoint still applies: owner-checked
// (404 cross-tenant), operator-tier RBAC (403 otherwise), session-gated by
// the RequireSession group the route is registered in, and the attempt is
// recorded as a new Execution carrying the operator's identity.
package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// errCutoverRunInFlight marks a Re-run request that arrived while a cutover
// run already holds the engine's single-run flag. Spawning a second engine
// goroutine would race the first over the same step Jobs + status ConfigMap,
// so the honest answer is 409 "already running" — the same answer
// HandleCutoverStart gives for a concurrent /start.
var errCutoverRunInFlight = errors.New("a cutover run is already in flight on this catalyst-api Pod")

// cutoverStepSlugFromJobName extracts the cutover step slug from an activity
// step leaf's JobName, or "" when the leaf is not a CUTOVER step.
//
// The projected leaf name is `ActivityStepJobName(GroupCutover, slug)` ==
// "cutover-step-<slug>" (jobs/activity_bridge.go). Other activity groups
// (a future DR switchover) mint "<group>-step-<slug>" under their own slug
// and are deliberately NOT matched here — they need their own engine leg.
func cutoverStepSlugFromJobName(jobName string) string {
	prefix := jobs.ActivityStepJobName(jobs.GroupCutover, "")
	if !strings.HasPrefix(jobName, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(jobName, prefix))
}

// retryActivityStep re-drives a FAILED projected activity step. Today the
// only activity projecting steps is the self-sovereign cutover; any other
// group returns errNotDirectlyRetryable so the operator gets an honest 422
// instead of a silent no-op.
func (h *Handler) retryActivityStep(ctx context.Context, job jobs.Job, _ time.Time) (string, error) {
	slug := cutoverStepSlugFromJobName(job.JobName)
	if slug == "" {
		return "", fmt.Errorf(
			"step leaf %q belongs to an activity with no re-run engine: %w",
			job.JobName, errNotDirectlyRetryable,
		)
	}
	return h.retryCutoverStep(ctx, slug)
}

// retryCutoverStep spawns the cutover engine in operator-retry mode so the
// named step's stale failed Job is deleted + re-run and the chain resumes.
// Returns a short human description of the action for the audit LogLine.
func (h *Handler) retryCutoverStep(ctx context.Context, slug string) (string, error) {
	deps, err := h.cutoverDepsFor()
	if err != nil {
		return "", fmt.Errorf("cutover engine unconfigured on this catalyst-api: %w", err)
	}

	steps, err := listCutoverSteps(ctx, deps)
	if err != nil {
		return "", fmt.Errorf("cutover step discovery failed: %w", err)
	}
	if len(steps) == 0 {
		return "", fmt.Errorf(
			"no cutover-step ConfigMaps found in namespace %q — bp-self-sovereign-cutover not installed?: %w",
			deps.ns, errNotDirectlyRetryable,
		)
	}

	known := false
	for _, s := range steps {
		if s.stepName == slug {
			known = true
			break
		}
	}
	if !known {
		return "", fmt.Errorf(
			"cutover step %q is not among the %d steps installed on this Sovereign: %w",
			slug, len(steps), errNotDirectlyRetryable,
		)
	}

	// Single-run flag — the same guard HandleCutoverStart uses. Released by
	// runCutover's `defer bus.endRun()`.
	bus := h.cutoverBusFor()
	if !bus.tryStartRun() {
		return "", errCutoverRunInFlight
	}

	// context.Background() (not the request context): a multi-step cutover
	// must not be cancelled when the operator's browser closes the POST.
	// Each step is bounded by cutoverStepTimeout.
	//
	// operatorRetry=true — this IS the deliberate human CTA #3379 describes,
	// reached through the session-gated + operator-RBAC-gated retry route.
	go h.runCutover(context.Background(), deps, steps, true)

	return fmt.Sprintf(
		"re-ran cutover step %q (operator retry: prior failed Job deleted + re-run, chain resumed)",
		slug,
	), nil
}
