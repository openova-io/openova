package suite

import (
	"strings"
	"testing"
)

// priorStep is a step recorded before the checks.
func priorStep(journey, id, status, why string) *Step {
	return &Step{Journey: journey, ID: id, Name: "a step", Status: status, Error: why}
}

// allJourneysPassed are the steps of a run in which the discovery and the
// five journeys passed.
func allJourneysPassed() []*Step {
	return []*Step{
		priorStep("D0", "D0.1", Pass, ""), priorStep("D0", "D0.10", Pass, ""),
		priorStep("J1", "J1.1", Pass, ""), priorStep("J1", "J1.2", Pass, ""),
		priorStep("J2", "J2.1", Pass, ""),
		priorStep("J3", "J3.1", Pass, ""), priorStep("J3", "J3.2", Pass, ""),
		priorStep("J4", "J4.1", Pass, ""),
		priorStep("J5", "J5.1", Pass, ""),
	}
}

// replaced returns steps with the step of that journey and id replaced by s,
// or with s added when there is no such step. A nil s removes every step of
// the journey.
func replaced(steps []*Step, journey, id string, s *Step) []*Step {
	out, done := []*Step{}, false
	for _, x := range steps {
		switch {
		case x.Journey == journey && s == nil:
		case x.Journey == journey && x.ID == id:
			out, done = append(out, s), true
		default:
			out = append(out, x)
		}
	}
	if s != nil && !done {
		out = append(out, s)
	}
	return out
}

func TestJourneysChecks(t *testing.T) {
	want := map[string]string{"FND-G10": "J1 J2", "FND-G11": "J3 J4", "FND-G12": "J5"}
	for id, journeys := range want {
		c, ok := registry[id].(journeysCheck)
		if !ok || strings.Join(c.journeys, " ") != journeys {
			t.Fatalf("check %s is not registered for journeys %s: %+v", id, journeys, registry[id])
		}
	}
	all := allJourneysPassed()
	for name, c := range map[string]struct {
		prior []*Step
		want  map[string]string // check -> "" for PASS, else a part of the reason of the FAIL
	}{
		"every journey passed": {all, map[string]string{"FND-G10": "", "FND-G11": "", "FND-G12": ""}},
		"a step of J2 failed": {replaced(all, "J2", "J2.1", priorStep("J2", "J2.1", Fail, "the replica holds 0 rows")),
			map[string]string{"FND-G10": "journey J2: step J2.1 is FAIL: the replica holds 0 rows", "FND-G11": "", "FND-G12": ""}},
		"J4 was skipped": {replaced(replaced(all, "J4", "", nil), "J4", "-", priorStep("J4", "-", Skip, "J3 failed — the app is not in B to move back")),
			map[string]string{"FND-G10": "", "FND-G11": "journey J4: step - is SKIP: J3 failed", "FND-G12": ""}},
		"J5 did not run": {replaced(all, "J5", "", nil),
			map[string]string{"FND-G10": "", "FND-G11": "", "FND-G12": "journey J5: no step was recorded in this run"}},
		"no journey ran": {all[:2],
			map[string]string{"FND-G10": "journey J1: no step was recorded in this run", "FND-G11": "journey J3: no step was recorded in this run",
				"FND-G12": "journey J5: no step was recorded in this run"}},
	} {
		for id, reason := range c.want {
			// A copy: the runner appends to the steps it is given.
			r := runCheck(t, "none", registry[id], &CheckContext{}, append([]*Step(nil), c.prior...)...)
			got := r.run.Checks[0]
			switch {
			case reason == "" && got.Status != Pass:
				t.Errorf("%s, %s: want PASS, got %+v", name, id, got)
			case reason != "" && (got.Status != Fail || !strings.Contains(got.Reason, reason)):
				t.Errorf("%s, %s: want FAIL with %q, got %+v", name, id, reason, got)
			}
		}
	}
	// One step per journey; the evidence counts the steps and names those that did not pass.
	r := runCheck(t, "none", registry["FND-G10"], &CheckContext{},
		replaced(all, "J2", "J2.1", priorStep("J2", "J2.1", Fail, "x"))...)
	steps := r.run.Steps[len(r.run.Steps)-2:]
	if steps[0].ID != "FND-G10.1" || steps[0].Status != Pass || steps[0].Evidence["steps"] != 2 ||
		steps[1].ID != "FND-G10.2" || steps[1].Status != Fail || steps[1].Evidence["steps"] != 1 ||
		strings.Join(steps[1].Evidence["not_passed"].([]string), ",") != "J2.1" || steps[1].Evidence["journey"] != "J2" {
		t.Errorf("the steps of FND-G10 do not hold the evidence: %+v %+v", steps[0], steps[1])
	}
}
