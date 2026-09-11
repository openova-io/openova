package external

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Exporter delivers one non-invoice document to the operator's billing
// system and answers with the reference it will be known by, or "" when the
// far end answers later. The same contract as commercial.Exporter for the
// bill: at-least-once on the idempotency key, an error is retried with
// backoff, nothing on the issuing path ever waits for it.
type Exporter interface {
	DeliverDocument(ctx context.Context, env Envelope) (externalRef string, err error)
}

// ---------------------------------------------------------------------------
// csvfile
// ---------------------------------------------------------------------------

// CSVFile writes one CSV per document into a directory the operator's own
// job collects from, named `<type>-<key>.csv` so a redelivery overwrites its
// own file rather than duplicating the document.
type CSVFile struct {
	Dir string
	mu  sync.Mutex
}

// Ref is the reference a csvfile export is known by.
func Ref(env Envelope) string {
	key := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, env.IdempotencyKey)
	if len(key) > 48 {
		key = key[:48]
	}
	return env.Type + "-" + key
}

// DeliverDocument writes the document and returns its reference.
func (e *CSVFile) DeliverDocument(_ context.Context, env Envelope) (string, error) {
	if strings.TrimSpace(e.Dir) == "" {
		return "", fmt.Errorf("%w: no export directory is configured", store.ErrInvalid)
	}
	rows, err := csvRows(env)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(e.Dir, 0o750); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ref := Ref(env)
	f, err := os.OpenFile(filepath.Join(e.Dir, ref+".csv"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	return ref, nil
}

// csvRows renders a document as rows: a header, then one row per line the
// document carries (a rated-usage line, a payment item, a balance entry) or
// one row for a single-line document. Every money value is the exact
// decimal string.
func csvRows(env Envelope) ([][]string, error) {
	ref := Ref(env)
	switch env.Type {
	case DocRatedUsage:
		var d RatedUsageDocument
		if err := json.Unmarshal(env.Document, &d); err != nil {
			return nil, err
		}
		rows := [][]string{{"external_ref", "record", "statement_id", "invoice_number", "billing_account_id", "billing_account_name", "customer_slug", "period_start", "period_end", "currency",
			"product_ref", "quantity", "unit", "unit_price", "amount", "resource_count", "source_ref"}}
		for _, u := range d.RatedUsage {
			rows = append(rows, []string{ref, "usage", d.StatementID, d.InvoiceNumber, d.BillingAccount.ID, d.BillingAccount.Name, d.BillingAccount.Slug, d.Period.StartDateTime, d.Period.EndDateTime, d.TaxExcluded.Unit,
				u.ProductRef, string(u.UsageQuantity), u.UnitOfMeasure, string(u.RatingUnitPrice.Value), string(u.TaxExcludedRatingAmount.Value), fmt.Sprint(u.ResourceCount), u.SourceRef})
		}
		rows = append(rows, []string{ref, "total", d.StatementID, d.InvoiceNumber, d.BillingAccount.ID, d.BillingAccount.Name, d.BillingAccount.Slug, d.Period.StartDateTime, d.Period.EndDateTime, d.TaxExcluded.Unit,
			"tax_excluded_amount", "", "", "", string(d.TaxExcluded.Value), "", ""})
		return rows, nil
	case DocSummaryCharge:
		var d SummaryChargeDocument
		if err := json.Unmarshal(env.Document, &d); err != nil {
			return nil, err
		}
		return [][]string{
			{"external_ref", "billing_account_id", "billing_account_name", "customer_slug", "reference", "description", "period_start", "period_end", "charge_date", "due_date", "currency", "tax_excluded_amount", "tax_amount", "tax_included_amount", "customer_tax_registration_number", "tax_exempt"},
			{ref, d.BillingAccount.ID, d.BillingAccount.Name, d.BillingAccount.Slug, d.Reference, d.Description, d.Period.StartDateTime, d.Period.EndDateTime, d.ChargeDate, d.PaymentDueDate, d.TaxIncluded.Unit,
				string(d.TaxExcluded.Value), string(d.Tax.Value), string(d.TaxIncluded.Value), d.CustomerTaxNumber, fmt.Sprint(d.TaxExempt)},
		}, nil
	case DocPayment:
		var d PaymentDocument
		if err := json.Unmarshal(env.Document, &d); err != nil {
			return nil, err
		}
		rows := [][]string{{"external_ref", "record", "payment_id", "billing_account_id", "billing_account_name", "customer_slug", "payment_date", "status", "method", "reference", "purpose", "currency", "amount", "statement_id", "invoice_number"}}
		rows = append(rows, []string{ref, "payment", d.ID, d.BillingAccount.ID, d.BillingAccount.Name, d.BillingAccount.Slug, d.PaymentDate, d.Status, d.PaymentMethod, d.Reference, d.Purpose, d.Amount.Unit, string(d.Amount.Value), "", ""})
		for _, it := range d.PaymentItems {
			rows = append(rows, []string{ref, "allocation", d.ID, d.BillingAccount.ID, d.BillingAccount.Name, d.BillingAccount.Slug, d.PaymentDate, d.Status, d.PaymentMethod, d.Reference, d.Purpose, it.Amount.Unit, string(it.Amount.Value), it.InvoiceRef, it.InvoiceNumber})
		}
		return rows, nil
	case DocJournal:
		var d JournalDocument
		if err := json.Unmarshal(env.Document, &d); err != nil {
			return nil, err
		}
		rows := [][]string{{"external_ref", "record", "period", "period_status", "seq", "date", "event", "account_key", "account_code", "account_name",
			"debit", "credit", "currency", "customer_slug", "customer_name", "source_kind", "source_id", "reference", "memo"}}
		for _, l := range d.Lines {
			rows = append(rows, []string{ref, "line", d.Period, d.Status, fmt.Sprint(l.Seq), l.Date, l.Event, l.AccountKey, l.AccountCode, l.AccountName,
				string(l.Debit.Value), string(l.Credit.Value), l.Debit.Unit, l.CustomerSlug, l.CustomerName, l.SourceKind, l.SourceID, l.Reference, l.Memo})
		}
		// The totals the balance assertion checked, as their own record, so
		// the far end can assert them again without re-summing.
		rows = append(rows, []string{ref, "total", d.Period, d.Status, "", "", "", "", "", "",
			string(d.TotalDebit.Value), string(d.TotalCredit.Value), d.TotalDebit.Unit, "", "", "", "", "", "debits and credits"})
		return rows, nil
	case DocAccount:
		var d AccountDocument
		if err := json.Unmarshal(env.Document, &d); err != nil {
			return nil, err
		}
		rows := [][]string{{"external_ref", "billing_account_id", "billing_account_name", "customer_slug", "state", "as_of", "balance_type", "currency", "amount"}}
		for _, b := range d.AccountBalance {
			rows = append(rows, []string{ref, d.Account.ID, d.Account.Name, d.Account.Slug, d.State, d.AsOf, b.BalanceType, b.Amount.Unit, string(b.Amount.Value)})
		}
		return rows, nil
	}
	return nil, fmt.Errorf("%w: unknown document type %q", store.ErrInvalid, env.Type)
}

// ---------------------------------------------------------------------------
// recorder and null
// ---------------------------------------------------------------------------

// Recorder keeps every envelope instead of writing anywhere — the fake a
// test asserts the export against.
type Recorder struct {
	mu   sync.Mutex
	envs []Envelope
	// Err, when set, is returned instead of recording.
	Err error
}

// DeliverDocument records the envelope.
func (r *Recorder) DeliverDocument(_ context.Context, env Envelope) (string, error) {
	if r.Err != nil {
		return "", r.Err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envs = append(r.envs, env)
	return Ref(env), nil
}

// Envelopes returns what was exported, oldest first.
func (r *Recorder) Envelopes() []Envelope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Envelope{}, r.envs...)
}

// OfType returns the exported envelopes of one type.
func (r *Recorder) OfType(t string) []Envelope {
	var out []Envelope
	for _, e := range r.Envelopes() {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// Null accepts every document and delivers it nowhere — for a Sovereign
// that wants the outbox drained without any transport wired.
type Null struct{}

// DeliverDocument discards the envelope.
func (Null) DeliverDocument(_ context.Context, env Envelope) (string, error) { return Ref(env), nil }
