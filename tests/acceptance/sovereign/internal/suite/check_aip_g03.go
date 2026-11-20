package suite

import (
	"context"
	"fmt"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/modelcontract"
)

// Check AIP-G03, A streamed call is metered. A streamed call on the alias
// conformance-echo, with include_usage, keeps the order of
// docs/contracts/model-contract.md section 1.4 and ends with a usage chunk
// whose output tokens are the stub's: the answer's length divided by 4,
// rounded up, and no cached tokens. Exactly one meter pod has the entry of
// the request: status ok, stream true, published, for Organization
// conformance, with the event's measures equal to that usage under the
// mapping of section 4.
const aipG03ID = "AIP-G03"

func init() { RegisterCheck(modelCheck{id: aipG03ID, steps: aipG03Steps}) }

func aipG03Steps(ctx context.Context, _ *CheckContext, s *modelSession, step modelStep) {
	alias := modelcontract.ConformanceAlias
	var r modelcontract.Reply
	if !step("a streamed call on "+alias+" ends with a usage chunk of the stub's counts", func(ev Evidence) error {
		var err error
		r, err = s.client.Stream(ctx, alias, "probe")
		ev["request_id"], ev["status"], ev["text"] = r.RequestID, r.Status, r.Text
		if err != nil {
			return err
		}
		ev["prompt_tokens"], ev["completion_tokens"] = r.Usage.Prompt, r.Usage.Completion
		if want := int64(len(r.Text)+3) / 4; r.Usage.Completion != want || *r.Usage.Details.Cached != 0 {
			return fmt.Errorf("the usage chunk has %d completion and %d cached tokens, the stub gives %d and 0", r.Usage.Completion, *r.Usage.Details.Cached, want)
		}
		return nil
	}) {
		return
	}
	step("one meter pod published one event for the call, with the measures of its usage", func(ev Evidence) error {
		e, err := s.entry(ctx, r.RequestID)
		if err != nil {
			return err
		}
		ev["organization"], ev["measures"] = e.Organization, e.Event.Data.Measures
		if err := e.Metered(alias, true, *r.Usage); err != nil {
			return err
		}
		if e.Organization != aipG07Org {
			return fmt.Errorf("the event is for Organization %q, the token's is %s", e.Organization, aipG07Org)
		}
		return nil
	})
}
