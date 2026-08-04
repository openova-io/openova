// cutover_snapshot_steps.go — #5437: skip-on-success is WRONG for a step whose
// output is a snapshot of a moving external source.
//
// ── The defect (live, hw290 2026-07-27) ─────────────────────────────────────
//
// runCutover skips any step whose durable row already reads result="success".
// That is correct idempotency for a step that ENSURES state (harbor-projects
// creates projects; re-running is a no-op). It is WRONG for step-01
// `gitea-mirror`, whose success means only "at time T the local Gitea matched
// upstream". Every later step consumes that snapshot, so its validity DECAYS.
//
// On hw290 a re-attempt 14 hours after the first one skipped step-01 (recorded
// success at 2026-07-26T15:02:02Z, attempt started 2026-07-27T04:56:51Z) while
// step-03 harbor-prewarm re-ran FRESH against the current catalog. The mirror
// still pinned the bootstrap-kit chart version whose values reference
// `catalyst-api:b1b472d`; the fresh prewarm pushed only what the live cluster
// was running (`…:3a8803e`). Step-05 then pointed Flux at the stale mirror, the
// Deployment rolled to the tag nothing had pushed, and the control plane went
// down:
//
//	catalyst-api  0/1  ImagePullBackOff
//	registry.hw290.omani.works/…/catalyst-api:b1b472d -> code = NotFound
//
// The local registry ANSWERED and said the tag does not exist — not egress, not
// x509. The skip predicate simply could not tell "idempotent action" from
// "snapshot of external state".
//
// ── The fix ─────────────────────────────────────────────────────────────────
//
//  1. A snapshot step whose recorded success predates the CURRENT attempt is
//     re-run instead of skipped (decideSnapshotRerun).
//  2. Re-taking the snapshot invalidates everything that consumed it: the
//     recorded successes of every LATER step are cleared so they re-run against
//     the refreshed snapshot too. Without this the fix would just invert the
//     drift — a fresh mirror with a stale harbor-prewarm pins chart versions the
//     prewarm never mirrored (#5237 Phase A2-pins reads the mirror).
//  3. The re-run is SUPPRESSED once the chain has advanced to the pivot
//     boundary (step-05 flux-gitrepository-patch). From that point the mirrored
//     repo is the live GitOps source and steps 06/07/10/11 have committed
//     sovereign-LOCAL changes onto its `main`; step-01's
//     `git push --force refs/heads/*:refs/heads/*` would REVERT them — that is
//     the hw86 mirror-resync incident the G75 block in
//     chart/templates/01-gitea-mirror-job.yaml exists to prevent. Suppression
//     also keeps the cascade off the far end of the chain, where re-running
//     step-08 would repeat the 10-minute deny-egress hold (the t40 / TBD-V56
//     regression).
//  4. Defence in depth: before flux-gitrepository-patch runs, the engine
//     asserts the mirror snapshot is from this attempt
//     (assertMirrorSnapshotFresh). Pointing Flux at a snapshot older than the
//     artifacts harbor-prewarm pushed is the exact hw290 outage, so it fails
//     LOUD and EARLY instead of taking the control plane down.
//
// Refs #5437.
package handler

import (
	"fmt"
	"time"
)

// snapshotCutoverSteps is the set of step slugs whose recorded "success" is a
// point-in-time SNAPSHOT of a moving external source rather than an idempotent
// state-ensure action.
//
// gitea-mirror is the only member, and the audit behind that is deliberate —
// every other step in the shipped 11-step chain either ensures durable state
// (02 harbor-projects, 04 registry-pivot, 05 flux-gitrepository-patch, 06
// helmrepository-patches, 07 catalyst-api-env-patch, 09 gitea-token-mint, 10
// vcluster-registry-pivot, 11 crossplane-provider-pivot — the last two resolve
// upstream digests but PIN them immutably, so the resolution cannot decay) or
// produces a proof nothing downstream consumes (08 egress-block-test).
//
// 03 harbor-prewarm is the one near-miss: its success also decays, because the
// artifact set it pushes is derived from the mirror's bootstrap-kit pins
// (#5237) plus the live cluster. It is deliberately NOT listed here — its input
// is the snapshot, not the moving upstream, so the correct invalidation is the
// cascade in rule 2 above (re-taking the mirror invalidates it) rather than an
// independent time predicate that would re-run a ~50-minute step on every
// resume.
var snapshotCutoverSteps = map[string]struct{}{
	cutoverStepGiteaMirror: {},
}

// isSnapshotCutoverStep reports whether a step slug is snapshot-shaped.
func isSnapshotCutoverStep(stepName string) bool {
	_, ok := snapshotCutoverSteps[stepName]
	return ok
}

