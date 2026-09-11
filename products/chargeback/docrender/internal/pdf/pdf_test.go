package pdf

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/docrender"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/i18n"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/view"
)

var fixedNow = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

func catalog(t *testing.T, locale string) *i18n.Catalog {
	t.Helper()
	b, err := i18n.Load(docrender.Assets())
	if err != nil {
		t.Fatalf("load locales: %v", err)
	}
	c, ok := b.Catalog(locale)
	if !ok {
		t.Fatalf("locale %q not loaded", locale)
	}
	return c
}

func mustRender(t *testing.T, req document.Request) []byte {
	t.Helper()
	m, err := view.Build(req, catalog(t, req.Locale))
	if err != nil {
		t.Fatalf("build view: %v", err)
	}
	b, err := Render(m, Options{Now: fixedNow})
	if err != nil {
		t.Fatalf("render %s: %v", req.Template, err)
	}
	return b
}

func mustRenderFixture(t *testing.T, template string) []byte {
	t.Helper()
	req, err := fixture.Load(template)
	if err != nil {
		t.Fatalf("fixture %s: %v", template, err)
	}
	return mustRender(t, req)
}

// Every template renders a real, non-empty PDF whose TEXT LAYER carries the
// facts the reader came for: the document number, the total, the parties and
// the page numbering. A blank page or a rasterised one fails here.
func TestEveryTemplateRendersItsDocument(t *testing.T) {
	cases := map[string]struct{ mustContain []string }{
		"invoice": {[]string{
			"Tax Invoice", "INV-2026-00042",
			"Sovereign Cloud Operator LLC", "Acme Trading LLC",
			"PO-2026-118", "OM1100098765",
			"k8s.vcpu", "vcpu-hour", "2,976", "0.012",
			"List subtotal", "97.080", // 4 lines at list price
			"Discounts", "-9.708",
			"Net subtotal", "87.372",
			"Tax (5%)", "4.369", // 4.3686 rounds to the 3-decimal OMR unit
			"Total", "91.741",
			"Paid", "-50.000",
			"Balance due", "41.741",
			"Launch campaign (10%)",
			"TRF-88213",
			"Net 30 days",
			"Page 1 of",
		}},
		"credit-note": {[]string{
			"Credit Note", "CN-2026-00003", "INV-2026-00042",
			"Total credited", "52.500",
			"Applied to invoice", "41.741",
			"Credit on account", "10.759",
		}},
		"statement": {[]string{
			"Statement", "2026-08", "Beta Co",
			"eip.traffic_gb", "Total", "29.999",
			"OMT-4471902",
		}},
		"quote": {[]string{
			"Quotation", "Q-2026-0117", "PRO FORMA",
			"Future Health LLC", "support.gold",
			"Quoted total", "7,064.820",
			"Annual commitment (10%)",
			"Net 45 days",
		}},
	}
	for template, want := range cases {
		t.Run(template, func(t *testing.T) {
			out := mustRenderFixture(t, template)
			if !bytes.HasPrefix(out, []byte("%PDF-")) {
				t.Fatalf("output is not a PDF: %q", out[:min(16, len(out))])
			}
			if !bytes.Contains(out, []byte("%%EOF")) {
				t.Fatal("PDF is truncated: the trailer marker is absent")
			}
			if len(out) < 1200 {
				t.Fatalf("PDF is suspiciously small (%d bytes)", len(out))
			}
			text := extractText(t, out)
			for _, s := range want.mustContain {
				if !strings.Contains(text, s) {
					t.Errorf("text layer is missing %q\n--- extracted ---\n%s", s, text)
				}
			}
		})
	}
}

// Money is rendered at the CURRENCY's minor unit, not at a fixed two places:
// the same document in OMR shows three decimals and in USD two. The amounts
// arrive as decimal strings and are never parsed into a float on the way.
func TestMinorUnitFollowsTheCurrency(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	omr := extractText(t, mustRender(t, req))
	if !strings.Contains(omr, "91.741") {
		t.Errorf("OMR total not at 3 decimals\n%s", omr)
	}

	req.Currency = "USD"
	usd := extractText(t, mustRender(t, req))
	if !strings.Contains(usd, "91.74") || strings.Contains(usd, "91.741") {
		t.Errorf("USD total not at 2 decimals\n%s", usd)
	}
}

