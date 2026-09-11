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
	"net/http"
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

// Purpose says WHY a gateway is asked for money (DESIGN.md §9.2, founder
// refinement (a)): the two are different triggers with different owners.
const (
	// PurposeCheckout — the customer is present and pays now: a marketplace
	// plan purchase, a top-up. It is a SALE, so our product calls the
	// gateway in EVERY commercial mode, external included.
	PurposeCheckout = store.PurposeCheckout
	// PurposeCollection — an unpaid invoice is pursued. Only the owner of
	// the receivable triggers that, so with an external billing system this
	// request is refused before it reaches a gateway.
	PurposeCollection = store.PurposeCollection
)

// Request asks a gateway to collect money. For a COLLECTION it carries the
// statement being pursued and the amount is the statement's total; for a
// CHECKOUT there may be no statement at all — Amount and Currency say what
// is being taken, IntentID is the payment intent the answer is recorded on.
type Request struct {
	Statement store.Statement
	Customer  store.Customer
	// Method is the customer's payment method — gateway, transfer or
	// internal — and GatewayName the key this implementation was
	// registered under when the method is gateway.
	Method      string
	GatewayName string
	// Purpose is checkout or collection; empty reads as collection, which
	// is what every pre-seam caller meant.
	Purpose string
	// Amount and Currency are what to collect. Empty falls back to the
	// statement's total and currency.
	Amount   store.Decimal
	Currency string
	// IntentID is the payment intent this request belongs to, when the
	// caller recorded one; a gateway echoes it in its own reference so the
	// confirmation can be matched.
	IntentID string
}

// IsCheckout reports whether the request is a sale rather than a collection.
func (r Request) IsCheckout() bool { return r.Purpose == PurposeCheckout }

// AmountDue is what the request collects: Amount when given, else the
// statement's total.
func (r Request) AmountDue() (store.Decimal, string) {
	if strings.TrimSpace(string(r.Amount)) != "" {
		cur := r.Currency
		if cur == "" {
			cur = r.Statement.Currency
		}
		return r.Amount, cur
	}
	return r.Statement.Total, r.Statement.Currency
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
//
// A confirmation a gateway delivers on its own (VerifyCallback) names WHAT
// it is about by identifier — CustomerID or CustomerSlug, and the
// StatementID or IntentID when it settles one — because the gateway holds
// no store; the callback handler resolves Statement and Customer from them.
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
	// Identifiers a gateway callback carries (DESIGN.md §9.2). Empty when
	// the caller already resolved Statement and Customer.
	CustomerID   string
	CustomerSlug string
	StatementID  string
	IntentID     string
	// Status is the gateway's word for the outcome — settled, pending,
	// failed, refunded — mapped by the handler; empty is settled.
	Status string
}

// ErrCallbackNotSupported is answered by a gateway that has no inbound
// callback: the built-in Manual gateway (a transfer is recorded by the
// operator), and a hook that predates the seam.
var ErrCallbackNotSupported = errors.New("this gateway delivers no payment callback")

// ErrCallbackRejected wraps a callback whose signature did not verify or
// whose body could not be read; the route answers 401.
var ErrCallbackRejected = errors.New("gateway callback rejected")

// ErrMethodSetupNotSupported is answered by a gateway that cannot SAVE a
// payment method: the built-in Manual gateway (a bank transfer has nothing
// to save) and a hook that predates the seam.
var ErrMethodSetupNotSupported = errors.New("this gateway cannot save a payment method")

// SetupRequest asks a gateway to begin saving a payment method for a
// customer (DESIGN.md §16). It carries no card details and never will: the
// card is entered on the GATEWAY's page, not on ours, which is the whole
// reason this seam exists.
type SetupRequest struct {
	Customer store.Customer
	// GatewayName is the key the implementation was registered under.
	GatewayName string
	// ReturnURL is where the gateway sends the payer back once the method
	// is entered; empty when the gateway needs no redirect.
	ReturnURL string
	// Label is the customer's own name for the method, passed through.
	Label string
	// Actor is who asked, for the audit trail.
	Actor string
}

// SetupResult is what the gateway answers a SetupRequest with: the page the
// customer completes on, and the id that completion is confirmed under.
// Method is set only when the gateway saved the method during the call.
type SetupResult struct {
	Gateway string
	// SetupID is the gateway's own id for this setup, echoed back to
	// ConfirmMethod. It is an opaque handle, never a secret of ours.
	SetupID string
	// SetupURL is the hosted page the customer enters the card on; empty
	// when the gateway saved the method during the call.
	SetupURL string
	// Detail is one human-readable line for the customer.
	Detail string
	// Method is the saved method, when the gateway saved one during the
	// call rather than sending the customer to a page.
	Method *SavedMethod
}

