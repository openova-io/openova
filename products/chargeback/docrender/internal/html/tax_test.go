package html

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/view"
)

// The HTML rendition is the SAME document the PDF draws (DESIGN.md §17): the
// tax summary by rate, the note a zero-rated row carries, and the QR as an
// inline SVG — one <rect> per dark module, so the symbol is really there and
// not an empty box.
func TestInvoiceHTMLCarriesTheTaxSummaryAndTheQR(t *testing.T) {
	set, cat := load(t)
	req, err := fixture.Load("bss-invoice")
	if err != nil {
		t.Fatal(err)
	}
	m, err := view.Build(req, cat)
	if err != nil {
		t.Fatal(err)
	}
	if m.QR == nil {
		t.Fatal("the model carries no encoded QR")
	}
	out, err := set.Render(m)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"Tax summary", "Taxable amount", "Oman VAT standard", "5%",
		"Tax authority QR", "Scan to verify this invoice.", "<svg",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the HTML invoice does not carry %q", want)
		}
	}
	dark := 0
	for y := 0; y < m.QR.Size; y++ {
		for x := 0; x < m.QR.Size; x++ {
			if m.QR.Dark(x, y) {
				dark++
			}
		}
	}
	if dark < 100 {
		t.Fatalf("the encoded symbol has only %d dark modules", dark)
	}
	// One <rect> per dark module, plus the one white background rect.
	if n := strings.Count(got, "<rect"); n != dark+1 {
		t.Fatalf("the SVG has %d rects for %d dark modules", n, dark)
	}

	// A document with NO payload draws no SVG at all, so the assertions
	// above cannot pass on a template that always emits one.
	req.Document.Tax.QRPayload = ""
	plain, err := view.Build(req, cat)
	if err != nil {
		t.Fatal(err)
	}
	if plain.QR != nil {
		t.Fatal("a document with no payload produced a symbol")
	}
	bare, err := set.Render(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bare), "Tax authority QR") {
		t.Error("a document with no QR payload rendered a QR block")
	}
}
