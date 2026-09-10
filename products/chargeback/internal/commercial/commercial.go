// Package commercial owns WHO invoices — and it is not always us.
//
// Omantel, and any operator like it, already runs invoicing, payment and
// collections. This product must not become a second system of record beside
// theirs. What it is unambiguously good at is mediation and rating: collecting
// usage, pricing it, and producing a rated bill. Invoicing, payment,
// collections and the customer account belong to whichever system is the
// system of record for the Sovereign.
//
// So the Sovereign carries one setting, billing_settings.commercial_provider:
//
//	internal  — this product invoices: it numbers, sends, records payments and
//	            runs the lifecycle. The DEFAULT, so an upgraded Sovereign
//	            behaves exactly as it did.
//	external  — the operator's billing system is the system of record. Issuing
//	            a statement EXPORTS the rated bill to it and records the
//	            reference it comes back with; after that the lifecycle is
//	            driven ONLY by imports, and our own send / payment / cancel
//	            endpoints refuse with "owned by the external billing system".
//
// Both paths sit behind the same InvoiceProvider interface, so the API
// handlers do not branch on the setting and neither does the UI.
//
// The exported documents are aimed at the TM Forum Open APIs — TMF678
// (Customer Bill) for the bill, TMF635 (Usage Management) for the rated
// usage inside it, TMF666 (Account) for the billing account it is raised
// against, TMF676 (Payment) for what comes back — and the field names here
// follow TMF678 where a field exists there. No operator's endpoints are
// modelled: the transport is an Exporter implementation, and `csvfile` is the
// one that ships.
package commercial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// ErrExternallyOwned is returned by every lifecycle call this product does
// not own in external mode. The API answers it as 409.
var ErrExternallyOwned = errors.New("owned by the external billing system")

// InvoiceProvider is the whole surface a system of record has to offer: the
// four things that happen to an invoice after a period is rated.
type InvoiceProvider interface {
	// Name is "internal" or "external", for audit entries and the UI.
	Name() string
	// Issue turns a draft into an invoice and reports whether THIS call made
	// the transition, so the caller mails the customer exactly once.
	Issue(ctx context.Context, statementID string) (store.Statement, bool, error)
	// Send records that the customer has the invoice.
	Send(ctx context.Context, statementID string) (store.Statement, bool, error)
	// RecordPayment books one payment against the invoice.
	RecordPayment(ctx context.Context, statementID string, in store.PaymentInput) (store.Statement, store.StatementPayment, error)
	// Cancel voids the invoice.
	Cancel(ctx context.Context, statementID, reason string) (store.Statement, bool, error)
}

// Selector resolves the provider from the Sovereign's setting on each call.
// The setting is a single row and changing it must take effect at once, so it
// is read rather than cached.
type Selector struct {
	Store    *store.Store
	Internal InvoiceProvider
	External InvoiceProvider
}

// NewSelector wires the two providers over one store. exporter may be nil:
// issuing still queues the document in the outbox, and it waits there until
// an exporter is configured — a bill is never lost because the transport was
// not wired yet.
func NewSelector(st *store.Store, exporter Exporter) *Selector {
	return &Selector{
		Store:    st,
		Internal: Internal{Store: st},
		External: External{Store: st, Exporter: exporter},
	}
}

// For returns the provider in force, and the setting it came from.
func (s *Selector) For(ctx context.Context) (InvoiceProvider, store.BillingSettings, error) {
	settings, err := s.Store.GetBillingSettings(ctx)
	if err != nil {
		return nil, settings, err
	}
	if settings.ExternalCommercial() {
		return s.External, settings, nil
	}
	return s.Internal, settings, nil
}

// ---------------------------------------------------------------------------
// internal — this product is the system of record
// ---------------------------------------------------------------------------

// Internal is the provider that invoices here: gapless numbering, the
// lifecycle in store/invoicing.go, payments recorded against our own ledger.
type Internal struct{ Store *store.Store }

func (Internal) Name() string { return store.ProviderInternal }

func (p Internal) Issue(ctx context.Context, id string) (store.Statement, bool, error) {
	return p.Store.IssueStatementOnce(ctx, id)
}

