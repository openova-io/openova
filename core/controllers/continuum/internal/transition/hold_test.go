package transition

import (
	"strings"
	"testing"
	"time"
)

// A move holds the reboot agent of its regions while it can still act, and
// no longer than a bound once it cannot.
func TestHoldOf_IsBounded(t *testing.T) {
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	wait := 20 * time.Minute
	rec := func(outcome string, failedFor time.Duration) *Record {
		r := &Record{ID: "b-to-a-1", From: "b", To: "a", Outcome: outcome}
		if outcome == OutcomeFailed {
			r.FailedSince = ts(now.Add(-failedFor))
		}
		return r
	}
	for _, tc := range []struct {
		name   string
		r      *Record
		region string
		tick   string // this evaluation's outcome; "" = the record's own
		held   bool
		reason string // "" = nil (nothing to say)
	}{
		{"no move", nil, "a", OutcomeConverged, false, ""},
		{"converged", rec(OutcomeConverged, 0), "a", "", false, ""},
		{"a region not of the move", rec(OutcomeTransitioning, 0), "c", "", false, ""},
		{"in progress", rec(OutcomeTransitioning, 0), "b", "", true, ReasonHeldForMove},
		{"failed within the bound", rec(OutcomeFailed, wait), "a", "", true, ReasonHeldForMove},
		{"failed past the bound", rec(OutcomeFailed, wait+time.Second), "a", "", false, ReasonReleasedFailedMove},
		{"failed, no failedSince (an older image acts)", &Record{ID: "b-to-a-1", From: "b", To: "a", Outcome: OutcomeFailed}, "a", "", false, ReasonReleasedFailedMove},
		// The record is still in flight, but this evaluation walks it no more.
		{"in flight, re-targeted back (Converged)", rec(OutcomeTransitioning, 0), "a", OutcomeConverged, false, ""},
		{"in flight, no file committed (Pending)", rec(OutcomeTransitioning, 0), "b", OutcomePending, false, ""},
		{"in flight, Blocked", rec(OutcomeTransitioning, 0), "b", OutcomeBlocked, false, ReasonReleasedBlockedMove},
		{"failed within the bound, Blocked", rec(OutcomeFailed, 0), "a", OutcomeBlocked, false, ReasonReleasedBlockedMove},
	} {
		tick := tc.tick
		if tick == "" {
			tick = tc.r.Outcome
		}
		h := HoldOf(tick, tc.r, tc.region, wait, now)
		switch {
		case tc.reason == "" && h != nil:
			t.Errorf("%s: want nothing, got %+v", tc.name, h)
		case tc.reason != "" && (h == nil || h.Held != tc.held || h.Reason != tc.reason || h.Message == ""):
			t.Errorf("%s: want held=%v %s, got %+v", tc.name, tc.held, tc.reason, h)
		}
	}
}

// Past the re-issue bound nothing more is re-issued: the move releases the
// reboot agent at once, and says why.
func TestHoldOf_ReleasedWhenTheReissueBoundIsExhausted(t *testing.T) {
	w, g := failback(t)
	w.interrupt["b"] = 100 // every demotion is interrupted
	var rec *Record
	var res Result
	exhaustedAt := -1
	for i := 0; i < 80; i++ {
		res, rec = w.tick(g, rec, policy)
		if res.Interruption != nil && res.Interruption.Reason == ReasonDemotionReissueExhausted && exhaustedAt < 0 {
			exhaustedAt = i
		}
		if h := HoldOf(res.Outcome, rec, "b", 0, w.now); exhaustedAt < 0 && (h == nil || !h.Held) {
			t.Fatalf("tick %d: before the bound is exhausted the move holds the agent, got %+v", i, h)
		}
	}
	if exhaustedAt < 0 {
		t.Fatal("the re-issue bound was never exhausted: the test proves nothing")
	}
	for _, r := range []string{"a", "b"} {
		h := HoldOf(res.Outcome, rec, r, time.Hour, w.now)
		if h == nil || h.Held || h.Reason != ReasonReleasedReissueExhausted || !strings.Contains(h.Message, "acme/wp") {
			t.Fatalf("%s: past the re-issue bound the hold is released and says why, got %+v", r, h)
		}
	}
	// The old primary runs a primary again (its operator recovers): the move
	// can act again and holds the agent again.
	db := &w.live["acme/wp"]["b"].DB
	db.Primary, db.RunningPrimary = true, True
	res, rec = w.tick(g, rec, policy)
	if h := HoldOf(res.Outcome, rec, "b", time.Hour, w.now); h == nil || !h.Held {
		t.Fatalf("a move that can act again holds the agent again: %+v (%s %+v)", h, res.Outcome, rec.Demotions)
	}
}

