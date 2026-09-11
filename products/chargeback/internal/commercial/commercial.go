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
//
// TWO SEAMS EXACTLY (DESIGN.md §9, founder direction 2026-09-10): seam one is
// the billing system of record — invoice, collections and account are ONE
// capability, binary per Sovereign, this setting; seam two is the payment
// gateway (internal/settle), pluggable by name and independent of seam one.
// Everything outbound couples by DOCUMENTS through the outbox; everything
// inbound arrives through the HMAC-verified webhook family. There is no
// third seam.
package commercial

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
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

// ErrCollectionExternallyOwned is what a COLLECTION request meets in
// external mode: only the owner of the receivable pursues an unpaid
// invoice, and here that is the operator's billing system. A CHECKOUT is
// never refused by this check — it is a sale, whoever invoices.
var ErrCollectionExternallyOwned = fmt.Errorf("%w: pursuing an unpaid invoice is %w; only its settled status is imported", store.ErrConflict, ErrExternallyOwned)

// AllowsCollection is the provider check of the founder's refinement (a):
// nil in internal mode; ErrCollectionExternallyOwned (a 409) in external
// mode. Every path that would ask a gateway for an INVOICE's money — issue,
// a collection intent, a recorded collection — goes through it.
func (s *Selector) AllowsCollection(ctx context.Context) (store.BillingSettings, error) {
	settings, err := s.Store.GetBillingSettings(ctx)
	if err != nil {
		return settings, err
	}
	if settings.ExternalCommercial() {
		return settings, ErrCollectionExternallyOwned
	}
	return settings, nil
}

// OwnsCollections reports whether this product runs reminders, escalation
// and enforcement-by-policy: only in internal mode. In external mode
// collections belong to the billing system and enforcement is executed here
// ONLY on an explicit imported command (DESIGN.md §9.7).
func (s *Selector) OwnsCollections(ctx context.Context) (bool, error) {
	settings, err := s.Store.GetBillingSettings(ctx)
	if err != nil {
		return false, err
	}
	return !settings.ExternalCommercial(), nil
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
// never sends anything to a customer, never pursues an invoice, and never
// decides that an invoice is paid: those are the billing system's, and they
// reach us as imports. It numbers an invoice in ONE case — the
// summary-charge variant (DESIGN.md §9.1), where the billing system cannot
// ingest a rated bill and asks us for the document plus one line to book.
type External struct {
	Store *store.Store
	// Exporter is used by the delivery loop, not here: nothing on the issuing
	// path talks to the billing system.
	Exporter Exporter
}

func (External) Name() string { return store.ProviderExternal }

// Issue builds the documents, then flips the statement to issued and queues
// them in the outbox IN ONE TRANSACTION. It does not deliver: the delivery
// loop does that afterwards, with backoff, so issuing a bill completes at
// our speed and on our availability. An export that cannot be delivered
// right now is a queued row with a last_error the operator can see — never a
// bill that was silently not raised.
//
// What is queued depends on external_ingest:
//
//	rated_bill      the TMF678 bill (no number: theirs to assign) and the
//	                TMF635 rated usage beside it;
//	summary_charge  ONE summary charge line quoting the invoice number THIS
//	                product assigns inside the same transaction, plus the
//	                TMF635 rated usage — the detailed invoice document is
//	                ours, the receivable is booked over there.
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
	settings, err := p.Store.GetBillingSettings(ctx)
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
	// The buyer's tax profile as it stands, so the exported documents and
	// the frozen snapshot agree.
	// The per-rule summary and the determination audit ride along (DESIGN.md
	// §17): the exported bill and the frozen snapshot must state the same
	// rates, or the operator's billing system books something the invoice
	// does not say.
	st.TaxSnapshot = &store.TaxSnapshot{Rate: st.TaxRate, Exempt: c.TaxExempt, ExemptReason: c.TaxExemptReason, CustomerName: c.Name, CustomerTaxNumber: c.TaxRegistrationNumber,
		SellerLegalName: settings.LegalName, SellerTaxNumber: settings.TaxRegistrationNumber, SellerAddress: settings.Address,
		Lines: st.TaxLines, Audit: st.TaxAudit, CustomerCountry: c.TaxCountry, SellerCountry: settings.TaxCountry,
		CustomerExemptionNumber: c.TaxExemptionNumber}
	if c.TaxExemptionExpiresOn != nil {
		st.TaxSnapshot.CustomerExemptionExpiresOn = *c.TaxExemptionExpiresOn
	}
	// The rated-bill document is validated up front — a customer with no
	// billing-account id is refused before the transaction opens.
	if _, err := BuildInvoiceDocument(st, c); err != nil {
		return store.Statement{}, false, err
	}
	summary := settings.SummaryCharge()
	build := func(invoiceNumber string) ([]byte, []store.OutboxDocument, error) {
		st.InvoiceNumber = invoiceNumber
		usage, err := json.Marshal(external.BuildRatedUsage(st, c))
		if err != nil {
			return nil, nil, err
		}
		extra := []store.OutboxDocument{{DocType: external.DocRatedUsage, Document: usage}}
		if summary {
			doc, err := external.BuildSummaryCharge(st, c, invoiceNumber)
			if err != nil {
				return nil, nil, err
			}
			primary, err := json.Marshal(doc)
			return primary, extra, err
		}
		doc, err := BuildInvoiceDocument(st, c)
		if err != nil {
			return nil, nil, err
		}
		primary, err := json.Marshal(doc)
		return primary, extra, err
	}
	docType := store.OutboxInvoice
	if summary {
		docType = external.DocSummaryCharge
	}
	return p.Store.IssueStatementExternally(ctx, store.ExternalIssue{
		StatementID: id, DocType: docType,
		PORef: poRef, TermsDays: terms, IssuedAt: issuedAt,
		NumberInvoice: summary, BuildDocuments: build,
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

// ---------------------------------------------------------------------------
// exporting what a checkout did, in external mode
// ---------------------------------------------------------------------------

// QueuePaymentExport queues the TMF676 payment and the TMF666 account
// documents for a settled CHECKOUT payment when the billing system owns the
// account (DESIGN.md §9.2): we took the money because it was a sale, and
// the account it lands on is theirs, so they must learn about both. A no-op
// in internal mode, and for a customer with no billing-account id (there is
// no account over there to attribute it to; the operator sees it on ours).
func (s *Selector) QueuePaymentExport(ctx context.Context, p store.Payment) error {
	settings, err := s.Store.GetBillingSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.ExternalCommercial() {
		return nil
	}
	c, err := s.Store.GetCustomer(ctx, store.OperatorScope, p.CustomerID)
	if err != nil {
		return err
	}
	if c.ExternalAccountID == "" {
		return nil
	}
	bal, err := s.Store.GetAccountBalance(ctx, store.OperatorScope, c.ID)
	if err != nil {
		return err
	}
	currency, err := s.Store.CustomerCurrency(ctx, c.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	pay, err := external.Wrap(external.DocPayment, fmt.Sprintf("payment-%d", p.ID), external.BuildPayment(p, c, currency))
	if err != nil {
		return err
	}
	acc := external.BuildAccount(c, bal, currency, now)
	account, err := external.Wrap(external.DocAccount, acc.IdempotencyKey, acc)
	if err != nil {
		return err
	}
	return s.Store.QueueOutbox(ctx, c.ID, "",
		store.OutboxDocument{DocType: pay.Type, IdempotencyKey: pay.IdempotencyKey, Document: pay.Document},
		store.OutboxDocument{DocType: account.Type, IdempotencyKey: account.IdempotencyKey, Document: account.Document})
}
