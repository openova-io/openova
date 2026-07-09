package handler

import (
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// Console-unreachable self-heal (#4746 residual) — the OutcomeReady sibling of
// the #3319 converged-late rescue in phase1_converged_late.go.
//
// #4746 fixed the ORIGINAL false-'failed' on a slow 2-region prov two ways:
// the ready-sentinel (helmwatch.DefaultReadySentinelComponent) stops
// OutcomeReady firing before bp-catalyst-platform — the console's OWN backend —
// is even observed, and the #4706 external console-reachability gate now waits
// consoleProbeBudget (35m, up from the 90s that false-failed hw221/hw222) for
// the public console front door to answer. But BOTH of those only bound the
// FIRST probe; neither re-probes after it. The issue's own root-cause point #4
// named the residual harm exactly: once markPhase1Done stamps `failed` for
// OutcomeReady + console-unreachable, "the watch already terminated — nothing
// re-probes when the console DOES come up. Permanent false-negative." A
// converged Sovereign whose gateway/DNS crosses the public boundary a few
// minutes past the 35m budget (or that was transiently unreachable at the exact
// tick the budget expired) is then latched `failed` FOREVER — inviting the
// wrong wipe of a perfectly healthy, converged env.
//
// The converged-late rescue does NOT cover this: it gates on
// outcome==OutcomeTimeout, whereas the console-unreachable failure carries
// outcome==OutcomeReady (Flux fully converged — strictly MORE done than a
// timeout, yet strictly LESS recoverable). This hook closes that asymmetry, so
// a catalyst-api roll re-probes the converged record and heals it zero-touch
// the moment the console answers.
//
// Safety mirrors the converged-late rescue exactly: it re-runs the SAME hard
// console probe (h.consoleProbe → defaultConsoleReachable, the < 400 front-door
// gate), so it can ONLY EVER upgrade failed→ready on a POSITIVE, externally-
// observed console answer — the hw217/hw218 false-GREEN this probe exists to
// kill stays killed. A probe error leaves the record `failed` untouched (it
// never invents readiness). Every downstream step (fireHandover + the
// post-handover producer chain) is idempotent and self-guarding, identical to
// the converged-late chain.

// shouldConsoleUnreachableRescue gates the startup re-probe hook. Read-only.
// Qualifies iff the record is `failed` with Phase1Outcome==OutcomeReady (Flux
// converged; ONLY the external console probe failed), the handover has not
// already fired, Phase-1 has terminated, and the production console gate is
// wired (h.consoleProbe != nil — unit tests without a stub never touch the
// network). A failed+timeout / failed+hard-failure / already-ready / already-
// handed-over record never qualifies.
func (h *Handler) shouldConsoleUnreachableRescue(dep *Deployment) bool {
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" || dep.Result == nil {
		return false
	}
	// Only the console-unreachable failure carries OutcomeReady. A
	// timeout / hard-failure / flux-not-reconciling record is NOT a
	// console-unreachable false-negative — leave those to their own paths
	// (the converged-late rescue owns OutcomeTimeout).
	if dep.Result.Phase1Outcome != helmwatch.OutcomeReady {
		return false
	}
	if dep.Result.HandoverFiredAt != nil {
		return false
	}
	if dep.Result.Phase1FinishedAt == nil {
		return false
	}
	// The production console gate must be wired to re-probe; a Handler with
	// no consoleProbe (unit tests, or a build with the #4706 gate off) has
	// nothing to re-evaluate against and must not flip blind.
	return h.consoleProbe != nil
}

// runConsoleUnreachableRescue re-probes the public console for a converged
// record that was stamped `failed` only because the console had not crossed the
// public boundary within the first probe's budget. On a POSITIVE answer it
// flips the record ready (clearing the stale #4706 Error) and fires the full,
// idempotent handover chain; on a negative answer it leaves the record `failed`
// untouched. Run on a goroutine from the restore loop.
func (h *Handler) runConsoleUnreachableRescue(dep *Deployment) {
	if h.consoleProbe == nil {
		return
	}
	if err := h.consoleProbe(dep.Request.SovereignFQDN); err != nil {
		h.log.Info("console-unreachable rescue: console still not externally reachable; record stays failed",
			"id", dep.ID, "err", err)
		return
	}

	now := time.Now().UTC()
	dep.mu.Lock()
	// Re-check under lock — a concurrent resume/flip (or a second rescue
	// goroutine) loses. ONLY a still-`failed`, still-OutcomeReady,
	// still-unfired record may be upgraded, so a record that another path
	// already advanced is never double-processed.
	if dep.Status != "failed" || dep.Result == nil ||
		dep.Result.Phase1Outcome != helmwatch.OutcomeReady ||
		dep.Result.HandoverFiredAt != nil {
		dep.mu.Unlock()
		return
	}
	dep.Status = "ready"
	// Clear the stale #4706 "console NOT externally reachable" Error — a
	// ready deployment must not carry a failure message the wizard's
	// FailureCard would render as a hard failure (see markPhase1Done).
	dep.Error = ""
	if dep.Result.Phase1FinishedAt == nil {
		dep.Result.Phase1FinishedAt = &now
	}
	dep.mu.Unlock()
	h.persistDeployment(dep)
	h.log.Info("console-unreachable RESCUE: converged record re-probed and the console now answers — flipped to ready, firing full handover chain (#4746)",
		"id", dep.ID)

	// The full OutcomeReady chain, each step idempotent/self-guarding —
	// identical to the converged-late rescue (phase1_converged_late.go).
	// fireHandover + the sweep run inline (both no-op when their deps are
	// unwired); the three producers ride spawnPostHandoverHook so the
	// fire-and-forget shape stays test-suppressible.
	h.fireHandover(dep)
	h.runHandoverJobSweep(dep)
	h.spawnPostHandoverHook(func() { h.runPostHandoverPolicyEnforceFlip(dep) })
	h.spawnPostHandoverHook(func() { h.runPostHandoverAdoptionApply(dep) })
	h.spawnPostHandoverHook(func() { h.runPostHandoverSpineApplications(dep) })
	h.spawnPostHandoverHook(func() { h.runPostHandoverGatewayELB(dep) })
}
