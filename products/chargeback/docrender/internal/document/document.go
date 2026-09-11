// Package document is the wire contract of POST /v1/render: the request
// envelope, the document model the four templates share, and its validation.
//
// Every amount is a DECIMAL STRING at the ledger's own precision; the
// renderer never receives, stores or emits a float. Dates are ISO
// (YYYY-MM-DD, or RFC 3339 for timestamps). Nothing here is persisted.
package document

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/money"
)

// The four document kinds.
const (
	Invoice    = "invoice"
	CreditNote = "credit-note"
	Statement  = "statement"
	Quote      = "quote"
)

// Templates lists the document kinds, in a stable order.
var Templates = []string{Invoice, CreditNote, Statement, Quote}

// Request is the POST /v1/render body.
type Request struct {
	// Template is one of Templates.
	Template string `json:"template"`
	// Locale selects the label catalog ("en"). Empty = en.
	Locale string `json:"locale"`
	// Currency is the ISO 4217 code every amount is in; it fixes the minor
	// unit money is rendered at.
	Currency string   `json:"currency"`
	Document Document `json:"document"`
}

// Document is the content of one invoice / credit note / statement / quote.
type Document struct {
	// Number is the document's own number (INV-2026-00042, CN-2026-00003, a
	// statement label, a quote reference). Required.
	Number string `json:"number"`
	// Status is shown as a stamp when it matters: draft, issued, sent, paid,
	// overdue, cancelled, pro_forma. Optional.
	Status string `json:"status,omitempty"`
	// IssuedAt is the issue date (YYYY-MM-DD or RFC 3339). Required.
	IssuedAt string `json:"issued_at"`
	// DueAt is the payment due date (invoice). Optional.
	DueAt string `json:"due_at,omitempty"`
	// PeriodStart / PeriodEnd bound the charges (invoice, statement). Optional.
	PeriodStart string `json:"period_start,omitempty"`
	PeriodEnd   string `json:"period_end,omitempty"`
	// ValidUntil is the quote's validity end. Optional.
	ValidUntil string `json:"valid_until,omitempty"`

	Seller Party `json:"seller"`
	Buyer  Party `json:"buyer"`

	References References `json:"references"`

	Lines []Line `json:"lines"`

	Waterfall Waterfall  `json:"waterfall"`
	Discounts []Discount `json:"discounts,omitempty"`
	Tax       Tax        `json:"tax"`

	Payments []Payment `json:"payments,omitempty"`

	Terms Terms  `json:"terms"`
	Notes string `json:"notes,omitempty"`
}

// Party is the seller or the buyer.
type Party struct {
	// Name is the legal name. Required for both parties.
	Name string `json:"name"`
	// Address is free text; newlines separate lines.
	Address         string `json:"address,omitempty"`
	TaxRegistration string `json:"tax_registration,omitempty"`
	Email           string `json:"email,omitempty"`
	Phone           string `json:"phone,omitempty"`
	// LogoDataURI is an optional data:image/png;base64,… or
	// data:image/jpeg;base64,… the seller supplies; at most 512 KiB decoded.
	LogoDataURI string `json:"logo_data_uri,omitempty"`
}

// References are the cross-references the document quotes.
type References struct {
	// PO is the buyer's purchase-order reference.
	PO string `json:"po,omitempty"`
	// InvoiceNumber is the invoice a credit note or a payment refers to.
	InvoiceNumber string `json:"invoice_number,omitempty"`
	// StatementID is the BSS statement behind the document.
	StatementID string `json:"statement_id,omitempty"`
	// External is the reference in the operator's own billing system.
	External string `json:"external,omitempty"`
}

