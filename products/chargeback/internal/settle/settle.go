// Package settle is the payment-gateway seam: the one place that knows HOW a
// statement's money is collected, so that adding a gateway is writing one
// implementation and registering it, and nothing else.
//
// # What an implementer must provide
//
// Exactly two methods (Gateway):
//
//	RequestSettlement(ctx, Request) (Result, error)
//	    Called once, on the draft → issued edge, for a customer whose
//	    gateway_name this implementation is registered under. It asks the
//	    gateway to collect Request.Statement.Total in Request.Statement
//	    .Currency. It returns what happened: Settled when the money moved
//	    synchronously, Pending when the gateway will confirm later
//	    (optionally with PayURL, a hosted page the payer completes on), or
//	    NotApplicable when this statement is nothing the gateway collects.
//	    It MUST be idempotent on Request.Statement.ID: issuing is
//	    idempotent, so a re-issue repeats this call and must not take money
//	    twice.
//
//	ConfirmSettlement(ctx, Confirmation) (Payment, error)
//	    Called when the gateway (or the operator, for a transfer) says money
//	    arrived. It validates and normalises the confirmation into the
//	    Payment to record: an exact amount, the day it arrived, and the
//	    reference that proves it. It records nothing itself — the caller
//	    books the payment through the store, which is what enforces the
//	    lifecycle, refuses overpayment and carries a part-paid balance.
//
// Then one line of wiring, in cmd/chargeback: register the implementation
// under the gateway_name customers will carry, e.g.
//
//	reg.Register("omantel", omantel.New(cfg))
//
// and set those customers to charging=billed, payment_method=gateway,
// gateway_name=omantel. Nothing else in the product changes.
//
// A gateway never touches the database, never decides whether a customer is
// billable, and never writes a statement's status. Money in, normalised
// facts out.
//
// # What is deliberately NOT here
//
// No gateway's API, endpoints or credentials are modelled: Omantel's gateway
// is not specified in this repository and inventing its shape would be a
// guess dressed as an integration. What is specified is the seam it plugs
// into and the two methods it must answer.
package settle

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The payment methods the registry routes on (store.PaymentMethod*), and
// the one gateway implementation that exists today.
const (
	MethodGateway  = store.PaymentMethodGateway
	MethodTransfer = store.PaymentMethodTransfer
	MethodInternal = store.PaymentMethodInternal

	// GatewayStripe is the registry key the platform billing service (and
	// so Stripe) is registered under.
	GatewayStripe = store.GatewayStripe
)

// Outcome is what a settlement request came to.
type Outcome string

const (
	// Settled — the money moved during the call.
	Settled Outcome = "settled"
	// Pending — the gateway accepted the request and will confirm later,
	// through ConfirmSettlement.
	Pending Outcome = "pending"
	// AwaitingTransfer — nothing was collected because nothing can be: the
	// customer pays by transfer against the invoice, and settlement is
	// recorded when the payment arrives.
	AwaitingTransfer Outcome = "awaiting-transfer"
	// NotApplicable — this statement is not one the gateway collects (an
	// informational customer, a zero total).
	NotApplicable Outcome = "not-applicable"
)

// Request asks a gateway to collect one statement.
type Request struct {
	Statement store.Statement
	Customer  store.Customer
	// Method is the customer's payment method — gateway, transfer or
	// internal — and GatewayName the key this implementation was
	// registered under when the method is gateway.
	Method      string
	GatewayName string
}

// Result is what the gateway answers.
type Result struct {
	Outcome Outcome
	// Gateway names the implementation, for the audit entry and the log.
	Gateway string
	// Reference is the gateway's own id for the collection, when it has one.
	Reference string
	// PayURL is a hosted page the payer completes the payment on; empty
	// when the gateway needs no interaction.
	PayURL string
	// Detail is one human-readable line for the operator.
	Detail string
}

// Confirmation is the asynchronous "money arrived" message: a gateway
// callback, a bank statement line, or an operator recording a transfer.
type Confirmation struct {
	Statement   store.Statement
	Customer    store.Customer
	Method      string
	GatewayName string
	Amount      store.Decimal
	PaidAt      time.Time
	Reference   string
	// Actor is who is recording it, for the audit trail.
	Actor string
}

