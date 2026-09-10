package collections

import (
	"context"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// CustomerFacts gathers, for the aging report, what each customer in scope
// carries beyond its invoices: the platform suspension this product holds
// and the credit it could still apply.
func CustomerFacts(ctx context.Context, st *store.Store, scope store.Scope) (map[string]customerFacts, error) {
	customers, err := st.ListCustomers(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := map[string]customerFacts{}
	for _, c := range customers {
		f := customerFacts{suspendedAt: c.PlatformSuspendedAt, source: c.SuspensionSource, reason: c.SuspensionReason, credit: "0"}
		if bal, err := st.GetAccountBalance(ctx, store.OperatorScope, c.ID); err == nil {
			f.credit = bal.AvailableCredit
		}
		out[c.ID] = f
	}
	return out, nil
}
