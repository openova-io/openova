// Package external is the contract with an operator's billing system when it
// — not this product — is the system of record (DESIGN.md §9.1).
//
// Our product is MEDIATION and RATING. Invoicing, payment, collections, the
// customer's account and the customer master belong to whichever system is
// the system of record for the Sovereign. So this package defines, against
// the TM Forum Open API shapes such a system speaks:
//
//   - the OUTBOUND documents we export — TMF635 rated usage, TMF666 account,
//     TMF676 payment, and the one-line summary charge for a billing system
//     that cannot ingest a rated bill — beside the TMF678 bill lane 1 already
//     exports (commercial.InvoiceDocument);
//   - the Exporter every document leaves through, with a csvfile
//     implementation for ERPs that batch and a recording fake for tests;
//   - the INBOUND commands — invoice status, payment status, account balance,
//     enforcement — that arrive over the HMAC-verified webhook family, and a
//     directory poller as the fallback for a system that cannot call us.
//
// No operator's endpoints are modelled. The Omantel adapter is written
// against their specification later, exactly as a payment gateway is.
package external

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Document types the outbox carries. `invoice` is lane 1's TMF678 bill; the
// rest are this lane's. Every one is keyed on its subject's id, so
// at-least-once delivery of a row is one document at the far end.
const (
	DocInvoice       = "invoice"        // TMF678 CustomerBill (commercial.InvoiceDocument)
	DocRatedUsage    = "rated-usage"    // TMF635 usage, rated
	DocAccount       = "account"        // TMF666 BillingAccount with its balance
	DocPayment       = "payment"        // TMF676 Payment
	DocSummaryCharge = "summary-charge" // one receivable line per statement
	// DocJournal is a period's double-entry journal (DESIGN.md §18.2) — the
	// finance handover, delivered the same way as the bills.
	DocJournal = "journal"
)

// Money is an exact amount with its currency — a store.Decimal, never a
// float, on its way out of this product.
type Money struct {
	Value store.Decimal `json:"value"`
	Unit  string        `json:"unit"`
}

// Period is TMF's timePeriod.
type Period struct {
	StartDateTime string `json:"startDateTime"`
	EndDateTime   string `json:"endDateTime"`
}

