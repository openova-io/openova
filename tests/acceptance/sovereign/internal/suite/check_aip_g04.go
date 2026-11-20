package suite

import (
	"context"
	"fmt"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/modelcontract"
)

// Check AIP-G04, Usage carries the verified Organization. The call of
// ModelUsageProbe also sends x-openova-organization: someone-else; its
// meter entry and its usage event name Organization conformance, the
// token's (docs/contracts/model-contract.md section 3). A call without a
// token gets 401 and no meter pod has an entry for its request; a call for
// the model nope gets 404 (section 1.5).
const aipG04ID = "AIP-G04"

func init() { RegisterCheck(modelCheck{id: aipG04ID, steps: aipG04Steps}) }

func aipG04Steps(ctx context.Context, cc *CheckContext, s *modelSession, step modelStep) {
	alias := modelcontract.ConformanceAlias
	step("a call that names Organization someone-else in a header is metered for the token's Organization", func(ev Evidence) error {
		org, a, id, err := ModelUsageProbe(withModelRun(ctx, cc))
		ev["organization"], ev["alias"], ev["request_id"] = org, a, id
		if err == nil && org != aipG07Org {
			err = fmt.Errorf("the usage event is for Organization %q, the token's is %s", org, aipG07Org)
		}
		return err
	})
	var anonymous string
	step("a call without a token gets 401", func(ev Evidence) error {
		r, err := s.client.Unauthorized(ctx, alias)
		anonymous, ev["request_id"], ev["status"], ev["error"] = r.RequestID, r.RequestID, r.Status, r.Code
		return err
	})
	step("a call for the model nope gets 404", func(ev Evidence) error {
		r, err := s.client.UnknownModel(ctx)
		ev["status"], ev["error"] = r.Status, r.Code
		return err
	})
	step("no meter pod has an entry for the call without a token", func(ev Evidence) error {
		got, err := s.entries(ctx, anonymous)
		if ev["entries"] = len(got); err == nil && len(got) > 0 {
			err = fmt.Errorf("%d meter pods have an entry for request %s, which had no token", len(got), anonymous)
		}
		return err
	})
}
