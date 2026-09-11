// Package docs is the client of the document renderer (EPIC #6867): it maps
// a statement — the waterfall, the rated lines, the invoice block and the
// payments — onto the renderer's document JSON and posts it, receiving a PDF.
//
// The renderer is a separate, stateless, in-cluster service
// (products/chargeback/docrender). This package holds the MAPPING; it makes
// no decision about money. Every amount is passed through as the decimal
// STRING the store holds, so nothing here can round, re-scale or float a
// figure the ledger already settled — the renderer formats at the currency's
// minor unit and this side never touches the digits.
//
// Unconfigured (URL empty) is a first-class state, not an error: a Sovereign
// that has not enabled the renderer answers 503 on the download route and is
// otherwise completely unaffected.
package docs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// ErrNotConfigured is returned when no renderer URL is set.
var ErrNotConfigured = errors.New("document renderer not configured")

// DefaultTimeout bounds one render call. The renderer's own deadline is 10s;
// this is that plus the round trip, so the renderer's 504 arrives first and
// says which document failed.
const DefaultTimeout = 20 * time.Second

// maxDocumentBytes caps what is accepted back. The renderer's own request cap
// is 2 MiB of JSON; a PDF from that is far smaller, and a response past this
// is a misconfiguration rather than a document.
const maxDocumentBytes = 32 << 20

// Client posts documents to the renderer.
type Client struct {
	// BaseURL is the renderer's in-cluster address, e.g.
	// http://chargeback-docrender.chargeback.svc.cluster.local:8080. Empty =
	// the feature is off and every call returns ErrNotConfigured.
	BaseURL string
	// Token is the optional shared secret sent as X-Render-Token.
	Token string
	// HTTP is the transport; nil uses a client with DefaultTimeout.
	HTTP *http.Client
}

// New builds a client. An empty url yields a client whose Enabled reports
// false — the caller checks that rather than branching on nil.
func New(url, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(url), "/"),
		Token:   strings.TrimSpace(token),
		HTTP:    &http.Client{Timeout: DefaultTimeout},
	}
}

// Enabled reports whether a renderer is configured.
func (c *Client) Enabled() bool { return c != nil && c.BaseURL != "" }

// Document kinds the renderer understands.
const (
	TemplateInvoice    = "invoice"
	TemplateCreditNote = "credit-note"
	TemplateStatement  = "statement"
	TemplateQuote      = "quote"
)

// Request is the renderer's wire contract (docrender/internal/document).
type Request struct {
	Template string   `json:"template"`
	Locale   string   `json:"locale"`
	Currency string   `json:"currency"`
	Document Document `json:"document"`
}

// Document is one rendered document.
type Document struct {
	Number      string `json:"number"`
	Status      string `json:"status,omitempty"`
	IssuedAt    string `json:"issued_at"`
	DueAt       string `json:"due_at,omitempty"`
	PeriodStart string `json:"period_start,omitempty"`
	PeriodEnd   string `json:"period_end,omitempty"`
	ValidUntil  string `json:"valid_until,omitempty"`

	Seller Party `json:"seller"`
	Buyer  Party `json:"buyer"`

	References References `json:"references"`
	Lines      []Line     `json:"lines"`

	Waterfall Waterfall  `json:"waterfall"`
	Discounts []Discount `json:"discounts,omitempty"`
	Tax       Tax        `json:"tax"`
	Payments  []Payment  `json:"payments,omitempty"`

	Terms Terms  `json:"terms"`
	Notes string `json:"notes,omitempty"`
}

// Party is the seller or the buyer.
type Party struct {
	Name            string `json:"name"`
	Address         string `json:"address,omitempty"`
	TaxRegistration string `json:"tax_registration,omitempty"`
	Email           string `json:"email,omitempty"`
	Phone           string `json:"phone,omitempty"`
	LogoDataURI     string `json:"logo_data_uri,omitempty"`
}

// References are the cross-references the document quotes.
type References struct {
	PO            string `json:"po,omitempty"`
	InvoiceNumber string `json:"invoice_number,omitempty"`
	StatementID   string `json:"statement_id,omitempty"`
	External      string `json:"external,omitempty"`
}

// Line is one priced row.
type Line struct {
	SKU         string `json:"sku"`
	Description string `json:"description,omitempty"`
	Unit        string `json:"unit,omitempty"`
	Quantity    string `json:"quantity"`
	UnitPrice   string `json:"unit_price"`
	Amount      string `json:"amount"`
}

// Waterfall is the totals block.
type Waterfall struct {
	ListSubtotal  string `json:"list_subtotal,omitempty"`
	DiscountTotal string `json:"discount_total,omitempty"`
	NetSubtotal   string `json:"net_subtotal"`
	TaxRate       string `json:"tax_rate,omitempty"`
	Tax           string `json:"tax,omitempty"`
	Total         string `json:"total"`
	Paid          string `json:"paid,omitempty"`
	Credited      string `json:"credited,omitempty"`
	Balance       string `json:"balance,omitempty"`
	Applied       string `json:"applied,omitempty"`
	Unapplied     string `json:"unapplied,omitempty"`
}

