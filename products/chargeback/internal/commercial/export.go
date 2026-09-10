package commercial

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/commercial/external"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Exporter hands one rated bill to the operator's billing system and answers
// with the reference that system will know it by, when it has one.
//
// It is called by the DELIVERY LOOP, never on the issuing path: a document is
// queued in the outbox inside the issuing transaction and pushed afterwards,
// so rating and issuing never wait on, and never fail because of, a system in
// someone else's estate.
//
// The contract:
//
//   - Delivery is AT-LEAST-ONCE. The same document, identified by
//     doc.IdempotencyKey (the statement id), may arrive more than once, and
//     the implementation must make that one bill at the far end — by writing
//     to a name derived from the key, or by sending the key as the receiver's
//     idempotency header.
//   - A non-empty externalRef is stored on the statement as
//     external_invoice_ref and is what every later import is matched on.
//     Returning "" is fine when the billing system answers asynchronously:
//     the inbound webhook can name the reference later.
//   - An error is recorded as last_error and the row is retried with
//     exponential backoff. Nothing is lost and nothing blocks.
//
// The transport is entirely the implementer's: a file the operator's own job
// collects, an HTTP POST to a TMF678 endpoint, a message on a queue. No
// operator's endpoints are modelled here.
type Exporter interface {
	Deliver(ctx context.Context, doc InvoiceDocument) (externalRef string, err error)
}

// ---------------------------------------------------------------------------
// the document
// ---------------------------------------------------------------------------

// Money is an exact amount with its currency. Value is a store.Decimal, which
// marshals as a bare JSON number, so money never passes through a float on
// its way out of this product.
type Money struct {
	Value store.Decimal `json:"value"`
	Unit  string        `json:"unit"`
}

// Period is TMF678's billingPeriod.
type Period struct {
	StartDateTime string `json:"startDateTime"`
	EndDateTime   string `json:"endDateTime"`
}

// BillingAccountRef is TMF666's account, as TMF678 references it. ID is the
// customer's external_account_id — the identifier the billing system knows —
// and Name plus the OpenOva slug are carried so a human can reconcile a
// document by eye when the id has not been filled in yet.
type BillingAccountRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Slug is this product's own customer slug: not a TMF field, kept under
	// a clearly non-TMF name so it can be dropped by a strict consumer.
	Slug string `json:"@openovaCustomerSlug"`
}

// RatedUsage is one rated line, in the shape TMF635 gives rated product
// usage: what was used, how much, at what rate, for what money.
type RatedUsage struct {
	// ProductRef is the SKU the meter priced.
	ProductRef string `json:"productRef"`
	// UsageQuantity and UnitOfMeasure are the metered amount.
	UsageQuantity store.Decimal `json:"usageQuantity"`
	UnitOfMeasure string        `json:"unitOfMeasure"`
	// RatingUnitPrice and TaxExcludedRatingAmount are the price and the money.
	RatingUnitPrice         Money `json:"ratingUnitPrice"`
	TaxExcludedRatingAmount Money `json:"taxExcludedRatingAmount"`
	// ResourceCount is how many resources the line aggregates; not TMF, so
	// it is namespaced.
	ResourceCount int `json:"@openovaResourceCount"`
	// SourceRef names the cost source the usage came from, when there is one.
	SourceRef string `json:"@openovaSourceRef,omitempty"`
}

// InvoiceDocument is the rated bill handed to the operator's billing system.
// The field names follow TMF678 CustomerBill where a field exists there;
// everything with no TMF equivalent is namespaced `@openova…` so a strict
// consumer can drop it without losing a required field.
//
// billNo is deliberately EMPTY: the billing system numbers its own invoices,
// and this product never numbers one for it.
type InvoiceDocument struct {
	ID string `json:"id"`
	// IdempotencyKey is the statement id: the same key is the same bill,
	// however many times it is delivered.
	IdempotencyKey string            `json:"@openovaIdempotencyKey"`
	BillNo         string            `json:"billNo"`
	BillDate       string            `json:"billDate"`
	BillingPeriod  Period            `json:"billingPeriod"`
	PaymentDueDate string            `json:"paymentDueDate,omitempty"`
	Category       string            `json:"category"`
	State          string            `json:"state"`
	BillingAccount BillingAccountRef `json:"billingAccount"`

	TaxExcludedAmount Money `json:"taxExcludedAmount"`
	TaxIncludedAmount Money `json:"taxIncludedAmount"`
	TaxAmount         Money `json:"taxAmount"`
	AmountDue         Money `json:"amountDue"`
	RemainingAmount   Money `json:"remainingAmount"`

	// PaymentTerms and PurchaseOrder are the invoice's commercial terms.
	// TMF678 has no field for either, so both are namespaced.
	PaymentTermsDays int    `json:"@openovaPaymentTermsDays"`
	PurchaseOrder    string `json:"@openovaPurchaseOrderReference,omitempty"`
	// DiscountTotal is what discounts took off the list subtotal.
	DiscountTotal Money `json:"@openovaDiscountTotal"`

	// RatedProductUsage is the TMF635-shaped detail behind the totals.
	RatedProductUsage []RatedUsage `json:"ratedProductUsage"`
}

