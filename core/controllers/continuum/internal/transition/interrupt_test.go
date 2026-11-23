package transition

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// The measured defect (hw307, 21 Nov 2026, journey J4): a failback b → a
// committed traffic-off, the reboot agent drained the node of b's only
// database instance, the instance was re-created while CNPG applied the
// demotion, came back as a standby, and never published a demotion token;
// traffic-off waited until the gate run gave up.

func failback(t *testing.T) (*world, Group) {
	t.Helper()
	m := Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: []string{"a", "b"}}
	w := newWorld(t, []string{"a", "b"}, []Member{m}, "b")
	g, errs := Groups([]Member{m})
	if len(errs) > 0 || len(g) != 1 {
		t.Fatalf("groups: %v %v", g, errs)
	}
	w.setDesired("a")
	return w, g[0]
}

// tick evaluates once, applies the commit and the native operators, and
// advances the clock.
func (w *world) tick(g Group, rec *Record, p Policy) (Result, *Record) {
	res := Plan(g, rec, w.view(), p)
	if res.Record != nil {
		rec = res.Record
	}
	if res.Commit != nil {
		w.commit(*res.Commit)
	}
	w.apply()
	w.now = w.now.Add(10 * time.Second)
	return res, rec
}

func TestDemotionInterruptedByARecreatedPod_IsIssuedAgainAndConvergesWithinTheBound(t *testing.T) {
	w, g := failback(t)
	w.interrupt["b"] = 1
	var rec *Record
	var res Result
	reasons := map[string]bool{}
	for i := 0; i < 60; i++ {
		res, rec = w.tick(g, rec, policy)
		if res.Interruption != nil {
			reasons[res.Interruption.Reason] = true
		}
		if c := res.Commit; c != nil && c.Region == "b" && c.Fields.Primary == "b" {
			// The old primary is restored, and CNPG takes a while to run it
			// as a primary again: the demotion is not committed once more
			// before it does (only a running primary's demotion yields a
			// token).
			db := &w.live["acme/wp"]["b"].DB
			saved := *db
			db.RunningPrimary = False // Ready, still in recovery
			for j := 0; j < 3; j++ {
				res, rec = w.tick(g, rec, policy)
				if res.Commit != nil || res.Interruption == nil || res.Interruption.Reason != ReasonDemotionInterrupted {
					t.Fatalf("the demotion was issued again before b ran a primary: %+v %+v", res.Commit, res.Interruption)
				}
			}
			*db = saved
		}
		if res.Commit == nil && res.Outcome != OutcomeTransitioning {
			break
		}
	}
	if res.Outcome != OutcomeConverged {
		t.Fatalf("an interrupted demotion must still converge, got %s: %s", res.Outcome, res.Message)
	}
	// Demote (lost), restore the old primary, demote again, promote with the
	// token of the demotion that completed.
	if got, want := commitsString(w.commits), "wp@b:a wp@b:b wp@b:a wp@a:a+tok-b"; got != want {
		t.Fatalf("commits\n got %s\nwant %s", got, want)
	}
	if !reasons[ReasonDemotionInterrupted] {
		t.Fatalf("the re-issue must be named in the Interrupted condition, saw %v", reasons)
	}
	if res.Interruption != nil || rec.Interruption != nil {
		t.Fatalf("a converged move carries no interruption: %+v", res.Interruption)
	}
	if d := rec.Demotions["acme/wp"]; d.Attempts != 1 || d.Restoring {
		t.Fatalf("demotion state: %+v", d)
	}
	for _, a := range rec.Activity {
		if a.State != StateSucceeded {
			t.Fatalf("activity %+v not succeeded", a)
		}
		start, _ := parseTS(a.StartedAt)
		end, _ := parseTS(a.FinishedAt)
		if end.Sub(start) > policy.PhaseTimeout {
			t.Fatalf("%s took %s, beyond the phase deadline %s", a.ID, end.Sub(start), policy.PhaseTimeout)
		}
	}
	if !w.live["acme/wp"]["a"].DB.Primary || w.live["acme/wp"]["b"].DB.Primary {
		t.Fatal("the writer did not move to a")
	}
}

