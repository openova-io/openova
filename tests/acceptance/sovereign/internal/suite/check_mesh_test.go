package suite

import (
	"strings"
	"testing"
)

func TestMeshCheck(t *testing.T) {
	if _, ok := registry[meshID]; !ok || meshID != "FND-G08" {
		t.Fatalf("check %s is not registered", meshID)
	}
	passed := priorStep("D0", "D0.10", Pass, "")
	passed.Evidence = Evidence{"checked": 12}
	failed := priorStep("D0", "D0.10", Fail, "1 agent(s) lack peer gateway identities")
	failed.Evidence = Evidence{"checked": 12, "missing": map[string][]string{"region-b/cilium-x": {"region-a ingress 10.0.0.5"}}}
	for name, c := range map[string]struct {
		prior []*Step
		want  string // "" = PASS, else a part of the reason of the FAIL
	}{
		"step D0.10 passed":      {[]*Step{priorStep("D0", "D0.1", Pass, ""), passed, priorStep("J1", "J1.1", Pass, "")}, ""},
		"step D0.10 failed":      {[]*Step{priorStep("D0", "D0.1", Pass, ""), failed}, "the mesh step of the discovery: step D0.10 is FAIL: 1 agent(s) lack peer gateway identities"},
		"step D0.10 was skipped": {[]*Step{priorStep("D0", "D0.10", Skip, "")}, "step D0.10 is SKIP: no reason recorded"},
		"step D0.10 is missing":  {[]*Step{priorStep("D0", "D0.1", Pass, ""), priorStep("J1", "D0.10", Pass, "")}, "the mesh step of the discovery: no step was recorded in this run"},
	} {
		r := runCheck(t, "none", meshCheck{}, &CheckContext{}, c.prior...)
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
	}
	// The step carries the evidence of step D0.10.
	r := runCheck(t, "none", meshCheck{}, &CheckContext{}, failed)
	last := r.run.Steps[len(r.run.Steps)-1]
	if last.ID != "FND-G08.1" || last.Evidence["checked"] != 12 || last.Evidence["missing"] == nil || last.Evidence["steps"] != 1 {
		t.Errorf("the step does not hold the evidence of D0.10: %+v", last)
	}
}