// BuildInvoiceDocument renders a rated statement as the document to export.
// It refuses a customer with no external account id: the billing system has
// nowhere to put a bill it cannot attribute, and discovering that at import
// time would be far worse than refusing to issue.
func BuildInvoiceDocument(st store.Statement, c store.Customer) (InvoiceDocument, error) {
	if strings.TrimSpace(c.ExternalAccountID) == "" {
		return InvoiceDocument{}, fmt.Errorf("%w: customer %s has no external_account_id, and this Sovereign invoices through the operator's billing system", store.ErrInvalid, c.Slug)
	}
	money := func(d store.Decimal) Money { return Money{Value: d, Unit: st.Currency} }
	billDate := st.CreatedAt
	if st.IssuedAt != nil {
		billDate = *st.IssuedAt
	}
	doc := InvoiceDocument{
		ID:             st.ID,
		IdempotencyKey: st.ID,
		BillNo:         "",
		BillDate:       billDate.UTC().Format(time.RFC3339),
		BillingPeriod:  Period{StartDateTime: st.PeriodStart, EndDateTime: st.PeriodEnd},
		Category:       "normal",
		State:          "new",
		BillingAccount: BillingAccountRef{ID: c.ExternalAccountID, Name: c.Name, Slug: c.Slug},

		TaxExcludedAmount: money(st.Subtotal),
		TaxIncludedAmount: money(st.Total),
		TaxAmount:         money(st.Tax),
		AmountDue:         money(st.Total),
		RemainingAmount:   money(st.OutstandingAt()),

		PurchaseOrder: st.PORef,
		DiscountTotal: money(st.DiscountTotal),
	}
	if st.DueAt != nil {
		doc.PaymentDueDate = st.DueAt.UTC().Format(time.RFC3339)
	}
	if st.PaymentTermsDays != nil {
		doc.PaymentTermsDays = *st.PaymentTermsDays
	}
	for _, l := range st.Lines {
		u := RatedUsage{
			ProductRef:              l.SKU,
			UsageQuantity:           l.Quantity,
			UnitOfMeasure:           l.Unit,
			RatingUnitPrice:         money(l.UnitPrice),
			TaxExcludedRatingAmount: money(l.Amount),
			ResourceCount:           l.ResourceCount,
		}
		if l.SourceID != nil {
			u.SourceRef = *l.SourceID
		}
		doc.RatedProductUsage = append(doc.RatedProductUsage, u)
	}
	return doc, nil
}

// ---------------------------------------------------------------------------
// csvfile — the one exporter that ships
// ---------------------------------------------------------------------------

// CSVFileExporter writes one CSV per bill into a directory the operator
// collects from. Getting the file to the billing system — SFTP, a mounted
// share, a pickup job — is the operator's concern and deliberately not this
// product's.
//
// The reference it returns is the file's stem, which is also written into
// every row, so the billing system can echo it back on the import.
//
// It is ALSO the exporter of every other document the outbox carries
// (DESIGN.md §9.1) — TMF635 rated usage, TMF666 account, TMF676 payment,
// the summary charge — through external.CSVFile in the same directory: one
// transport, one wiring, one directory the billing system collects from.
type CSVFileExporter struct {
	Dir string
	mu  sync.Mutex
}

// NewCSVFileExporter returns an exporter writing into dir.
func NewCSVFileExporter(dir string) *CSVFileExporter { return &CSVFileExporter{Dir: dir} }

// DeliverDocument implements external.Exporter for the non-invoice
// documents, writing them beside the bills.
func (e *CSVFileExporter) DeliverDocument(ctx context.Context, env external.Envelope) (string, error) {
	return (&external.CSVFile{Dir: e.Dir}).DeliverDocument(ctx, env)
}

// ExportRef is the reference a csvfile export is known by:
// <period>-<customer slug>-<first 8 of the statement id>. It is derived from
// the idempotency key, so a redelivery overwrites its own file rather than
// making a second bill.
func ExportRef(doc InvoiceDocument) string {
	period := doc.BillingPeriod.StartDateTime
	if len(period) >= 7 {
		period = period[:7]
	}
	id := doc.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("%s-%s-%s", period, doc.BillingAccount.Slug, id)
}

