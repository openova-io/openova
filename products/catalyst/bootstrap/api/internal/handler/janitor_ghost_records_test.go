package handler

import (
	"testing"
	"time"
)

// Test_isGhostRecord covers the safety-critical decision: a ghost is ONLY a
// live-claimed status + old + apiserver-unreachable. Everything else PROTECTS
// (the #4454/#4614 fail-safe philosophy — never reap a live or in-flight dep).
func Test_isGhostRecord(t *testing.T) {
	const thr = 72 * time.Hour
	old := 100 * time.Hour
	young := 1 * time.Hour
	cases := []struct {
		name      string
		status    string
		age       time.Duration
		reachable bool
		want      bool
	}{
		{"ready+old+unreachable = GHOST", "ready", old, false, true},
		{"ready+old+REACHABLE = live, protect", "ready", old, true, false},
		{"ready+young+unreachable = too young, protect", "ready", young, false, false},
		{"phase1-watching in-flight, never ghost", "phase1-watching", old, false, false},
		{"provisioning in-flight, never ghost", "provisioning", old, false, false},
		{"wiped has own reap, not ghost here", "wiped", old, false, false},
		{"failed protected (DEBUG-BEFORE-WIPE), not ghost", "failed", old, false, false},
		{"unknown future status fails safe", "quantum-foam", old, false, false},
		{"cutover-complete+old+unreachable = GHOST", "cutover-complete", old, false, true},
	}
	for _, c := range cases {
		if got := isGhostRecord(c.status, c.age, thr, c.reachable); got != c.want {
			t.Errorf("%s: isGhostRecord(%q,age=%v,reach=%v)=%v want %v", c.name, c.status, c.age, c.reachable, got, c.want)
		}
	}
}

// Test_apiserverReachable_NoKubeconfig proves a missing kubeconfig reads as
// probed=false (can't judge → caller PROTECTS), never a false ghost.
func Test_apiserverReachable_NoKubeconfig(t *testing.T) {
	_, probed := apiserverReachable(t.Context(), "/nonexistent/path.yaml")
	if probed {
		t.Fatal("missing kubeconfig must read probed=false (can't judge → protect)")
	}
}
