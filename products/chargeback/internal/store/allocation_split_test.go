package store

import (
	"math"
	"math/big"
	"testing"
)

// The split arithmetic, pinned without a database. Each case is chosen so a
// plausible mutant fails it: swapped weights, a forgotten renormalisation,
// a rounding residual left on the floor.

func basisRows() []AllocationRow {
	return []AllocationRow{
		{CustomerID: "a", CustomerSlug: "acme", Tier: "organization", VCPUHours: "100", MemGiBHours: "200", PVCGBHours: "0"},
		{CustomerID: "b", CustomerSlug: "bravo", Tier: "organization", VCPUHours: "300", MemGiBHours: "0", PVCGBHours: "100"},
		{CustomerID: "s", CustomerSlug: "sov", Tier: "platform-overhead", VCPUHours: "100", MemGiBHours: "100", PVCGBHours: "100"},
	}
}

func rowByID(rows []AllocationRow, id string) *AllocationRow {
	for i := range rows {
		if rows[i].CustomerID == id {
			return &rows[i]
		}
	}
	return nil
}

func sumAllocated(rows []AllocationRow) *big.Rat {
	t := new(big.Rat)
	for _, r := range rows {
		t.Add(t, ratOf(r.AllocatedCost))
	}
	return t
}

func TestSplitEqualWeightsSeparateKeepsOverheadAndSumsToOne(t *testing.T) {
	rows, total := splitAllocation(basisRows(), AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadSeparate, big.NewRat(900, 1))
	if len(rows) != 3 {
		t.Fatalf("separate must keep the overhead row: %d rows", len(rows))
	}
	// Weights 300 / 400 / 300 of 1000 → 0.3 / 0.4 / 0.3; pool 900 → 270 / 360 / 270.
	if a := rowByID(rows, "a"); a.Weight != "300.000000" || math.Abs(a.Share-0.3) > 1e-12 || a.AllocatedCost != "270.000000" {
		t.Fatalf("a = %+v", *a)
	}
	if b := rowByID(rows, "b"); b.Weight != "400.000000" || math.Abs(b.Share-0.4) > 1e-12 || b.AllocatedCost != "360.000000" {
		t.Fatalf("b = %+v", *b)
	}
	if s := rowByID(rows, "s"); s.AllocatedCost != "270.000000" {
		t.Fatalf("overhead = %+v", *s)
	}
	if math.Abs(total-1) > 1e-12 {
		t.Fatalf("share_total = %v", total)
	}
	if sumAllocated(rows).Cmp(big.NewRat(900, 1)) != 0 {
		t.Fatalf("allocated %s ≠ pool 900", decOf(sumAllocated(rows)))
	}
}

// Weights change the shares. With only vCPU counting, b (300 vCPU-h) must
// outweigh a (100 vCPU-h) three to one — a mutant that ignores the weights
// (or applies them to the wrong meter) still reports 0.3 / 0.4.
func TestSplitWeightsChangeTheShares(t *testing.T) {
	rows, _ := splitAllocation(basisRows(), AllocationWeights{VCPU: 1, MemGiB: 0, PVCGB: 0}, OverheadSeparate, big.NewRat(1000, 1))
	a, b, s := rowByID(rows, "a"), rowByID(rows, "b"), rowByID(rows, "s")
	if math.Abs(a.Share-0.2) > 1e-12 || math.Abs(b.Share-0.6) > 1e-12 || math.Abs(s.Share-0.2) > 1e-12 {
		t.Fatalf("vcpu-only shares = %v / %v / %v, want 0.2 / 0.6 / 0.2", a.Share, b.Share, s.Share)
	}
	if a.AllocatedCost != "200.000000" || b.AllocatedCost != "600.000000" {
		t.Fatalf("vcpu-only costs = %s / %s", a.AllocatedCost, b.AllocatedCost)
	}
	// PVC only: a has none, so a earns nothing and b/s split 1:1.
	rows, _ = splitAllocation(basisRows(), AllocationWeights{PVCGB: 2}, OverheadSeparate, big.NewRat(1000, 1))
	if a := rowByID(rows, "a"); a.Share != 0 || a.AllocatedCost != "0.000000" {
		t.Fatalf("pvc-only a = %+v", *a)
	}
	if b := rowByID(rows, "b"); math.Abs(b.Share-0.5) > 1e-12 {
		t.Fatalf("pvc-only b share = %v", b.Share)
	}
	// A fractional weight is exact: 0.1 × 300 = 30, not 30.000000000000004.
	rows, _ = splitAllocation(basisRows(), AllocationWeights{VCPU: 0.1, MemGiB: 0.1, PVCGB: 0.1}, OverheadSeparate, big.NewRat(900, 1))
	if a := rowByID(rows, "a"); a.Weight != "30.000000" || a.AllocatedCost != "270.000000" {
		t.Fatalf("fractional weights a = %+v", *a)
	}
}