func TestDemotionIssuedAgain_IsBoundedAndSaysSoWhenTheBoundIsOver(t *testing.T) {
	w, g := failback(t)
	w.interrupt["b"] = 100 // every demotion is interrupted
	var rec *Record
	var res Result
	for i := 0; i < 80; i++ {
		res, rec = w.tick(g, rec, policy)
	}
	restores := strings.Count(commitsString(w.commits), "wp@b:b")
	if restores != ReissueAttempts {
		t.Fatalf("the demotion must be issued again exactly %d times, was %d (%s)", ReissueAttempts, restores, commitsString(w.commits))
	}
	for _, c := range w.commits {
		if c.Region == "a" {
			t.Fatalf("promoted a without a demotion token: %+v", c)
		}
	}
	if res.Outcome != OutcomeFailed || res.Interruption == nil || res.Interruption.Reason != ReasonDemotionReissueExhausted {
		t.Fatalf("past the bound the phase fails and says why: %s %+v", res.Outcome, res.Interruption)
	}
	if rec.Activity[0].State != StateFailed {
		t.Fatalf("traffic-off must report failed: %+v", rec.Activity[0])
	}
}

func TestPhaseRunTwice_IsHarmless(t *testing.T) {
	w, g := failback(t)
	w.interrupt["b"] = 1
	var rec *Record
	promoted := false
	for i := 0; i < 60; i++ {
		v := w.view()
		first := Plan(g, rec, v, policy)
		// The same inputs evaluated a second time (a restarted actor, a
		// second region): the same answer, so no step is taken twice.
		second := Plan(g, rec, v, policy)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("tick %d: a second evaluation differs\n%+v\n%+v", i, first, second)
		}
		if first.Record != nil {
			rec = first.Record
		}
		if c := first.Commit; c != nil {
			if promoted && c.Region == "b" {
				t.Fatalf("the old primary's file was changed after the new primary was committed: %+v", c)
			}
			promoted = promoted || c.Region == "a"
			// The same commit made twice changes nothing more.
			w.commit(*c)
			w.commit(*c)
		}
		w.apply()
		w.now = w.now.Add(10 * time.Second)
		if first.Commit == nil && first.Outcome == OutcomeConverged {
			break
		}
	}
	if rec.Outcome != OutcomeConverged {
		t.Fatalf("outcome %s: %s", rec.Outcome, rec.Message)
	}
	if rec.Tokens["acme/wp"] != "tok-b" || w.files["acme/wp"]["a"].Fields.PromotionToken != "tok-b" {
		t.Fatalf("the new primary must carry the token of the demotion that completed: %v / %+v", rec.Tokens, w.files["acme/wp"]["a"].Fields)
	}
	// Converged: evaluating again commits nothing.
	if res := Plan(g, rec, w.view(), policy); res.Commit != nil || res.Outcome != OutcomeConverged {
		t.Fatalf("after: %+v", res)
	}
}

// A promotion whose Pod is re-created still completes: CNPG records
// status.lastPromotionToken whenever the target primary runs (cloudnative-pg
// 1.29 instance_controller.go reconcilePrimary), so the wait is level-
// triggered on the Cluster's own status and nothing is re-issued.
func TestPromotionWithARecreatedPod_Converges(t *testing.T) {
	w, g := failback(t)
	var rec *Record
	var res Result
	for i := 0; i < 60; i++ {
		res, rec = w.tick(g, rec, policy)
		// The new primary's Pod restarts right after the promotion commit:
		// for one tick the Cluster is not Ready and reports no token.
		if res.Commit != nil && res.Commit.Region == "a" {
			db := &w.live["acme/wp"]["a"].DB
			saved := *db
			db.Ready, db.LastPromotionToken, db.RunningPrimary = false, "", False
			res, rec = w.tick(g, rec, policy)
			if res.Outcome != OutcomeTransitioning || res.Commit != nil {
				t.Fatalf("while the promoted Pod restarts the move waits: %+v", res)
			}
			*db = saved
		}
		if res.Commit == nil && res.Outcome == OutcomeConverged {
			break
		}
	}
	if res.Outcome != OutcomeConverged {
		t.Fatalf("%s: %s", res.Outcome, res.Message)
	}
	if got, want := commitsString(w.commits), "wp@b:a wp@a:a+tok-b"; got != want {
		t.Fatalf("commits\n got %s\nwant %s", got, want)
	}
}