// MethodConfirmation says a setup completed. It names the setup by the id
// the gateway gave and nothing else: the implementation reads the rest from
// the gateway, so nothing a caller could forge decides what is saved.
type MethodConfirmation struct {
	Customer    store.Customer
	GatewayName string
	SetupID     string
	Actor       string
}

// SavedMethod is the DISPLAY record of a saved payment method — precisely
// what a gateway returns for showing it back to the payer, and nothing more.
// There is deliberately nowhere here to put a card number, a security code
// or a gateway secret: the only identifier is Token, the gateway's own
// opaque handle, which is worthless without the gateway's own credentials.
type SavedMethod struct {
	Gateway string
	// Token is the gateway's id for the saved method — what a later charge
	// names. Stored; never rendered on the wire and never audited.
	Token string
	// Brand, Last4 and the expiry are the display triple a payer recognises
	// the card by. Last4 is FOUR DIGITS AT MOST: the store's own CHECK
	// constraint refuses anything longer, so a gateway that mistakenly
	// returned a whole number could not be recorded.
	Brand    string
	Last4    string
	ExpMonth int
	ExpYear  int
	// Label is the customer's own name for it.
	Label string
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

// Gateway collects money for statements. Three methods: ask for settlement,
// accept the confirmation that it happened, and verify the confirmation the
// gateway delivers on its own.
//
// VerifyCallback is the inbound half of the seam (DESIGN.md §9.2): the
// gateway posts to POST /api/v1/gateways/{name}/callback, unauthenticated,
// and the implementation verifies the request with the gateway's OWN
// signature scheme — a shared secret, a public key, whatever the gateway
// specifies — and normalises the body into the Confirmation the route books
// through the commercial provider. It returns ErrCallbackRejected (wrapped)
// for a request that does not verify and ErrCallbackNotSupported when the
// gateway has no callback at all.
//
// SetupMethod and ConfirmMethod are the SAVED-METHOD half (DESIGN.md §16):
// the customer asks to keep a method on file, the gateway answers with the
// page to enter it on, and the completion is confirmed back through
// ConfirmMethod into the display record the store keeps. A gateway with no
// such facility returns ErrMethodSetupNotSupported, exactly as a gateway
// with no callback returns ErrCallbackNotSupported.
type Gateway interface {
	RequestSettlement(ctx context.Context, req Request) (Result, error)
	ConfirmSettlement(ctx context.Context, c Confirmation) (Payment, error)
	VerifyCallback(r *http.Request) (Confirmation, error)
	SetupMethod(ctx context.Context, req SetupRequest) (SetupResult, error)
	ConfirmMethod(ctx context.Context, c MethodConfirmation) (SavedMethod, error)
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

// Gateway resolves an implementation by the name a callback route names:
// a registered gateway, or the built-in manual one under "manual".
func (r *Registry) Gateway(name string) (Gateway, bool) {
	if r == nil {
		return nil, false
	}
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "manual" {
		if r.manual == nil {
			return Manual{}, true
		}
		return r.manual, true
	}
	g, ok := r.byGateway[name]
	return g, ok
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
	return g.RequestSettlement(ctx, Request{Statement: st, Customer: c, Method: c.PaymentMethod, GatewayName: c.GatewayName, Purpose: PurposeCollection})
}

// RequestPayment is RequestSettlement for an explicit request — a checkout
// with no statement, or a collection the caller built itself. The route is
// resolved from the customer exactly as RequestSettlement does; the
// commercial-provider check (a collection is never requested when the
// external billing system owns the receivable) is the caller's, in
// commercial.Intents, because the gateway seam must not know who invoices.
func (r *Registry) RequestPayment(ctx context.Context, req Request) (Result, error) {
	g, route := r.For(req.Customer)
	req.Method, req.GatewayName = req.Customer.PaymentMethod, route
	if req.Purpose == "" {
		req.Purpose = PurposeCollection
	}
	if g == nil {
		if req.Customer.IsBilled() && req.Customer.PaymentMethod == MethodGateway {
			return Result{Outcome: NotApplicable, Detail: "no implementation is registered for gateway " + route},
				fmt.Errorf("%w: %s", ErrNoGateway, route)
		}
		return Result{Outcome: NotApplicable, Detail: "nothing is collected for a customer whose charging is " + req.Customer.Charging}, nil
	}
	return g.RequestSettlement(ctx, req)
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

// SetupMethod resolves the customer's gateway and asks it to begin saving a
// payment method. A customer nothing collects for, or one that pays by
// transfer, is ErrMethodSetupNotSupported rather than a silent success:
// there is no instrument to keep, and telling the caller so is the answer.
func (r *Registry) SetupMethod(ctx context.Context, req SetupRequest) (SetupResult, error) {
	g, route := r.For(req.Customer)
	req.GatewayName = route
	if g == nil {
		if req.Customer.IsBilled() && req.Customer.PaymentMethod == MethodGateway {
			return SetupResult{}, fmt.Errorf("%w: %s", ErrNoGateway, route)
		}
		return SetupResult{}, ErrMethodSetupNotSupported
	}
	res, err := g.SetupMethod(ctx, req)
	if res.Gateway == "" {
		res.Gateway = route
	}
	return res, err
}

// ConfirmMethod resolves the customer's gateway and asks it what the
// completed setup saved.
func (r *Registry) ConfirmMethod(ctx context.Context, c MethodConfirmation) (SavedMethod, error) {
	g, route := r.For(c.Customer)
	c.GatewayName = route
	if g == nil {
		if c.Customer.IsBilled() && c.Customer.PaymentMethod == MethodGateway {
			return SavedMethod{}, fmt.Errorf("%w: %s", ErrNoGateway, route)
		}
		return SavedMethod{}, ErrMethodSetupNotSupported
	}
	m, err := g.ConfirmMethod(ctx, c)
	if m.Gateway == "" {
		m.Gateway = route
	}
	return m, err
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
	if req.IsCheckout() {
		// A top-up or purchase paid by transfer: the operator records the
		// payment when the bank shows it, and it lands as account credit.
		amount, cur := req.AmountDue()
		if req.Method == MethodInternal {
			return Result{Outcome: AwaitingTransfer, Gateway: "manual", Detail: fmt.Sprintf("%s %s is an internal recharge; record it as a top-up when the cost centre confirms", amount, cur)}, nil
		}
		return Result{Outcome: AwaitingTransfer, Gateway: "manual", Detail: fmt.Sprintf("%s %s by transfer; record the top-up when the transfer arrives", amount, cur)}, nil
	}
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

// VerifyCallback: a transfer has no gateway to call back; the operator
// records it when the bank shows it.
func (Manual) VerifyCallback(*http.Request) (Confirmation, error) {
	return Confirmation{}, ErrCallbackNotSupported
}

// SetupMethod / ConfirmMethod: a bank transfer and an internal recharge
// have nothing to keep on file — there is no instrument, only an invoice
// and a payer who settles it.
func (Manual) SetupMethod(context.Context, SetupRequest) (SetupResult, error) {
	return SetupResult{}, ErrMethodSetupNotSupported
}

func (Manual) ConfirmMethod(context.Context, MethodConfirmation) (SavedMethod, error) {
	return SavedMethod{}, ErrMethodSetupNotSupported
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
	if req.IsCheckout() {
		// The pre-seam hook only ever debited an ISSUED statement; it has no
		// checkout surface, and pretending otherwise would take nothing.
		return Result{Outcome: NotApplicable, Gateway: "statement-hook", Detail: "the statement hook collects issued statements only; a checkout needs a gateway with a payment page"}, nil
	}
	if err := g.h.StatementIssued(ctx, req.Statement, req.Customer); err != nil {
		return Result{Outcome: NotApplicable, Gateway: "statement-hook"}, err
	}
	return Result{Outcome: Settled, Gateway: "statement-hook", Reference: req.Statement.ID}, nil
}

func (g hookGateway) ConfirmSettlement(_ context.Context, c Confirmation) (Payment, error) {
	return normalise(c, "statement-hook")
}

// CallbackVerifier is the one method a legacy hook may add to accept
// callbacks through the adapter.
type CallbackVerifier interface {
	VerifyCallback(r *http.Request) (Confirmation, error)
}

// VerifyCallback delegates to the hook when it verifies callbacks itself;
// a hook that predates the seam has none.
func (g hookGateway) VerifyCallback(r *http.Request) (Confirmation, error) {
	if v, ok := g.h.(CallbackVerifier); ok {
		return v.VerifyCallback(r)
	}
	return Confirmation{}, ErrCallbackNotSupported
}

// MethodSaver is the pair of methods a legacy hook may add to save payment
// methods through the adapter, exactly as CallbackVerifier is the one it
// may add to accept callbacks.
type MethodSaver interface {
	SetupMethod(ctx context.Context, req SetupRequest) (SetupResult, error)
	ConfirmMethod(ctx context.Context, c MethodConfirmation) (SavedMethod, error)
}

// SetupMethod / ConfirmMethod delegate to the hook when it saves methods
// itself; a hook that predates the seam saves none.
func (g hookGateway) SetupMethod(ctx context.Context, req SetupRequest) (SetupResult, error) {
	if v, ok := g.h.(MethodSaver); ok {
		return v.SetupMethod(ctx, req)
	}
	return SetupResult{}, ErrMethodSetupNotSupported
}

func (g hookGateway) ConfirmMethod(ctx context.Context, c MethodConfirmation) (SavedMethod, error) {
	if v, ok := g.h.(MethodSaver); ok {
		return v.ConfirmMethod(ctx, c)
	}
	return SavedMethod{}, ErrMethodSetupNotSupported
}
