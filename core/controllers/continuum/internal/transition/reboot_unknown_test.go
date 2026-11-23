package transition

import (
	"strings"
	"testing"
	"time"
)

// A reboot agent that could not be read is not "no agent": the move waits
// on it under its own reason, bounded like every other wait for the agent.
func TestMoveWaitsWhileARegionsRebootAgentCannotBeRead(t *testing.T) {
	w, g := failback(t)
	w.reboot["a"] = held("continuum-a")
	// b's read of kured's DaemonSet was refused: b may not hold it yet.
	w.reboot["b"] = &RebootStatus{Unknown: true, Message: `read DaemonSet kube-system/kured: daemonsets.apps "kured" is forbidden`}
	p := policy
	p.RebootWait = 3 * time.Minute
	start := w.now
	var rec *Record
	for i := 0; i < 6; i++ {
		res := Plan(g, rec, w.view(), p)
		if res.Commit != nil {
			t.Fatalf("committed %+v while b's reboot agent could not be read", res.Commit)
		}
		if res.Outcome != OutcomeTransitioning || res.Interruption == nil || res.Interruption.Reason != ReasonRebootAgentUnknown ||
			!strings.Contains(res.Interruption.Message, "forbidden") || !strings.Contains(res.Message, "waiting") {
			t.Fatalf("the wait must be named: %s %s %+v", res.Outcome, res.Message, res.Interruption)
		}
		rec = res.Record
		if rec.RebootWaitSince != ts(start) || rec.Activity[0].StartedAt != "" {
			t.Fatalf("the step has not begun and the wait is counted from its start: %q %+v", rec.RebootWaitSince, rec.Activity[0])
		}
		w.now = w.now.Add(10 * time.Second)
	}
	// b is read again and holds its agent: the move starts.
	w.reboot["b"] = held("continuum-b")
	res := Plan(g, rec, w.view(), p)
	if res.Commit == nil || res.Commit.Region != "b" || res.Interruption != nil || res.Record.RebootWaitSince != "" {
		t.Fatalf("want the first commit once both regions are read and hold: %+v %+v", res.Commit, res.Interruption)
	}
	rec = res.Record
	w.commit(*res.Commit)
	w.apply()
	// Mid-move a failed read in a region of the move holds the next step up:
	// a drain there cannot be ruled out.
	w.reboot["a"] = &RebootStatus{Agent: true, Holdable: true, Unknown: true, Message: "list nodes: the server is currently unable to handle the request"}
	res = Plan(g, rec, w.view(), p)
	if res.Commit != nil || res.Interruption == nil || res.Interruption.Reason != ReasonRebootAgentUnknown || !strings.Contains(res.Interruption.Message, "list nodes") {
		t.Fatalf("mid-move unknown agent: %+v %+v", res.Commit, res.Interruption)
	}
	rec = res.Record
	// The wait is bounded: past it the move goes on and says so.
	w.now = w.now.Add(p.RebootWait + time.Second)
	res = Plan(g, rec, w.view(), p)
	if res.Commit == nil || res.Commit.Region != "a" || res.Interruption == nil || res.Interruption.Reason != ReasonRebootWaitExceeded {
		t.Fatalf("past the bound the move goes on and says so: %+v %+v", res.Commit, res.Interruption)
	}
}
