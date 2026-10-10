package packages

import (
	"errors"
	"os"
	"testing"
)

// The document hw307 published on 2026-10-10 carries `teaser` cells
// (vuln_dashboard and compliance below the package that includes them). The
// parser refused the whole document on that state, so every storefront quote
// answered 503. The live body is pinned here, unchanged.
func TestParse_LiveDocumentWithTeaserCells(t *testing.T) {
	body, err := os.ReadFile("testdata/live-hw307-2026-10-10.json")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse(live document) = %v", err)
	}
	q, err := doc.Price("plan.m", []string{"addon.backup"})
	if err != nil {
		t.Fatalf("Price(M + backup) = %v", err)
	}
	if q.PlanMinor != 4490 || len(q.Lines) != 1 || q.Lines[0].PriceMinor != 1500 {
		t.Fatalf("M + backup = plan %d, lines %+v; want 4490 and one 1500 line", q.PlanMinor, q.Lines)
	}
}

// A teaser cell is shown, never sold: buying it on that package is refused
// like not_offered.
func TestPrice_TeaserIsRefused(t *testing.T) {
	doc, err := Parse([]byte(`{"currency":"OMR","price_book":"b","prices_as_of":"2026-10-10",
	 "packages":[{"sku":"plan.s","name":"S","price_month":"2.490"}],
	 "features":[{"key":"vuln","name":"Vulnerability dashboard","kind":"boolean","addon_sku":"addon.vuln",
	   "cells":{"plan.s":{"state":"teaser","addon_sku":"addon.vuln","included_from":"plan.m"}}}]}`))
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	_, err = doc.Price("plan.s", []string{"addon.vuln"})
	var refused *RefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("Price(teaser) = %v; want a RefusedError", err)
	}
}