// snapshotRerunDecision is the outcome of the skip-vs-re-run judgement for one
// snapshot step. reason is always populated when the decision is interesting
// (rerun, or a suppression) so the engine can publish it verbatim to the SSE
// bus — an operator watching the wizard sees WHY the mirror re-ran.
type snapshotRerunDecision struct {
	rerun      bool
	suppressed bool
	reason     string
}

// decideSnapshotRerun answers: this step's durable row says success — must it
// nevertheless re-run on the attempt that started at attemptStart?
//
// stepHasJobs reports whether the cluster still carries any Job for a step
// slug. It is injected rather than called directly so the judgement stays a
// pure function of its inputs and is unit-testable without a cluster. A nil
// func is read as "no Jobs anywhere".
func decideSnapshotRerun(
	step cutoverStep,
	steps []cutoverStep,
	priorStatus map[string]string,
	attemptStart time.Time,
	stepHasJobs func(stepName string) bool,
) snapshotRerunDecision {
	if !isSnapshotCutoverStep(step.stepName) {
		return snapshotRerunDecision{}
	}
	if priorStatus["step."+step.stepName+".result"] != "success" {
		// Not being skipped in the first place — the engine will run it.
		return snapshotRerunDecision{}
	}

	// Suppression wins over freshness: a re-mirror past the pivot boundary
	// DESTROYS downstream work, which is strictly worse than a stale snapshot
	// that has already been pivoted onto.
	if blocker := snapshotRerunSuppressedBy(steps, priorStatus, step.order, stepHasJobs); blocker != "" {
		return snapshotRerunDecision{
			suppressed: true,
			reason: fmt.Sprintf(
				"step %s snapshot is from a prior attempt, but the chain has already reached step %s — re-mirroring now would force-push upstream over the sovereign-local commits pushed to the mirror, and would re-run steps that must not repeat; keeping the recorded success (#5437)",
				step.stepName, blocker),
		}
	}

	recorded := priorStatus["step."+step.stepName+".finishedAt"]
	if ts, err := time.Parse(time.RFC3339, recorded); err == nil && !ts.Before(attemptStart) {
		// Taken during this very attempt — nothing to refresh.
		return snapshotRerunDecision{}
	}

	when := recorded
	if when == "" {
		when = "<unrecorded>"
	}
	return snapshotRerunDecision{
		rerun: true,
		reason: fmt.Sprintf(
			"step %s succeeded at %s, before this attempt started at %s — its output is a snapshot of a moving upstream, not an idempotent action, so the recorded success is re-taken rather than skipped (#5437)",
			step.stepName, when, attemptStart.UTC().Format(time.RFC3339)),
	}
}

// snapshotRerunBoundaryOrder returns the order at which re-taking the snapshot
// stops being safe.
//
// That is the pivot boundary, cutoverStepFluxGitRepoPatch: once Flux reconciles
// from the local Gitea, steps 06/07/10/11 commit sovereign-local changes onto
// the mirrored `main` branch and step-01's force-push of every upstream ref
// would revert them (hw86, 2026-05-31: a mirror-mode push wiped step-06's
// HelmRepository pivot 11 minutes after it landed, and step-08 then failed
// against still-ghcr.io HelmRepositories). Steps below the boundary — 02
// harbor-projects, 03 harbor-prewarm, 04 registry-pivot — are pure
// state-ensure, so re-running them against the refreshed snapshot is safe.
//
// A chain carrying NO pivot-boundary step (partial / custom) cannot be reasoned
// about, so the boundary collapses to "the very next step": any progress past
// the snapshot suppresses the re-run.
func snapshotRerunBoundaryOrder(steps []cutoverStep, snapshotOrder int) int {
	for _, s := range steps {
		if s.stepName == cutoverStepFluxGitRepoPatch {
			return s.order
		}
	}
	return snapshotOrder + 1
}

// snapshotRerunSuppressedBy returns the name of the step whose progress makes
// re-taking the snapshot unsafe, or "" when a re-run is safe.
//
// Progress is read from TWO signals, because neither alone is sufficient:
//
//   - the durable result row — but ResumeInterruptedCutover blanks the row of a
//     step that was in flight when catalyst-api died, so a step can have run to
//     Complete and still read "";
//   - a surviving Job carrying the step's cutover.openova.io/step label — the
//     same primitive runCutoverStep's idempotency check uses, and immune to
//     that status reset.
//
// The second signal is what keeps the TBD-V56 / t40 guarantee intact: a
// Pod-restart resume that stopped inside step-08 must not have its 10-minute
// deny-egress hold re-run by a cascade from the top of the chain.
func snapshotRerunSuppressedBy(
	steps []cutoverStep,
	priorStatus map[string]string,
	snapshotOrder int,
	stepHasJobs func(stepName string) bool,
) string {
	boundaryOrder := snapshotRerunBoundaryOrder(steps, snapshotOrder)
	for _, s := range steps {
		if s.order <= snapshotOrder || s.order < boundaryOrder {
			continue
		}
		if priorStatus["step."+s.stepName+".result"] == "success" {
			return s.stepName
		}
		if stepHasJobs != nil && stepHasJobs(s.stepName) {
			return s.stepName
		}
	}
	return ""
}

