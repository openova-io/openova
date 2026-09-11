package rating

import (
	"math/big"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The cost-centre breakdown (DESIGN.md §19). What is under test is one
// property and its boundaries: the rows sum to the statement EXACTLY, for
// every column, on figures chosen because they do not divide.

func weights(pairs ...any) []store.CostCentreWeight {
	out := make([]store.CostCentreWeight, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, store.CostCentreWeight{Code: pairs[i].(string), Amount: store.Decimal(pairs[i+1].(string))})
	}
	return out
}

func sumCol(t *testing.T, lines []store.CostCentreLine, pick func(store.CostCentreLine) store.Decimal) store.Decimal {
	t.Helper()
	vals := make([]store.Decimal, 0, len(lines))
	for _, l := range lines {
		vals = append(vals, pick(l))
	}
	got, err := Sum(vals...)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// assertExact is the whole contract: every money column of the breakdown
// sums to the statement's own figure, to the last unit of the money scale.
func assertExact(t *testing.T, lines []store.CostCentreLine, net, discount, tax store.Decimal) {
	t.Helper()
	wantTotal, err := Sum(net, tax)
	if err != nil {
		t.Fatal(err)
	}
	wantList, err := Sum(net, discount)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		got  store.Decimal
		want store.Decimal
	}{
		{"net", sumCol(t, lines, func(l store.CostCentreLine) store.Decimal { return l.Net }), net},
		{"discount", sumCol(t, lines, func(l store.CostCentreLine) store.Decimal { return l.Discount }), discount},
		{"tax", sumCol(t, lines, func(l store.CostCentreLine) store.Decimal { return l.Tax }), tax},
		{"total", sumCol(t, lines, func(l store.CostCentreLine) store.Decimal { return l.Total }), wantTotal},
		{"list", sumCol(t, lines, func(l store.CostCentreLine) store.Decimal { return l.List }), wantList},
	} {
		if !sameDec(t, c.got, c.want) {
			t.Errorf("sum(%s) = %s, the statement carries %s — the breakdown and the invoice disagree", c.name, c.got, c.want)
		}
	}
	// Every row is internally consistent too, or a reader cannot check one
	// line against its own columns.
	for _, l := range lines {
		list, err := Sum(l.Net, l.Discount)
		if err != nil {
			t.Fatal(err)
		}
		total, err := Sum(l.Net, l.Tax)
		if err != nil {
			t.Fatal(err)
		}
		if !sameDec(t, l.List, list) {
			t.Errorf("%s: list %s != net %s + discount %s", l.Code, l.List, l.Net, l.Discount)
		}
		if !sameDec(t, l.Total, total) {
			t.Errorf("%s: total %s != net %s + tax %s", l.Code, l.Total, l.Net, l.Tax)
		}
		if r, err := parseRat(string(l.Net)); err != nil || r.Sign() < 0 {
			t.Errorf("%s: net %s is negative", l.Code, l.Net)
		}
	}
}

func sameDec(t *testing.T, a, b store.Decimal) bool {
	t.Helper()
	x, err := parseRat(string(a))
	if err != nil {
		t.Fatalf("parse %q: %v", a, err)
	}
	y, err := parseRat(string(b))
	if err != nil {
		t.Fatalf("parse %q: %v", b, err)
	}
	return x.Cmp(y) == 0
}

func TestCostCentreBreakdownSumsExactlyOverAwkwardSplits(t *testing.T) {
	// Three equal weights and figures that do not divide by three: the naive
	// answer is three parts a unit short of the whole.
	cases := []struct {
		name               string
		w                  []store.CostCentreWeight
		net, discount, tax store.Decimal
	}{
		{"thirds", weights("a", "1", "b", "1", "c", "1"), "100.000000", "10.000001", "5.000002"},
		{"sevenths", weights("a", "1", "b", "2", "c", "4"), "999.999999", "0.000007", "49.999999"},
		{"one big one tiny", weights("a", "9999.999999", "b", "0.000001"), "1234.567891", "1.000003", "61.728395"},
		{"zero weight among them", weights("a", "0", "b", "5", "c", "0"), "77.777777", "7.000001", "3.888889"},
		{"no discount", weights("a", "3", "b", "5"), "12.345678", "0", "0.617284"},
		{"everything discounted away", weights("a", "3", "b", "5"), "0", "40.000001", "0"},
		{"one centre", weights("a", "42"), "100.000001", "9.999999", "5.000000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines, err := CostCentreBreakdown(c.w, c.net, c.discount, c.tax)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != len(c.w) {
				t.Fatalf("lines = %d, weights = %d", len(lines), len(c.w))
			}
			assertExact(t, lines, c.net, c.discount, c.tax)
		})
	}
}