func held(holder string) *RebootStatus {
	return &RebootStatus{Agent: true, Holdable: true, Held: true, Holder: holder}
}

func TestMoveDoesNotStartWhileTheRebootAgentDrainsANodeOfARegionOfTheMove(t *testing.T) {
	w, g := failback(t)
	w.reboot["a"] = held("continuum-a")
	w.reboot["b"] = &RebootStatus{Agent: true, Holdable: true, Holder: "w5cce4d", Draining: []string{"w5cce4d"}}
	start := w.now
	var rec *Record
	for i := 0; i < 30; i++ { // 5 minutes
		res := Plan(g, rec, w.view(), policy)
		if res.Commit != nil {
			t.Fatalf("committed %+v while node w5cce4d of b is drained", res.Commit)
		}
		if res.Outcome != OutcomeTransitioning || res.Interruption == nil || res.Interruption.Reason != ReasonNodeDraining ||
			!strings.Contains(res.Interruption.Message, "w5cce4d") || !strings.Contains(res.Message, "waiting") {
			t.Fatalf("the wait must be named: %s %s %+v", res.Outcome, res.Message, res.Interruption)
		}
		rec = res.Record
		if rec.RebootWaitSince != ts(start) || rec.Activity[0].StartedAt != "" || rec.Activity[0].State != StatePending {
			t.Fatalf("the step has not begun and the wait is counted from its start: %+v %+v", rec.RebootWaitSince, rec.Activity[0])
		}
		w.now = w.now.Add(10 * time.Second)
	}
	// The node is back and kured's lock is free: b does not hold it yet, so
	// the move still does not start.
	w.reboot["b"] = &RebootStatus{Agent: true, Holdable: true}
	res := Plan(g, rec, w.view(), policy)
	if res.Commit != nil || res.Interruption == nil || res.Interruption.Reason != ReasonRebootHoldPending {
		t.Fatalf("a move starts only when every region of it holds its reboot agent: %+v %+v", res.Commit, res.Interruption)
	}
	rec = res.Record
	// b holds it: the move starts, and the wait is over.
	w.reboot["b"] = held("continuum-b")
	res = Plan(g, rec, w.view(), policy)
	if res.Commit == nil || res.Commit.Region != "b" || res.Interruption != nil || res.Record.RebootWaitSince != "" {
		t.Fatalf("want the first commit once both regions hold: %+v %+v", res.Commit, res.Interruption)
	}
	rec = res.Record
	w.commit(*res.Commit)
	w.apply()
	// Mid-move a drain in a region of the move holds the next step up; the
	// hold is not asked for again once the move has begun.
	w.reboot["a"] = &RebootStatus{Agent: true, Holdable: true, Holder: "wa1", Draining: []string{"wa1"}}
	res = Plan(g, rec, w.view(), policy)
	if res.Commit != nil || res.Interruption == nil || res.Interruption.Reason != ReasonNodeDraining {
		t.Fatalf("mid-move drain: %+v %+v", res.Commit, res.Interruption)
	}
	rec = res.Record
	w.reboot["a"] = &RebootStatus{Agent: true, Holdable: true}
	res = Plan(g, rec, w.view(), policy)
	if res.Interruption != nil || res.Commit == nil || res.Commit.Region != "a" {
		t.Fatalf("after the drain the move goes on without asking for the hold again: %+v %+v", res.Commit, res.Interruption)
	}
}

