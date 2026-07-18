// Step-8 — re-clone-on-divergence (#5125 Defect-2).
//
// The 7-step Sequencer (sequence.go) drives a NEW switchover
// (validate-lease → cordon → drain → dns → swap-lease → promote →
// audit). It has no opinion about what happens LATER, when a region
// that was down resumes and its cnpg-pair half tries to rejoin as the
// standby of whichever half was promoted during the outage.
//
// The hw256 G12 region-kill walk (2026-07-15) proved that rejoin can
// wedge: on region-a resume, its cnpg-pair Cluster CR (now flipped to
// spec.replica.enabled=true by the switchover's promote step) tries to
// stream from the newly-promoted primary, and CNPG's walreceiver
// crash-loops with `highest timeline N of the primary is behind
// recovery timeline N+1` — the standby's own recovery history has
// advanced past what the primary can serve (typically because the
// standby itself was briefly promoted, or did its own local crash
// recovery, while the primary was unreachable). A plain reconnect can
// never resolve this; CNPG requires a full re-bootstrap. The proven
// manual heal is "delete the replica Cluster CR + re-apply" (~2-3
// min/cluster to pg_basebackup back to a consistent state).
//
// CheckRejoin is that heal, automated + bounded. It is NOT part of
// Execute()'s atomic step chain — it is invoked once per reconcile
// tick (continuum_controller.go's runPerCR, alongside the existing
// #4901 standby-availability check, reusing the SAME FindPair + Get
// calls) so it naturally only becomes live once region-a's own
// cnpg-pair Cluster CR status is observable again post-resume.
package switchover

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// DefaultRejoinMaxAttempts + DefaultRejoinCooldown — see RejoinOptions.
const (
	DefaultRejoinMaxAttempts = 3
	DefaultRejoinCooldown    = 3 * time.Minute
)

// RejoinOptions configures step-8. Zero-value is filled by Defaults().
type RejoinOptions struct {
	// MaxAttempts caps how many re-clone attempts step-8 will issue for
	// a single divergence episode before giving up and failing loud
	// (RejoinResult.Bounded=true). Default 3.
	MaxAttempts int

	// Cooldown is the minimum wait between attempts, scaled linearly by
	// the attempt number (attempt 1 fires immediately on detection;
	// attempt 2 waits ≥1×Cooldown after attempt 1; attempt 3 waits
	// ≥2×Cooldown after attempt 2; …) — simple backoff so a re-clone
	// that takes the proven ~2-3 min to converge is never interrupted
	// by another delete+recreate on the very next reconcile tick.
	// Default 3 minutes.
	Cooldown time.Duration
}

// Defaults applies missing-field defaults. Returns a copy.
func (o RejoinOptions) Defaults() RejoinOptions {
	out := o
	if out.MaxAttempts <= 0 {
		out.MaxAttempts = DefaultRejoinMaxAttempts
	}
	if out.Cooldown <= 0 {
		out.Cooldown = DefaultRejoinCooldown
	}
	return out
}

// RejoinState is the bookkeeping CheckRejoin carries ACROSS reconcile
// ticks for one Continuum CR. The caller (continuum_controller.go)
// keeps this in the per-CR goroutine's in-memory state (the same
// session-scoped pattern already used for the F-1 spec-fingerprint
// drift check) and mirrors it onto the CR's status for operator
// visibility.
type RejoinState struct {
	// Attempts is the number of re-clone attempts issued so far for the
	// CURRENT divergence episode. Reset to 0 the moment the standby
	// stops reporting divergence (healthy rejoin, or a fresh re-clone
	// wiped its status).
	Attempts int

	// LastAttemptAt is the wall-clock time of the most recent attempt.
	// Zero means no attempt has been issued yet.
	LastAttemptAt time.Time

	// Bounded is true once Attempts has exhausted MaxAttempts without
	// the standby converging. Once true, CheckRejoin stops issuing new
	// attempts automatically — it fails loud (RejoinResult.Err set)
	// every subsequent tick until an operator intervenes and the
	// standby's status changes (which naturally clears Bounded, since a
	// fresh RejoinState is returned once DetectDivergence stops firing).
	Bounded bool

	// Target is the Cluster CR name step-8 last (re-)cloned. Surfaced
	// on the CR status for observability.
	Target string
}

