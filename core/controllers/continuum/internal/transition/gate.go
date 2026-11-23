package transition

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// A move and a drain take turns.
//
// The reboot agent (kured) drains a node by evicting its Pods; a drain during
// a move re-creates the very database Pod a phase waits on (hw307, 21 Nov
// 2026: the old primary's only instance was re-created 60 s into a failback
// and never published its demotion token). So:
//
//   - before the first step of a planned move, every region of the move
//     (its old and its new primary) that reports its reboot agent must hold
//     it: kured's single lock is held by something that is no node, so kured
//     cannot start a drain (RebootHoldPending);
//   - before every step, no region of the move may have a node that kured
//     holds its lock for and has cordoned (NodeDraining);
//   - before every step, the reboot agent of every region of the move must
//     have been read: a read that failed (a refusal, a timeout, a 5xx) is not
//     "no agent", it is not knowing whether a node is drained or the hold is
//     taken (RebootAgentUnknown).
//
// Each wait is bounded by Policy.RebootWait, counted from when the move began
// waiting (Record.RebootWaitSince). Past the bound the move goes on and the
// Interrupted condition says so (RebootWaitExceeded): a node that never comes
// back from its restart must not stop a move for ever, and a move interrupted
// by a drain still converges (the demotion is issued again, Demotion). While
// the gate holds a step that has begun, the step's phase clock stands still
// (Record.GateHeld): a bounded wait for the reboot agent does not mark the
// move Failed.
//
// A region-loss move does not wait: the old region is gone, and the promotion
// in the survivor does not depend on its Pods staying put (CNPG records
// status.lastPromotionToken whenever the target primary runs, whichever Pod
// promoted it).
//
// A region that is unreachable, or reports no reboot agent (nil: a Continuum
// that predates the report), adds no wait: the steps' own waits cover an
// unreachable region, and a region without the report cannot be held.

// involved lists the regions of the move whose reboot agent the move waits
// on: the old primary and the new one.
func involved(rec *Record) []string {
	if rec.From == rec.To {
		return []string{rec.From}
	}
	return []string{rec.From, rec.To}
}

// reboot is a region's reboot agent when the move waits on it: the region is
// reachable and reports an agent, or reports that it could not read it.
func (e evaluator) reboot(region string) *RebootStatus {
	rv := e.v.Regions[region]
	if !rv.Reachable || rv.Reboot == nil || (!rv.Reboot.Agent && !rv.Reboot.Unknown) {
		return nil
	}
	return rv.Reboot
}

func (e evaluator) rebootWait() time.Duration {
	if e.p.RebootWait > 0 {
		return e.p.RebootWait
	}
	return DefaultRebootWait
}

// rebootGate returns a wait message when the next step must wait for the
// reboot agent, and the interruption to report (a wait, or a wait that
// reached its bound and no longer holds the step).
func (e evaluator) rebootGate(starting bool) (string, *Interruption) {
	if e.rec.LossMode {
		e.rec.RebootWaitSince = ""
		return "", nil
	}
	// A drain is named before a hold not yet taken: it is what the move
	// waits for.
	reason, why := "", ""
	for _, r := range involved(e.rec) {
		if rs := e.reboot(r); rs != nil && len(rs.Draining) > 0 {
			reason = ReasonNodeDraining
			why = fmt.Sprintf("node %s of region %s is being drained by the reboot agent (kured holds its lock for it and has cordoned it)", strings.Join(rs.Draining, ", "), r)
			break
		}
	}
	for _, r := range involved(e.rec) {
		if rs := e.reboot(r); reason == "" && rs != nil && rs.Unknown {
			reason = ReasonRebootAgentUnknown
			why = fmt.Sprintf("the reboot agent of region %s could not be read (%s); the move waits until it is known whether a node there can be drained", r, rs.Message)
		}
	}
	for _, r := range involved(e.rec) {
		if rs := e.reboot(r); reason == "" && starting && rs != nil && !rs.Unknown && rs.Holdable && !rs.Held {
			lock := "free"
			if rs.Holder != "" {
				lock = "held by node " + rs.Holder
			}
			reason = ReasonRebootHoldPending
			why = fmt.Sprintf("region %s does not hold its reboot agent yet (kured's lock is %s); the move starts when no node of its regions can be drained", r, lock)
		}
	}
	if reason == "" {
		e.rec.RebootWaitSince = ""
		return "", nil
	}
	if e.rec.RebootWaitSince == "" {
		e.rec.RebootWaitSince = ts(e.v.Now)
	}
	since, ok := parseTS(e.rec.RebootWaitSince)
	if !ok {
		since = e.v.Now
	}
	if e.v.Now.Sub(since) <= e.rebootWait() {
		return "waiting: " + why, &Interruption{Reason: reason,
			Message: fmt.Sprintf("%s; waiting since %s, at most %s", why, e.rec.RebootWaitSince, e.rebootWait())}
	}
	return "", &Interruption{Reason: ReasonRebootWaitExceeded,
		Message: fmt.Sprintf("%s; the move waited %s since %s, its bound, and goes on without waiting for the reboot agent", why, e.rebootWait(), e.rec.RebootWaitSince)}
}

