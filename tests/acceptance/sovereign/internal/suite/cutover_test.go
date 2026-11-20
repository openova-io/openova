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

// In a run whose chain the gate read as not complete, every check whose subject
// the chain changes is skipped with the chain's state and why, and is not run;
// every other check runs. A required check skipped so is no SKIP for unmet
// needs: it fails the run, as pass rule 5 of scripts/acceptance-gate.py reads
// the record. The flag comes from the chain's state only: a declared
// cutover-complete is not read, and a run without a chain (by hand) judges all.
func TestCutoverSubjectsSkippedUntilTheChainCompletes(t *testing.T) {
	m := &Manifest{Checks: []ManifestCheck{
		entry("CTL-G04", StateExpectedFail), entry("FND-G01", StateExpectedFail), entry("FND-G02", StateExpectedFail),
		entry("FND-G03", StateRequired), {ID: "INF-G01", State: StateRequired, Needs: []string{"node-reboot"}},
		entry("NET-G08", StateRequired),
	}}
	run := func(capabilities []string, chain string, in RunIn) (*runner, map[string]bool) {
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
		r.runChecks(context.Background(), m, checks, &CheckContext{RunIn: in, Caps: runCaps(capabilities, chain), Chain: chain}, true)
		r.run.Finalize(nil)
		return r, ran
	}
	allJudged := func(what string, r *runner, ran map[string]bool) {
		for _, c := range r.run.Checks {
			if c.Status != Pass || !ran[c.ID] {
				t.Errorf("%s: %s: ran=%v %+v", what, c.ID, ran[c.ID], c)
			}
		}
		if r.run.Result != Pass {
			t.Errorf("%s: every check passes, yet the run is %s", what, r.run.Result)
		}
	}

	for _, chain := range []string{"not ended at step harbor-prewarm", "failed at step harbor-prewarm"} {
		// cutover-complete declared by hand is not read: only the chain's state sets the flag.
		r, ran := run([]string{"multi-region", "node-reboot", CutoverComplete}, chain, RunInCloud)
		for _, c := range r.run.Checks {
			why, skipped := cutoverSubjects[c.ID]
			switch {
			case skipped && (c.Status != Skip || ran[c.ID] || c.unmet ||
				c.Reason != "the run does not declare cutover-complete (the hand-over chain: "+chain+"; "+why+")"):
				t.Errorf("%s: %s: want SKIP, not run, not unmet, the chain's state in the reason; got ran=%v %+v", chain, c.ID, ran[c.ID], c)
			case !skipped && (c.Status != Pass || !ran[c.ID]):
				t.Errorf("%s: %s: its subject is not the chain's, so it is judged; got ran=%v %+v", chain, c.ID, ran[c.ID], c)
			}
		}
		if r.run.Result != Fail {
			t.Errorf("%s: required checks INF-G01 and NET-G08 were not judged, yet the run is %s", chain, r.run.Result)
		}
	}

	// Without node-reboot as well: both needs are named, and the skip is one for unmet needs, as today.
	r, _ := run([]string{"multi-region"}, "not ended at step registry-pivot", RunInCloud)
	for _, c := range r.run.Checks {
		if c.ID == "INF-G01" && (!c.unmet || !strings.HasPrefix(c.Reason, "the run does not declare node-reboot, cutover-complete (the hand-over chain: not ended at step registry-pivot; ")) {
			t.Errorf("INF-G01 without node-reboot: %+v", c)
		}
	}

	// The chain completed: every check is judged, and the run passes.
	r, ran := run([]string{"multi-region", "node-reboot"}, ChainComplete, RunInCloud)
	allJudged("chain complete", r, ran)
	// No gate read the chain (a run by hand, --chain empty): every check is judged, as before.
	r, ran = run([]string{"multi-region", "node-reboot"}, "", RunInCloud)
	allJudged("no chain given", r, ran)
	// A run in self has no gate that reads the chain, and its record never counts: nothing is skipped for it.
	r, ran = run([]string{"node-reboot"}, "not ended at step harbor-prewarm", RunInSelf)
	allJudged("run in self", r, ran)
}

// The flags the checks see are the declared capabilities, and cutover-complete
// exactly when the chain's state is complete; the record's capabilities are the
// declared ones, so the gate never has to put a flag outside the closed list of
// scripts/acceptance-gate.py validate-record into --capabilities.
func TestRunCapsCutoverCompleteOnlyFromTheChain(t *testing.T) {
	for _, tc := range []struct {
		capabilities []string
		chain        string
		want         []string
	}{
		{[]string{"multi-region", "node-reboot"}, ChainComplete, []string{CutoverComplete, "multi-region", "node-reboot"}},
		{[]string{"multi-region"}, "failed at step harbor-prewarm", []string{"multi-region"}},
		{[]string{"multi-region", CutoverComplete}, "not ended at step registry-pivot", []string{"multi-region"}},
		{[]string{"multi-region", CutoverComplete}, "", []string{"multi-region"}},
		{nil, ChainComplete, []string{CutoverComplete}},
	} {
		caps := runCaps(tc.capabilities, tc.chain)
		got := []string{}
		for c, on := range caps {
			if on {
				got = append(got, c)
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("runCaps(%v, %q) = %v, want %v", tc.capabilities, tc.chain, got, tc.want)
		}
	}
}

// Step D0.11 says why ready_at is missing: the state of the chain the gate's
// wait read, which the step's evidence carries as well.
func TestReadyAtNamesTheChainTheGateRead(t *testing.T) {
	const want = `ready_at: [region-a] the hand-over chain has not ended (current step: "gitea-mirror")`
	for chain, suffix := range map[string]string{
		"not ended at step gitea-mirror": "; the hand-over chain, as the gate's wait read it: not ended at step gitea-mirror",
		"":                               "",
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
