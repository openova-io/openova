// Package view turns a validated document.Request into the one model both
// renditions draw from: every amount already formatted at the currency's
// minor unit, every date in the locale's layout, and every label carried as a
// CATALOG KEY (plus placeholder values) so that the PDF layout and the HTML
// templates both resolve it through the same i18n `t` function. Nothing here
// knows about fonts, pages or markup.
package view

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/i18n"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/money"
)

// Row is a label (by catalog key, with placeholder values) and a value.
type Row struct {
	Key   string
	Vars  map[string]string
	Value string
	// Emphasis marks a total the reader looks for first (bold, ruled).
	Emphasis bool
	// Negative marks an amount that reduces the total (discounts, payments).
	Negative bool
}

// Party is a formatted seller or buyer block.
type Party struct {
	Name            string
	AddressLines    []string
	TaxRegistration string
	Email           string
	Phone           string
}

// Line is one formatted table row.
type Line struct {
	SKU, Description, Unit, Quantity, UnitPrice, Amount string
}

// Payment is one formatted payment row.
type Payment struct {
	Date, Method, Reference, Amount string
}

// Logo is the decoded seller logo.
type Logo struct {
	Bytes  []byte
	Format string // png | jpeg
}

// Model is what the renditions draw.
type Model struct {
	Template string
	Locale   string
	Currency string

	TitleKey  string
	Number    string
	StatusKey string // "" = no stamp

	Meta []Row

	SellerHeadingKey string
	Seller           Party
	Logo             *Logo
	BuyerHeadingKey  string
	Buyer            Party

	HasDescription bool
	Lines          []Line

	Totals    []Row
	Discounts []Row // detail under the waterfall; Key is "" (free label in Value? no: Vars["label"])
	Notes     []Row // tax exemption, discount rule — informational lines under the totals

	Payments []Payment

	Terms     []Row // sentences composed from the catalog
	TermsText string
	NotesText string

	cat *i18n.Catalog
}

// T resolves a label key through the model's catalog.
func (m *Model) T(key string) string { return m.cat.T(key) }

// Tf resolves a label key with placeholders.
func (m *Model) Tf(key string, vars map[string]string) string { return m.cat.Tf(key, vars) }

// Label resolves a Row's label.
func (m *Model) Label(r Row) string {
	if r.Key == "" {
		return r.Vars["label"]
	}
	return m.cat.Tf(r.Key, r.Vars)
}

// Catalog is the label catalog the model was built with.
func (m *Model) Catalog() *i18n.Catalog { return m.cat }

// StatusClass is the CSS modifier for the status stamp ("paid", "overdue",
// "cancelled", "draft", …), derived from the catalog key rather than from the
// label, so it is the same in every language.
func (m *Model) StatusClass() string {
	if m.StatusKey == "" {
		return ""
	}
	return strings.TrimPrefix(m.StatusKey, "status.")
}