// Line is one priced row.
type Line struct {
	SKU string `json:"sku"`
	// Description is optional; when empty the SKU column carries the row.
	Description string `json:"description,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	Amount      string `json:"amount"`
}

// Waterfall is the totals block, top to bottom. Every field is a decimal
// string; empty means "not shown". ListSubtotal is derived when empty
// (NetSubtotal + DiscountTotal).
type Waterfall struct {
	ListSubtotal  string `json:"list_subtotal,omitempty"`
	DiscountTotal string `json:"discount_total,omitempty"`
	NetSubtotal   string `json:"net_subtotal"`
	TaxRate       string `json:"tax_rate,omitempty"`
	Tax           string `json:"tax,omitempty"`
	Total         string `json:"total"`
	// Invoice / statement settlement.
	Paid     string `json:"paid,omitempty"`
	Credited string `json:"credited,omitempty"`
	Balance  string `json:"balance,omitempty"`
	// Credit note application.
	Applied   string `json:"applied,omitempty"`
	Unapplied string `json:"unapplied,omitempty"`
}

// Discount is one applied discount, for the detail list under the waterfall.
type Discount struct {
	Label  string `json:"label"`
	Amount string `json:"amount"`
}

// Tax is the tax context frozen on the document.
type Tax struct {
	Exempt       bool   `json:"exempt,omitempty"`
	ExemptReason string `json:"exempt_reason,omitempty"`
	// Rule names the discount combination rule in force, when the issuer
	// wants it stated.
	DiscountRule string `json:"discount_rule,omitempty"`
	// Summary is the per-rate tax summary block. An invoice that carries
	// SEVERAL rates has to show what each rate was charged on — the single
	// "Tax (5%)" line of the waterfall cannot say it. Empty = one rate, and
	// the waterfall line is the whole story.
	Summary []TaxRate `json:"summary,omitempty"`
	// Notes are the sentences a zero-rated, exempt or reverse-charge line
	// must carry. They are a LEGAL REQUIREMENT of the document, not a
	// courtesy: an invoice with a zero tax line and no explanation is the
	// defect a tax auditor looks for first.
	Notes []string `json:"notes,omitempty"`
	// QRPayload is the base64 payload of the QR code the tax authority
	// requires on the printed invoice. The renderer ENCODES it into a QR
	// symbol; it never invents or re-derives the payload.
	QRPayload string `json:"qr_payload,omitempty"`
}

// TaxRate is one row of the tax summary: what was taxed, at what rate, and
// how much tax that produced.
type TaxRate struct {
	// Label names the rule ("Oman VAT standard"). Optional; the rate
	// carries the row when it is absent.
	Label string `json:"label,omitempty"`
	// Kind is standard | zero_rated | exempt | reverse_charge |
	// out_of_state. Optional; it selects the wording, never the arithmetic.
	Kind string `json:"kind,omitempty"`
	// Rate is a FRACTION (0.05), rendered as a percentage.
	Rate string `json:"rate"`
	// Base is the taxable amount AFTER discounts; Amount is the tax on it.
	Base   string `json:"base"`
	Amount string `json:"amount"`
	// Note is the sentence this row requires on the document.
	Note string `json:"note,omitempty"`
}

// Payment is one received payment against the document.
type Payment struct {
	PaidAt    string `json:"paid_at"`
	Method    string `json:"method,omitempty"`
	Reference string `json:"reference,omitempty"`
	Amount    string `json:"amount"`
}

// Terms are the commercial terms shown at the foot of the document.
type Terms struct {
	PaymentTermsDays int    `json:"payment_terms_days,omitempty"`
	Text             string `json:"text,omitempty"`
}

// Limits the validator enforces.
const (
	MaxLines        = 5000
	MaxPayments     = 500
	MaxDiscounts    = 200
	MaxLogoBytes    = 512 << 10
	MaxFieldLength  = 2000
	MaxNumberLength = 64
	// MaxTaxRates bounds the tax summary block. A dozen rates on one
	// invoice is already extraordinary; a hundred is a caller mistake.
	MaxTaxRates = 64
	// MaxQRPayload bounds the QR payload. The largest QR symbol this
	// renderer can draw holds 2331 bytes of data, and a base64 payload past
	// that cannot be encoded at all — so it is refused here, with a message,
	// rather than at draw time.
	MaxQRPayload = 2331
)

// ValidationError lists every problem found, by JSON path.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid document: " + strings.Join(e.Problems, "; ")
}

// Validate checks the request and normalises what can be normalised
// (trimmed template/locale/currency). It returns *ValidationError with every
// problem, so a caller fixes them in one round.
func (r *Request) Validate() error {
	var p []string
	add := func(format string, a ...any) { p = append(p, fmt.Sprintf(format, a...)) }

	r.Template = strings.ToLower(strings.TrimSpace(r.Template))
	if !knownTemplate(r.Template) {
		add("template: must be one of %s", strings.Join(Templates, ", "))
	}
	r.Locale = strings.ToLower(strings.TrimSpace(r.Locale))
	if r.Locale == "" {
		r.Locale = "en"
	}
	r.Currency = strings.ToUpper(strings.TrimSpace(r.Currency))
	if len(r.Currency) != 3 || !letters(r.Currency) {
		add("currency: must be a three-letter ISO 4217 code")
	}

	d := &r.Document
	if strings.TrimSpace(d.Number) == "" {
		add("document.number: required")
	} else if len(d.Number) > MaxNumberLength {
		add("document.number: longer than %d", MaxNumberLength)
	}
	if _, err := ParseDate(d.IssuedAt); err != nil {
		add("document.issued_at: %v", err)
	}
	for name, v := range map[string]string{"due_at": d.DueAt, "period_start": d.PeriodStart, "period_end": d.PeriodEnd, "valid_until": d.ValidUntil} {
		if v == "" {
			continue
		}
		if _, err := ParseDate(v); err != nil {
			add("document.%s: %v", name, err)
		}
	}
	if strings.TrimSpace(d.Seller.Name) == "" {
		add("document.seller.name: required")
	}
	if strings.TrimSpace(d.Buyer.Name) == "" {
		add("document.buyer.name: required")
	}
	if d.Seller.LogoDataURI != "" {
		if _, _, err := DecodeLogo(d.Seller.LogoDataURI); err != nil {
			add("document.seller.logo_data_uri: %v", err)
		}
	}
	if d.Buyer.LogoDataURI != "" {
		add("document.buyer.logo_data_uri: only the seller carries a logo")
	}
	for _, f := range []struct{ name, v string }{
		{"seller.address", d.Seller.Address}, {"buyer.address", d.Buyer.Address},
		{"notes", d.Notes}, {"terms.text", d.Terms.Text},
	} {
		if len(f.v) > MaxFieldLength {
			add("document.%s: longer than %d", f.name, MaxFieldLength)
		}
	}

	if len(d.Lines) > MaxLines {
		add("document.lines: more than %d rows", MaxLines)
	}
	if len(d.Lines) == 0 && r.Template != Statement {
		add("document.lines: at least one line is required")
	}
	for i, l := range d.Lines {
		if strings.TrimSpace(l.SKU) == "" && strings.TrimSpace(l.Description) == "" {
			add("document.lines[%d]: sku or description is required", i)
		}
		for name, v := range map[string]string{"quantity": l.Quantity, "unit_price": l.UnitPrice, "amount": l.Amount} {
			if _, err := money.Parse(v); err != nil {
				add("document.lines[%d].%s: %v", i, name, err)
			}
		}
	}

	w := &d.Waterfall
	for name, v := range map[string]string{"net_subtotal": w.NetSubtotal, "total": w.Total} {
		if _, err := money.Parse(v); err != nil {
			add("document.waterfall.%s: %v", name, err)
		}
	}
	for name, v := range map[string]string{
		"list_subtotal": w.ListSubtotal, "discount_total": w.DiscountTotal, "tax_rate": w.TaxRate, "tax": w.Tax,
		"paid": w.Paid, "credited": w.Credited, "balance": w.Balance, "applied": w.Applied, "unapplied": w.Unapplied,
	} {
		if v == "" {
			continue
		}
		if _, err := money.Parse(v); err != nil {
			add("document.waterfall.%s: %v", name, err)
		}
	}
	if len(d.Discounts) > MaxDiscounts {
		add("document.discounts: more than %d rows", MaxDiscounts)
	}
	for i, x := range d.Discounts {
		if strings.TrimSpace(x.Label) == "" {
			add("document.discounts[%d].label: required", i)
		}
		if _, err := money.Parse(x.Amount); err != nil {
			add("document.discounts[%d].amount: %v", i, err)
		}
	}
	if len(d.Payments) > MaxPayments {
		add("document.payments: more than %d rows", MaxPayments)
	}
	for i, x := range d.Payments {
		if _, err := ParseDate(x.PaidAt); err != nil {
			add("document.payments[%d].paid_at: %v", i, err)
		}
		if _, err := money.Parse(x.Amount); err != nil {
			add("document.payments[%d].amount: %v", i, err)
		}
	}
	if d.Terms.PaymentTermsDays < 0 || d.Terms.PaymentTermsDays > 3650 {
		add("document.terms.payment_terms_days: out of range")
	}
	if len(d.Tax.Summary) > MaxTaxRates {
		add("document.tax.summary: more than %d rows", MaxTaxRates)
	}
	for i, t := range d.Tax.Summary {
		for name, v := range map[string]string{"rate": t.Rate, "base": t.Base, "amount": t.Amount} {
			if _, err := money.Parse(v); err != nil {
				add("document.tax.summary[%d].%s: %v", i, name, err)
			}
		}
	}
	if d.Tax.QRPayload != "" {
		if len(d.Tax.QRPayload) > MaxQRPayload {
			add("document.tax.qr_payload: longer than %d characters", MaxQRPayload)
		} else if _, err := base64.StdEncoding.DecodeString(d.Tax.QRPayload); err != nil {
			add("document.tax.qr_payload: must be base64 (%v)", err)
		}
	}

	if len(p) > 0 {
		sort.Strings(p)
		return &ValidationError{Problems: p}
	}
	return nil
}

func knownTemplate(t string) bool {
	for _, k := range Templates {
		if k == t {
			return true
		}
	}
	return false
}

func letters(s string) bool {
	for _, c := range s {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// ParseDate accepts YYYY-MM-DD or RFC 3339 and returns the date in UTC.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("required")
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("%q is not YYYY-MM-DD or RFC 3339", s)
}

// DecodeLogo parses a data URI into image bytes and its format ("png" or
// "jpeg"), enforcing MaxLogoBytes.
func DecodeLogo(uri string) ([]byte, string, error) {
	const prefix = "data:"
	if !strings.HasPrefix(uri, prefix) {
		return nil, "", errors.New("not a data URI")
	}
	meta, payload, ok := strings.Cut(uri[len(prefix):], ",")
	if !ok {
		return nil, "", errors.New("malformed data URI")
	}
	mime, params, _ := strings.Cut(meta, ";")
	if params != "base64" {
		return nil, "", errors.New("data URI must be base64")
	}
	var format string
	switch strings.ToLower(mime) {
	case "image/png":
		format = "png"
	case "image/jpeg", "image/jpg":
		format = "jpeg"
	default:
		return nil, "", fmt.Errorf("unsupported logo type %q (png or jpeg)", mime)
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > MaxLogoBytes+3 {
		return nil, "", fmt.Errorf("logo larger than %d bytes", MaxLogoBytes)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", fmt.Errorf("logo base64: %v", err)
	}
	if len(raw) > MaxLogoBytes {
		return nil, "", fmt.Errorf("logo larger than %d bytes", MaxLogoBytes)
	}
	return raw, format, nil
}

// sampleQRPayload is the TLV stream of the sample invoice's five QR fields,
// base64. It is BUILT here rather than pasted as a blob, so it always
// decodes to the five fields the sample states.
var sampleQRPayload = func() string {
	fields := []struct {
		tag   byte
		value string
	}{
		{1, "Sovereign Cloud Operator LLC"},
		{2, "OM1100012345"},
		{3, "2026-09-01T00:00:00Z"},
		{4, "91.740600"},
		{5, "4.368600"},
	}
	var raw []byte
	for _, f := range fields {
		raw = append(raw, f.tag, byte(len(f.value)))
		raw = append(raw, f.value...)
	}
	return base64.StdEncoding.EncodeToString(raw)
}()

// SampleTax is the tax block of the sample invoice: the per-rate summary and
// the QR payload. Used by the readiness self-render and as the base of the
// test fixtures.
func SampleTax() Tax {
	return Tax{
		// One row, and it agrees with the waterfall exactly: the base is the
		// net subtotal and the amount is the tax. A summary that did not add
		// up to the waterfall would be a document that contradicts itself.
		Summary: []TaxRate{
			{Label: "Oman VAT standard", Kind: "standard", Rate: "0.05", Base: "87.372000", Amount: "4.368600"},
		},
		// The TLV payload of the seller name, registration, timestamp, total
		// and tax — base64, exactly as the issuer computed it.
		QRPayload: sampleQRPayload,
	}
}

// Sample is a small valid invoice used by the readiness self-render and as
// the base of the test fixtures. Names are fictional; the domain is the
// test canon.
func Sample() Request {
	return Request{
		Template: Invoice,
		Locale:   "en",
		Currency: "OMR",
		Document: Document{
			Number:      "INV-2026-00042",
			Status:      "issued",
			IssuedAt:    "2026-09-01",
			DueAt:       "2026-10-01",
			PeriodStart: "2026-08-01",
			PeriodEnd:   "2026-08-31",
			Seller: Party{
				Name:            "Sovereign Cloud Operator LLC",
				Address:         "Knowledge Oasis Muscat\nBuilding 3, Floor 2\nMuscat 130, Oman",
				TaxRegistration: "OM1100012345",
				Email:           "billing@hw307.omani.works",
			},
			Buyer: Party{
				Name:            "Acme Trading LLC",
				Address:         "Al Khuwair\nMuscat, Oman",
				TaxRegistration: "OM1100098765",
				Email:           "finance@acme.omani.homes",
			},
			References: References{PO: "PO-2026-118", StatementID: "3f9c1b2e-7a4d-4c1e-9b0a-1d2e3f4a5b6c"},
			Lines: []Line{
				{SKU: "k8s.vcpu", Description: "Kubernetes vCPU allocation", Unit: "vcpu-hour", Quantity: "2976.000000", UnitPrice: "0.012000", Amount: "35.712000"},
				{SKU: "k8s.mem_gb", Description: "Kubernetes memory allocation", Unit: "gib-hour", Quantity: "5952.000000", UnitPrice: "0.001500", Amount: "8.928000"},
				{SKU: "k8s.pvc_gb", Description: "Persistent volume capacity", Unit: "gb-hour", Quantity: "74400.000000", UnitPrice: "0.000100", Amount: "7.440000"},
				{SKU: "plan.m", Description: "Catalyst plan M", Unit: "month", Quantity: "1.000000", UnitPrice: "45.000000", Amount: "45.000000"},
			},
			Waterfall: Waterfall{
				ListSubtotal:  "97.080000",
				DiscountTotal: "9.708000",
				NetSubtotal:   "87.372000",
				TaxRate:       "0.05",
				Tax:           "4.368600",
				Total:         "91.740600",
				Paid:          "50.000000",
				Balance:       "41.740600",
			},
			Tax:       SampleTax(),
			Discounts: []Discount{{Label: "Launch campaign (10%)", Amount: "9.708000"}},
			Payments:  []Payment{{PaidAt: "2026-09-05", Method: "transfer", Reference: "TRF-88213", Amount: "50.000000"}},
			Terms:     Terms{PaymentTermsDays: 30},
			Notes:     "Thank you for your business.",
		},
	}
}
