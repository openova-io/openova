package suite

import (
	"context"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/modelcontract"
)

// Check AIP-G05, Simulation aliases meet the model contract
// (docs/contracts/model-contract.md section 1.4). On agent-default: a tool
// round trip, two tool calls in one answer, a streamed tool call as deltas
// with an index, and a usage with no zero count. On agent-fast: a tool round
// trip. These aliases need the simulation back end and its credential, so
// the check stays expected-fail with a nightly record (AIP-135) until
// AIP-320 flips it.
const aipG05ID = "AIP-G05"

func init() { RegisterCheck(modelCheck{id: aipG05ID, steps: aipG05Steps}) }

func aipG05Steps(ctx context.Context, _ *CheckContext, s *modelSession, step modelStep) {
	c := s.client
	for _, p := range []struct {
		alias, what string
		probe       func(context.Context, string) (modelcontract.Reply, error)
	}{
		{"agent-default", "a tool round trip", c.ToolRoundTrip},
		{"agent-default", "two tool calls in one answer", c.ParallelTools},
		{"agent-default", "a streamed tool call as deltas", c.StreamWithToolDeltas},
		{"agent-default", "a usage with no zero count", c.UsageNonZero},
		{"agent-fast", "a tool round trip", c.ToolRoundTrip},
	} {
		step(p.what+" on "+p.alias, func(ev Evidence) error {
			r, err := p.probe(ctx, p.alias)
			ev["request_id"], ev["status"], ev["error"] = r.RequestID, r.Status, r.Code
			return err
		})
	}
}
