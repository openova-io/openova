package docs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func ptrTime(s string) *time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return &t
}
func ptrInt(n int) *int { return &n }

// issuedStatement is the shape the invoicing lane produces: rated lines, a
// frozen waterfall, an invoice number, a tax snapshot and a part payment.
func issuedStatement() store.Statement {
	return store.Statement{
		ID:               "3f9c1b2e-7a4d-4c1e-9b0a-1d2e3f4a5b6c",
		CustomerID:       "c1",
		CustomerName:     "Acme Trading LLC",
		PeriodStart:      "2026-08-01",
		PeriodEnd:        "2026-08-31",
		Currency:         "OMR",
		Subtotal:         "87.372000",
		DiscountTotal:    "9.708000",
		DiscountRule:     "best-single",
		TaxRate:          "0.05",
		Tax:              "4.368600",
		Total:            "91.740600",
		Status:           store.StatusSent,
		EffectiveStatus:  store.StatusOverdue,
		InvoiceNumber:    "INV-2026-00042",
		PORef:            "PO-2026-118",
		PaymentTermsDays: ptrInt(30),
		IssuedAt:         ptrTime("2026-09-01"),
		DueAt:            ptrTime("2026-10-01"),
		Paid:             "50.000000",
		Balance:          "41.740600",
		Lines: []store.RatedLine{
			{SKU: "k8s.vcpu", Unit: "vcpu-hour", Quantity: "2976.000000", UnitPrice: "0.012000", Amount: "35.712000"},
			{SKU: "plan.m", Unit: "month", Quantity: "1.000000", UnitPrice: "45.000000", Amount: "45.000000"},
		},
		Payments: []store.StatementPayment{
			{PaidAt: *ptrTime("2026-09-05"), Method: "transfer", Reference: "TRF-88213", Amount: "50.000000", Status: "received"},
		},
		TaxSnapshot: &store.TaxSnapshot{
			Rate:              "0.05",
			CustomerName:      "Acme Trading LLC",
			CustomerTaxNumber: "OM1100098765",
			SellerLegalName:   "Sovereign Cloud Operator LLC",
			SellerTaxNumber:   "OM1100012345",
			SellerAddress:     "Knowledge Oasis Muscat\nMuscat 130, Oman",
		},
		DiscountDetail: json.RawMessage(`[{"discount_id":"d1","name":"Launch campaign","kind":"percent","value":"10","amount":"9.708000"}]`),
	}
}

func customer() store.Customer {
	return store.Customer{ID: "c1", Slug: "acme", Name: "Acme Trading LLC", AdminEmail: "finance@acme.omani.homes"}
}

func settings() store.BillingSettings {
	return store.BillingSettings{
		LegalName:             "Live Legal Name That Must Not Win",
		Address:               "Live address",
		TaxRegistrationNumber: "OM-LIVE",
		InvoicePrefix:         "INV",
	}
}

