package rating

import (
	"math/big"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// CAPPED OR GROW (DESIGN.md §22.11): the customer's mode, not the cell,
// decides what happens above a quantity allowance; grow bills the compute
// above the headline from the LIMIT meters at the package's own rates;
// capped bills none of it; the spend limit caps the usage above the package.

func d(s string) *store.Decimal { v := store.Decimal(s); return &v }

func mLimits() map[string]store.PackageLimits {
	return map[string]store.PackageLimits{
		"m":  {PlanSlug: "m", Headline: store.GrowCeiling{VCPU: d("2"), MemoryGB: d("4")}, GrowAllowed: true, OverageVCPUMonth: d("1.796"), OverageMemGBMonth: d("0.337")},
		"s":  {PlanSlug: "s", Headline: store.GrowCeiling{VCPU: d("1"), MemoryGB: d("2")}, GrowAllowed: true, OverageVCPUMonth: d("1.992"), OverageMemGBMonth: d("0.374")},
		"xl": {PlanSlug: "xl", Headline: store.GrowCeiling{VCPU: d("8"), MemoryGB: d("16")}},
	}
}

func TestModeOverridesTheCellOverage(t *testing.T) {
	items := showcaseItems()
	ents := map[string][]store.Entitlement{"s": {qcell("s", bw, "50", store.OverageMetered)}}
	seg := []PlanSegment{{Slug: "s", Hours: hours(744)}}
	for _, c := range []struct {
		mode   string
		capped bool
	}{{"", false}, {store.OverageModeCapped, true}, {store.OverageModeGrow, false}} {
		res, err := ApplyPackage(store.CostSource{ID: "src", OverageMode: c.mode}, seg, ents, items)
		if err != nil {
			t.Fatal(err)
		}
		if res.Capped["eip.bandwidth_mbps"] != c.capped || string(res.Included["eip.bandwidth_mbps"]) != "37200.000000" {
			t.Fatalf("mode %q on a metered cell: capped %v included %v, want capped=%v and 37,200 allowed", c.mode, res.Capped, res.Included, c.capped)
		}
	}
	// A hard-capped cell meters in grow; unlimited stays unlimited in both.
	ents = map[string][]store.Entitlement{"s": {qcell("s", bw, "50", store.OverageHardCap)}}
	res, _ := ApplyPackage(store.CostSource{ID: "src", OverageMode: store.OverageModeGrow}, seg, ents, items)
	if res.Capped["eip.bandwidth_mbps"] {
		t.Fatal("grow left a hard-capped cell capped")
	}
	ents = map[string][]store.Entitlement{"s": {qcell("s", bw, "", store.OverageUnlimited)}}
	for _, mode := range []string{store.OverageModeCapped, store.OverageModeGrow} {
		res, _ := ApplyPackage(store.CostSource{ID: "src", OverageMode: mode}, seg, ents, items)
		if !res.Unlimited["eip.bandwidth_mbps"] || res.Capped["eip.bandwidth_mbps"] {
			t.Fatalf("%s turned an unlimited cell into %v / %v", mode, res.Unlimited, res.Capped)
		}
	}
}

func TestGrowOnlyCellIsNeverAnAddon(t *testing.T) {
	c := lcell("s", dr, store.EntitlementOptional, 0)
	c.GrowOnly = true
	res, err := ApplyPackage(store.CostSource{ID: "src", Addons: []string{"dr"}, OverageMode: store.OverageModeGrow}, []PlanSegment{{Slug: "s", Hours: hours(744)}}, map[string][]store.Entitlement{"s": {c}}, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 0 {
		t.Fatalf("a grow-only cell billed %v", res.Lines)
	}
}

// M in grow for a 744-hour month, one vCPU and 2 GB of limits above the
// headline all month: 744 vcpu-hours × 0.00246027 (1.796 × 12 ÷ 8760) =
// 1.830441 and 1,488 gib-hours × 0.00046164 (0.337 × 12 ÷ 8760) = 0.686920.
func TestGrowOverageBillsTheLimitsAboveTheHeadlineAtThePackageRate(t *testing.T) {
	src := store.CostSource{ID: "src", OverageMode: store.OverageModeGrow}
	in := GrowInput{Limits: map[string]*big.Rat{store.SKUVCPULimit: hours(3 * 744), store.SKUMemLimit: hours(6 * 744)}, Packages: mLimits(), Divisor: 8760, Currency: "OMR"}
	lines, unpriced, err := GrowOverage(src, []PlanSegment{{Slug: "m", Hours: hours(744)}}, in)
	if err != nil || len(unpriced) != 0 {
		t.Fatalf("err %v unpriced %v", err, unpriced)
	}
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	want := [][4]string{{store.SKUVCPU, "744.000000", "0.00246027", "1.830441"}, {store.SKUMem, "1488.000000", "0.00046164", "0.686920"}}
	for i, w := range want {
		l := lines[i]
		if l.SKU != w[0] || string(l.Quantity) != w[1] || string(l.UnitPrice) != w[2] || string(l.Amount) != w[3] || *l.SourceID != "src" {
			t.Fatalf("line %d = %+v, want %v", i, l, w)
		}
	}
	if !strings.Contains(lines[0].Description, "2232 vcpu-hour used, 1488 included (2 vCPU × 744 h on M)") {
		t.Fatalf("description = %q", lines[0].Description)
	}
	// Within the headline: nothing.
	in.Limits = map[string]*big.Rat{store.SKUVCPULimit: hours(2 * 744), store.SKUMemLimit: hours(3 * 744)}
	if lines, _, _ := GrowOverage(src, []PlanSegment{{Slug: "m", Hours: hours(744)}}, in); len(lines) != 0 {
		t.Fatalf("within the headline billed %+v", lines)
	}
}

func TestCappedBillsNoCompute(t *testing.T) {
	in := GrowInput{Limits: map[string]*big.Rat{store.SKUVCPULimit: hours(10 * 744)}, Packages: mLimits(), Divisor: 8760}
	for _, mode := range []string{"", store.OverageModeCapped} {
		if lines, _, _ := GrowOverage(store.CostSource{ID: "src", OverageMode: mode}, []PlanSegment{{Slug: "m", Hours: hours(744)}}, in); len(lines) != 0 {
			t.Fatalf("mode %q billed compute %+v", mode, lines)
		}
	}
}

// A plan change: S for 244 h then M for 500 h. The allowance adds each
// segment's headline × hours (1 × 244 + 2 × 500 = 1,244); the excess rates
// at the package the Source spent most of the period on (M, first).
func TestGrowOverageAcrossAPlanChange(t *testing.T) {
	segs := []PlanSegment{{Slug: "m", Hours: hours(500)}, {Slug: "s", Hours: hours(244)}}
	in := GrowInput{Limits: map[string]*big.Rat{store.SKUVCPULimit: hours(1744)}, Packages: mLimits(), Divisor: 8760, Currency: "OMR"}
	lines, _, err := GrowOverage(store.CostSource{ID: "src", OverageMode: store.OverageModeGrow}, segs, in)
	if err != nil || len(lines) != 1 || string(lines[0].Quantity) != "500.000000" || string(lines[0].UnitPrice) != "0.00246027" {
		t.Fatalf("lines %+v err %v, want 500 vcpu-hours at M's rate", lines, err)
	}
}

// A package that allows no grow rates reports the SKU unpriced rather than
// billing it at nothing.
func TestGrowOverageWithoutARateIsUnpriced(t *testing.T) {
	in := GrowInput{Limits: map[string]*big.Rat{store.SKUVCPULimit: hours(20 * 744)}, Packages: mLimits(), Divisor: 8760}
	lines, unpriced, err := GrowOverage(store.CostSource{ID: "src", OverageMode: store.OverageModeGrow}, []PlanSegment{{Slug: "xl", Hours: hours(744)}}, in)
	if err != nil || len(lines) != 0 || len(unpriced) != 1 || unpriced[0] != store.SKUVCPU {
		t.Fatalf("lines %+v unpriced %v err %v", lines, unpriced, err)
	}
}

func TestSpendLimitCapsTheUsageAboveThePackageOnly(t *testing.T) {
	sid, other := "src", "cloud"
	lines := []store.RatedLine{
		{SourceID: &sid, SKU: "plan.m", Amount: "4.576106"},
		{SourceID: &sid, SKU: store.SKUVCPU, Amount: "1.830441"},
		{SourceID: &sid, SKU: store.SKUMem, Amount: "0.686920"},
		{SourceID: &sid, SKU: store.SKUPVC, Amount: "0.718555"},
		{SourceID: &other, SKU: store.SKUBandwidth, Amount: "9.000000"},
	}
	src := store.CostSource{ID: sid, OverageMode: store.OverageModeGrow, SpendLimitMonth: d("3.000")}
	l, ok, err := SpendLimitLine(src, lines, "OMR")
	if err != nil || !ok || string(l.Amount) != "-0.235916" || l.SKU != SpendLimitSKU || *l.SourceID != sid {
		t.Fatalf("line %+v ok %v err %v, want −0.235916 (3.235916 − 3)", l, ok, err)
	}
	for _, s := range []store.CostSource{
		{ID: sid, OverageMode: store.OverageModeGrow, SpendLimitMonth: d("5")},
		{ID: sid, OverageMode: store.OverageModeGrow},
		{ID: sid, OverageMode: store.OverageModeCapped, SpendLimitMonth: d("1")},
	} {
		if _, ok, _ := SpendLimitLine(s, lines, "OMR"); ok {
			t.Fatalf("%+v wrote a spend-limit line", s)
		}
	}
}