// A long document must paginate, repeat the table header, and number every
// page — the property a fixed-height layout silently loses.
func TestLongDocumentPaginatesAndRepeatsTheHeader(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Lines = nil
	for i := 0; i < 220; i++ {
		req.Document.Lines = append(req.Document.Lines, document.Line{
			SKU:         fmt.Sprintf("meter.%03d", i),
			Description: "Metered allocation for a resource whose description is long enough to wrap inside its column",
			Unit:        "unit-hour",
			Quantity:    "720.000000",
			UnitPrice:   "0.001500",
			Amount:      "1.080000",
		})
	}
	out := mustRender(t, req)
	text := extractText(t, out)

	if n := strings.Count(text, "Page 1 of"); n != 1 {
		t.Errorf("Page 1 appears %d times, want 1", n)
	}
	for _, page := range []string{"Page 2 of", "Page 3 of"} {
		if !strings.Contains(text, page) {
			t.Errorf("%s missing — the document did not paginate", page)
		}
	}
	if n := strings.Count(text, "UNIT PRICE"); n < 3 {
		t.Errorf("the table header repeated %d times across the pages, want one per page", n)
	}
	if !strings.Contains(text, "meter.000") || !strings.Contains(text, "meter.219") {
		t.Error("the first and last line must both survive pagination")
	}
	if !strings.Contains(text, "Balance due") {
		t.Error("the waterfall was lost after the table spilled onto later pages")
	}
}

// Two renders of one document, at one timestamp, must be byte-identical:
// a document that differs run to run cannot be cached, compared, hashed or
// signed.
//
// The comparison is repeated because the defect this guards is PROBABILISTIC.
// fpdf emitted its font objects in Go map order, so two renders agreed most
// of the time and disagreed about a third of the time; a two-render check
// reported green on five runs out of eight while the bug was live. Rendering
// the same document many times turns a coin-flip into a reliable gate.
func TestRenderIsDeterministic(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	want := mustRender(t, req)
	for i := 0; i < 12; i++ {
		got := mustRender(t, req)
		if bytes.Equal(want, got) {
			continue
		}
		n := 0
		for n < len(want) && n < len(got) && want[n] == got[n] {
			n++
		}
		lo := max(0, n-60)
		t.Fatalf("render %d differs at byte %d of %d/%d\nwant: %q\n got: %q",
			i, n, len(want), len(got),
			want[lo:min(len(want), n+60)], got[lo:min(len(got), n+60)])
	}
}

// A seller logo is drawn, and one that will not decode is dropped rather than
// taking the invoice down with it.
func TestSellerLogo(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Seller.LogoDataURI = onePixelPNG
	if err := req.Validate(); err != nil {
		t.Fatalf("a valid PNG data URI was refused: %v", err)
	}
	withLogo := mustRender(t, req)
	if !strings.Contains(extractText(t, withLogo), "INV-2026-00042") {
		t.Error("the document lost its text when a logo was added")
	}

	// A well-formed data URI whose payload is not an image at all.
	req.Document.Seller.LogoDataURI = "data:image/png;base64,bm90LWEtcG5n"
	out := mustRender(t, req)
	if !strings.Contains(extractText(t, out), "INV-2026-00042") {
		t.Error("an undecodable logo must be dropped, not fail the render")
	}
}

// Runes the core PDF font cannot carry are transliterated or replaced, never
// written raw — a stray high byte renders as mojibake on a customer document.
func TestNonLatinTextDoesNotCorruptTheDocument(t *testing.T) {
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Buyer.Name = "Acme Trading — شركة"
	req.Document.Notes = "Grüße · 1 – 2"
	out := mustRender(t, req)
	text := extractText(t, out)
	if !strings.Contains(text, "Grüße") {
		t.Errorf("Latin-1 text was mangled\n%s", text)
	}
	if !strings.Contains(text, "1 – 2") {
		t.Errorf("the en dash was lost\n%s", text)
	}
	if !strings.Contains(text, "Acme Trading") {
		t.Error("the Latin part of a mixed-script name must survive")
	}
}

// A one-pixel transparent PNG, base64.
const onePixelPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

// TestWritePreviews is the developer affordance behind `make preview`: with
// DOCRENDER_PREVIEW_DIR set it writes every fixture's PDF there so the layout
// can be LOOKED AT. Testids and byte counts do not prove a document reads
// well; a person opening the file does.
func TestWritePreviews(t *testing.T) {
	dir := os.Getenv("DOCRENDER_PREVIEW_DIR")
	if dir == "" {
		t.Skip("DOCRENDER_PREVIEW_DIR not set")
	}
	// The BSS contract fixture is previewed alongside the four templates: it
	// is the document a customer actually downloads.
	for _, k := range append(append([]string{}, document.Templates...), "bss-invoice") {
		path := filepath.Join(dir, k+".pdf")
		if err := os.WriteFile(path, mustRenderFixture(t, k), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
	}
	// The multi-page case too: pagination is where a layout most often
	// looks wrong while every assertion still passes.
	long, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	long.Document.Lines = nil
	for i := 0; i < 60; i++ {
		long.Document.Lines = append(long.Document.Lines, document.Line{
			SKU:         fmt.Sprintf("meter.%03d", i),
			Description: "Metered allocation for a resource whose description is long enough to wrap inside its column",
			Unit:        "unit-hour", Quantity: "720.000000", UnitPrice: "0.001500", Amount: "1.080000",
		})
	}
	if err := os.WriteFile(filepath.Join(dir, "invoice-long.pdf"), mustRender(t, long), 0o644); err != nil {
		t.Fatal(err)
	}
}