// Distribute drops the overhead row and spreads its share over the
// Organizations by weight, so the remaining shares still sum to 1 and the
// rows still sum to the pool. A mutant that removes the row but forgets to
// renormalise leaves 0.3 + 0.4 = 0.7 on the table and fails here.
func TestSplitDistributeRemovesOverheadAndRenormalises(t *testing.T) {
	rows, total := splitAllocation(basisRows(), AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadDistribute, big.NewRat(900, 1))
	if len(rows) != 2 || rowByID(rows, "s") != nil {
		t.Fatalf("distribute must remove the overhead row: %+v", rows)
	}
	if math.Abs(total-1) > 1e-12 {
		t.Fatalf("share_total after distribute = %v, want 1 (renormalisation forgotten)", total)
	}
	// 300 : 400 → 3/7 : 4/7 of 900 = 385.714286 : 514.285714, rows sum to 900.
	a, b := rowByID(rows, "a"), rowByID(rows, "b")
	if math.Abs(a.Share-3.0/7) > 1e-12 || math.Abs(b.Share-4.0/7) > 1e-12 {
		t.Fatalf("distributed shares = %v / %v", a.Share, b.Share)
	}
	if sumAllocated(rows).Cmp(big.NewRat(900, 1)) != 0 {
		t.Fatalf("allocated %s ≠ pool 900 after distribute", decOf(sumAllocated(rows)))
	}
	// Under separate the same rows carried 270 + 360 = 630; distribute must
	// hand the overhead's 270 to them — every organization row grows.
	sep, _ := splitAllocation(basisRows(), AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadSeparate, big.NewRat(900, 1))
	for _, id := range []string{"a", "b"} {
		if ratOf(rowByID(rows, id).AllocatedCost).Cmp(ratOf(rowByID(sep, id).AllocatedCost)) <= 0 {
			t.Fatalf("%s did not receive its part of the distributed overhead", id)
		}
	}
}

// pool × share rounded per row can lose micro-units; the residual must land
// on a row so Σ rows == pool exactly. 1000 / 3 per row is the textbook case.
func TestSplitRoundingResidualNeverLeaksFromThePool(t *testing.T) {
	rows := []AllocationRow{
		{CustomerID: "a", Tier: "organization", VCPUHours: "1"},
		{CustomerID: "b", Tier: "organization", VCPUHours: "1"},
		{CustomerID: "c", Tier: "organization", VCPUHours: "1"},
	}
	out, total := splitAllocation(rows, AllocationWeights{VCPU: 1}, OverheadSeparate, big.NewRat(1000, 1))
	if math.Abs(total-1) > 1e-12 {
		t.Fatalf("share_total = %v", total)
	}
	if sumAllocated(out).Cmp(big.NewRat(1000, 1)) != 0 {
		t.Fatalf("three thirds of 1000 sum to %s — the rounding residual leaked", decOf(sumAllocated(out)))
	}
	// Each row is within one micro-unit of 333.333333.
	for _, r := range out {
		d := new(big.Rat).Sub(ratOf(r.AllocatedCost), ratOf("333.333333"))
		if d.Abs(d).Cmp(big.NewRat(1, 1000000)) > 0 {
			t.Fatalf("row %s = %s, more than a micro-unit from a third", r.CustomerID, r.AllocatedCost)
		}
	}
}

// Nothing ran: zero shares and zero cost, never an invented even split.
func TestSplitEmptyWindowInventsNothing(t *testing.T) {
	rows := []AllocationRow{{CustomerID: "a", Tier: "organization", VCPUHours: "0", MemGiBHours: "0", PVCGBHours: "0"}}
	out, total := splitAllocation(rows, AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadSeparate, big.NewRat(500, 1))
	if total != 0 || out[0].Share != 0 || out[0].AllocatedCost != "0.000000" {
		t.Fatalf("zero basis produced %+v (share_total %v)", out[0], total)
	}
	if out, total := splitAllocation(nil, AllocationWeights{VCPU: 1}, OverheadDistribute, big.NewRat(500, 1)); len(out) != 0 || total != 0 {
		t.Fatalf("no rows produced %+v", out)
	}
}

func TestAllocationSettingsValidation(t *testing.T) {
	good := AllocationSettings{Weights: AllocationWeights{VCPU: 1, MemGiB: 1, PVCGB: 1}, OverheadPolicy: "separate", Pool: "sovereign-cost", ManualAmount: "0", Currency: "OMR"}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid settings rejected: %v", err)
	}
	cases := map[string]func(a *AllocationSettings){
		"negative weight":  func(a *AllocationSettings) { a.Weights.VCPU = -1 },
		"NaN weight":       func(a *AllocationSettings) { a.Weights.MemGiB = math.NaN() },
		"all-zero weights": func(a *AllocationSettings) { a.Weights = AllocationWeights{} },
		"bad policy":       func(a *AllocationSettings) { a.OverheadPolicy = "spread" },
		"bad pool":         func(a *AllocationSettings) { a.Pool = "guess" },
		"negative amount":  func(a *AllocationSettings) { a.ManualAmount = "-5" },
		"non-numeric":      func(a *AllocationSettings) { a.ManualAmount = "lots" },
		"bad currency":     func(a *AllocationSettings) { a.Currency = "Rial" },
	}
	for name, mutate := range cases {
		a := good
		mutate(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Normalize upper-cases the currency and empties a blank customer id.
	blank := ""
	a := good
	a.Currency, a.SovereignCustomerID, a.ManualAmount = " omr ", &blank, ""
	a.Normalize()
	if a.Currency != "OMR" || a.SovereignCustomerID != nil || a.ManualAmount != "0" {
		t.Fatalf("normalised = %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("normalised settings rejected: %v", err)
	}
}