// Deliver writes the bill as CSV and returns its reference. Delivering the
// same document again overwrites the same file: the name comes from the
// idempotency key, which is what makes at-least-once delivery safe here.
func (e *CSVFileExporter) Deliver(_ context.Context, doc InvoiceDocument) (string, error) {
	if strings.TrimSpace(e.Dir) == "" {
		return "", fmt.Errorf("%w: no export directory is configured", store.ErrInvalid)
	}
	ref := ExportRef(doc)
	if err := os.MkdirAll(e.Dir, 0o750); err != nil {
		return "", err
	}
	// One writer at a time: two statements of the same customer and period
	// cannot exist, but two goroutines issuing at once must not interleave
	// into one file handle.
	e.mu.Lock()
	defer e.mu.Unlock()
	path := filepath.Join(e.Dir, ref+".csv")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := writeInvoiceCSV(w, ref, doc); err != nil {
		return "", err
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return ref, nil
}

// writeInvoiceCSV renders the document: a header row, one `line` row per
// rated line, and the `total` rows the billing system posts. Every money
// value is the exact decimal string, never a formatted number.
func writeInvoiceCSV(w *csv.Writer, ref string, doc InvoiceDocument) error {
	rows := [][]string{{
		"external_ref", "record", "bill_id", "bill_date", "period_start", "period_end", "due_date",
		"billing_account_id", "billing_account_name", "customer_slug", "currency",
		"product_ref", "quantity", "unit", "unit_price", "amount", "resource_count", "source_ref",
		"purchase_order", "payment_terms_days",
	}}
	head := func(record string) []string {
		return []string{
			ref, record, doc.ID, doc.BillDate, doc.BillingPeriod.StartDateTime, doc.BillingPeriod.EndDateTime, doc.PaymentDueDate,
			doc.BillingAccount.ID, doc.BillingAccount.Name, doc.BillingAccount.Slug, doc.AmountDue.Unit,
		}
	}
	for _, u := range doc.RatedProductUsage {
		row := append(head("line"),
			u.ProductRef, string(u.UsageQuantity), u.UnitOfMeasure, string(u.RatingUnitPrice.Value), string(u.TaxExcludedRatingAmount.Value),
			fmt.Sprint(u.ResourceCount), u.SourceRef, doc.PurchaseOrder, fmt.Sprint(doc.PaymentTermsDays))
		rows = append(rows, row)
	}
	totals := []struct {
		name string
		m    Money
	}{
		{"discount_total", doc.DiscountTotal},
		{"tax_excluded_amount", doc.TaxExcludedAmount},
		{"tax_amount", doc.TaxAmount},
		{"tax_included_amount", doc.TaxIncludedAmount},
		{"amount_due", doc.AmountDue},
	}
	for _, t := range totals {
		rows = append(rows, append(head("total"),
			t.name, "", "", "", string(t.m.Value), "", "", doc.PurchaseOrder, fmt.Sprint(doc.PaymentTermsDays)))
	}
	return w.WriteAll(rows)
}

// ---------------------------------------------------------------------------
// a recording exporter, for tests and dry runs
// ---------------------------------------------------------------------------

// Recorder keeps every document it was handed instead of writing anywhere.
// It is what a test asserts the export document against, and what a dry run
// of a new Sovereign can be pointed at.
type Recorder struct {
	mu   sync.Mutex
	docs []InvoiceDocument
	// Ref, when set, produces the reference for a document; the default is
	// ExportRef, the same one the csvfile exporter uses.
	Ref func(InvoiceDocument) string
	// Err, when set, is returned instead of exporting.
	Err error
	// FailTimes fails the first N deliveries before succeeding.
	FailTimes int
	failed    int
	attempts  int
	envs      []external.Envelope
}

// DeliverDocument implements external.Exporter: the non-invoice documents
// are recorded too, so a test can assert what left the outbox by type.
func (r *Recorder) DeliverDocument(_ context.Context, env external.Envelope) (string, error) {
	if r.Err != nil {
		return "", r.Err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envs = append(r.envs, env)
	return external.Ref(env), nil
}

// Envelopes returns the non-invoice documents exported, oldest first.
func (r *Recorder) Envelopes() []external.Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]external.Envelope{}, r.envs...)
}

// OfType returns the exported envelopes of one document type.
func (r *Recorder) OfType(t string) []external.Envelope {
	var out []external.Envelope
	for _, e := range r.Envelopes() {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Deliver records the document and returns its reference. FailTimes, when
// set, fails that many deliveries first — the shape a test needs to prove
// that a retried document is delivered exactly once.
func (r *Recorder) Deliver(_ context.Context, doc InvoiceDocument) (string, error) {
	r.mu.Lock()
	r.attempts++
	if r.failed < r.FailTimes {
		r.failed++
		n := r.failed
		r.mu.Unlock()
		return "", fmt.Errorf("the billing system is unavailable (failure %d)", n)
	}
	r.mu.Unlock()
	if r.Err != nil {
		return "", r.Err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs = append(r.docs, doc)
	if r.Ref != nil {
		return r.Ref(doc), nil
	}
	return ExportRef(doc), nil
}

// Attempts is how many times Deliver was called, failures included.
func (r *Recorder) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// Docs returns what was exported, oldest first.
func (r *Recorder) Docs() []InvoiceDocument {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]InvoiceDocument{}, r.docs...)
}

// Last returns the most recent document, and whether there was one.
func (r *Recorder) Last() (InvoiceDocument, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.docs) == 0 {
		return InvoiceDocument{}, false
	}
	return r.docs[len(r.docs)-1], true
}
