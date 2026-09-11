package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/qr"
)

// The tax summary block and the tax authority's QR on a rendered invoice
// (DESIGN.md §17).
//
// A summary the customer cannot read is not a summary, and a QR that draws
// nothing is not a QR — so both are asserted on the OUTPUT: the summary as
// text pulled back out of the content stream, the QR as the marks actually
// painted onto the page.

// fillOps counts the rectangle-fill operators in the page content. The QR is
// drawn as one filled rectangle per dark module, so this is a direct measure
// of "were the modules painted", which no amount of text extraction can see.
var reFill = regexp.MustCompile(`re\s+f`)

func fillOps(t *testing.T, pdfBytes []byte) int {
	t.Helper()
	n := 0
	for _, m := range streamRe.FindAllSubmatch(pdfBytes, -1) {
		body := m[1]
		if r, err := zlib.NewReader(bytes.NewReader(body)); err == nil {
			if inflated, err := io.ReadAll(r); err == nil {
				body = inflated
			}
			_ = r.Close()
		}
		n += len(reFill.FindAll(body, -1))
	}
	return n
}

// twoRateInvoice is the sample invoice re-cut so its waterfall and its tax
// summary both say the same thing about two different rates.
func twoRateInvoice() document.Request {
	req := document.Sample()
	d := &req.Document
	d.Lines = []document.Line{
		{SKU: "ecs.c7.large", Description: "Compute", Unit: "hour", Quantity: "1.000000", UnitPrice: "400.000000", Amount: "400.000000"},
		{SKU: "evs.ssd", Description: "Block storage", Unit: "gb-hour", Quantity: "1.000000", UnitPrice: "120.000000", Amount: "120.000000"},
	}
	d.Waterfall = document.Waterfall{
		ListSubtotal: "520.000000", DiscountTotal: "52.000000", NetSubtotal: "468.000000",
		TaxRate: "0.0385", Tax: "18.000000", Total: "486.000000",
	}
	d.Discounts = []document.Discount{{Label: "Launch campaign (10%)", Amount: "52.000000"}}
	d.Payments = nil
	d.Tax = document.Tax{
		Summary: []document.TaxRate{
			{Label: "Oman VAT standard", Kind: "standard", Rate: "0.05", Base: "360.000000", Amount: "18.000000"},
			{Label: "Zero-rated storage", Kind: "zero_rated", Rate: "0", Base: "108.000000", Amount: "0.000000",
				Note: "Zero-rated supply under the Executive Regulation."},
		},
		QRPayload: document.SampleTax().QRPayload,
	}
	return req
}

// An invoice with TWO rates prints both of them, with what each was charged
// on — the single "Tax (…)" line of the waterfall cannot say it.
func TestInvoicePrintsTheTaxSummaryByRate(t *testing.T) {
	req := twoRateInvoice()
	if err := req.Validate(); err != nil {
		t.Fatalf("the two-rate document does not satisfy the wire contract: %v", err)
	}
	text := extractText(t, mustRender(t, req))
	for _, want := range []string{
		"TAX SUMMARY",
		"Rate", "Taxable amount",
		// The standard row: 5 %, on 360.000, producing 18.000.
		"5%", "360.000", "18.000",
		// The zero-rated row, named as such, on 108.000 producing nothing.
		"0%", "zero-rated", "108.000", "0.000",
		// And the note that makes a zero tax line lawful.
		"Zero-rated supply under the Executive Regulation.",
		"Oman VAT standard", "Zero-rated storage",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendered invoice does not show %q\n--- extracted ---\n%s", want, text)
		}
	}
}