// The mapping is where a document goes wrong silently, so it is asserted
// field by field against the statement it came from.
func TestInvoiceRequestMapsTheStatement(t *testing.T) {
	req := InvoiceRequest(issuedStatement(), customer(), settings(), "en")

	if req.Template != TemplateInvoice {
		t.Errorf("template = %q, want invoice", req.Template)
	}
	if req.Currency != "OMR" || req.Locale != "en" {
		t.Errorf("currency/locale = %q/%q", req.Currency, req.Locale)
	}
	d := req.Document
	if d.Number != "INV-2026-00042" {
		t.Errorf("number = %q", d.Number)
	}
	// The OVERDUE status, not the stored `sent`: the document says what is
	// true of the clock.
	if d.Status != store.StatusOverdue {
		t.Errorf("status = %q, want overdue", d.Status)
	}
	if d.IssuedAt != "2026-09-01" || d.DueAt != "2026-10-01" {
		t.Errorf("dates = %q / %q", d.IssuedAt, d.DueAt)
	}
	if d.PeriodStart != "2026-08-01" || d.PeriodEnd != "2026-08-31" {
		t.Errorf("period = %q .. %q", d.PeriodStart, d.PeriodEnd)
	}
	if d.References.PO != "PO-2026-118" || d.References.StatementID == "" || d.References.InvoiceNumber != "INV-2026-00042" {
		t.Errorf("references = %+v", d.References)
	}
	if d.Terms.PaymentTermsDays != 30 {
		t.Errorf("terms = %+v", d.Terms)
	}

	// Amounts pass through as the EXACT decimal strings the ledger holds.
	w := d.Waterfall
	for field, got := range map[string]string{
		"net_subtotal": w.NetSubtotal, "discount_total": w.DiscountTotal,
		"tax_rate": w.TaxRate, "tax": w.Tax, "total": w.Total,
		"paid": w.Paid, "balance": w.Balance,
	} {
		want := map[string]string{
			"net_subtotal": "87.372000", "discount_total": "9.708000",
			"tax_rate": "0.05", "tax": "4.368600", "total": "91.740600",
			"paid": "50.000000", "balance": "41.740600",
		}[field]
		if got != want {
			t.Errorf("waterfall.%s = %q, want %q (the ledger's own digits, unrounded)", field, got, want)
		}
	}
	// The list subtotal is deliberately NOT sent: the renderer derives it, so
	// there is no second copy of a figure that could disagree with the ledger.
	if w.ListSubtotal != "" {
		t.Errorf("list_subtotal = %q — it must be left for the renderer to derive", w.ListSubtotal)
	}

	if len(d.Lines) != 2 || d.Lines[0].SKU != "k8s.vcpu" || d.Lines[0].UnitPrice != "0.012000" {
		t.Errorf("lines = %+v", d.Lines)
	}
	if len(d.Discounts) != 1 || d.Discounts[0].Label != "Launch campaign" || d.Discounts[0].Amount != "9.708000" {
		t.Errorf("discounts = %+v", d.Discounts)
	}
	if d.Tax.DiscountRule != "best-single" {
		t.Errorf("discount rule = %q", d.Tax.DiscountRule)
	}
	if len(d.Payments) != 1 || d.Payments[0].Reference != "TRF-88213" || d.Payments[0].PaidAt != "2026-09-05" {
		t.Errorf("payments = %+v", d.Payments)
	}
}

// The seller block is the one the invoice was ISSUED with. An operator who
// renames the Sovereign must not silently rewrite the document a customer is
// already holding — that is what the tax snapshot is for.
func TestTaxSnapshotBeatsLiveSettings(t *testing.T) {
	req := InvoiceRequest(issuedStatement(), customer(), settings(), "en")
	if req.Document.Seller.Name != "Sovereign Cloud Operator LLC" {
		t.Errorf("seller = %q — the live billing settings overwrote the snapshot", req.Document.Seller.Name)
	}
	if req.Document.Seller.TaxRegistration != "OM1100012345" {
		t.Errorf("seller tax registration = %q", req.Document.Seller.TaxRegistration)
	}
	if req.Document.Buyer.TaxRegistration != "OM1100098765" {
		t.Errorf("buyer tax registration = %q", req.Document.Buyer.TaxRegistration)
	}

	// A DRAFT has no snapshot and falls back to the live settings, which is
	// correct: it has not been issued to anyone yet.
	draft := issuedStatement()
	draft.TaxSnapshot = nil
	draft.InvoiceNumber = ""
	draft.Status, draft.EffectiveStatus = store.StatusDraft, store.StatusDraft
	req = InvoiceRequest(draft, customer(), settings(), "en")
	if req.Document.Seller.Name != "Live Legal Name That Must Not Win" {
		t.Errorf("draft seller = %q — a draft should use the live settings", req.Document.Seller.Name)
	}
	// ... and it is a STATEMENT, not an invoice: there is no invoice number
	// to print, and calling it a Tax Invoice would be a document nobody can
	// account for.
	if req.Template != TemplateStatement {
		t.Errorf("an unnumbered draft mapped to %q, want statement", req.Template)
	}
	if req.Document.Number != "2026-08" {
		t.Errorf("statement number = %q, want the period", req.Document.Number)
	}
	// Tax exemption rides on the snapshot.
	exempt := issuedStatement()
	exempt.TaxSnapshot.Exempt = true
	exempt.TaxSnapshot.ExemptReason = "Export of services"
	req = InvoiceRequest(exempt, customer(), settings(), "en")
	if !req.Document.Tax.Exempt || req.Document.Tax.ExemptReason != "Export of services" {
		t.Errorf("tax = %+v", req.Document.Tax)
	}
}

