package rating

import (
	"math/big"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func TestUnitPriceUsesAnnualDivisor(t *testing.T) {
	cases := []struct {
		annual  string
		divisor int
		want    string
	}{
		{"8760", 8760, "1.00000000"},
		{"876.00", 8760, "0.10000000"},
		{"1000", 8760, "0.11415525"}, // 0.114155251... rounds half-up at 8
		{"1", 3, "0.33333333"},
		{"2", 3, "0.66666667"},
		{"0", 8760, "0.00000000"},
		{"1000", 8000, "0.12500000"}, // a shorter billing year raises the hourly rate
	}
	for _, c := range cases {
		got, err := UnitPrice(c.annual, c.divisor)
		if err != nil {
			t.Fatalf("%s/%d: %v", c.annual, c.divisor, err)
		}
		if string(got) != c.want {
			t.Errorf("UnitPrice(%s, %d) = %s, want %s", c.annual, c.divisor, got, c.want)
		}
	}
	if _, err := UnitPrice("10", 0); err == nil {
		t.Error("zero divisor accepted")
	}
	if _, err := UnitPrice("-1", 10); err == nil {
		t.Error("negative annual accepted")
	}
	if _, err := UnitPrice("abc", 10); err == nil {
		t.Error("garbage accepted")
	}
}

func TestAmountAndTotals(t *testing.T) {
	amt, err := Amount("744.000000", "0.10000000")
	if err != nil || string(amt) != "74.400000" {
		t.Fatalf("amount = %s err=%v", amt, err)
	}
	amt, _ = Amount("0.333333", "3")
	if string(amt) != "0.999999" {
		t.Fatalf("amount = %s", amt)
	}
	lines := []store.RatedLine{{Amount: "74.400000"}, {Amount: "25.600000"}}
	sub, tax, total, err := Totals(lines, "0.05")
	if err != nil {
		t.Fatal(err)
	}
	if string(sub) != "100.000000" || string(tax) != "5.000000" || string(total) != "105.000000" {
		t.Fatalf("totals = %s %s %s", sub, tax, total)
	}
	if r := roundRat(mustRat("2.5"), 0); r != "3" {
		t.Errorf("half-up 2.5 = %s", r)
	}
	if r := roundRat(mustRat("-1.25"), 1); r != "-1.3" {
		t.Errorf("negative rounding = %s", r)
	}
	if r := roundRat(mustRat("0.0000001"), 6); r != "0.000000" {
		t.Errorf("tiny = %s", r)
	}
}

func mustRat(s string) *big.Rat {
	r, _ := parseRat(s)
	return r
}

func TestRateAppliesStoppedPolicy(t *testing.T) {
	src := "src-1"
	usage := []store.RatableUsage{
		{SourceID: src, SKU: "ecs.s6.large.2", Unit: "instance-hour", ResourceKind: "ecs", Quantity: "100.000000", StoppedQuantity: "40.000000", ResourceCount: 2},
		{SourceID: src, SKU: "evs.ssd.gb", Unit: "gb-hour", ResourceKind: "evs", Quantity: "1000.000000", StoppedQuantity: "300.000000", ResourceCount: 3},
		{SourceID: src, SKU: "eip", Unit: "hour", ResourceKind: "eip", Quantity: "10.000000", ResourceCount: 1},
		{SourceID: src, SKU: "ecs.cpu_util", Unit: "pct-hour-avg", ResourceKind: "ecs", Quantity: "12.500000", ResourceCount: 2},
	}
	items := map[string]store.PriceItem{
		"ecs.s6.large.2": {SKU: "ecs.s6.large.2", UnitPrice: "0.10000000"},
		"evs.ssd.gb":     {SKU: "evs.ssd.gb", UnitPrice: "0.00100000"},
		"eip":            {SKU: "eip", UnitPrice: "0.00500000"},
	}
	find := func(lines []store.RatedLine, sku string) store.RatedLine {
		for _, l := range lines {
			if l.SKU == sku {
				return l
			}
		}
		t.Fatalf("no line for %s", sku)
		return store.RatedLine{}
	}
	// compute: stopped hours billed in full.
	lines, unpriced, err := Rate(usage, items, BillStoppedCompute)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 || len(unpriced) != 1 || unpriced[0] != "ecs.cpu_util" {
		t.Fatalf("lines=%d unpriced=%v", len(lines), unpriced)
	}
	if l := find(lines, "ecs.s6.large.2"); string(l.Quantity) != "100.000000" || string(l.Amount) != "10.000000" || l.ResourceCount != 2 {
		t.Fatalf("compute ecs line = %+v", l)
	}
	// storage-only: stopped instance hours dropped, volumes untouched.
	lines, _, _ = Rate(usage, items, BillStoppedStorageOnly)
	if l := find(lines, "ecs.s6.large.2"); string(l.Quantity) != "60.000000" || string(l.Amount) != "6.000000" {
		t.Fatalf("storage-only ecs line = %+v", l)
	}
	if l := find(lines, "evs.ssd.gb"); string(l.Quantity) != "1000.000000" || string(l.Amount) != "1.000000" {
		t.Fatalf("storage-only evs line = %+v", l)
	}
	// none: both the instance and its attached volumes lose the stopped share.
	lines, _, _ = Rate(usage, items, BillStoppedNone)
	if l := find(lines, "ecs.s6.large.2"); string(l.Quantity) != "60.000000" {
		t.Fatalf("none ecs line = %+v", l)
	}
	if l := find(lines, "evs.ssd.gb"); string(l.Quantity) != "700.000000" || string(l.Amount) != "0.700000" {
		t.Fatalf("none evs line = %+v", l)
	}
	if l := find(lines, "eip"); string(l.Amount) != "0.050000" || *l.SourceID != src {
		t.Fatalf("eip line = %+v", l)
	}
}

func TestParsePriceBookCSV(t *testing.T) {
	items, errs, err := ParsePriceBookCSV(strings.NewReader(PriceBookCSVTemplate), 8760)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("template produced errors: %+v", errs)
	}
	if len(items) != 9 {
		t.Fatalf("items = %d", len(items))
	}
	by := map[string]store.PriceItem{}
	for _, it := range items {
		by[it.SKU] = it
	}
	if it := by["ecs.s6.large.2"]; string(it.UnitPrice) != "0.10000000" || it.AnnualPrice == nil || string(*it.AnnualPrice) != "876.00" || it.Unit != "instance-hour" {
		t.Fatalf("ecs item = %+v", it)
	}
	if it := by["eip"]; string(it.UnitPrice) != "0.00500000" {
		t.Fatalf("eip item = %+v", it)
	}

	// Column order is free, unit_price may be given directly, bad rows are
	// reported by line and do not abort the import, duplicates are rejected.
	csvText := "description,unit,sku,annual_price,unit_price\n" +
		"x,hour,elb,8760,\n" +
		"direct,hour,nat.1,,0.25\n" +
		"bad,hour,eip,abc,\n" +
		"missing,hour,,1,\n" +
		"dup,hour,elb,1,\n" +
		"neither,hour,obs.gb,,\n"
	items, errs, err = ParsePriceBookCSV(strings.NewReader(csvText), 8760)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || string(items[0].UnitPrice) != "1.00000000" || string(items[1].UnitPrice) != "0.25" || items[1].AnnualPrice != nil {
		t.Fatalf("items = %+v", items)
	}
	if len(errs) != 4 || errs[0].Line != 4 || errs[1].Line != 5 || errs[2].Line != 6 || errs[3].Line != 7 {
		t.Fatalf("errs = %+v", errs)
	}
	if _, _, err := ParsePriceBookCSV(strings.NewReader("sku,description\nx,y\n"), 8760); err == nil {
		t.Fatal("missing columns accepted")
	}
}