// snapshotRerunCascade returns the durable status keys that must be cleared
// when the snapshot step at index rerunIdx is re-taken: its own rows plus the
// rows of every later step BELOW the pivot boundary, so each consumer re-runs
// against the refreshed snapshot instead of carrying a success that was
// computed from the old one.
//
// The boundary bound is load-bearing in both directions. Without a cascade the
// fix would merely invert the drift (fresh mirror, stale harbor-prewarm pinning
// chart versions the prewarm never mirrored — #5237 Phase A2-pins reads the
// mirror). Without the bound the cascade would reach step-08 and repeat the
// 10-minute deny-egress hold (t40 / TBD-V56). A re-run is only ever decided
// when nothing at/after the boundary has progressed, so the bounded set is
// exactly the steps that consumed the old snapshot.
//
// Returned as a patch map (empty-string values blank the keys) that the caller
// folds into the attempt seed, and applied to the in-memory priorStatus so the
// same run's skip checks and activity projection agree with the durable record.
func snapshotRerunCascade(steps []cutoverStep, priorStatus map[string]string, rerunIdx int) map[string]string {
	clear := map[string]string{}
	if rerunIdx < 0 || rerunIdx >= len(steps) {
		return clear
	}
	boundaryOrder := snapshotRerunBoundaryOrder(steps, steps[rerunIdx].order)
	for i := rerunIdx; i < len(steps); i++ {
		if i != rerunIdx && steps[i].order >= boundaryOrder {
			continue
		}
		name := steps[i].stepName
		for _, suffix := range []string{".result", ".startedAt", ".finishedAt", ".jobName"} {
			key := "step." + name + suffix
			if _, present := priorStatus[key]; !present {
				continue
			}
			clear[key] = ""
			delete(priorStatus, key)
		}
	}
	return clear
}

// assertMirrorSnapshotFresh is the pre-pivot invariant (#5437 defence in
// depth): flux-gitrepository-patch MUST NOT point Flux at a mirror snapshot
// older than the attempt that pushed the artifacts into the local registry.
//
// That combination is precisely the hw290 outage — the mirror pins chart /
// image versions that the fresh harbor-prewarm never mirrored, so the first
// post-pivot reconcile rolls the control plane onto tags the local registry
// answers `NotFound` for. Failing here converts a silently unpullable control
// plane into a loud, early step failure with the cutover still recoverable.
//
// status is the LIVE status map (re-read at pivot time, so a mirror re-run
// earlier in this same attempt is already reflected). Returns nil when the
// chain carries no gitea-mirror step — there is no snapshot to assert.
func assertMirrorSnapshotFresh(steps []cutoverStep, status map[string]string, attemptStart time.Time) error {
	hasMirror := false
	for _, s := range steps {
		if s.stepName == cutoverStepGiteaMirror {
			hasMirror = true
			break
		}
	}
	if !hasMirror {
		return nil
	}
	recorded := status["step."+cutoverStepGiteaMirror+".finishedAt"]
	if ts, err := time.Parse(time.RFC3339, recorded); err == nil && !ts.Before(attemptStart) {
		return nil
	}
	when := recorded
	if when == "" {
		when = "<unrecorded>"
	}
	return fmt.Errorf(
		"refusing to pivot Flux onto a stale mirror: step %s last succeeded at %s, before this attempt started at %s. "+
			"The snapshot pins chart/image versions that this attempt's %s never pushed to the local registry, so the first "+
			"post-pivot reconcile would roll workloads onto tags the local registry answers NotFound for (#5437, hw290 "+
			"catalyst-api ImagePullBackOff). Refresh the snapshot before re-firing: blank the step.%s.* rows on the "+
			"%s ConfigMap and delete the leftover cutover-%s-* Job so the engine re-mirrors from the top",
		cutoverStepGiteaMirror, when, attemptStart.UTC().Format(time.RFC3339),
		cutoverStepHarborPrewarm, cutoverStepGiteaMirror,
		cutoverStatusConfigMapName(), cutoverStepGiteaMirror)
}