func (p Internal) Send(ctx context.Context, id string) (store.Statement, bool, error) {
	return p.Store.SendStatement(ctx, id)
}

func (p Internal) RecordPayment(ctx context.Context, id string, in store.PaymentInput) (store.Statement, store.StatementPayment, error) {
	return p.Store.RecordStatementPayment(ctx, id, in)
}

func (p Internal) Cancel(ctx context.Context, id, reason string) (store.Statement, bool, error) {
	return p.Store.CancelStatement(ctx, id, reason)
}

// ---------------------------------------------------------------------------
// external — the operator's billing system is the system of record
// ---------------------------------------------------------------------------

// External queues the rated bill for export and then gets out of the way. It
// never assigns an invoice number, never sends anything to a customer, and
// never decides that an invoice is paid: those are the billing system's, and
// they reach us as imports.
type External struct {
	Store *store.Store
	// Exporter is used by the delivery loop, not here: nothing on the issuing
	// path talks to the billing system.
	Exporter Exporter
}

func (External) Name() string { return store.ProviderExternal }

// Issue builds the document, then flips the statement to issued and queues
// the document in the outbox IN ONE TRANSACTION. It does not deliver: the
// delivery loop does that afterwards, with backoff, so issuing a bill
// completes at our speed and on our availability. An export that cannot be
// delivered right now is a queued row with a last_error the operator can
// see — never a bill that was silently not raised.
func (p External) Issue(ctx context.Context, id string) (store.Statement, bool, error) {
	st, err := p.Store.GetStatement(ctx, store.OperatorScope, id)
	if err != nil {
		return store.Statement{}, false, err
	}
	if st.Status == store.StatusIssued {
		// Already issued and queued; issuing is idempotent.
		return st, false, nil
	}
	if st.Status != store.StatusDraft {
		return store.Statement{}, false, fmt.Errorf("%w: a %s statement cannot be issued", store.ErrConflict, st.Status)
	}
	c, err := p.Store.GetCustomer(ctx, store.OperatorScope, st.CustomerID)
	if err != nil {
		return store.Statement{}, false, err
	}
	// Resolve the invoice terms HERE, so the document carries exactly what
	// the statement will say: a statement override wins over the customer's
	// standing purchase order and terms, and the due date follows from them.
	issuedAt := time.Now().UTC()
	poRef := st.PORef
	if poRef == "" {
		poRef = c.PORef
	}
	terms := c.PaymentTermsDays
	if terms < 0 {
		terms = store.DefaultPaymentTermsDays
	}
	if st.PaymentTermsDays != nil {
		terms = *st.PaymentTermsDays
	}
	due := issuedAt.AddDate(0, 0, terms)
	st.PORef, st.PaymentTermsDays, st.IssuedAt, st.DueAt = poRef, &terms, &issuedAt, &due
	doc, err := BuildInvoiceDocument(st, c)
	if err != nil {
		return store.Statement{}, false, err
	}
	body, err := json.Marshal(doc)
	if err != nil {
		return store.Statement{}, false, err
	}
	return p.Store.IssueStatementExternally(ctx, store.ExternalIssue{
		StatementID: id, DocType: store.OutboxInvoice, Document: body,
		PORef: poRef, TermsDays: terms, IssuedAt: issuedAt,
	})
}

func (External) Send(context.Context, string) (store.Statement, bool, error) {
	return store.Statement{}, false, fmt.Errorf("%w: sending the invoice is %w", store.ErrConflict, ErrExternallyOwned)
}

func (External) RecordPayment(context.Context, string, store.PaymentInput) (store.Statement, store.StatementPayment, error) {
	return store.Statement{}, store.StatementPayment{}, fmt.Errorf("%w: recording a payment is %w", store.ErrConflict, ErrExternallyOwned)
}

func (External) Cancel(context.Context, string, string) (store.Statement, bool, error) {
	return store.Statement{}, false, fmt.Errorf("%w: cancelling the invoice is %w", store.ErrConflict, ErrExternallyOwned)
}