// waited is how long a wait of the reboot gate that began at since has held
// the move up to now, at most its bound.
func (e evaluator) waited(since time.Time) time.Duration {
	d := e.v.Now.Sub(since)
	if b := e.rebootWait(); d > b {
		d = b
	}
	if d < 0 {
		d = 0
	}
	return d
}

// gateHeld is how long the reboot gate held step a after it had begun: the
// waits that ended (Record.GateHeld) and a wait still counted (one past its
// bound, which no longer holds the step), each at most its bound. The step's
// phase deadline moves by that much.
func (e evaluator) gateHeld(a Activity, started time.Time) time.Duration {
	d := time.Duration(e.rec.GateHeld[a.ID]) * time.Second
	if since, ok := parseTS(e.rec.RebootWaitSince); ok && !started.After(since) {
		d += e.waited(since)
	}
	return d
}

// endGateHold records a wait of the reboot gate that ended this evaluation at
// step a (prevSince is Record.RebootWaitSince before the gate was asked), if
// the step had begun before the wait.
func (e evaluator) endGateHold(a Activity, prevSince string) {
	if prevSince == "" || e.rec.RebootWaitSince != "" || a.StartedAt == "" {
		return
	}
	since, ok := parseTS(prevSince)
	started, ok2 := parseTS(a.StartedAt)
	if !ok || !ok2 || started.After(since) {
		return
	}
	if e.rec.GateHeld == nil {
		e.rec.GateHeld = map[string]int64{}
	}
	e.rec.GateHeld[a.ID] += int64(e.waited(since) / time.Second)
}

// HoldOf says whether the move recorded in r holds the reboot agent of region
// at now (nil: no move of the region is in progress — Transitioning or
// Failed). A move holds it while it can still act, with a bound:
//
//   - a move whose demotion was issued again ReissueAttempts times without a
//     token, and is re-issued no more, releases it
//     (ReasonReleasedReissueExhausted);
//   - a Failed move (a step past its deadline) is still evaluated and may
//     still commit, so it keeps it, but at most wait (0 = DefaultRebootWait)
//     past Record.FailedSince (ReasonReleasedFailedMove). Otherwise every
//     node of both regions would go without its security restarts, and the
//     reboot agent's own checks would wait on the lock, for as long as the
//     move stays Failed.
//
// A move that acts again (the Failed step is reached, the next step begins)
// is no longer Failed and holds the agent again.
func HoldOf(r *Record, region string, wait time.Duration, now time.Time) *RebootHold {
	if r == nil || (r.Outcome != OutcomeTransitioning && r.Outcome != OutcomeFailed) || (region != r.From && region != r.To) {
		return nil
	}
	if wait <= 0 {
		wait = DefaultRebootWait
	}
	move := fmt.Sprintf("the move %s → %s (%s)", r.From, r.To, r.ID)
	keys := make([]string, 0, len(r.Demotions))
	for k := range r.Demotions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	done := map[string]bool{}
	for _, a := range r.Activity {
		done[a.ID] = a.State == StateSucceeded
	}
	for _, k := range keys {
		if d := r.Demotions[k]; d.Exhausted && !done[PhaseTrafficOff+"-"+strings.Replace(k, "/", "-", 1)] {
			return &RebootHold{Reason: ReasonReleasedReissueExhausted, Message: fmt.Sprintf(
				"%s: the demotion of %s in %s was issued again %d times without a demotion token and is re-issued no more; the reboot agent of region %s is released",
				move, k, r.From, d.Attempts, region)}
		}
	}
	if r.Outcome == OutcomeFailed {
		since, ok := parseTS(r.FailedSince)
		if ok && now.Sub(since) > wait {
			return &RebootHold{Reason: ReasonReleasedFailedMove, Message: fmt.Sprintf(
				"%s has been Failed since %s, longer than %s (rebootAgent.waitSeconds): the reboot agent of region %s is released so that its nodes restart for their updates; the move is still evaluated and holds it again if it acts",
				move, r.FailedSince, wait, region)}
		}
		until := "its bound"
		if ok {
			until = ts(since.Add(wait))
		}
		return &RebootHold{Held: true, Reason: ReasonHeldForMove, Message: fmt.Sprintf(
			"%s is Failed but still evaluated: the reboot agent of region %s stays held until %s at most (rebootAgent.waitSeconds)",
			move, region, until)}
	}
	return &RebootHold{Held: true, Reason: ReasonHeldForMove, Message: fmt.Sprintf(
		"%s is in progress: the reboot agent of region %s is held, so no node there is drained until the move ends", move, region)}
}
