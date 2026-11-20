package suite

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

// The checks whose subject the hand-over chain changes are the four the gate
// settled, each a registered check, and CTL-G04 is not one of them.
func TestCutoverSubjectsAreTheSettledChecks(t *testing.T) {
	ids := []string{}
	for id := range cutoverSubjects {
		ids = append(ids, id)
		if _, ok := registry[id]; !ok {
			t.Errorf("%s is not a registered check, so its skip would never apply", id)
		}
	}
	slices.Sort(ids)
	if want := []string{"FND-G01", "FND-G02", "INF-G01", "NET-G08"}; !slices.Equal(ids, want) {
		t.Errorf("the checks skipped until the chain completes are %v, want %v", ids, want)
	}
}

// In a run whose chain has not completed, every check whose subject the chain
// changes is skipped with the chain's state and why, and is not run; every
// other check runs. A required check skipped so is no SKIP for unmet needs: it
// fails the run, as pass rule 5 of scripts/acceptance-gate.py reads the record.
func TestCutoverSubjectsSkippedUntilTheChainCompletes(t *testing.T) {
	const chain = "repeating step 03, attempt 2 of 3"
	m := &Manifest{Checks: []ManifestCheck{
		entry("CTL-G04", StateExpectedFail), entry("FND-G01", StateExpectedFail), entry("FND-G02", StateExpectedFail),
		entry("FND-G03", StateRequired), {ID: "INF-G01", State: StateRequired, Needs: []string{"node-reboot"}},
		entry("NET-G08", StateRequired),
	}}
	run := func(caps map[string]bool, in ...RunIn) (*runner, map[string]bool) {
		where := RunInCloud
		if len(in) > 0 {
			where = in[0]
		}
		ran := map[string]bool{}
		checks := []Check{}
		for _, e := range m.Checks {
			id := e.ID
			checks = append(checks, fakeCheck{id: id, run: func(cc *CheckContext, rec StepRecorder) error {
				ran[id] = true
				return stepHolds(cc, rec)
			}})
		}
		r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
		r.runChecks(context.Background(), m, checks, &CheckContext{RunIn: where, Caps: caps, Chain: chain}, true)
		r.run.Finalize(nil)
		return r, ran
	}

	r, ran := run(map[string]bool{"multi-region": true, "node-reboot": true})
	for _, c := range r.run.Checks {
		why, skipped := cutoverSubjects[c.ID]
		switch {
		case skipped && (c.Status != Skip || ran[c.ID] || c.unmet ||
			c.Reason != "the run does not declare cutover-complete (the hand-over chain: "+chain+"; "+why+")"):
			t.Errorf("%s: want SKIP, not run, not unmet, the chain's state in the reason; got ran=%v %+v", c.ID, ran[c.ID], c)
		case !skipped && (c.Status != Pass || !ran[c.ID]):
			t.Errorf("%s: its subject is not the chain's, so it is judged; got ran=%v %+v", c.ID, ran[c.ID], c)
		}
	}
	if r.run.Result != Fail {
		t.Errorf("required checks INF-G01 and NET-G08 were not judged, yet the run is %s", r.run.Result)
	}

	// Without node-reboot as well: both needs are named, and the skip is one for unmet needs, as today.
	r, _ = run(map[string]bool{"multi-region": true})
	for _, c := range r.run.Checks {
		if c.ID == "INF-G01" && (!c.unmet || !strings.HasPrefix(c.Reason, "the run does not declare node-reboot, cutover-complete (the hand-over chain: ")) {
			t.Errorf("INF-G01 without node-reboot: %+v", c)
		}
	}

	// The chain completed: every check is judged, and the run passes.
	r, ran = run(map[string]bool{"multi-region": true, "node-reboot": true, CutoverComplete: true})
	for _, c := range r.run.Checks {
		if c.Status != Pass || !ran[c.ID] {
			t.Errorf("%s with cutover-complete declared: ran=%v %+v", c.ID, ran[c.ID], c)
		}
	}
	if r.run.Result != Pass {
		t.Errorf("a run of a complete chain whose checks pass is %s", r.run.Result)
	}

	// A run in self has no gate that reads the chain, and its record never counts: nothing is skipped for it.
	r, ran = run(map[string]bool{"node-reboot": true}, RunInSelf)
	for _, c := range r.run.Checks {
		if c.Status != Pass || !ran[c.ID] {
			t.Errorf("%s in a run in self: ran=%v %+v", c.ID, ran[c.ID], c)
		}
	}
}

// The checks see cutover-complete; the record never lists it, because the
// closed list of scripts/acceptance-gate.py validate-record does not hold it.
func TestSplitCapabilities(t *testing.T) {
	record, caps := splitCapabilities(" multi-region,cutover-complete, node-reboot,")
	if !slices.Equal(record, []string{"multi-region", "node-reboot"}) {
		t.Errorf("record capabilities %v", record)
	}
	if len(caps) != 3 || !caps[CutoverComplete] || !caps["multi-region"] || !caps["node-reboot"] {
		t.Errorf("capabilities the checks see %v", caps)
	}
}

// Step D0.11 says why ready_at is missing: the state of the chain the gate's
// wait read, which the step's evidence carries as well.
func TestReadyAtNamesTheChainTheGateRead(t *testing.T) {
	const want = `ready_at: [region-a] the hand-over chain has not ended (current step: "gitea-mirror")`
	for chain, suffix := range map[string]string{
		"repeating step 03, attempt 2 of 3": "; the hand-over chain, as the gate's wait read it: repeating step 03, attempt 2 of 3",
		"":                                  "",
	} {
		t.Setenv(fakeKubectlEnv, "provision-chain-running")
		regions := fakeRegions(t)
		r := &runner{cfg: Config{Chain: chain}, out: io.Discard, run: &Run{Mode: "run", Candidate: Candidate{ID: freshCandidate}},
			A: regions[0], B: regions[1]}
		r.measureProvision(context.Background(), map[string]int{"region-a": 3, "region-b": 2})
		step := r.run.Steps[len(r.run.Steps)-1]
		if step.ID != provisionStepID || step.Status != Fail || !strings.Contains(step.Error, want+suffix) ||
			chain == "" && strings.Contains(step.Error, "the gate's wait") {
			t.Errorf("chain %q: want D0.11 FAIL naming %q, got %s %q", chain, want+suffix, step.Status, step.Error)
		}
		if got, has := step.Evidence["chain"]; (chain != "") != has || has && got != chain {
			t.Errorf("chain %q: the evidence of D0.11 carries %v", chain, step.Evidence)
		}
	}
}