// Build formats a validated request against the catalog. It returns an error
// only for a value the validator accepted but the formatter cannot render,
// which is a defect rather than a client mistake.
func Build(req document.Request, cat *i18n.Catalog) (*Model, error) {
	d := req.Document
	style := money.Style{Thousands: cat.Thousands, Decimal: cat.Decimal}
	amount := func(dec string) (string, error) { return money.Format(dec, req.Currency, style) }
	date := func(s string) (string, error) {
		t, err := document.ParseDate(s)
		if err != nil {
			return "", err
		}
		return t.Format(cat.DateLayout), nil
	}
	var firstErr error
	must := func(s string, err error) string {
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return s
	}

	m := &Model{
		Template:         req.Template,
		Locale:           cat.Locale(),
		Currency:         req.Currency,
		Number:           strings.TrimSpace(d.Number),
		SellerHeadingKey: "party.seller",
		Seller:           party(d.Seller),
		Buyer:            party(d.Buyer),
		TermsText:        strings.TrimSpace(d.Terms.Text),
		NotesText:        strings.TrimSpace(d.Notes),
		cat:              cat,
	}
	switch req.Template {
	case document.Invoice:
		m.TitleKey, m.BuyerHeadingKey = "invoice.title", "party.bill_to"
	case document.CreditNote:
		m.TitleKey, m.BuyerHeadingKey = "credit_note.title", "party.credit_to"
	case document.Statement:
		m.TitleKey, m.BuyerHeadingKey = "statement.title", "party.customer"
	case document.Quote:
		m.TitleKey, m.BuyerHeadingKey = "quote.title", "party.prepared_for"
	default:
		return nil, fmt.Errorf("unknown template %q", req.Template)
	}
	m.StatusKey = statusKey(d.Status, cat)

	if d.Seller.LogoDataURI != "" {
		raw, format, err := document.DecodeLogo(d.Seller.LogoDataURI)
		if err != nil {
			return nil, err
		}
		m.Logo = &Logo{Bytes: raw, Format: format}
	}

	// ── meta block ────────────────────────────────────────────────────────
	m.Meta = append(m.Meta, Row{Key: "field.issued", Value: must(date(d.IssuedAt))})
	if d.DueAt != "" && req.Template == document.Invoice {
		m.Meta = append(m.Meta, Row{Key: "field.due", Value: must(date(d.DueAt))})
	}
	if d.PeriodStart != "" && d.PeriodEnd != "" {
		m.Meta = append(m.Meta, Row{Key: "field.period", Value: must(date(d.PeriodStart)) + " – " + must(date(d.PeriodEnd))})
	}
	if d.ValidUntil != "" && req.Template == document.Quote {
		m.Meta = append(m.Meta, Row{Key: "field.valid_until", Value: must(date(d.ValidUntil))})
	}
	if d.References.PO != "" {
		m.Meta = append(m.Meta, Row{Key: "field.po", Value: d.References.PO})
	}
	if d.References.InvoiceNumber != "" && req.Template != document.Invoice {
		m.Meta = append(m.Meta, Row{Key: "field.invoice", Value: d.References.InvoiceNumber})
	}
	if d.References.External != "" {
		m.Meta = append(m.Meta, Row{Key: "field.external_reference", Value: d.References.External})
	}
	if d.References.StatementID != "" && req.Template == document.Invoice {
		m.Meta = append(m.Meta, Row{Key: "field.statement", Value: shortID(d.References.StatementID)})
	}
	m.Meta = append(m.Meta, Row{Key: "field.currency", Value: req.Currency})

	// ── lines ─────────────────────────────────────────────────────────────
	for _, l := range d.Lines {
		if strings.TrimSpace(l.Description) != "" {
			m.HasDescription = true
		}
		m.Lines = append(m.Lines, Line{
			SKU:         strings.TrimSpace(l.SKU),
			Description: strings.TrimSpace(l.Description),
			Unit:        strings.TrimSpace(l.Unit),
			Quantity:    must(money.FormatQuantity(l.Quantity, style)),
			UnitPrice:   must(money.FormatUnitPrice(l.UnitPrice, req.Currency, style)),
			Amount:      must(amount(l.Amount)),
		})
	}

	// ── waterfall ─────────────────────────────────────────────────────────
	w := d.Waterfall
	showDiscount := !money.IsZero(w.DiscountTotal) || w.ListSubtotal != ""
	if showDiscount {
		list := w.ListSubtotal
		if list == "" {
			list = must(money.Add(w.NetSubtotal, orZero(w.DiscountTotal)))
		}
		m.Totals = append(m.Totals,
			Row{Key: "waterfall.list_subtotal", Value: must(amount(list))},
			Row{Key: "waterfall.discounts", Value: must(amount(must(money.Negate(orZero(w.DiscountTotal))))), Negative: true},
		)
	}
	m.Totals = append(m.Totals, Row{Key: "waterfall.net_subtotal", Value: must(amount(w.NetSubtotal))})
	switch {
	case d.Tax.Exempt:
		m.Totals = append(m.Totals, Row{Key: "waterfall.tax_exempt", Value: must(amount(orZero(w.Tax)))})
	case w.Tax != "" || w.TaxRate != "":
		rate := "0%"
		if w.TaxRate != "" {
			rate = must(money.FormatRatePercent(w.TaxRate, style))
		}
		m.Totals = append(m.Totals, Row{Key: "waterfall.tax", Vars: map[string]string{"rate": rate}, Value: must(amount(orZero(w.Tax)))})
	}
	switch req.Template {
	case document.CreditNote:
		m.Totals = append(m.Totals, Row{Key: "waterfall.total_credited", Value: must(amount(w.Total)), Emphasis: true})
		if w.Applied != "" {
			m.Totals = append(m.Totals, Row{Key: "waterfall.applied", Value: must(amount(w.Applied))})
		}
		if !money.IsZero(w.Unapplied) {
			m.Totals = append(m.Totals, Row{Key: "waterfall.unapplied", Value: must(amount(w.Unapplied))})
		}
	case document.Quote:
		m.Totals = append(m.Totals, Row{Key: "waterfall.quote_total", Value: must(amount(w.Total)), Emphasis: true})
	default:
		m.Totals = append(m.Totals, Row{Key: "waterfall.total", Value: must(amount(w.Total)), Emphasis: true})
		settled := false
		if !money.IsZero(w.Paid) {
			m.Totals = append(m.Totals, Row{Key: "waterfall.paid", Value: must(amount(must(money.Negate(w.Paid)))), Negative: true})
			settled = true
		}
		if !money.IsZero(w.Credited) {
			m.Totals = append(m.Totals, Row{Key: "waterfall.credited", Value: must(amount(must(money.Negate(w.Credited)))), Negative: true})
			settled = true
		}
		if w.Balance != "" && (settled || req.Template == document.Invoice) {
			m.Totals = append(m.Totals, Row{Key: "waterfall.balance_due", Value: must(amount(w.Balance)), Emphasis: true})
		}
	}
	for _, x := range d.Discounts {
		m.Discounts = append(m.Discounts, Row{Vars: map[string]string{"label": strings.TrimSpace(x.Label)}, Value: must(amount(must(money.Negate(x.Amount)))), Negative: true})
	}
	if d.Tax.Exempt && strings.TrimSpace(d.Tax.ExemptReason) != "" {
		m.Notes = append(m.Notes, Row{Key: "tax.exempt_reason", Vars: map[string]string{"reason": strings.TrimSpace(d.Tax.ExemptReason)}})
	}
	if strings.TrimSpace(d.Tax.DiscountRule) != "" && showDiscount {
		m.Notes = append(m.Notes, Row{Key: "discounts.rule", Vars: map[string]string{"rule": strings.TrimSpace(d.Tax.DiscountRule)}})
	}

	// ── payments ──────────────────────────────────────────────────────────
	for _, p := range d.Payments {
		m.Payments = append(m.Payments, Payment{
			Date:      must(date(p.PaidAt)),
			Method:    strings.TrimSpace(p.Method),
			Reference: strings.TrimSpace(p.Reference),
			Amount:    must(amount(p.Amount)),
		})
	}

	// ── terms ─────────────────────────────────────────────────────────────
	switch req.Template {
	case document.Invoice:
		if d.Terms.PaymentTermsDays > 0 {
			m.Terms = append(m.Terms, Row{Key: "terms.net_days", Vars: map[string]string{"days": fmt.Sprint(d.Terms.PaymentTermsDays)}})
		}
		if d.DueAt != "" {
			m.Terms = append(m.Terms, Row{Key: "terms.due_on", Vars: map[string]string{"date": must(date(d.DueAt))}})
		}
	case document.Statement:
		if d.PeriodStart != "" && d.PeriodEnd != "" {
			m.Terms = append(m.Terms, Row{Key: "terms.statement_period", Vars: map[string]string{"from": must(date(d.PeriodStart)), "to": must(date(d.PeriodEnd))}})
		}
	case document.Quote:
		if d.ValidUntil != "" {
			m.Terms = append(m.Terms, Row{Key: "terms.quote_valid", Vars: map[string]string{"date": must(date(d.ValidUntil))}})
		}
		m.Terms = append(m.Terms, Row{Key: "terms.quote_prices", Vars: map[string]string{"currency": req.Currency}})
		if d.Terms.PaymentTermsDays > 0 {
			m.Terms = append(m.Terms, Row{Key: "terms.net_days", Vars: map[string]string{"days": fmt.Sprint(d.Terms.PaymentTermsDays)}})
		}
	case document.CreditNote:
		if d.References.InvoiceNumber != "" {
			m.Terms = append(m.Terms, Row{Key: "terms.credit_note_against", Vars: map[string]string{"number": d.References.InvoiceNumber}})
		}
	}

	if firstErr != nil {
		return nil, firstErr
	}
	return m, nil
}

