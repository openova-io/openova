package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
)

func phaseOf(t *testing.T, c *Controller) string {
	t.Helper()
	cr, err := c.Dyn.Resource(ContinuumGVR).Namespace("acme").Get(context.Background(), "dr-wp", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p, _, _ := unstructured.NestedString(cr.Object, "status", "phase")
	return p
}

func conditionOf(t *testing.T, c *Controller, typ string) map[string]interface{} {
	t.Helper()
	cr, err := c.Dyn.Resource(ContinuumGVR).Namespace("acme").Get(context.Background(), "dr-wp", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	conds, _, _ := unstructured.NestedSlice(cr.Object, "status", "conditions")
	for _, x := range conds {
		if cm, ok := x.(map[string]interface{}); ok && cm["type"] == typ {
			return cm
		}
	}
	return nil
}

// A Failed transition (a step past its phase deadline) is still evaluated and
// may still commit, so it keeps the reboot agent held (renewed past its TTL),
// but only up to a bound: RebootWait after it failed the hold is released in
// both regions, kured takes its lock again, and the Continuum says so. The
// move is still evaluated: when it can act again it still converges.
func TestRebootAgent_AFailedMoveHoldsItAtMostRebootWait_ThenReleasesItAndSaysSo(t *testing.T) {
	w := newHoldWorld(t)
	const bound = 6 * time.Minute // longer than the hold's TTL (5 minutes)
	for _, c := range w.ctl {
		c.RebootWait = bound
	}
	nodes := map[string]string{regionA: "wa1", regionB: "wb1"}
	// A's database never applies the demotion (its operator is stuck): it
	// stays primary and publishes no token, so traffic-off waits past its
	// phase deadline (the default 15 minutes).
	stuck := true
	hold := func() {
		if stuck {
			w.s.dbs[regionA].primary, w.s.dbs[regionA].demTok = true, ""
		}
	}
	for i := 0; i < 130 && phaseOf(t, w.ctl[regionA]) != "Failed"; i++ {
		w.round(t)
		hold()
	}
	if w.fileCommits() != 1 || phaseOf(t, w.ctl[regionA]) != "Failed" {
		t.Fatalf("want a move Failed in traffic-off: phase=%s commits=%d", phaseOf(t, w.ctl[regionA]), w.fileCommits())
	}
	failedAt := w.clock.Add(-10 * time.Second) // the tick that first reported Failed
	released := false
	for i := 0; i < 60 && !released; i++ {
		at := w.clock
		w.round(t)
		hold()
		if at.Sub(failedAt) <= bound {
			// Within the bound: held in both regions, and said so.
			for r, n := range nodes {
				if ok, holder := kuredAcquire(t, w.dyn[r], n, w.clock); ok || holder != "continuum-"+r {
					t.Fatalf("%s after Failed: kured took its lock in %s within the bound (holder %q)", at.Sub(failedAt), r, holder)
				}
				if c := conditionOf(t, w.ctl[r], "RebootAgentHeld"); c == nil || c["status"] != "True" || c["reason"] != transition.ReasonHeldForMove {
					t.Fatalf("%s after Failed: %s must say its reboot agent is held, got %v", at.Sub(failedAt), r, c)
				}
			}
			continue
		}
		// At the bound: released in both regions, kured takes its lock, and
		// the Continuum names why.
		for r, n := range nodes {
			if l, ok := lockOf(t, w.dyn[r]); ok {
				t.Fatalf("%s after Failed (bound %s): %s still holds kured: %+v", at.Sub(failedAt), bound, r, l)
			}
			if ok, holder := kuredAcquire(t, w.dyn[r], n, w.clock); !ok {
				t.Fatalf("kured cannot take its lock in %s at the bound (holder %q)", r, holder)
			}
			c := conditionOf(t, w.ctl[r], "RebootAgentHeld")
			if c == nil || c["status"] != "False" || c["reason"] != transition.ReasonReleasedFailedMove {
				t.Fatalf("%s must say its reboot agent was released at the bound, got %v", r, c)
			}
		}
		if phaseOf(t, w.ctl[regionA]) != "Failed" {
			t.Fatalf("the move is still Failed, got %s", phaseOf(t, w.ctl[regionA]))
		}
		released = true
	}
	if !released {
		t.Fatal("the Failed move never released the reboot agent")
	}
	// kured finishes its restarts and frees its lock: while the move stays
	// Failed the hold is not taken again.
	for r, n := range nodes {
		kuredFinishes(t, w.dyn[r], n)
	}
	for i := 0; i < 3; i++ {
		w.round(t)
		hold()
		for _, r := range []string{regionA, regionB} {
			if l, ok := lockOf(t, w.dyn[r]); ok {
				t.Fatalf("the Failed move took the hold back in %s past its bound: %+v", r, l)
			}
		}
	}
	// A's database demotes: the Failed step is reached, the move acts again,
	// converges, and holds nothing once it is over.
	stuck = false
	for i := 0; i < 40; i++ {
		w.round(t)
	}
	if !w.s.dbs[regionB].primary || w.s.dbs[regionA].primary || w.fileCommits() != 2 {
		t.Fatalf("the move did not converge once A demoted: %+v %+v commits=%d", w.s.dbs[regionA], w.s.dbs[regionB], w.fileCommits())
	}
	for _, r := range []string{regionA, regionB} {
		if l, ok := lockOf(t, w.dyn[r]); ok {
			t.Fatalf("%s still holds kured after the move: %+v", r, l)
		}
		if c := conditionOf(t, w.ctl[r], "RebootAgentHeld"); c != nil {
			t.Fatalf("%s: a converged move carries no RebootAgentHeld condition, got %v", r, c)
		}
	}
}
