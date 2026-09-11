package i18n

import (
	"testing"
	"testing/fstest"

	"github.com/openova-io/openova/products/chargeback/docrender"
)

func TestLoadsTheShippedCatalog(t *testing.T) {
	b, err := Load(docrender.Assets())
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Locales(); len(got) == 0 || got[0] != "en" {
		t.Fatalf("locales = %v", got)
	}
	c, ok := b.Catalog("en")
	if !ok {
		t.Fatal("en missing")
	}
	if c.Thousands != "," || c.Decimal != "." || c.DateLayout == "" {
		t.Fatalf("number/date metadata not read: %q %q %q", c.Thousands, c.Decimal, c.DateLayout)
	}
	if len(c.Keys()) < 40 {
		t.Fatalf("only %d label keys — the catalog looks truncated", len(c.Keys()))
	}
}

// The lookup accepts a region suffix, so a browser sending en-GB is served
// English rather than refused.
func TestCatalogLookupIsForgiving(t *testing.T) {
	b, _ := Load(docrender.Assets())
	for _, in := range []string{"en", "EN", " en ", "en-GB", "en-US"} {
		if _, ok := b.Catalog(in); !ok {
			t.Errorf("Catalog(%q) not found", in)
		}
	}
	if _, ok := b.Catalog("fr"); ok {
		t.Error("a locale that is not shipped must not resolve")
	}
}

// A missing key prints the key — visible and greppable — and is RECORDED, so
// a test can assert a whole document asked for nothing the catalog lacks.
func TestMissingKeysAreVisibleAndRecorded(t *testing.T) {
	b, _ := Load(docrender.Assets())
	c, _ := b.Catalog("en")
	if got := c.T("no.such.key"); got != "no.such.key" {
		t.Fatalf("missing key rendered as %q", got)
	}
	if c.Has("no.such.key") {
		t.Error("Has reported a key that does not exist")
	}
	if c.Has("_number.decimal") {
		t.Error("metadata must not be reachable as a label")
	}
	found := false
	for _, k := range c.Missing() {
		if k == "no.such.key" {
			found = true
		}
	}
	if !found {
		t.Fatal("the missing key was not recorded")
	}
}

func TestPlaceholderSubstitution(t *testing.T) {
	b, _ := Load(docrender.Assets())
	c, _ := b.Catalog("en")
	if got := c.Tf("terms.net_days", map[string]string{"days": "30"}); got != "Net 30 days" {
		t.Fatalf("Tf = %q", got)
	}
	if got := c.Tf("footer.page", map[string]string{"page": "2", "pages": "5"}); got != "Page 2 of 5" {
		t.Fatalf("Tf = %q", got)
	}
}

// English is REQUIRED: it is the fallback the whole service is built on, and
// a tree without it must fail at start rather than at the first request.
func TestEnglishIsRequired(t *testing.T) {
	only := fstest.MapFS{"locales/fr.json": &fstest.MapFile{Data: []byte(`{"_locale":"fr"}`)}}
	if _, err := Load(only); err == nil {
		t.Fatal("a locale tree without en.json was accepted")
	}
	mismatched := fstest.MapFS{"locales/en.json": &fstest.MapFile{Data: []byte(`{"_locale":"de"}`)}}
	if _, err := Load(mismatched); err == nil {
		t.Fatal("a catalog whose _locale contradicts its filename was accepted")
	}
	broken := fstest.MapFS{"locales/en.json": &fstest.MapFile{Data: []byte(`{`)}}
	if _, err := Load(broken); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}