// Discount is one applied discount.
type Discount struct {
	Label  string `json:"label"`
	Amount string `json:"amount"`
}

// Tax is the tax context frozen on the document.
type Tax struct {
	Exempt       bool   `json:"exempt,omitempty"`
	ExemptReason string `json:"exempt_reason,omitempty"`
	DiscountRule string `json:"discount_rule,omitempty"`
}

// Payment is one received payment.
type Payment struct {
	PaidAt    string `json:"paid_at"`
	Method    string `json:"method,omitempty"`
	Reference string `json:"reference,omitempty"`
	Amount    string `json:"amount"`
}

// Terms are the commercial terms.
type Terms struct {
	PaymentTermsDays int    `json:"payment_terms_days,omitempty"`
	Text             string `json:"text,omitempty"`
}

// InvoiceRequest maps a statement onto the renderer's document.
//
// WHICH DOCUMENT it is follows the statement itself: one that has been issued
// carries an invoice number and is an INVOICE; a draft, or one the operator's
// external billing system numbers, has no number of ours to print and is a
// STATEMENT of the period. Printing "Tax Invoice" over a document with no
// invoice number would be a document nobody can account for.
//
// The seller block comes from the statement's own TAX SNAPSHOT when it has
// one — that is the whole point of the snapshot: an invoice the customer
// holds must keep saying what it said when it was issued, even after the
// operator edits the Sovereign's legal name. Only a draft, which has no
// snapshot, falls back to the live billing settings.
func InvoiceRequest(st store.Statement, c store.Customer, settings store.BillingSettings, locale string) Request {
	if locale == "" {
		locale = "en"
	}

	template, number := TemplateStatement, statementLabel(st)
	if st.InvoiceNumber != "" {
		template, number = TemplateInvoice, st.InvoiceNumber
	}

	seller := Party{
		Name:            settings.LegalName,
		Address:         settings.Address,
		TaxRegistration: settings.TaxRegistrationNumber,
	}
	buyer := Party{
		Name:            firstNonEmpty(st.CustomerName, c.Name, c.Slug),
		TaxRegistration: c.TaxRegistrationNumber,
		Email:           c.AdminEmail,
	}
	tax := Tax{DiscountRule: st.DiscountRule}
	if snap := st.TaxSnapshot; snap != nil {
		// Frozen at issue — authoritative over anything live.
		if snap.SellerLegalName != "" {
			seller.Name = snap.SellerLegalName
		}
		if snap.SellerAddress != "" {
			seller.Address = snap.SellerAddress
		}
		if snap.SellerTaxNumber != "" {
			seller.TaxRegistration = snap.SellerTaxNumber
		}
		if snap.CustomerName != "" {
			buyer.Name = snap.CustomerName
		}
		if snap.CustomerTaxNumber != "" {
			buyer.TaxRegistration = snap.CustomerTaxNumber
		}
		tax.Exempt, tax.ExemptReason = snap.Exempt, snap.ExemptReason
	}
	if seller.Name == "" {
		// A tax invoice with no seller would be refused by the renderer's
		// validation, which is the right place for it to be caught — but
		// naming the gap beats an empty box on a customer's document.
		seller.Name = "(seller legal name not set in billing settings)"
	}

	lines := make([]Line, 0, len(st.Lines))
	for _, l := range st.Lines {
		lines = append(lines, Line{
			SKU:       l.SKU,
			Unit:      l.Unit,
			Quantity:  dec(l.Quantity),
			UnitPrice: dec(l.UnitPrice),
			Amount:    dec(l.Amount),
		})
	}

	doc := Document{
		Number:      number,
		Status:      firstNonEmpty(st.EffectiveStatus, st.Status),
		IssuedAt:    issuedAt(st),
		PeriodStart: st.PeriodStart,
		PeriodEnd:   st.PeriodEnd,
		Seller:      seller,
		Buyer:       buyer,
		References: References{
			PO:            st.PORef,
			StatementID:   st.ID,
			InvoiceNumber: st.InvoiceNumber,
			External:      st.ExternalInvoiceRef,
		},
		Lines: lines,
		Waterfall: Waterfall{
			DiscountTotal: dec(st.DiscountTotal),
			NetSubtotal:   dec(st.Subtotal),
			TaxRate:       dec(st.TaxRate),
			Tax:           dec(st.Tax),
			Total:         dec(st.Total),
			Paid:          optional(st.Paid),
			Credited:      optional(st.Credited),
			Balance:       optional(st.Balance),
		},
		Discounts: appliedDiscounts(st.DiscountDetail),
		Tax:       tax,
		Terms:     Terms{},
	}
	// The renderer derives the list subtotal when it is absent; sending it
	// is redundant, so the one figure that could disagree with the ledger is
	// simply never sent.
	if st.DueAt != nil {
		doc.DueAt = st.DueAt.UTC().Format("2006-01-02")
	}
	if st.PaymentTermsDays != nil {
		doc.Terms.PaymentTermsDays = *st.PaymentTermsDays
	}
	for _, p := range st.Payments {
		if p.Status != "" && p.Status != "received" {
			// A pending or failed payment settles nothing and must not
			// appear on the document as money received.
			continue
		}
		if p.RefundedAt != nil {
			continue
		}
		doc.Payments = append(doc.Payments, Payment{
			PaidAt:    p.PaidAt.UTC().Format("2006-01-02"),
			Method:    p.Method,
			Reference: p.Reference,
			Amount:    dec(p.Amount),
		})
	}

	return Request{Template: template, Locale: locale, Currency: st.Currency, Document: doc}
}

