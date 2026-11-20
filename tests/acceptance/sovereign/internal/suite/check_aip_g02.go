package suite

import (
	"context"
	"fmt"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/modelcontract"
)

// Check AIP-G02, Tool-call round trip through the gateway (docs/contracts/model-contract.md
// section 1), with an agent token: GET /v1/models lists conformance-echo with owned_by stub,
// the key of its back end; a request that offers one tool on that alias is answered with one
// call of it, and the follow-up with the tool's result is answered echo:<result> (the stub).
const aipG02ID = "AIP-G02"

func init() { RegisterCheck(modelCheck{id: aipG02ID, steps: aipG02Steps}) }

func aipG02Steps(ctx context.Context, _ *CheckContext, s *modelSession, step modelStep) {
	alias := modelcontract.ConformanceAlias
	step("GET /v1/models lists "+alias+" with owned_by stub", func(ev Evidence) error {
		owner, err := s.client.ModelOwner(ctx, alias)
		if ev["owned_by"] = owner; err == nil && owner != "stub" {
			err = fmt.Errorf("1.1: GET /v1/models lists %s with owned_by %q, want stub", alias, owner)
		}
		return err
	})
	step("a tool offered on "+alias+" is called, and its result comes back as echo:<result>", func(ev Evidence) error {
		r, err := s.client.ToolRoundTrip(ctx, alias)
		ev["request_id"], ev["status"], ev["text"] = r.RequestID, r.Status, r.Text
		if want := "echo:" + modelcontract.ToolResult; err == nil && r.Text != want {
			err = fmt.Errorf("the answer to the tool result is %q, want %q", r.Text, want)
		}
		return err
	})
}