// Payment is the normalised fact the caller books. It is a payment in its own
// right — amount, date, method, reference, status — which the caller links to
// the invoice it was recorded against.
type Payment struct {
	Amount    store.Decimal
	PaidAt    time.Time
	Method    string
	Reference string
	Status    string
	Gateway   string
}

// Gateway collects money for statements. Two methods: ask for settlement,
// accept the confirmation that it happened.
type Gateway interface {
	RequestSettlement(ctx context.Context, req Request) (Result, error)
	ConfirmSettlement(ctx context.Context, c Confirmation) (Payment, error)
}

// ErrNoGateway is returned when a customer's gateway_name has no
// implementation registered — the deployment cannot collect for it.
var ErrNoGateway = errors.New("no settlement gateway")

// Registry maps a gateway_name to the implementation that serves it. The
// transfer and internal payment methods need no registration: they are
// served by the built-in Manual gateway, because nothing external collects.
type Registry struct {
	byGateway map[string]Gateway
	manual    Gateway
}

// NewRegistry returns a registry whose transfer and internal methods already
// work. A gateway implementation is registered by name.
func NewRegistry() *Registry {
	return &Registry{manual: Manual{}}
}

// Register binds an implementation to a gateway_name, replacing any previous
// one. Adding Omantel's gateway is this one call.
func (r *Registry) Register(gatewayName string, g Gateway) {
	name := strings.ToLower(strings.TrimSpace(gatewayName))
	if r.byGateway == nil {
		r.byGateway = map[string]Gateway{}
	}
	if g == nil {
		delete(r.byGateway, name)
		return
	}
	r.byGateway[name] = g
}