// RenderInvoice maps the statement and posts it, returning the PDF and the
// filename to offer it under.
func (c *Client) RenderInvoice(ctx context.Context, st store.Statement, cust store.Customer, settings store.BillingSettings) ([]byte, string, error) {
	if !c.Enabled() {
		return nil, "", ErrNotConfigured
	}
	req := InvoiceRequest(st, cust, settings, "en")
	body, err := c.Render(ctx, req)
	if err != nil {
		return nil, "", err
	}
	return body, Filename(req), nil
}

// Filename is the download name for a mapped request.
func Filename(req Request) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '-'
	}, req.Document.Number)
	name = strings.Trim(name, "-.")
	if name == "" {
		name = req.Template
	}
	return name + ".pdf"
}

// Render posts a document and returns the PDF bytes.
func (c *Client) Render(ctx context.Context, doc Request) ([]byte, error) {
	if !c.Enabled() {
		return nil, ErrNotConfigured
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode document: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/render", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/pdf")
	if c.Token != "" {
		httpReq.Header.Set("X-Render-Token", c.Token)
	}

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("document renderer unreachable: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes))
	if err != nil {
		return nil, fmt.Errorf("read rendered document: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The renderer answers 400 with the exact problems it found; carrying
		// them into our log is the difference between fixing a mapping in
		// minutes and guessing at it.
		return nil, fmt.Errorf("document renderer returned %d: %s", resp.StatusCode, clip(string(body), 500))
	}
	if !bytes.HasPrefix(body, []byte("%PDF-")) {
		return nil, fmt.Errorf("document renderer returned %d bytes that are not a PDF", len(body))
	}
	return body, nil
}

// ── mapping helpers ────────────────────────────────────────────────────────

// dec passes a stored decimal through unchanged, defaulting a blank to "0".
// It NEVER parses: the digits the ledger settled are the digits the document
// carries, and the renderer is the only thing that formats them.
func dec(d store.Decimal) string {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return "0"
	}
	return s
}

// optional passes a decimal through but leaves a blank blank, so an absent
// figure stays absent on the document rather than printing as zero.
func optional(d store.Decimal) string {
	return strings.TrimSpace(string(d))
}

func issuedAt(st store.Statement) string {
	if st.IssuedAt != nil {
		return st.IssuedAt.UTC().Format("2006-01-02")
	}
	if !st.CreatedAt.IsZero() {
		return st.CreatedAt.UTC().Format("2006-01-02")
	}
	return st.PeriodEnd
}

// statementLabel names a statement that has no invoice number: its period.
func statementLabel(st store.Statement) string {
	if len(st.PeriodStart) >= 7 {
		return st.PeriodStart[:7]
	}
	if st.ID != "" {
		return st.ID
	}
	return "statement"
}

// appliedDiscount is the subset of the rating layer's frozen discount detail
// a document shows. It is decoded structurally rather than by importing the
// rating package, so the renderer's wire shape and the rating engine's
// internals stay independent of each other.
type appliedDiscount struct {
	Name         string        `json:"name"`
	Kind         string        `json:"kind"`
	Value        store.Decimal `json:"value"`
	SKU          string        `json:"sku"`
	Amount       store.Decimal `json:"amount"`
	SupersededBy string        `json:"superseded_by"`
}

func appliedDiscounts(raw json.RawMessage) []Discount {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var applied []appliedDiscount
	if err := json.Unmarshal(raw, &applied); err != nil {
		// Frozen detail we cannot read must not cost the customer their
		// invoice: the waterfall still carries the discount TOTAL, and the
		// per-discount breakdown is simply omitted.
		return nil
	}
	out := make([]Discount, 0, len(applied))
	for _, a := range applied {
		if a.SupersededBy != "" {
			// It lost to another discount under the combination rule and took
			// nothing off this bill (DESIGN.md §2.11).
			continue
		}
		amount := strings.TrimSpace(string(a.Amount))
		if amount == "" || amount == "0" {
			continue
		}
		out = append(out, Discount{Label: discountLabel(a), Amount: amount})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func discountLabel(a appliedDiscount) string {
	label := strings.TrimSpace(a.Name)
	if label == "" {
		label = "Discount"
	}
	if a.SKU != "" {
		label += " (" + a.SKU + ")"
	}
	return label
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