// AccountRef names the billing account a document is about: the customer's
// external_account_id (TMF666 id), with the name and our own slug beside it
// so a human can reconcile by eye.
type AccountRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"@openovaCustomerSlug"`
}

// Envelope is what the outbox hands the Exporter for every non-invoice
// document: the type, the idempotency key, and the document itself.
type Envelope struct {
	Type           string          `json:"type"`
	IdempotencyKey string          `json:"idempotency_key"`
	Document       json.RawMessage `json:"document"`
}

// RatedUsage is one rated line in the TMF635 shape.
type RatedUsage struct {
	ProductRef              string        `json:"productRef"`
	UsageQuantity           store.Decimal `json:"usageQuantity"`
	UnitOfMeasure           string        `json:"unitOfMeasure"`
	RatingUnitPrice         Money         `json:"ratingUnitPrice"`
	TaxExcludedRatingAmount Money         `json:"taxExcludedRatingAmount"`
	ResourceCount           int           `json:"@openovaResourceCount"`
	SourceRef               string        `json:"@openovaSourceRef,omitempty"`
}

// RatedUsageDocument is the TMF635 usage report for one statement period —
// the detail a billing system that rates nothing itself needs beside, or
// instead of, the bill.
type RatedUsageDocument struct {
	ID             string       `json:"id"`
	IdempotencyKey string       `json:"@openovaIdempotencyKey"`
	StatementID    string       `json:"@openovaStatementId"`
	InvoiceNumber  string       `json:"@openovaInvoiceNumber,omitempty"`
	UsageDate      string       `json:"usageDate"`
	Period         Period       `json:"ratingPeriod"`
	Status         string       `json:"status"`
	BillingAccount AccountRef   `json:"billingAccount"`
	RatedUsage     []RatedUsage `json:"ratedProductUsage"`
	TaxExcluded    Money        `json:"@openovaTaxExcludedAmount"`
}

// BuildRatedUsage renders a statement's lines as the TMF635 document.
func BuildRatedUsage(st store.Statement, c store.Customer) RatedUsageDocument {
	money := func(d store.Decimal) Money { return Money{Value: d, Unit: st.Currency} }
	at := st.CreatedAt
	if st.IssuedAt != nil {
		at = *st.IssuedAt
	}
	doc := RatedUsageDocument{
		ID: st.ID, IdempotencyKey: st.ID, StatementID: st.ID, InvoiceNumber: st.InvoiceNumber,
		UsageDate:      at.UTC().Format(time.RFC3339),
		Period:         Period{StartDateTime: st.PeriodStart, EndDateTime: st.PeriodEnd},
		Status:         "rated",
		BillingAccount: AccountRef{ID: c.ExternalAccountID, Name: c.Name, Slug: c.Slug},
		TaxExcluded:    money(st.Subtotal),
	}
	for _, l := range st.Lines {
		u := RatedUsage{ProductRef: l.SKU, UsageQuantity: l.Quantity, UnitOfMeasure: l.Unit, RatingUnitPrice: money(l.UnitPrice), TaxExcludedRatingAmount: money(l.Amount), ResourceCount: l.ResourceCount}
		if l.SourceID != nil {
			u.SourceRef = *l.SourceID
		}
		doc.RatedUsage = append(doc.RatedUsage, u)
	}
	return doc
}

// SummaryChargeDocument is ONE receivable line for a billing system that
// cannot ingest a rated bill (founder refinement (b)): the account, OUR
// invoice number as the reference, total, tax, currency and period. We
// number and hold the detailed invoice; they book this.
type SummaryChargeDocument struct {
	ID             string     `json:"id"`
	IdempotencyKey string     `json:"@openovaIdempotencyKey"`
	BillingAccount AccountRef `json:"billingAccount"`
	// Reference is our invoice number — what the billing system quotes back.
	Reference      string `json:"reference"`
	Description    string `json:"description"`
	Period         Period `json:"billingPeriod"`
	ChargeDate     string `json:"chargeDate"`
	PaymentDueDate string `json:"paymentDueDate,omitempty"`
	TaxExcluded    Money  `json:"taxExcludedAmount"`
	Tax            Money  `json:"taxAmount"`
	TaxIncluded    Money  `json:"taxIncludedAmount"`
	// The tax profile the invoice was issued under, so the receivable
	// carries the same registration the document does.
	CustomerTaxNumber string `json:"@openovaCustomerTaxRegistrationNumber,omitempty"`
	TaxExempt         bool   `json:"@openovaTaxExempt"`
}

// BuildSummaryCharge renders a statement as the one-line charge.
func BuildSummaryCharge(st store.Statement, c store.Customer, invoiceNumber string) (SummaryChargeDocument, error) {
	if strings.TrimSpace(c.ExternalAccountID) == "" {
		return SummaryChargeDocument{}, fmt.Errorf("%w: customer %s has no external_account_id, and this Sovereign books its charges in the operator's billing system", store.ErrInvalid, c.Slug)
	}
	if strings.TrimSpace(invoiceNumber) == "" {
		return SummaryChargeDocument{}, fmt.Errorf("%w: a summary charge quotes the invoice number, and none was assigned", store.ErrInvalid)
	}
	money := func(d store.Decimal) Money { return Money{Value: d, Unit: st.Currency} }
	at := st.CreatedAt
	if st.IssuedAt != nil {
		at = *st.IssuedAt
	}
	doc := SummaryChargeDocument{
		ID: st.ID, IdempotencyKey: st.ID,
		BillingAccount: AccountRef{ID: c.ExternalAccountID, Name: c.Name, Slug: c.Slug},
		Reference:      invoiceNumber,
		Description:    fmt.Sprintf("Cloud usage %s to %s, invoice %s", st.PeriodStart, st.PeriodEnd, invoiceNumber),
		Period:         Period{StartDateTime: st.PeriodStart, EndDateTime: st.PeriodEnd},
		ChargeDate:     at.UTC().Format(time.RFC3339),
		TaxExcluded:    money(st.Subtotal), Tax: money(st.Tax), TaxIncluded: money(st.Total),
	}
	if st.DueAt != nil {
		doc.PaymentDueDate = st.DueAt.UTC().Format(time.RFC3339)
	}
	if st.TaxSnapshot != nil {
		doc.CustomerTaxNumber, doc.TaxExempt = st.TaxSnapshot.CustomerTaxNumber, st.TaxSnapshot.Exempt
	} else {
		doc.CustomerTaxNumber, doc.TaxExempt = c.TaxRegistrationNumber, c.TaxExempt
	}
	return doc, nil
}

// PaymentDocument is a TMF676 Payment: money this product took at a
// CHECKOUT that the billing system must learn about, since it owns the
// account the money lands on.
type PaymentDocument struct {
	ID             string     `json:"id"`
	IdempotencyKey string     `json:"@openovaIdempotencyKey"`
	BillingAccount AccountRef `json:"account"`
	Amount         Money      `json:"amount"`
	TotalAmount    Money      `json:"totalAmount"`
	PaymentDate    string     `json:"paymentDate"`
	Status         string     `json:"status"`
	// PaymentMethod names how the money moved (gateway name, transfer, internal).
	PaymentMethod string `json:"paymentMethod"`
	// Reference is the gateway or bank reference that proves it.
	Reference string `json:"correlatorId,omitempty"`
	Purpose   string `json:"@openovaPurpose"`
	// PaymentItems name the invoices the payment was allocated to; empty is
	// credit on the account.
	PaymentItems []PaymentItem `json:"paymentItem,omitempty"`
}

// PaymentItem is one allocation of a payment to an invoice.
type PaymentItem struct {
	Amount        Money  `json:"amount"`
	InvoiceRef    string `json:"@openovaStatementId"`
	InvoiceNumber string `json:"@openovaInvoiceNumber,omitempty"`
}

// BuildPayment renders a payment as the TMF676 document.
func BuildPayment(p store.Payment, c store.Customer, currency string) PaymentDocument {
	money := func(d store.Decimal) Money { return Money{Value: d, Unit: currency} }
	method := p.Method
	if p.Method == store.PaymentMethodGateway && p.Gateway != "" && p.Gateway != "manual" {
		method = p.Gateway
	}
	status := "succeeded"
	switch p.Status {
	case store.PaymentPending:
		status = "pending"
	case store.PaymentFailed:
		status = "failed"
	case store.PaymentRefunded:
		status = "refunded"
	}
	doc := PaymentDocument{
		ID: fmt.Sprintf("payment-%d", p.ID), IdempotencyKey: fmt.Sprintf("payment-%d", p.ID),
		BillingAccount: AccountRef{ID: c.ExternalAccountID, Name: c.Name, Slug: c.Slug},
		Amount:         money(p.Amount), TotalAmount: money(p.Amount),
		PaymentDate: p.PaidAt.UTC().Format(time.RFC3339),
		Status:      status, PaymentMethod: method, Reference: p.Reference, Purpose: p.Purpose,
	}
	for _, a := range p.Allocations {
		doc.PaymentItems = append(doc.PaymentItems, PaymentItem{Amount: money(a.Amount), InvoiceRef: a.StatementID, InvoiceNumber: a.InvoiceNumber})
	}
	return doc
}

// AccountDocument is the TMF666 BillingAccount with the balance as this
// product sees it — exported beside a checkout payment so the billing
// system that owns the account can reconcile what we hold.
type AccountDocument struct {
	ID             string     `json:"id"`
	IdempotencyKey string     `json:"@openovaIdempotencyKey"`
	Account        AccountRef `json:"billingAccount"`
	State          string     `json:"state"`
	// AccountBalance is TMF666's balance list: one entry per kind.
	AccountBalance []AccountBalance `json:"accountBalance"`
	AsOf           string           `json:"@openovaAsOf"`
}

// AccountBalance is one TMF666 balance entry.
type AccountBalance struct {
	BalanceType string `json:"balanceType"`
	Amount      Money  `json:"amount"`
	ValidFor    Period `json:"validFor"`
}

// BuildAccount renders the customer's account as the TMF666 document. The
// idempotency key carries the instant, so each snapshot is its own row.
func BuildAccount(c store.Customer, b store.AccountBalance, currency string, at time.Time) AccountDocument {
	money := func(d store.Decimal) Money { return Money{Value: d, Unit: currency} }
	stamp := at.UTC().Format(time.RFC3339)
	state := "active"
	if c.Status == "suspended" {
		state = "suspended"
	}
	valid := Period{StartDateTime: stamp}
	return AccountDocument{
		ID: c.ExternalAccountID, IdempotencyKey: fmt.Sprintf("account-%s-%s", c.ID, stamp),
		Account: AccountRef{ID: c.ExternalAccountID, Name: c.Name, Slug: c.Slug},
		State:   state,
		AccountBalance: []AccountBalance{
			{BalanceType: "receivableBalance", Amount: money(b.Balance), ValidFor: valid},
			{BalanceType: "availableCredit", Amount: money(b.AvailableCredit), ValidFor: valid},
			{BalanceType: "outstanding", Amount: money(b.Outstanding), ValidFor: valid},
		},
		AsOf: stamp,
	}
}

// JournalLine is one double-entry line of a period's journal, in the shape
// the finance handover renders (DESIGN.md §18.2). TMF has no journal object,
// so every field is namespaced `@openova…` except the three a reader would
// recognise anyway; nothing here is claimed as a TM Forum shape it is not.
type JournalLine struct {
	Seq         int    `json:"@openovaSeq"`
	Date        string `json:"date"`
	Event       string `json:"@openovaEvent"`
	AccountKey  string `json:"@openovaAccountKey"`
	AccountCode string `json:"accountCode"`
	AccountName string `json:"accountName,omitempty"`
	Debit       Money  `json:"debitAmount"`
	Credit      Money  `json:"creditAmount"`
	// Who the line is about, when it is about anybody.
	CustomerSlug string `json:"@openovaCustomerSlug,omitempty"`
	CustomerName string `json:"@openovaCustomerName,omitempty"`
	// SourceKind and SourceID are the object the line came from — a
	// statement, a payment, a credit note, a reconciliation run — which is
	// what makes any figure in the export traceable back.
	SourceKind string `json:"@openovaSourceKind"`
	SourceID   string `json:"@openovaSourceId"`
	Reference  string `json:"@openovaReference,omitempty"`
	Memo       string `json:"description,omitempty"`
}

// JournalDocument is one period's journal as the outbox carries it. The
// totals are the ones the balance assertion checked before the document was
// built: a journal that does not balance is never exported at all.
type JournalDocument struct {
	ID             string `json:"id"`
	IdempotencyKey string `json:"@openovaIdempotencyKey"`
	Period         string `json:"@openovaPeriod"`
	// Status is the period's close state: open, closed or reopened. A closed
	// period's journal is the frozen one, and re-exporting it re-delivers the
	// same document.
	Status      string        `json:"@openovaPeriodStatus"`
	TotalDebit  Money         `json:"@openovaTotalDebit"`
	TotalCredit Money         `json:"@openovaTotalCredit"`
	Lines       []JournalLine `json:"@openovaJournalLine"`
}

// BuildJournal renders a period's journal as the document to export.
func BuildJournal(period, status string, lines []JournalLine, totalDebit, totalCredit store.Decimal, currency string) JournalDocument {
	return JournalDocument{
		ID: "journal-" + period, IdempotencyKey: "journal-" + period + "-" + status,
		Period: period, Status: status,
		TotalDebit:  Money{Value: totalDebit, Unit: currency},
		TotalCredit: Money{Value: totalCredit, Unit: currency},
		Lines:       lines,
	}
}

// Wrap marshals a typed document into the Envelope the outbox stores.
func Wrap(docType, idempotencyKey string, doc any) (Envelope, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: docType, IdempotencyKey: idempotencyKey, Document: raw}, nil
}
