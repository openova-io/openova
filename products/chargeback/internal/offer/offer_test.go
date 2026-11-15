package offer

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// The offer the engine ships parses, and it is the offer published on 20
// October 2026: 4 packages, the 8 floor items, 19 features with a cell on
// every package (76), the 7 groups, 12 price items, every icon a file here.
func TestDefaultOfferParses(t *testing.T) {
	o, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	d := o.Document
	if d.Currency != "OMR" || d.PriceBook != "OpenOva plans" {
		t.Fatalf("currency %q, book %q", d.Currency, d.PriceBook)
	}
	cells := 0
	for _, f := range d.Features {
		if len(f.Cells) != len(d.Packages) {
			t.Errorf("feature %s has %d cells, want one per package (%d)", f.Key, len(f.Cells), len(d.Packages))
		}
		cells += len(f.Cells)
	}
	for _, c := range []struct {
		what      string
		got, want int
	}{
		{"packages", len(d.Packages), 4}, {"floor items", len(d.Floor), 8}, {"features", len(d.Features), 19},
		{"cells", cells, 76}, {"groups", len(d.Groups), 7}, {"price items", len(o.PriceItems), 12},
	} {
		if c.got != c.want {
			t.Errorf("%d %s, want %d", c.got, c.what, c.want)
		}
	}
	if len(o.PlanItems()) != 4 {
		t.Errorf("%d plan items, want 4", len(o.PlanItems()))
	}
	for _, f := range o.IconFiles() {
		b, err := IconBytes(f)
		if err != nil || !bytes.Contains(b, []byte("<svg")) {
			t.Errorf("icon %s: %v", f, err)
		}
	}
	// The recommended package carries the badge, and only it.
	recommended := 0
	for _, p := range d.Packages {
		if p.Recommended {
			recommended++
			if p.Badge == "" {
				t.Errorf("%s is recommended and has no badge", p.SKU)
			}
		}
	}
	if recommended != 1 {
		t.Errorf("%d recommended packages, want 1", recommended)
	}
}

// Parse refuses what the writer could not write: a package that is not a
// priced plan item, an icon that is not a file here, an add-on nobody prices.
func TestParseRefusesAnOfferTheWriterCannotWrite(t *testing.T) {
	good, err := Files.ReadFile("default-offer.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"a package that is not priced", `"sku": "plan.xl",`, `"sku": "plan.xxl",`},
		{"an icon that is not a file here", `"file": "cpu.svg"`, `"file": "no-such.svg"`},
		{"an add-on that is not priced", `"addon_sku": "addon.backup",`, `"addon_sku": "addon.nothing",`},
		{"an unknown field", `"comment": [`, `"comments": [`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !bytes.Contains(good, []byte(tc.from)) {
				t.Fatalf("the offer no longer contains %s; update the mutant", tc.from)
			}
			if _, err := Parse(bytes.Replace(good, []byte(tc.from), []byte(tc.to), 1)); err == nil {
				t.Fatalf("Parse accepted an offer with %s", tc.name)
			}
		})
	}
	if _, err := Parse(good); err != nil {
		t.Fatalf("the unchanged offer: %v", err)
	}
}

// The file is in the open-source tree: it names no person, no mailbox, no
// host or installation and no pricing workbook, only the offer.
func TestDefaultOfferNamesNoInstallationOrPerson(t *testing.T) {
	b, err := Files.ReadFile("default-offer.json")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(b))
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`[a-z0-9._-]+@[a-z0-9.-]+`),                          // a mailbox
		regexp.MustCompile(`\b[a-z0-9-]+\.(omani|omantel|openova)\.[a-z]+\b`),   // a host of the domains canon
		regexp.MustCompile(`\.xlsx\b`),                                          // a workbook
		regexp.MustCompile(`\bhw[0-9]{2,}\b|\bt[0-9]{2,}\.`),                    // an installation
		regexp.MustCompile(`https?://`),                                         // any address
	} {
		if m := re.FindString(text); m != "" {
			t.Errorf("default-offer.json carries %q", m)
		}
	}
}
