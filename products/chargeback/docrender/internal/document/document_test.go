package document

import (
	"errors"
	"strings"
	"testing"
)

func valid() Request { return Sample() }

func TestSampleIsValid(t *testing.T) {
	r := valid()
	if err := r.Validate(); err != nil {
		t.Fatalf("the sample the readiness probe renders is not valid: %v", err)
	}
}

// Validation reports EVERY problem at once, by JSON path, so a caller fixes
// the document in one round instead of discovering faults one at a time.
func TestValidateReportsEveryProblem(t *testing.T) {
	r := valid()
	r.Template = "receipt"
	r.Currency = "Omani Rial"
	r.Document.Number = ""
	r.Document.IssuedAt = "01/09/2026"
	r.Document.Seller.Name = "  "
	r.Document.Lines[0].Amount = "35,712.00"
	r.Document.Waterfall.Total = "ninety"

	err := r.Validate()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want a ValidationError, got %v", err)
	}
	for _, want := range []string{
		"template:", "currency:", "document.number:", "document.issued_at:",
		"document.seller.name:", "document.lines[0].amount:", "document.waterfall.total:",
	} {
		if !strings.Contains(strings.Join(ve.Problems, "\n"), want) {
			t.Errorf("no problem reported for %s\n%v", want, ve.Problems)
		}
	}
}

// Money arrives as a decimal string. A value that has been through a float,
// a spreadsheet or a locale formatter is REFUSED rather than silently
// re-interpreted — a wrong total on an invoice is worse than a 400.
func TestMoneyMustBeAPlainDecimalString(t *testing.T) {
	for _, bad := range []string{"1,234.56", "1.2e3", "OMR 5", "", "5.", "٥"} {
		r := valid()
		r.Document.Waterfall.Total = bad
		if err := r.Validate(); err == nil {
			t.Errorf("total %q was accepted", bad)
		}
	}
	for _, good := range []string{"0", "-12", "91.740600", "  91.7406  "} {
		r := valid()
		r.Document.Waterfall.Total = good
		if err := r.Validate(); err != nil {
			t.Errorf("total %q was refused: %v", good, err)
		}
	}
}

func TestTemplateAndLocaleAreNormalised(t *testing.T) {
	r := valid()
	r.Template, r.Locale, r.Currency = "  INVOICE ", "", " omr "
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	if r.Template != "invoice" || r.Locale != "en" || r.Currency != "OMR" {
		t.Fatalf("normalised to %q / %q / %q", r.Template, r.Locale, r.Currency)
	}
}

// A statement may carry no lines (a period with nothing rated); an invoice,
// credit note or quote may not — an empty one is a document nobody meant.
func TestLinesRequiredExceptOnAStatement(t *testing.T) {
	for _, kind := range Templates {
		r := valid()
		r.Template = kind
		r.Document.Lines = nil
		err := r.Validate()
		if kind == Statement && err != nil {
			t.Errorf("%s: an empty period must render: %v", kind, err)
		}
		if kind != Statement && err == nil {
			t.Errorf("%s: a document with no lines was accepted", kind)
		}
	}
}

func TestDateFormats(t *testing.T) {
	for _, good := range []string{"2026-09-01", "2026-09-01T12:30:00Z", "2026-09-01T12:30:00+04:00"} {
		if _, err := ParseDate(good); err != nil {
			t.Errorf("ParseDate(%q): %v", good, err)
		}
	}
	for _, bad := range []string{"", "01-09-2026", "2026/09/01", "Sep 1 2026", "2026-13-01"} {
		if _, err := ParseDate(bad); err == nil {
			t.Errorf("ParseDate(%q) accepted", bad)
		}
	}
}

func TestDecodeLogo(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	raw, format, err := DecodeLogo(png)
	if err != nil || format != "png" || len(raw) == 0 {
		t.Fatalf("DecodeLogo = %d bytes, %q, %v", len(raw), format, err)
	}
	for _, bad := range []string{
		"https://example.test/logo.png",      // not a data URI: no outbound fetch, ever
		"data:image/svg+xml;base64,PHN2Zy8+", // SVG is a script surface
		"data:image/png,notbase64",           // must be base64
		"data:text/html;base64,PGI+aGk8L2I+", // not an image
	} {
		if _, _, err := DecodeLogo(bad); err == nil {
			t.Errorf("DecodeLogo(%q) accepted", bad)
		}
	}
	// The size cap is enforced before the decode allocates.
	big := "data:image/png;base64," + strings.Repeat("A", (MaxLogoBytes+1024)*2)
	if _, _, err := DecodeLogo(big); err == nil {
		t.Error("an oversized logo was accepted")
	}
	// Only the seller carries one.
	r := valid()
	r.Document.Buyer.LogoDataURI = png
	if err := r.Validate(); err == nil {
		t.Error("a buyer logo was accepted")
	}
}

func TestRowCaps(t *testing.T) {
	r := valid()
	line := r.Document.Lines[0]
	r.Document.Lines = nil
	for i := 0; i <= MaxLines; i++ {
		r.Document.Lines = append(r.Document.Lines, line)
	}
	if err := r.Validate(); err == nil {
		t.Error("a document past the line cap was accepted")
	}
}