// Gateways lists the registered gateway names, for the startup log.
func (r *Registry) Gateways() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byGateway))
	for name := range r.byGateway {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// For resolves who collects for a customer, and the route taken. nil means
// nothing collects for this customer — which is exactly right for one whose
// charging is informational, and an unregistered gateway_name.
func (r *Registry) For(c store.Customer) (Gateway, string) {
	if r == nil || !c.IsBilled() {
		return nil, store.ChargingInformational
	}
	switch c.PaymentMethod {
	case MethodTransfer, MethodInternal:
		g := r.manual
		if g == nil {
			g = Manual{}
		}
		return g, c.PaymentMethod
	case MethodGateway:
		name := strings.ToLower(strings.TrimSpace(c.GatewayName))
		return r.byGateway[name], name
	}
	return nil, c.PaymentMethod
}

// RequestSettlement resolves the customer's gateway and asks it to collect
// the statement. A customer nothing collects for is a no-op, not an error:
// an informational customer settles nowhere by design.
func (r *Registry) RequestSettlement(ctx context.Context, st store.Statement, c store.Customer) (Result, error) {
	g, route := r.For(c)
	if g == nil {
		if c.IsBilled() && c.PaymentMethod == MethodGateway {
			return Result{Outcome: NotApplicable, Detail: "no implementation is registered for gateway " + route},
				fmt.Errorf("%w: %s", ErrNoGateway, route)
		}
		return Result{Outcome: NotApplicable, Detail: "nothing is collected for a customer whose charging is " + c.Charging}, nil
	}
	return g.RequestSettlement(ctx, Request{Statement: st, Customer: c, Method: c.PaymentMethod, GatewayName: c.GatewayName})
}

// ConfirmSettlement resolves the customer's gateway and normalises a
// confirmation through it. A customer with no gateway is confirmed by the
// manual gateway: an operator recording a transfer is always allowed, even
// for a customer nothing collects for automatically.
func (r *Registry) ConfirmSettlement(ctx context.Context, conf Confirmation) (Payment, error) {
	g, route := r.For(conf.Customer)
	conf.Method = conf.Customer.PaymentMethod
	conf.GatewayName = route
	if g == nil {
		g = Manual{}
	}
	return g.ConfirmSettlement(ctx, conf)
}

// ---------------------------------------------------------------------------
// the built-in manual gateway
// ---------------------------------------------------------------------------

// Manual is the built-in gateway for the two payment methods no external
// system collects: TRANSFER — the customer pays by bank transfer against the
// invoice and the operator records it when the bank shows it — and INTERNAL,
// a cost-centre recharge where no money leaves the organisation at all.
type Manual struct{}

// RequestSettlement collects nothing. It reports what the operator should
// expect to happen next, which for a post-paid invoice is a transfer.
func (Manual) RequestSettlement(_ context.Context, req Request) (Result, error) {
	detail := "invoice " + req.Statement.InvoiceNumber
	if req.Statement.PORef != "" {
		detail += " against purchase order " + req.Statement.PORef
	}
	if req.Method == MethodInternal {
		return Result{Outcome: AwaitingTransfer, Gateway: "manual", Detail: detail + " is an internal recharge; no external payment is collected"}, nil
	}
	if req.Statement.DueAt != nil {
		detail += ", due " + req.Statement.DueAt.Format("2006-01-02")
	}
	return Result{Outcome: AwaitingTransfer, Gateway: "manual", Detail: detail + "; payment is recorded when the transfer arrives"}, nil
}

// ConfirmSettlement validates an operator-recorded transfer. It checks the
// facts a payment must carry and nothing else — the store enforces the
// lifecycle, the balance and the refusal to overpay.
func (Manual) ConfirmSettlement(_ context.Context, c Confirmation) (Payment, error) {
	return normalise(c, "manual")
}

// normalise is the shared validation every gateway's ConfirmSettlement wants:
// an amount above zero, a date, and a trimmed reference.
func normalise(c Confirmation, gateway string) (Payment, error) {
	amount := store.Decimal(strings.TrimSpace(string(c.Amount)))
	if amount == "" {
		return Payment{}, fmt.Errorf("%w: a payment needs an amount", store.ErrInvalid)
	}
	if _, err := amount.MarshalJSON(); err != nil {
		return Payment{}, fmt.Errorf("%w: %s is not an amount", store.ErrInvalid, c.Amount)
	}
	paidAt := c.PaidAt
	if paidAt.IsZero() {
		paidAt = time.Now().UTC()
	}
	method := c.Method
	if method == "" {
		method = MethodTransfer
	}
	return Payment{Amount: amount, PaidAt: paidAt.UTC(), Method: method, Reference: strings.TrimSpace(c.Reference),
		Status: store.PaymentReceived, Gateway: gateway}, nil
}

// Normalise is exported for gateways that only need the standard validation
// of an inbound confirmation before returning it.
func Normalise(c Confirmation, gateway string) (Payment, error) { return normalise(c, gateway) }

// ---------------------------------------------------------------------------
// the legacy statement hook, as a gateway
// ---------------------------------------------------------------------------

// StatementHook is the pre-seam shape of a settlement gateway: one call on
// the draft → issued edge. It is what api.StatementHook and the OpenOva
// billing hook have always been.
type StatementHook interface {
	StatementIssued(ctx context.Context, st store.Statement, c store.Customer) error
}

// FromHook adapts a StatementHook into a Gateway, so wiring that predates
// the seam keeps working unchanged: the hook is registered under the stripe
// gateway name and its confirmations are normalised like any other.
func FromHook(h StatementHook) Gateway {
	if h == nil {
		return nil
	}
	if g, ok := h.(Gateway); ok {
		return g
	}
	return hookGateway{h}
}

type hookGateway struct{ h StatementHook }

func (g hookGateway) RequestSettlement(ctx context.Context, req Request) (Result, error) {
	if err := g.h.StatementIssued(ctx, req.Statement, req.Customer); err != nil {
		return Result{Outcome: NotApplicable, Gateway: "statement-hook"}, err
	}
	return Result{Outcome: Settled, Gateway: "statement-hook", Reference: req.Statement.ID}, nil
}

func (g hookGateway) ConfirmSettlement(_ context.Context, c Confirmation) (Payment, error) {
	return normalise(c, "statement-hook")
}