func TestRebootWait_IsBounded(t *testing.T) {
	w, g := failback(t)
	w.reboot["b"] = &RebootStatus{Agent: true, Holdable: true, Holder: "w5cce4d", Draining: []string{"w5cce4d"}}
	p := policy
	p.RebootWait = 3 * time.Minute
	res := Plan(g, nil, w.view(), p)
	if res.Commit != nil {
		t.Fatalf("committed during the drain: %+v", res.Commit)
	}
	w.now = w.now.Add(3*time.Minute + time.Second)
	res = Plan(g, res.Record, w.view(), p)
	if res.Commit == nil || res.Interruption == nil || res.Interruption.Reason != ReasonRebootWaitExceeded {
		t.Fatalf("past the bound the move goes on and says so: %+v %+v", res.Commit, res.Interruption)
	}
}

// A move waits at its start for a drain, is re-targeted back (or is Blocked),
// and is asked for again past the bound of that wait, while the reboot agent
// drains another node of its region: the record left in flight is resumed,
// and it waits again, counted from now, not from the wait of before.
func TestAMoveAskedForAgainAfterItStoppedWalking_WaitsAgainForADrain(t *testing.T) {
	for _, stop := range []string{OutcomeConverged, OutcomeBlocked} {
		w, g := failback(t)
		w.reboot["a"] = held("continuum-a")
		w.reboot["b"] = &RebootStatus{Agent: true, Holdable: true, Holder: "wb1", Draining: []string{"wb1"}}
		res := Plan(g, nil, w.view(), policy)
		if res.Commit != nil || res.Interruption == nil || res.Interruption.Reason != ReasonNodeDraining {
			t.Fatalf("%s: the move must wait at its start: %+v %+v", stop, res.Commit, res.Interruption)
		}
		rec := res.Record
		w.now = w.now.Add(time.Minute)
		if stop == OutcomeConverged {
			w.setDesired("b") // re-targeted back: every file names b
		} else {
			k := g.Members[0].Key() // a's file declares b, b's file declares a
			f := w.files[k]["a"]
			f.Desired = "b"
			w.files[k]["a"] = f
		}
		res = Plan(g, rec, w.view(), policy)
		if res.Outcome != stop || res.Commit != nil || res.Record == nil || res.Record.Outcome != OutcomeTransitioning {
			t.Fatalf("%s: want the record left in flight: %s %+v %+v", stop, res.Outcome, res.Commit, res.Record)
		}
		rec = res.Record // what the lease keeps
		w.now = w.now.Add(DefaultRebootWait + 10*time.Minute)
		w.setDesired("a")
		w.reboot["b"] = &RebootStatus{Agent: true, Holdable: true, Holder: "wb2", Draining: []string{"wb2"}}
		res = Plan(g, rec, w.view(), policy)
		if res.Commit != nil || res.Interruption == nil || res.Interruption.Reason != ReasonNodeDraining || res.Record.RebootWaitSince != ts(w.now) {
			t.Fatalf("%s: asked for again while node wb2 of b is drained, the move must wait from now: commit=%+v %+v since=%s",
				stop, res.Commit, res.Interruption, res.Record.RebootWaitSince)
		}
	}
}

func TestRegionLossMove_DoesNotWaitForTheRebootAgent(t *testing.T) {
	m := Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: []string{"a", "b"}}
	w := newWorld(t, []string{"a", "b"}, []Member{m}, "a")
	g, _ := Groups([]Member{m})
	w.down["a"] = w.now.Add(-10 * time.Minute)
	w.reboot["b"] = &RebootStatus{Agent: true, Holdable: true, Holder: "wb1", Draining: []string{"wb1"}}
	p := policy
	p.AutoPromoteOnRegionLoss = true
	res, rec := w.run(g[0], nil, p, 40)
	if res.Outcome != OutcomeConverged || !rec.LossMode {
		t.Fatalf("a region-loss move must not wait for a drain in the survivor: %s %s", res.Outcome, res.Message)
	}
}