func party(p document.Party) Party {
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(p.Address, "\r\n", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return Party{
		Name:            strings.TrimSpace(p.Name),
		AddressLines:    lines,
		TaxRegistration: strings.TrimSpace(p.TaxRegistration),
		Email:           strings.TrimSpace(p.Email),
		Phone:           strings.TrimSpace(p.Phone),
	}
}

// statusKey maps a document status to the stamp shown on it. Issued and sent
// are the ordinary states of an invoice and get no stamp; a draft, a paid, an
// overdue or a cancelled document does.
func statusKey(status string, cat *i18n.Catalog) string {
	s := strings.ToLower(strings.TrimSpace(status))
	switch s {
	case "", "issued", "sent":
		return ""
	}
	key := "status." + strings.ReplaceAll(s, "-", "_")
	if cat.Has(key) {
		return key
	}
	return ""
}

func orZero(dec string) string {
	if strings.TrimSpace(dec) == "" {
		return "0"
	}
	return dec
}

var uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// shortID shows the first block of a UUID; anything else is shown whole.
func shortID(id string) string {
	if uuidShape.MatchString(id) {
		return id[:8]
	}
	return id
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Filename is the download name for the document: its number, made safe.
func (m *Model) Filename(ext string) string {
	name := unsafeFilename.ReplaceAllString(m.Number, "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = m.Template
	}
	return name + "." + ext
}
