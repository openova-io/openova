package view

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/docrender"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/i18n"
)

func catalog(t *testing.T) *i18n.Catalog {
	t.Helper()
	b, err := i18n.Load(docrender.Assets())
	if err != nil {
		t.Fatal(err)
	}
	c, ok := b.Catalog("en")
	if !ok {
		t.Fatal("en not loaded")
	}
	return c
}

func build(t *testing.T, req document.Request) *Model {
	t.Helper()
	m, err := Build(req, catalog(t))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return m
}

func totalsMap(m *Model) map[string]string {
	out := map[string]string{}
	for _, r := range m.Totals {
		out[r.Key] = r.Value
	}
	return out
}

// The waterfall reads top to bottom: list → discounts → net → tax → total →
// settlement → balance. Getting the ORDER wrong makes a correct set of
// numbers into a wrong document.
func TestInvoiceWaterfallOrderAndSigns(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	m := build(t, req)

	var keys []string
	for _, r := range m.Totals {
		keys = append(keys, r.Key)
	}
	want := []string{
		"waterfall.list_subtotal", "waterfall.discounts", "waterfall.net_subtotal",
		"waterfall.tax", "waterfall.total", "waterfall.paid", "waterfall.balance_due",
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("waterfall order:\n got %v\nwant %v", keys, want)
	}

	got := totalsMap(m)
	// What reduces the total is shown negative; what IS the total is not.
	if !strings.HasPrefix(got["waterfall.discounts"], "-") {
		t.Errorf("discounts not negative: %q", got["waterfall.discounts"])
	}
	if !strings.HasPrefix(got["waterfall.paid"], "-") {
		t.Errorf("paid not negative: %q", got["waterfall.paid"])
	}
	if strings.HasPrefix(got["waterfall.total"], "-") || strings.HasPrefix(got["waterfall.balance_due"], "-") {
		t.Error("a total must not be rendered negative")
	}
	// Every amount at the OMR minor unit.
	for k, v := range got {
		if dot := strings.LastIndex(v, "."); dot < 0 || len(v)-dot-1 != 3 {
			t.Errorf("%s = %q is not at the OMR minor unit (3 decimals)", k, v)
		}
	}
}

// The list subtotal is DERIVED when the caller does not send it, from the
// net plus the discounts — exact decimal arithmetic, never float.
func TestListSubtotalIsDerivedWhenAbsent(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Waterfall.ListSubtotal = ""
	m := build(t, req)
	if got := totalsMap(m)["waterfall.list_subtotal"]; got != "97.080" {
		t.Fatalf("derived list subtotal = %q, want 97.080", got)
	}
}

// Each document kind names its own total and its own party heading.
func TestPerTemplateHeadingsAndTotals(t *testing.T) {
	cases := map[string]struct{ title, buyerHeading, totalKey string }{
		"invoice":     {"invoice.title", "party.bill_to", "waterfall.total"},
		"credit-note": {"credit_note.title", "party.credit_to", "waterfall.total_credited"},
		"statement":   {"statement.title", "party.customer", "waterfall.total"},
		"quote":       {"quote.title", "party.prepared_for", "waterfall.quote_total"},
	}
	for kind, want := range cases {
		req, err := fixture.Load(kind)
		if err != nil {
			t.Fatal(err)
		}
		m := build(t, req)
		if m.TitleKey != want.title || m.BuyerHeadingKey != want.buyerHeading {
			t.Errorf("%s: %q / %q", kind, m.TitleKey, m.BuyerHeadingKey)
		}
		if _, ok := totalsMap(m)[want.totalKey]; !ok {
			t.Errorf("%s: no %s row", kind, want.totalKey)
		}
	}
}

// An ordinary issued or sent invoice carries no stamp; a draft, paid,
// overdue, cancelled or pro-forma document does.
func TestStatusStamp(t *testing.T) {
	for status, want := range map[string]string{
		"": "", "issued": "", "sent": "",
		"draft": "status.draft", "paid": "status.paid", "overdue": "status.overdue",
		"cancelled": "status.cancelled", "pro_forma": "status.pro_forma",
		"something-else": "",
	} {
		req, err := fixture.Load("invoice")
		if err != nil {
			t.Fatal(err)
		}
		req.Document.Status = status
		m := build(t, req)
		if m.StatusKey != want {
			t.Errorf("status %q -> %q, want %q", status, m.StatusKey, want)
		}
	}
}

// Tax exemption replaces the rate row and states the reason; it never shows
// a rate the customer is not being charged.
func TestTaxExemption(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Tax.Exempt = true
	req.Document.Tax.ExemptReason = "Export of services"
	req.Document.Waterfall.Tax = "0.000000"
	m := build(t, req)

	got := totalsMap(m)
	if _, ok := got["waterfall.tax"]; ok {
		t.Error("an exempt document still showed a tax rate row")
	}
	if _, ok := got["waterfall.tax_exempt"]; !ok {
		t.Error("no exemption row")
	}
	found := false
	for _, n := range m.Notes {
		if n.Key == "tax.exempt_reason" && n.Vars["reason"] == "Export of services" {
			found = true
		}
	}
	if !found {
		t.Error("the exemption reason is not stated on the document")
	}
}

// Every label key a rendered document asks for must exist in en.json. A
// missing key would print the key itself onto a customer's invoice.
func TestNoTemplateAsksForALabelEnglishDoesNotHave(t *testing.T) {
	cat := catalog(t)
	all, err := fixture.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range all {
		m, err := Build(req, cat)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range append(append([]Row{}, m.Meta...), append(m.Totals, append(m.Notes, m.Terms...)...)...) {
			m.Label(r)
		}
		m.T(m.TitleKey)
		m.T(m.SellerHeadingKey)
		m.T(m.BuyerHeadingKey)
		if m.StatusKey != "" {
			m.T(m.StatusKey)
		}
		for _, k := range []string{
			"table.sku", "table.description", "table.unit", "table.quantity",
			"table.unit_price", "table.amount", "discounts.title", "payments.title",
			"payments.date", "payments.method", "payments.reference", "payments.amount",
			"terms.title", "notes.title", "footer.generated", "footer.page",
			"field.tax_registration",
		} {
			m.T(k)
		}
	}
	if miss := cat.Missing(); len(miss) > 0 {
		t.Fatalf("locales/en.json is missing keys the documents ask for: %v", miss)
	}
}

func TestFilenameIsSafe(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Number = "../../etc/passwd"
	m := build(t, req)
	name := m.Filename("pdf")
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") {
		t.Fatalf("filename %q escapes its directory", name)
	}
}