// Money that did not arrive must never appear on a document as money
// received: a pending, failed or refunded payment settles nothing.
func TestOnlyReceivedPaymentsReachTheDocument(t *testing.T) {
	st := issuedStatement()
	refunded := *ptrTime("2026-09-09")
	st.Payments = []store.StatementPayment{
		{PaidAt: *ptrTime("2026-09-05"), Reference: "GOOD", Amount: "50.000000", Status: "received"},
		{PaidAt: *ptrTime("2026-09-06"), Reference: "PENDING", Amount: "10.000000", Status: "pending"},
		{PaidAt: *ptrTime("2026-09-07"), Reference: "FAILED", Amount: "10.000000", Status: "failed"},
		{PaidAt: *ptrTime("2026-09-08"), Reference: "REFUNDED", Amount: "10.000000", Status: "received", RefundedAt: &refunded},
	}
	req := InvoiceRequest(st, customer(), settings(), "en")
	if len(req.Document.Payments) != 1 || req.Document.Payments[0].Reference != "GOOD" {
		t.Fatalf("payments on the document = %+v", req.Document.Payments)
	}
}

// A discount that LOST under the combination rule took nothing off the bill
// and must not be listed as if it had (DESIGN.md §2.11).
func TestSupersededDiscountsAreNotListed(t *testing.T) {
	st := issuedStatement()
	st.DiscountDetail = json.RawMessage(`[
	  {"name":"Launch campaign","amount":"9.708000"},
	  {"name":"Loyalty","amount":"0","superseded_by":"d1"},
	  {"name":"Zero","amount":"0"}]`)
	req := InvoiceRequest(st, customer(), settings(), "en")
	if len(req.Document.Discounts) != 1 || req.Document.Discounts[0].Label != "Launch campaign" {
		t.Fatalf("discounts = %+v", req.Document.Discounts)
	}

	// Detail that cannot be decoded costs the breakdown, never the invoice.
	st.DiscountDetail = json.RawMessage(`{"not":"an array"}`)
	req = InvoiceRequest(st, customer(), settings(), "en")
	if req.Document.Discounts != nil {
		t.Errorf("discounts = %+v, want none", req.Document.Discounts)
	}
	if req.Document.Waterfall.DiscountTotal != "9.708000" {
		t.Error("the discount TOTAL must survive undecodable detail")
	}
}

// ── the client, against a fake renderer ───────────────────────────────────

type fakeRenderer struct {
	*httptest.Server
	gotBody  []byte
	gotToken string
	gotPath  string
	status   int
	respond  []byte
}

func newFakeRenderer(t *testing.T) *fakeRenderer {
	t.Helper()
	f := &fakeRenderer{status: 200, respond: []byte("%PDF-1.3\nfake document\n%%EOF")}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.gotPath = r.URL.Path
		f.gotToken = r.Header.Get("X-Render-Token")
		f.gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/pdf")
		w.WriteHeader(f.status)
		w.Write(f.respond)
	}))
	t.Cleanup(f.Close)
	return f
}

