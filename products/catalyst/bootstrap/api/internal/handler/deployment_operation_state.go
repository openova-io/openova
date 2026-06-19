// deployment_operation_state.go — the `operationInProgress` projection
// that backs the Convergence-Monitor top-bar readiness chip (#3925
// surface D).
//
// # Why a separate boolean from `status`
//
// The deployment `status` enum (deployments.go:80) answers "where is the
// INITIAL provision?" — pending → tofu-applying → flux-bootstrapping →
// phase1-watching → ready. Once a Sovereign reaches `ready` an operator
// can still kick off a FINITE multi-step operation — the classic case is
// `bp-self-sovereign-cutover`'s 8 sequential Jobs, or a DR-switchover.
// While that operation runs the env is NOT "still provisioning" (status
// stays `ready`) and it is NOT idle either. The chip needs to render
// `OPERATION-IN-PROGRESS` distinctly so a stuck cutover is never misread
// as ongoing initial provisioning (ticket §5(c) — "a 2-hour-stuck
// cutover was read as still provisioning").
//
// `operationInProgress` is true while a cutover / DR-switchover Job group
// is non-terminal. It is surface-only — it NEVER gates `status` and never
// changes the convergence machinery. The Reconciliation DAG stays green
// throughout (the Flux spine is reconciled; the operation is a finite job
// that's running, not a broken convergence).
package handler

import (
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// operationGroupSlugs are the group slugs whose non-terminal presence
// flips operationInProgress true. These are the FINITE multi-step
// operations an operator runs on an already-provisioned Sovereign —
// distinct from the initial provision (provisioner / bootstrap-kit / apps
// groups, which `status` already tracks). Kept as a set so the detection
// is an O(1) membership test, not a brittle prefix string-match.
var operationGroupSlugs = map[string]struct{}{
	jobs.GroupCutover: {}, // bp-self-sovereign-cutover's 8 sequential steps
	// DR-switchover is modelled as a cutover-class step group; if a
	// dedicated group slug is introduced it is added here verbatim.
}

// operationInProgress reports whether a finite, post-provision operation
// (cutover / DR-switchover) is currently running for the given deployment.
//
// True iff the jobs store holds at least one Job that BOTH:
//   - belongs to an operation group (cutover/DR-switchover) — matched on
//     either the group Job's own slug OR a "<group>-step-…" leaf, AND
//   - is NOT in a terminal state (succeeded / failed) — i.e. it is
//     pending or running.
//
// Returns false (never errors) when the jobs store is unconfigured or the
// deployment has no jobs — the chip degrades to reading `status` alone.
func (h *Handler) operationInProgress(deploymentID string) bool {
	st := h.jobsStore()
	if st == nil {
		return false
	}
	js, err := st.ListJobs(deploymentID)
	if err != nil || len(js) == 0 {
		return false
	}
	for _, j := range js {
		if !isOperationJob(j) {
			continue
		}
		if isNonTerminalJobStatus(j.Status) {
			return true
		}
	}
	return false
}

// isOperationJob reports whether a Job belongs to one of the post-provision
// operation groups (cutover / DR-switchover). It matches:
//   - a group Job whose JobName is an operation slug, OR
//   - a step leaf named "<operationSlug>-step-<…>" (KindStep under an
//     operation group), OR
//   - a leaf whose ParentID resolves to "<deploymentId>:<operationSlug>".
//
// The "<slug>-step-" infix check mirrors jobs.kindForLeaf's ordering so a
// "cutover-step-05-egress-block" leaf is recognised as an operation step.
func isOperationJob(j jobs.Job) bool {
	if _, ok := operationGroupSlugs[j.JobName]; ok {
		return true
	}
	for slug := range operationGroupSlugs {
		// "<slug>-step-" leaf (e.g. "cutover-step-05-…").
		if strings.HasPrefix(j.JobName, slug+"-step-") {
			return true
		}
		// Leaf hanging off the operation group's synthesised parent.
		if j.ParentID != "" && strings.HasSuffix(j.ParentID, ":"+slug) {
			return true
		}
	}
	return false
}

// isNonTerminalJobStatus reports whether a Job status string represents an
// in-flight (pending/running) — anything other than succeeded/failed.
// An empty/unknown status is treated as non-terminal (conservative: a
// freshly-seeded step with no status yet is "in progress", not done).
func isNonTerminalJobStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case jobs.StatusSucceeded, jobs.StatusFailed:
		return false
	default:
		return true
	}
}
