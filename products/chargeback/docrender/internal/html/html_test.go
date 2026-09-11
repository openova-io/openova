package html

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/docrender"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/document"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/fixture"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/i18n"
	"github.com/openova-io/openova/products/chargeback/docrender/internal/view"
)

func load(t *testing.T) (*Set, *i18n.Catalog) {
	t.Helper()
	set, err := Load(docrender.Assets())
	if err != nil {
		t.Fatalf("load templates: %v", err)
	}
	b, err := i18n.Load(docrender.Assets())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := b.Catalog("en")
	return set, c
}

// Every document kind has a template, and each renders the same facts the
// PDF carries — the HTML rendition is the same document, not a summary.
func TestEveryTemplateRenders(t *testing.T) {
	set, cat := load(t)
	if len(set.Kinds()) != len(document.Templates) {
		t.Fatalf("parsed %d templates, want %d", len(set.Kinds()), len(document.Templates))
	}
	cases := map[string][]string{
		"invoice":     {"Tax Invoice", "INV-2026-00042", "Acme Trading LLC", "k8s.vcpu", "91.741", "41.741", "Balance due", "TRF-88213"},
		"credit-note": {"Credit Note", "CN-2026-00003", "Total credited", "52.500"},
		"statement":   {"Statement", "Beta Co", "29.999"},
		"quote":       {"Quotation", "Q-2026-0117", "Quoted total", "7,064.820", "PRO FORMA"},
	}
	for kind, want := range cases {
		t.Run(kind, func(t *testing.T) {
			req, err := fixture.Load(kind)
			if err != nil {
				t.Fatal(err)
			}
			m, err := view.Build(req, cat)
			if err != nil {
				t.Fatal(err)
			}
			out, err := set.Render(m)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			got := string(out)
			if !strings.HasPrefix(got, "<!doctype html>") {
				t.Fatalf("not an HTML document: %.40q", got)
			}
			for _, s := range want {
				if !strings.Contains(got, s) {
					t.Errorf("missing %q", s)
				}
			}
			if strings.Contains(got, "ZgotmplZ") {
				t.Error("html/template refused a URL it was handed")
			}
		})
	}
}

// A template must never carry a literal label: every one resolves through
// the catalog, which is what makes a second language a data change.
func TestLabelsComeFromTheCatalogNotTheTemplate(t *testing.T) {
	set, _ := load(t)
	b, err := i18n.Load(docrender.Assets())
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := b.Catalog("en")

	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	m, err := view.Build(req, cat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.Render(m); err != nil {
		t.Fatal(err)
	}
	if miss := cat.Missing(); len(miss) > 0 {
		t.Fatalf("the invoice template asked for label keys en.json lacks: %v", miss)
	}
}

// The seller logo reaches the page as a data URI. html/template's URL filter
// rejects `data:` unless the value is typed, so this is the assertion that
// catches the day someone drops the typing and the logo silently vanishes.
func TestSellerLogoBecomesADataURI(t *testing.T) {
	set, cat := load(t)
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	req.Document.Seller.LogoDataURI = png
	m, err := view.Build(req, cat)
	if err != nil {
		t.Fatal(err)
	}
	out, err := set.Render(m)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if !strings.Contains(got, `src="data:image/png;base64,`) {
		t.Fatalf("the logo did not reach the page as a data URI\n%s", excerpt(got, "logo"))
	}
	if strings.Contains(got, "ZgotmplZ") {
		t.Fatal("the data URI was filtered out by html/template")
	}
}

// Content from the document is ESCAPED. A buyer name is customer-controlled
// data and must never become markup.
func TestDocumentContentIsEscaped(t *testing.T) {
	set, cat := load(t)
	req, err := fixture.Load("invoice")
	if err != nil {
		t.Fatal(err)
	}
	req.Document.Buyer.Name = `<script>alert(1)</script>`
	req.Document.Notes = `<img src=x onerror=alert(2)>`
	m, err := view.Build(req, cat)
	if err != nil {
		t.Fatal(err)
	}
	out, err := set.Render(m)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	// The assertion is on the ANGLE BRACKETS, not on the payload text:
	// `onerror=alert(2)` is still present after escaping and is harmless
	// there — what must never survive is the tag that would execute it.
	for _, tag := range []string{"<script>alert(1)", "<img src=x"} {
		if strings.Contains(got, tag) {
			t.Fatalf("document content was rendered as markup: %q", tag)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatal("the buyer name did not survive as escaped text")
	}
	if !strings.Contains(got, "&lt;img src=x onerror=alert(2)&gt;") {
		t.Fatal("the note did not survive as escaped text")
	}
}

func excerpt(s, around string) string {
	i := strings.Index(s, around)
	if i < 0 {
		return s[:min(400, len(s))]
	}
	lo, hi := max(0, i-200), min(len(s), i+200)
	return s[lo:hi]
}