// RejoinResult is what CheckRejoin returns for THIS tick — the caller
// uses it to decide whether to patch status / emit an audit event.
type RejoinResult struct {
	// Diverged reports whether DetectDivergence fired this tick (even
	// if no new attempt was issued because we're inside the cooldown
	// window, or already Bounded).
	Diverged bool

	// Attempted is true iff this tick actually issued a
	// CNPG.RecloneCluster call.
	Attempted bool

	// Bounded is true iff MaxAttempts is now exhausted — fail-loud,
	// operator intervention required.
	Bounded bool

	// Target is the Cluster CR name involved (standby side).
	Target string

	// Message is a human-readable summary for the audit/status message.
	Message string

	// Err is non-nil when an attempted re-clone failed, OR when Bounded
	// just became true. Never set for the plain "no divergence" /
	// "within cooldown" cases — those are healthy no-ops, not errors.
	Err error
}

// DetectDivergence decides whether the CR CURRENTLY ACTING AS STANDBY
// of a cnpg-pair (the CR reporting spec.replica.enabled=true, i.e.
// Status.IsReplicaCluster) has left the timeline history the CR
// CURRENTLY ACTING AS PRIMARY (IsReplicaCluster=false) can serve.
//
// Deliberately narrow gate — this must NEVER fire on an ordinary
// transient reconnect blip or ordinary lag:
//
//  1. Exactly one of the two inputs must be acting as standby right
//     now (the other must be acting as primary). Both-standby /
//     both-primary / unresolved states are a mid-switchover or
//     malformed pair — never guess; report no divergence.
//  2. The standby must be un-Ready. A Ready standby is, by definition,
//     successfully streaming — that IS a healthy rejoin, regardless of
//     what its timeline history says.
//  3. Both sides must have reported a TimelineID (CNPG populates this
//     once it has enough status to say; a fresh/absent value means
//     "insufficient data", not "healthy" and not "diverged" — never
//     guess on absent data).
//  4. The standby's TimelineID must be STRICTLY GREATER than the
//     primary's. This is the literal structured form of "highest
//     timeline of the primary is behind the standby's recovery
//     timeline" and is exactly what separates a genuine re-clone-
//     needed divergence from an ordinary catch-up-in-progress standby
//     (whose timeline is behind or equal, and which will heal itself
//     once it catches up).
//
// `aName`/`bName` identify which of the two STATUS inputs the caller
// should re-clone if this reports diverged=true — the caller only
// knows them by their STATIC pair-label role ("primary-labeled" /
// "replica-labeled" per cnpg.PairRoleLabel), which does NOT necessarily
// match which one is playing which CNPG role after a failover, so the
// live spec.replica.enabled flag (not the label) decides.
func DetectDivergence(aName string, aStatus cnpg.Status, bName string, bStatus cnpg.Status) (target string, diverged bool, reason string) {
	switch {
	case aStatus.IsReplicaCluster && !bStatus.IsReplicaCluster:
		return evalDivergence(aName, aStatus, bName, bStatus)
	case bStatus.IsReplicaCluster && !aStatus.IsReplicaCluster:
		return evalDivergence(bName, bStatus, aName, aStatus)
	default:
		// Both replica, both primary, or some other ambiguous
		// combination (mid-switchover in-flight, malformed pair). Never
		// guess — no-op.
		return "", false, ""
	}
}

func evalDivergence(standbyName string, standby cnpg.Status, primaryName string, primary cnpg.Status) (string, bool, string) {
	if standby.Ready {
		// Healthy rejoin — the standby is successfully streaming.
		return "", false, ""
	}
	if !standby.HasTimelineID || !primary.HasTimelineID {
		// Insufficient data to judge — could be a fresh/bootstrapping
		// CR that hasn't populated status.timelineID yet. Never guess.
		return "", false, ""
	}
	if standby.TimelineID <= primary.TimelineID {
		// Un-Ready but NOT ahead of the primary's timeline — an
		// ordinary transient reconnect / catch-up-in-progress standby,
		// not a divergence. Must NOT trigger a re-clone.
		return "", false, ""
	}
	reason := fmt.Sprintf(
		"standby %q not Ready with TimelineID=%d strictly ahead of primary %q TimelineID=%d — walreceiver timeline-divergence (highest timeline of the primary is behind the standby's recovery timeline); re-clone required",
		standbyName, standby.TimelineID, primaryName, primary.TimelineID,
	)
	return standbyName, true, reason
}