// A period with no usage at all still has a statement — a contract true-up,
// a minimum commitment. The figure lands in the NAMED unassigned bucket
// rather than being dropped or shared over centres no usage supports.
func TestCostCentreBreakdownWithNoUsageGoesToTheUnassignedBucket(t *testing.T) {
	lines, err := CostCentreBreakdown(nil, "500.000000", "0", "25.000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Code != store.CostCentreUnassigned || lines[0].Name != store.CostCentreUnassignedName {
		t.Fatalf("lines = %+v", lines)
	}
	if lines[0].Net != "500.000000" || lines[0].Tax != "25.000000" || lines[0].Total != "525.000000" {
		t.Fatalf("unassigned row = %+v", lines[0])
	}
	assertExact(t, lines, "500.000000", "0", "25.000000")
}

// Centres exist but nothing they cover was priced this period (every record
// unpriced, or none at all). There is no proportion to go by, so the whole
// figure goes to the unassigned bucket — visible, never spread.
func TestCostCentreBreakdownWithOnlyZeroWeightsGoesToUnassignedFirst(t *testing.T) {
	w := []store.CostCentreWeight{
		{Code: "eng", Amount: "0.000000"},
		{Code: store.CostCentreUnassigned, Amount: "0.000000"},
		{Code: "res", Amount: "0.000000"},
	}
	lines, err := CostCentreBreakdown(w, "90.000000", "10.000000", "4.500000")
	if err != nil {
		t.Fatal(err)
	}
	if lines[0].Code != store.CostCentreUnassigned {
		t.Fatalf("first row = %s, want the unassigned bucket to carry an unsplittable figure", lines[0].Code)
	}
	if lines[0].Total != "94.500000" {
		t.Fatalf("unassigned total = %s", lines[0].Total)
	}
	for _, l := range lines[1:] {
		if l.Total != "0.000000" {
			t.Fatalf("%s carried %s with no usage behind it", l.Code, l.Total)
		}
	}
	assertExact(t, lines, "90.000000", "10.000000", "4.500000")
}

// The share each centre gets is its share of the WEIGHTS, not of the row
// count: a centre with four fifths of the usage carries four fifths of the
// bill.
func TestCostCentreBreakdownIsProRataByUsage(t *testing.T) {
	lines, err := CostCentreBreakdown(weights("eng", "80", "res", "20"), "100.000000", "0", "5.000000")
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]store.CostCentreLine{}
	for _, l := range lines {
		byCode[l.Code] = l
	}
	if byCode["eng"].Net != "80.000000" || byCode["res"].Net != "20.000000" {
		t.Fatalf("net split = %s / %s", byCode["eng"].Net, byCode["res"].Net)
	}
	if byCode["eng"].Tax != "4.000000" || byCode["res"].Tax != "1.000000" {
		t.Fatalf("tax split = %s / %s", byCode["eng"].Tax, byCode["res"].Tax)
	}
}

// ApportionWeights is shared with the tax step (DESIGN.md §17.3). Its two
// boundaries are pinned here because both callers depend on them.
func TestApportionWeightsBoundaries(t *testing.T) {
	rat := func(s string) *big.Rat {
		r, err := parseRat(s)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	zero, err := ApportionWeights("0", []*big.Rat{rat("1"), rat("2")})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range zero {
		if s.Sign() != 0 {
			t.Fatalf("share %d of nothing = %s", i, s.RatString())
		}
	}
	none, err := ApportionWeights("7.000000", []*big.Rat{rat("0"), rat("0")})
	if err != nil {
		t.Fatal(err)
	}
	if none[0].Cmp(rat("7")) != 0 || none[1].Sign() != 0 {
		t.Fatalf("unsplittable total = %s / %s, want all on the first group", none[0].RatString(), none[1].RatString())
	}
	if empty, err := ApportionWeights("7.000000", nil); err != nil || len(empty) != 0 {
		t.Fatalf("no groups = %v %v", empty, err)
	}
}

func TestCostCentreTotalsAddUpTheColumns(t *testing.T) {
	lines, err := CostCentreBreakdown(weights("a", "1", "b", "2"), "30.000000", "3.000000", "1.500000")
	if err != nil {
		t.Fatal(err)
	}
	usage, list, discount, net, tax, total, err := CostCentreTotals(lines)
	if err != nil {
		t.Fatal(err)
	}
	if usage != "3.000000" || list != "33.000000" || discount != "3.000000" || net != "30.000000" || tax != "1.500000" || total != "31.500000" {
		t.Fatalf("totals = %s %s %s %s %s %s", usage, list, discount, net, tax, total)
	}
}
