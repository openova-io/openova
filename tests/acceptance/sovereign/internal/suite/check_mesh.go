package suite

import "context"

// Check FND-G08, Mesh: every Cilium agent holds the gateway address of every
// node of the peer region as an ingress identity. Step D0.10 of the discovery
// (discover.go) asserts exactly this before the journeys run. The check does
// not run or copy that step: it reports its result, read from Prior. The
// check passes only when step D0.10 was recorded and is PASS.
const meshID = "FND-G08"

// meshStep is the journey and the identifier of the step the check reports.
const (
	meshJourney = "D0"
	meshStep    = "D0.10"
)

type meshCheck struct{}

func init() { RegisterCheck(meshCheck{}) }

func (meshCheck) ID() string { return meshID }

func (meshCheck) Run(_ context.Context, cc *CheckContext, rec StepRecorder) error {
	rec.Step(meshID+".1", "step "+meshStep+" passed: every Cilium agent holds the peer region's gateway addresses", func(ev Evidence) error {
		for _, s := range cc.Prior {
			if s != nil && s.Journey == meshJourney && s.ID == meshStep {
				for k, v := range s.Evidence {
					ev[k] = v
				}
			}
		}
		return priorPassed(cc.Prior, func(s *Step) bool { return s.Journey == meshJourney && s.ID == meshStep }, "the mesh step of the discovery", ev)
	})
	return nil
}
