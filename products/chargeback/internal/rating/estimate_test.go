package rating

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The public calculator's items: an hourly instance at 0.1, SSD storage at
// the 8-decimal rate the annual list derives (1.20 / 8760), and plan M as the
// plans book prices it (108 / 8760).
func estimateItems() map[string]store.PriceItem {
	return map[string]store.PriceItem{
		"ecs.s6.large.2": {SKU: "ecs.s6.large.2", Unit: "instance-hour", UnitPrice: "0.10000000"},
		"evs.ssd.gb":     {SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.00013699"},
		"plan.m":         {SKU: "plan.m", Unit: "plan-hour", UnitPrice: "0.01232877"},
	}
}

// TestPriceEstimateEqualsRate is the discriminating proof that the calculator
// has no pricing path of its own: for the same SKU, quantity and hours, the
// estimate line IS the line Rate produces from the ledger aggregate a month
// of records would leave — at 8-decimal prices and non-integer hours, where
// a float or a second rounding rule would show.
func TestPriceEstimateEqualsRate(t *testing.T) {
	items := estimateItems()
	lines := []EstimateLine{
		{SKU: "evs.ssd.gb", Quantity: "100", Hours: "730"},     // 73000 gb-hour × 0.00013699 = 10.00027
		{SKU: "ecs.s6.large.2", Quantity: "3", Hours: "100.5"}, // 301.5 instance-hour × 0.1 = 30.15
	}
	res, err := PriceEstimate(items, BillStoppedStorageOnly, lines, "0.05")
	if err != nil {
		t.Fatal(err)
	}
	// What the ledger would hold after the month, rated by the statement run.
	usage := []store.RatableUsage{
		{SourceID: "src", SKU: "evs.ssd.gb", Unit: "gb-hour", Quantity: "73000.000000", ResourceCount: 100},
		{SourceID: "src", SKU: "ecs.s6.large.2", Unit: "instance-hour", Quantity: "301.500000", ResourceCount: 3},
	}
	rated, unpriced, err := Rate(usage, items, BillStoppedStorageOnly)
	if err != nil || len(unpriced) != 0 {
		t.Fatalf("Rate: %v %v", err, unpriced)
	}
	if len(res.Lines) != len(rated) {
		t.Fatalf("estimate %d lines, statement %d", len(res.Lines), len(rated))
	}
	for i := range rated {
		e, s := res.Lines[i], rated[i]
		if e.SKU != s.SKU || e.Quantity != s.Quantity || e.UnitPrice != s.UnitPrice || e.Amount != s.Amount || e.Unit != s.Unit {
			t.Fatalf("line %d: estimate %+v ≠ statement %+v", i, e, s)
		}
	}
	if res.Lines[0].Amount != "10.000270" || res.Lines[1].Amount != "30.150000" {
		t.Fatalf("amounts = %s %s", res.Lines[0].Amount, res.Lines[1].Amount)
	}
	// The totals are Totals: subtotal 40.15027, tax 5 % = 2.0075135, total 42.1577835.
	sub, tax, total, err := Totals(rated, "0.05")
	if err != nil {
		t.Fatal(err)
	}
	if res.Subtotal != sub || res.Tax != tax || res.Total != total {
		t.Fatalf("totals: estimate %s/%s/%s ≠ statement %s/%s/%s", res.Subtotal, res.Tax, res.Total, sub, tax, total)
	}
	if res.Tax != "2.007514" || res.Total != "42.157784" {
		t.Fatalf("tax/total = %s/%s", res.Tax, res.Total)
	}
	// All lines are one month: Monthly is Total to the digit; Yearly is 12×.
	if res.Monthly != res.Total {
		t.Fatalf("monthly %s ≠ total %s", res.Monthly, res.Total)
	}
	if res.Yearly != "505.893408" {
		t.Fatalf("yearly = %s, want 12 × %s", res.Yearly, res.Monthly)
	}
}

// A plan line over several months: the term total counts every month, the
// recurring figure is one month, and both come from the same rated line.
func TestPriceEstimatePlanMonths(t *testing.T) {
	res, err := PriceEstimate(estimateItems(), BillStoppedCompute, []EstimateLine{{SKU: "plan.m", Quantity: "1", Months: 3}}, "0.05")
	if err != nil {
		t.Fatal(err)
	}
	// 3 × 730 = 2190 plan-hours × 0.01232877 = 27.0000063 → 27.000006.
	if l := res.Lines[0]; l.Quantity != "2190.000000" || l.Amount != "27.000006" {
		t.Fatalf("line = %+v", l)
	}
	if res.Subtotal != "27.000006" || res.Total != "28.350006" {
		t.Fatalf("term totals = %s / %s", res.Subtotal, res.Total)
	}
	// One month: 9.000002 + 5 % = 9.450002; a year of it 113.400024.
	if res.Monthly != "9.450002" || res.Yearly != "113.400024" {
		t.Fatalf("monthly/yearly = %s / %s", res.Monthly, res.Yearly)
	}
	// Months 1 gives Monthly == Total exactly.
	one, _ := PriceEstimate(estimateItems(), BillStoppedCompute, []EstimateLine{{SKU: "plan.m", Quantity: "1"}}, "0.05")
	if one.Monthly != one.Total || one.Total != "9.450002" {
		t.Fatalf("one month = %+v", one)
	}
}

// The default hours are HoursPerMonth; an unknown SKU is reported, not
// silently dropped; a non-positive quantity is refused; the stopped-instance
// policy of the book changes nothing (nothing is stopped in an estimate).
func TestPriceEstimateDefaultsAndRefusals(t *testing.T) {
	items := estimateItems()
	res, err := PriceEstimate(items, BillStoppedNone, []EstimateLine{{SKU: "ecs.s6.large.2", Quantity: "2"}, {SKU: "ecs.nope", Quantity: "1"}}, "0")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0].Quantity != "1460.000000" || res.Lines[0].Amount != "146.000000" {
		t.Fatalf("lines = %+v", res.Lines)
	}
	if len(res.Unknown) != 1 || res.Unknown[0] != "ecs.nope" {
		t.Fatalf("unknown = %v", res.Unknown)
	}
	if res.Tax != "0.000000" || res.Total != res.Subtotal {
		t.Fatalf("zero tax: %+v", res)
	}
	for _, bad := range []EstimateLine{{SKU: "ecs.s6.large.2", Quantity: "0"}, {SKU: "ecs.s6.large.2", Quantity: "-1"}, {SKU: "ecs.s6.large.2", Quantity: "1", Hours: "0"}, {SKU: "ecs.s6.large.2", Quantity: "x"}} {
		if _, err := PriceEstimate(items, BillStoppedCompute, []EstimateLine{bad}, "0.05"); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Fatalf("%+v must be refused naming the line, got %v", bad, err)
		}
	}
}