// The QR is DRAWN, not merely carried: the page gains hundreds of filled
// module rectangles that a document without a payload does not have, and the
// caption tells the reader what it is for.
func TestInvoiceDrawsTheTaxAuthorityQR(t *testing.T) {
	with := twoRateInvoice()
	without := twoRateInvoice()
	without.Document.Tax.QRPayload = ""

	withPDF, withoutPDF := mustRender(t, with), mustRender(t, without)
	drawn := fillOps(t, withPDF) - fillOps(t, withoutPDF)

	// The payload encodes to a symbol; the number of DARK modules is what
	// must appear as fills, so the assertion is against the encoder's own
	// count rather than an arbitrary threshold.
	code, err := qr.Encode([]byte(with.Document.Tax.QRPayload))
	if err != nil {
		t.Fatal(err)
	}
	dark := 0
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if code.Dark(x, y) {
				dark++
			}
		}
	}
	if dark < 100 {
		t.Fatalf("the encoded symbol has only %d dark modules — the payload is not what this test thinks", dark)
	}
	if drawn != dark {
		t.Fatalf("the page gained %d filled rectangles for a symbol with %d dark modules", drawn, dark)
	}

	text := extractText(t, withPDF)
	for _, want := range []string{"TAX AUTHORITY QR", "Scan to verify this invoice."} {
		if !strings.Contains(text, want) {
			t.Errorf("the QR block has no caption %q", want)
		}
	}
	// A document with NO payload draws no QR and prints no caption, so the
	// assertion above cannot pass on a renderer that always draws one.
	if strings.Contains(extractText(t, withoutPDF), "TAX AUTHORITY QR") {
		t.Error("a document with no QR payload printed a QR caption")
	}
}

// THE CONTRACT WITH THE BSS. The committed fixture the BSS's own mapper
// writes carries a tax summary and a QR payload, and both render — which is
// what makes "the rendered PDF carries the QR and the tax summary" a fact
// about the two modules rather than about this test's own fixture.
func TestTheBSSFixtureCarriesAndRendersTheTaxSummaryAndQR(t *testing.T) {
	req, err := fixture.Load("bss-invoice")
	if err != nil {
		t.Fatalf("the BSS contract fixture does not satisfy this renderer's own validation: %v", err)
	}
	if len(req.Document.Tax.Summary) == 0 {
		t.Fatal("the document the BSS sends carries no tax summary")
	}
	if req.Document.Tax.QRPayload == "" {
		t.Fatal("the document the BSS sends carries no QR payload")
	}
	// The payload the BSS computed DECODES — a QR nobody has read is a
	// picture, not a compliance artefact.
	raw, err := base64.StdEncoding.DecodeString(req.Document.Tax.QRPayload)
	if err != nil {
		t.Fatalf("the BSS's QR payload is not base64: %v", err)
	}
	fields := 0
	for i := 0; i < len(raw); {
		if i+2 > len(raw) {
			t.Fatalf("the BSS's QR payload is a truncated TLV stream at byte %d", i)
		}
		length := int(raw[i+1])
		if i+2+length > len(raw) {
			t.Fatalf("tag %d claims %d bytes but only %d remain", raw[i], length, len(raw)-i-2)
		}
		fields++
		i += 2 + length
	}
	if fields != 5 {
		t.Fatalf("the BSS's QR payload carries %d TLV fields, want 5", fields)
	}

	out := mustRender(t, req)
	text := extractText(t, out)
	for _, want := range []string{"TAX SUMMARY", "Oman VAT standard", "TAX AUTHORITY QR"} {
		if !strings.Contains(text, want) {
			t.Errorf("the BSS's invoice rendered without %q\n--- extracted ---\n%s", want, text)
		}
	}
	if fillOps(t, out) < 100 {
		t.Error("the BSS's invoice rendered with no QR modules painted")
	}
}

// The wire contract refuses a QR payload that is not base64 and a tax
// summary row whose figures are not money — a document that would render as
// a blank square or a blank cell is caught at the door with a message.
func TestValidationRefusesABadQRPayloadAndABadSummaryRow(t *testing.T) {
	req := twoRateInvoice()
	req.Document.Tax.QRPayload = "not base64!!"
	err := req.Validate()
	if err == nil || !strings.Contains(err.Error(), "qr_payload") {
		t.Fatalf("a non-base64 QR payload validated: %v", err)
	}
	req = twoRateInvoice()
	req.Document.Tax.Summary[0].Base = "three hundred"
	err = req.Validate()
	if err == nil || !strings.Contains(err.Error(), "tax.summary[0].base") {
		t.Fatalf("a non-numeric taxable amount validated: %v", err)
	}
}