func TestRenderInvoicePostsTheDocumentAndReturnsThePDF(t *testing.T) {
	f := newFakeRenderer(t)
	c := New(f.URL, "s3cret")

	body, filename, err := c.RenderInvoice(context.Background(), issuedStatement(), customer(), settings())
	if err != nil {
		t.Fatalf("RenderInvoice: %v", err)
	}
	if !strings.HasPrefix(string(body), "%PDF-") {
		t.Fatalf("body is not a PDF: %.20q", body)
	}
	if filename != "INV-2026-00042.pdf" {
		t.Errorf("filename = %q", filename)
	}
	if f.gotPath != "/v1/render" {
		t.Errorf("posted to %q", f.gotPath)
	}
	if f.gotToken != "s3cret" {
		t.Errorf("X-Render-Token = %q", f.gotToken)
	}

	// What went over the wire is the document, not a summary of it.
	var sent Request
	if err := json.Unmarshal(f.gotBody, &sent); err != nil {
		t.Fatalf("the posted body is not the renderer's contract: %v", err)
	}
	if sent.Document.Number != "INV-2026-00042" || sent.Document.Waterfall.Total != "91.740600" {
		t.Errorf("posted document = %+v", sent.Document)
	}
	if len(sent.Document.Lines) != 2 {
		t.Errorf("posted %d lines", len(sent.Document.Lines))
	}
}

func TestUnconfiguredClient(t *testing.T) {
	c := New("", "")
	if c.Enabled() {
		t.Fatal("a client with no URL reports itself enabled")
	}
	if _, _, err := c.RenderInvoice(context.Background(), issuedStatement(), customer(), settings()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	// ... and it never dials anything: there is nothing to dial.
	if _, err := c.Render(context.Background(), Request{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Render err = %v", err)
	}
}

// A renderer that refuses the document must surface WHAT it refused, or an
// operator is left guessing at a mapping bug.
func TestRendererErrorsAreCarried(t *testing.T) {
	f := newFakeRenderer(t)
	f.status, f.respond = 400, []byte(`{"error":"the document is not renderable","problems":["document.seller.name: required"]}`)
	c := New(f.URL, "")

	_, _, err := c.RenderInvoice(context.Background(), issuedStatement(), customer(), settings())
	if err == nil {
		t.Fatal("a 400 was treated as success")
	}
	if !strings.Contains(err.Error(), "document.seller.name") {
		t.Fatalf("the renderer's own problem list was dropped: %v", err)
	}

	// A 200 carrying something that is not a PDF is also a failure: handing
	// an HTML error page to a customer as an invoice is worse than an error.
	f.status, f.respond = 200, []byte("<html>proxy error</html>")
	if _, _, err := c.RenderInvoice(context.Background(), issuedStatement(), customer(), settings()); err == nil {
		t.Fatal("a non-PDF 200 was accepted as a document")
	}

	// An unreachable renderer is an error, never an empty document.
	dead := New("http://127.0.0.1:1/nope", "")
	if _, _, err := dead.RenderInvoice(context.Background(), issuedStatement(), customer(), settings()); err == nil {
		t.Fatal("an unreachable renderer was treated as success")
	}
}

func TestFilenameIsSafe(t *testing.T) {
	st := issuedStatement()
	st.InvoiceNumber = "../../etc/passwd"
	name := Filename(InvoiceRequest(st, customer(), settings(), "en"))
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		t.Fatalf("filename %q escapes its directory", name)
	}
}

// The filename goes into a Content-Disposition header, so an invoice number
// carrying a quote, a newline or a carriage return must not be able to end
// the header value and start another one. Filename keeps letters, digits,
// dash, underscore and dot and maps everything else to a dash, which is what
// makes the header safe by construction rather than by escaping at each
// call site.
func TestFilenameCannotInjectAHeader(t *testing.T) {
	for _, number := range []string{
		"INV-1\r\nSet-Cookie: a=b",
		"INV-1\nX-Evil: 1",
		`INV-1"; filename="evil.exe`,
		"INV\t1 2",
	} {
		st := issuedStatement()
		st.InvoiceNumber = number
		name := Filename(InvoiceRequest(st, customer(), settings(), "en"))
		if strings.ContainsAny(name, "\r\n\"\\;") || strings.Contains(name, " ") {
			t.Fatalf("invoice number %q yielded filename %q, which can break out of the header", number, name)
		}
		if !strings.HasSuffix(name, ".pdf") {
			t.Fatalf("filename %q is not a document name", name)
		}
	}
}
