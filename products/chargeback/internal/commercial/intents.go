package commercial

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Payment intents (DESIGN.md §9.2, founder refinement (a)).
//
// CHECKOUT and COLLECTION are different triggers with different owners.
// Checkout — the customer is present and pays now, a plan purchase or a
// top-up — is a SALE: our product calls the gateway seam in EVERY commercial
// mode, external included. Collection — pursuing an unpaid invoice — is the
// receivable owner's to trigger, so in external mode we NEVER call the
// gateway for an invoice and only import the settled status.
//
// An Intents service models that explicitly: every request to a gateway is
// a PaymentIntent with a purpose, the provider check refuses a collection
// intent in external mode with 409 before any gateway is reached, and the
// gateway's answer is recorded on the intent. A settled answer books the
// payment at once — as a top-up for a checkout, against the invoice for a
// collection.

// Intents requests money through the gateway seam under the provider check.
type Intents struct {
	Store      *store.Store
	Commercial *Selector
	Settlement *settle.Registry
}

// IntentRequest is what an operator (or a checkout flow) asks for.
type IntentRequest struct {
	CustomerID  string
	Purpose     string
	StatementID string
	Amount      store.Decimal
	Reference   string
	Actor       string
}

// Request creates the intent, applies the provider check, asks the gateway
// and records its answer. The returned intent carries the outcome; err is
// set only for a refusal or a failure before the gateway answered.
func (s *Intents) Request(ctx context.Context, in IntentRequest) (store.PaymentIntent, error) {
	purpose := strings.ToLower(strings.TrimSpace(in.Purpose))
	if purpose == "" {
		purpose = store.PurposeCollection
		if in.StatementID == "" {
			purpose = store.PurposeCheckout
		}
	}
	c, err := s.Store.GetCustomer(ctx, store.OperatorScope, in.CustomerID)
	if err != nil {
		return store.PaymentIntent{}, err
	}
	if !c.IsBilled() {
		return store.PaymentIntent{}, fmt.Errorf("%w: nothing is collected for a customer whose charging is %s", store.ErrConflict, c.Charging)
	}
	currency, err := s.Store.CustomerCurrency(ctx, c.ID)
	if err != nil {
		return store.PaymentIntent{}, err
	}
	var st store.Statement
	amount := in.Amount
	switch purpose {
	case store.PurposeCollection:
		if in.StatementID == "" {
			return store.PaymentIntent{}, fmt.Errorf("%w: a collection intent names the invoice it pursues (statement_id)", store.ErrInvalid)
		}
		// THE PROVIDER CHECK. Only the owner of the receivable pursues it.
		if _, err := s.Commercial.AllowsCollection(ctx); err != nil {
			return store.PaymentIntent{}, err
		}
		if st, err = s.Store.GetStatement(ctx, store.OperatorScope, in.StatementID); err != nil {
			return store.PaymentIntent{}, err
		}
		if st.CustomerID != c.ID {
			return store.PaymentIntent{}, fmt.Errorf("%w: invoice %s belongs to another customer", store.ErrConflict, in.StatementID)
		}
		if eff := st.EffectiveStatusAt(time.Now().UTC()); eff != store.StatusIssued && eff != store.StatusSent && eff != store.StatusOverdue {
			return store.PaymentIntent{}, fmt.Errorf("%w: a %s invoice has nothing to collect", store.ErrConflict, eff)
		}
		if strings.TrimSpace(string(amount)) == "" {
			amount = st.Balance
		}
		currency = st.Currency
	case store.PurposeCheckout:
		if strings.TrimSpace(string(amount)) == "" {
			return store.PaymentIntent{}, fmt.Errorf("%w: a checkout intent needs the amount being paid", store.ErrInvalid)
		}
	default:
		return store.PaymentIntent{}, fmt.Errorf("%w: purpose must be checkout or collection", store.ErrInvalid)
	}
	intent, err := s.Store.CreatePaymentIntent(ctx, store.PaymentIntent{
		CustomerID: c.ID, Purpose: purpose, StatementID: in.StatementID, Amount: amount, Currency: currency,
		Gateway: c.GatewayName, Reference: strings.TrimSpace(in.Reference), RequestedBy: in.Actor,
	})
	if err != nil {
		return store.PaymentIntent{}, err
	}
	res, gerr := s.Settlement.RequestPayment(ctx, settle.Request{
		Statement: st, Customer: c, Purpose: purpose, Amount: intent.Amount, Currency: currency, IntentID: intent.ID,
	})
	status := store.IntentPending
	switch {
	case gerr != nil:
		status = store.IntentFailed
		res.Detail = strings.TrimSpace(res.Detail + " " + gerr.Error())
		slog.Warn("payment intent: gateway refused", "intent", intent.ID, "purpose", purpose, "error", gerr)
	case res.Outcome == settle.Settled:
		status = store.IntentSettled
	case res.Outcome == settle.AwaitingTransfer:
		status = store.IntentAwaitingTransfer
	case res.Outcome == settle.NotApplicable:
		status = store.IntentRefused
	}
	intent, err = s.Store.UpdatePaymentIntent(ctx, intent.ID, status, res.Gateway, res.Reference, res.PayURL, res.Detail)
	if err != nil {
		return store.PaymentIntent{}, err
	}
	if status == store.IntentSettled {
		// The money moved during the call: book it now. A checkout lands as
		// credit on the account; a collection settles its invoice.
		var allocations []store.AllocationInput
		if purpose == store.PurposeCollection {
			allocations = []store.AllocationInput{{StatementID: st.ID, Amount: intent.Amount}}
		}
		ref := res.Reference
		if ref == "" {
			ref = "intent-" + intent.ID
		}
		if _, err := s.Store.RecordCustomerPayment(ctx, store.CustomerPaymentInput{
			CustomerID: c.ID, Purpose: purpose, IntentID: intent.ID, Allocations: allocations,
			Payment: store.PaymentInput{Amount: intent.Amount, PaidAt: time.Now().UTC(), Method: c.PaymentMethod, Reference: ref, Status: store.PaymentReceived, Gateway: res.Gateway, Actor: in.Actor},
		}); err != nil {
			return intent, err
		}
		intent, err = s.Store.GetPaymentIntent(ctx, store.OperatorScope, intent.ID)
	}
	return intent, err
}
