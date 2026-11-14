package suite

import (
	"context"
	"fmt"
	"strconv"
)

// Checks FND-G10, FND-G11 and FND-G12 report the journeys as gate checks.
// They do no work of their own: the runner has run the journeys before the
// checks, and each check reads the steps of its journeys from Prior.
//
//	FND-G10 Install and data   journeys J1 and J2
//	FND-G11 Planned move       journeys J3 and J4
//	FND-G12 Clean delete       journey J5
//
// A journey passes only when it recorded at least one step and every one of
// its steps is PASS. A journey that recorded no step, or a step that is FAIL
// or SKIP, fails the check: a journey that did not run has not passed.
type journeysCheck struct {
	id       string
	journeys []string
}

func init() {
	RegisterCheck(journeysCheck{id: "FND-G10", journeys: []string{"J1", "J2"}})
	RegisterCheck(journeysCheck{id: "FND-G11", journeys: []string{"J3", "J4"}})
	RegisterCheck(journeysCheck{id: "FND-G12", journeys: []string{"J5"}})
}

func (c journeysCheck) ID() string { return c.id }

func (c journeysCheck) Run(_ context.Context, cc *CheckContext, rec StepRecorder) error {
	for i, j := range c.journeys {
		rec.Step(c.id+"."+strconv.Itoa(i+1), "journey "+j+" ran and every one of its steps passed", func(ev Evidence) error {
			ev["journey"] = j
			return priorPassed(cc.Prior, func(s *Step) bool { return s.Journey == j }, "journey "+j, ev)
		})
	}
	return nil
}

// priorPassed judges the steps of prior that match: there is at least one,
// and every one is PASS. It puts the number of matching steps and the
// identifiers of those that did not pass in ev. what names the steps in the
// error.
func priorPassed(prior []*Step, match func(*Step) bool, what string, ev Evidence) error {
	steps, notPassed := 0, []string{}
	var first *Step
	for _, s := range prior {
		if s == nil || !match(s) {
			continue
		}
		steps++
		if s.Status != Pass {
			notPassed = append(notPassed, s.ID)
			if first == nil {
				first = s
			}
		}
	}
	ev["steps"], ev["not_passed"] = steps, notPassed
	switch {
	case steps == 0:
		return fmt.Errorf("%s: no step was recorded in this run", what)
	case first != nil:
		return fmt.Errorf("%s: step %s is %s: %s", what, first.ID, first.Status, nonEmpty(first.Error, "no reason recorded"))
	}
	return nil
}
