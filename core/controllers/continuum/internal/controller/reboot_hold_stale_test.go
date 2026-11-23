package controller

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/transition"
	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// The hold follows this tick's evaluation, not the record as it was read:
// Plan returns an in-flight record unchanged when it walks it no more
// (Converged steady state, Blocked), and nothing then rewrites the lease.

// setFile is a person (or the application-controller) committing a region file.
func (w *holdWorld) setFile(t *testing.T, self, primary, desired string) {
	t.Helper()
	p := regionfile.AppPath(self, "wp")
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	w.store.files[p] = regionFile(t, self, primary, desired)
	w.store.ver[p]++
}

// leaseRecord is the transition record the lease holds now.
func (w *holdWorld) leaseRecord(t *testing.T) *transition.Record {
	t.Helper()
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	for p, d := range w.store.files {
		if strings.HasPrefix(p, "continuum/transitions/") {
			l, err := transition.ParseLease(d)
			if err != nil {
				t.Fatal(err)
			}
			return l.Transition
		}
	}
	return nil
}

// A move A → B waits at its start for B's reboot agent (kured holds its lock
// for wb1 and drains it); A has taken its hold. The person sets Primary back
// to A: every file still names A, so the group is Converged, while the record
// in the lease stays Transitioning. A's hold is released at once and kured can
// take its lock in A.
func TestRebootAgent_AMoveRetargetedBackDuringTheStartWait_ReleasesTheHold(t *testing.T) {
	start := time.Date(2026, 11, 21, 18, 15, 0, 0, time.UTC)
	b, _ := json.Marshal(kuredLock{NodeID: "wb1", Created: start, TTL: time.Hour})
	w := newHoldWorld(t, kuredDaemonSet(string(b)), workerNode("wb1", true))
	for i := 0; i < 6; i++ {
		w.round(t)
	}
	if la, ok := lockOf(t, w.dyn[regionA]); !ok || la.NodeID != "continuum-"+regionA || w.fileCommits() != 0 {
		t.Fatalf("want the move waiting at its start with A's hold taken: a=%+v commits=%d", la, w.fileCommits())
	}
	// Re-targeted back to A, where the writer still is.
	w.setFile(t, regionA, regionA, regionA)
	w.setFile(t, regionB, regionA, regionA)
	for i := 0; i < 35; i++ { // past the hold's TTL (5 minutes)
		w.round(t)
		if l, ok := lockOf(t, w.dyn[regionA]); ok {
			t.Fatalf("round %d after the re-target: A still holds kured for a move that is over: %+v", i, l)
		}
		for _, r := range []string{regionA, regionB} {
			if c := conditionOf(t, w.ctl[r], "RebootAgentHeld"); c != nil {
				t.Fatalf("round %d: %s says a move holds its reboot agent while the group is Converged: %v", i, r, c)
			}
			if c := conditionOf(t, w.ctl[r], "Ready"); c == nil || c["status"] != "True" || c["reason"] != "Converged" {
				t.Fatalf("round %d: %s: want Ready=True Converged, got %v", i, r, c)
			}
		}
	}
	if rec := w.leaseRecord(t); rec == nil || rec.Outcome != transition.OutcomeTransitioning {
		t.Fatalf("the lease record is no longer in flight: the test proves nothing (%+v)", rec)
	}
	if w.fileCommits() != 0 {
		t.Fatalf("a re-targeted move committed %d region files", w.fileCommits())
	}
	if ok, holder := kuredAcquire(t, w.dyn[regionA], "wa1", w.clock); !ok {
		t.Fatalf("kured cannot take its lock in A after the move was re-targeted back (holder %q)", holder)
	}
	if l, _ := lockOf(t, w.dyn[regionB]); l.NodeID != "wb1" {
		t.Fatalf("kured's lock in B was written over: %+v", l)
	}
}

// A move A → B is Blocked mid-way (after traffic-off, its files declare
// different desired primaries) and stays Blocked: the move cannot act, so
// both regions release kured and say why. When it can act again it holds
// kured again and converges.
func TestRebootAgent_AMoveBlockedMidWay_ReleasesTheHoldUntilItActsAgain(t *testing.T) {
	w := newHoldWorld(t)
	for i := 0; i < 40 && w.fileCommits() == 0; i++ {
		w.round(t)
	}
	if w.fileCommits() != 1 {
		t.Fatalf("the move did not start: commits=%d", w.fileCommits())
	}
	// A's file (primary=B since traffic-off) now declares A desired: Blocked.
	w.setFile(t, regionA, regionB, regionA)
	nodes := map[string]string{regionA: "wa1", regionB: "wb1"}
	for i := 0; i < 35; i++ { // past the hold's TTL (5 minutes)
		w.round(t)
		for r := range nodes {
			if l, ok := lockOf(t, w.dyn[r]); ok {
				t.Fatalf("round %d of the Blocked move: %s still holds kured: %+v", i, r, l)
			}
			if c := conditionOf(t, w.ctl[r], "RebootAgentHeld"); c == nil || c["status"] != "False" || c["reason"] != transition.ReasonReleasedBlockedMove {
				t.Fatalf("round %d: %s must say the Blocked move released its reboot agent, got %v", i, r, c)
			}
			if p := phaseOf(t, w.ctl[r]); p != "Degraded" {
				t.Fatalf("round %d: %s: want the Blocked phase Degraded, got %s", i, r, p)
			}
		}
	}
	if rec := w.leaseRecord(t); rec == nil || rec.Outcome != transition.OutcomeTransitioning {
		t.Fatalf("the lease record is no longer in flight: the test proves nothing (%+v)", rec)
	}
	for r, n := range nodes {
		if ok, holder := kuredAcquire(t, w.dyn[r], n, w.clock); !ok {
			t.Fatalf("kured cannot take its lock in %s while the move is Blocked (holder %q)", r, holder)
		}
		kuredFinishes(t, w.dyn[r], n)
	}
	// Unblocked: the move acts again, holds kured again, and converges.
	w.setFile(t, regionA, regionB, regionB)
	w.round(t)
	for r := range nodes {
		if l, ok := lockOf(t, w.dyn[r]); !ok || l.NodeID != "continuum-"+r {
			t.Fatalf("the move acts again but %s does not hold kured: %+v", r, l)
		}
	}
	for i := 0; i < 40; i++ {
		w.round(t)
	}
	if !w.s.dbs[regionB].primary || w.s.dbs[regionA].primary || w.fileCommits() != 2 {
		t.Fatalf("the move did not converge once unblocked: %+v %+v commits=%d", w.s.dbs[regionA], w.s.dbs[regionB], w.fileCommits())
	}
	for r := range nodes {
		if l, ok := lockOf(t, w.dyn[r]); ok {
			t.Fatalf("%s still holds kured after the move: %+v", r, l)
		}
	}
}