// The phase clock does not run while the reboot gate holds a step that has
// begun: a bounded wait for a drain mid-step does not mark the move Failed.
func TestAWaitForTheRebootAgentMidStep_DoesNotCountAgainstThePhaseDeadline(t *testing.T) {
	w, g := failback(t)
	w.reboot["a"], w.reboot["b"] = held("continuum-a"), held("continuum-b")
	p := policy // PhaseTimeout 5 minutes
	p.RebootWait = 6 * time.Minute
	res := Plan(g, nil, w.view(), p)
	if res.Commit == nil || res.Commit.Region != "b" {
		t.Fatalf("want traffic-off committed: %+v", res)
	}
	rec := res.Record
	w.commit(*res.Commit) // Flux does not apply it yet
	tick := func(apply bool) Result {
		t.Helper()
		w.now = w.now.Add(10 * time.Second)
		r := Plan(g, rec, w.view(), p)
		rec = r.Record
		if r.Commit != nil {
			w.commit(*r.Commit)
		}
		if apply {
			w.apply()
		}
		if r.Outcome == OutcomeFailed {
			t.Fatalf("the move was marked Failed although it only waited for the reboot agent: %s", r.Message)
		}
		return r
	}
	// Three minutes into traffic-off (Flux is slow) ...
	for i := 0; i < 18; i++ {
		tick(false)
	}
	// ... the reboot agent drains a node of a for four minutes ...
	w.reboot["a"] = &RebootStatus{Agent: true, Holdable: true, Holder: "wa1", Draining: []string{"wa1"}}
	for i := 0; i < 24; i++ {
		if r := tick(false); r.Interruption == nil || r.Interruption.Reason != ReasonNodeDraining {
			t.Fatalf("the drain must hold the step: %+v", r.Interruption)
		}
	}
	// ... and is done; Flux takes one more minute. The step has run eight
	// minutes, four of them held by the gate: within its five-minute deadline.
	w.reboot["a"] = held("continuum-a")
	for i := 0; i < 6; i++ {
		tick(false)
	}
	if got := rec.GateHeld["traffic-off-acme-wp"]; got != 240 {
		t.Fatalf("the step must be credited the four minutes the gate held it, got %ds (%v)", got, rec.GateHeld)
	}
	var last Result
	for i := 0; i < 40 && last.Outcome != OutcomeConverged; i++ {
		last = tick(true)
	}
	if last.Outcome != OutcomeConverged {
		t.Fatalf("%s: %s", last.Outcome, last.Message)
	}
}

// A wait past its bound no longer holds the step: from the bound on its clock
// runs again, and it is credited only the bound.
func TestAWaitPastItsBound_IsCreditedOnlyTheBound(t *testing.T) {
	w, g := failback(t)
	w.reboot["a"], w.reboot["b"] = held("continuum-a"), held("continuum-b")
	p := policy // PhaseTimeout 5 minutes
	p.RebootWait = 2 * time.Minute
	res := Plan(g, nil, w.view(), p)
	if res.Commit == nil {
		t.Fatalf("want traffic-off committed: %+v", res)
	}
	start := w.now
	rec := res.Record
	w.commit(*res.Commit) // Flux never applies it: the step never completes
	for i := 0; i < 6; i++ {
		w.now = w.now.Add(10 * time.Second)
		rec = Plan(g, rec, w.view(), p).Record
	}
	// A drain from minute 1 that does not end: the gate holds the step two
	// minutes, then the move goes on; the step fails at 5 + 2 minutes.
	w.reboot["a"] = &RebootStatus{Agent: true, Holdable: true, Holder: "wa1", Draining: []string{"wa1"}}
	for {
		w.now = w.now.Add(10 * time.Second)
		r := Plan(g, rec, w.view(), p)
		rec = r.Record
		age := w.now.Sub(start)
		if r.Outcome == OutcomeFailed {
			if age <= p.PhaseTimeout+p.RebootWait {
				t.Fatalf("failed at %s, before its deadline plus the bound the gate held it", age)
			}
			break
		}
		if age > p.PhaseTimeout+p.RebootWait+10*time.Second {
			t.Fatalf("not failed at %s: a wait past its bound is credited only the bound", age)
		}
	}
}