// CheckRejoin is step-8. Call it once per reconcile tick with the
// freshest (primaryName, primaryStatus, replicaName, replicaStatus) —
// the SAME values continuum_controller.go's runPerCR already computes
// for the #4901 standby-availability check, so no extra CNPG API calls
// are needed.
//
// Returns the updated RejoinState (the caller persists it for the next
// tick) and a RejoinResult describing what happened this tick.
//
// Bounded + backoff: an already-Bounded prior state short-circuits to
// fail-loud without touching the cluster again. A fresh (non-Bounded)
// divergence outside the cooldown window issues ONE RecloneCluster
// call; a divergence still inside the cooldown window is a no-op this
// tick (waiting for the in-flight re-clone to converge). The state
// resets to zero the instant DetectDivergence stops firing (healthy
// rejoin, OR the re-clone's freshly-recreated CR has wiped its own
// status so there's nothing to judge yet).
func (s *Sequencer) CheckRejoin(ctx context.Context, namespace string, primaryName string, primaryStatus cnpg.Status, replicaName string, replicaStatus cnpg.Status, prior RejoinState, opts RejoinOptions) (RejoinState, RejoinResult) {
	opts = opts.Defaults()
	result := RejoinResult{}

	target, diverged, reason := DetectDivergence(primaryName, primaryStatus, replicaName, replicaStatus)
	if !diverged {
		// No divergence this tick — reset any prior episode state
		// (including a prior Bounded fail-loud latch: the standby
		// converged, or a fresh re-clone reset its status).
		return RejoinState{}, result
	}

	result.Diverged = true
	result.Target = target
	result.Message = reason

	if prior.Bounded && prior.Target == target {
		result.Bounded = true
		result.Err = fmt.Errorf("rejoin re-clone of %q did not converge after %d attempts: %s", target, opts.MaxAttempts, reason)
		result.Message = result.Err.Error()
		return prior, result
	}

	// A divergence on a DIFFERENT target than the prior episode is a
	// fresh episode — never carry over another CR's bounded/attempt
	// state.
	state := prior
	if prior.Target != target {
		state = RejoinState{Target: target}
	}

	now := s.now()
	if !state.LastAttemptAt.IsZero() {
		backoff := time.Duration(state.Attempts) * opts.Cooldown
		if now.Sub(state.LastAttemptAt) < backoff {
			result.Message = fmt.Sprintf("%s (within backoff window, attempt %d/%d already issued at %s)", reason, state.Attempts, opts.MaxAttempts, state.LastAttemptAt.UTC().Format(time.RFC3339))
			return state, result
		}
	}

	nextAttempt := state.Attempts + 1
	if nextAttempt > opts.MaxAttempts {
		state.Bounded = true
		result.Bounded = true
		result.Err = fmt.Errorf("rejoin re-clone of %q did not converge after %d attempts: %s", target, opts.MaxAttempts, reason)
		result.Message = result.Err.Error()
		return state, result
	}

	if s.CNPG == nil {
		result.Err = errors.New("rejoin re-clone: CNPG reader is required")
		result.Message = result.Err.Error()
		return state, result
	}

	if err := s.CNPG.RecloneCluster(ctx, namespace, target); err != nil {
		// The attempt still counts (bounded backoff must advance even
		// on failure, or a persistently-erroring cluster would retry
		// every tick forever).
		state.Attempts = nextAttempt
		state.LastAttemptAt = now
		result.Err = fmt.Errorf("re-clone attempt %d/%d of %q failed: %w", nextAttempt, opts.MaxAttempts, target, err)
		result.Message = result.Err.Error()
		return state, result
	}

	state.Attempts = nextAttempt
	state.LastAttemptAt = now
	result.Attempted = true
	result.Message = fmt.Sprintf("re-clone attempt %d/%d issued for %q (delete+recreate Cluster CR to force pg_basebackup): %s", nextAttempt, opts.MaxAttempts, target, reason)
	return state, result
}
