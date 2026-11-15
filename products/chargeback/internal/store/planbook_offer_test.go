package store

import (
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// TestPlanBookItemsAreTheDefaultOffer: the plans book the engine creates is
// priced by the default offer — S 2.490 · M 4.490 · L 7.990 · XL 13.990 OMR a
// month, the published ladder — and not by a second list. Before this the
// engine created it at S 5 · M 9 · L 16 · XL 30, so the first customer
// Organization of a fresh Sovereign published a ladder at those prices and
// billing priced orders from it. The four figures are also the catalog
// service's seed (in baisa), read from its source here so the two cannot
// drift.
func TestPlanBookItemsAreTheDefaultOffer(t *testing.T) {
	want := map[string]struct{ annual, unit, monthly string }{
		"plan.s":  {"29.88", "0.00341096", "2.490"},
		"plan.m":  {"53.88", "0.00615068", "4.490"},
		"plan.l":  {"95.88", "0.01094521", "7.990"},
		"plan.xl": {"167.88", "0.01916438", "13.990"},
	}
	items := PlanBookItems()
	if len(items) != len(want) {
		t.Fatalf("%d plan items, want %d", len(items), len(want))
	}
	for _, it := range items {
		w, ok := want[it.SKU]
		if !ok {
			t.Errorf("%s is not a catalog package", it.SKU)
			continue
		}
		if it.Unit != PlanUnit || it.AnnualPrice == nil || string(*it.AnnualPrice) != w.annual || string(it.UnitPrice) != w.unit {
			t.Errorf("%s = %s per %s, annual %v; want %s, annual %s", it.SKU, it.UnitPrice, it.Unit, it.AnnualPrice, w.unit, w.annual)
		}
		// The published monthly price: unit price × 730, at the minor unit.
		m := new(big.Rat).Mul(ratOf(it.UnitPrice), big.NewRat(730, 1)).FloatString(3)
		if m != w.monthly {
			t.Errorf("%s publishes %s a month, want %s", it.SKU, m, w.monthly)
		}
		if PlanMonthly(it.SKU[len(PlanSKUPrefix):]).FloatString(3) != w.monthly {
			t.Errorf("PlanMonthly(%s) = %s, want %s", it.SKU, PlanMonthly(it.SKU[len(PlanSKUPrefix):]).FloatString(3), w.monthly)
		}
	}
	if PlanMonthly(PlanFlexi) != nil {
		t.Error("flexi has a monthly plan price; it is pay per use")
	}

	// The catalog service seeds the same four prices, in baisa.
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "core", "services", "catalog", "handlers", "seed.go"))
	if err != nil {
		t.Fatalf("read the catalog seed: %v", err)
	}
	row := regexp.MustCompile(`\{Slug: "(s|m|l|xl)", Name: "[A-Z]+",[^\n]*PriceBaisa: ([0-9]+),`)
	seen := 0
	for _, m := range row.FindAllStringSubmatch(string(src), -1) {
		baisa, _ := strconv.Atoi(m[2])
		got := new(big.Rat).SetFrac64(int64(baisa), 1000).FloatString(3)
		if got != want["plan."+m[1]].monthly {
			t.Errorf("the catalog seeds plan %s at %s OMR, the default offer at %s", m[1], got, want["plan."+m[1]].monthly)
		}
		seen++
	}
	if seen != 4 {
		t.Fatalf("read %d of the catalog's 4 package rows from seedPlanRows; the row shape changed", seen)
	}
}
