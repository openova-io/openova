package rating

import (
	"math/big"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The package matrix on the bill (DESIGN.md §22). Every figure is PINNED:
// the add-on priced for the plan-hours the package ran, the included feature
// as a 0.000 line and never an add-on on top, and the included bandwidth as
// an allowance through the one allowance path — 50 Mbps included on S with
// 70 Mbps used leaves 20 Mbps-hours priced for every hour.

var (
	backup = store.Feature{ID: "f-backup", Key: "backup", Name: "Backup", Kind: store.FeatureKindBoolean, AddonSKU: "addon.backup", SortOrder: 4}
	ssl    = store.Feature{ID: "f-ssl", Key: "ssl", Name: "Unlimited free SSL", Kind: store.FeatureKindBoolean, SortOrder: 6}
	bw     = store.Feature{ID: "f-bw", Key: "bandwidth", Name: "Bandwidth", Kind: store.FeatureKindQuantity, Unit: "Mbps", AddonSKU: "eip.bandwidth_mbps", SortOrder: 15}
)

// showcaseItems is the plans book as the seeder prices it: the plans, the
// backup add-on at 1.500 OMR/month ÷ 730, and bandwidth per Mbps-hour.
func showcaseItems() map[string]store.PriceItem {
	return map[string]store.PriceItem{
		"plan.s":             {SKU: "plan.s", Unit: store.PlanUnit, UnitPrice: "0.00684932"},
		"plan.xl":            {SKU: "plan.xl", Unit: store.PlanUnit, UnitPrice: "0.04109589"},
		"addon.backup":       {SKU: "addon.backup", Unit: store.PlanUnit, UnitPrice: "0.00205479"},
		"eip.bandwidth_mbps": {SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.01716667"},
	}
}

func cell(plan string, f store.Feature, state string, qty string) store.Entitlement {
	e := store.Entitlement{PlanSKU: store.PlanSKU(plan), FeatureID: f.ID, State: state, Feature: f}
	if qty != "" {
		e.IncludedQuantity = dc(qty)
	}
	return e
}

func hours(h int64) *big.Rat { return big.NewRat(h, 1) }

func TestPlanSegmentsReadTheSourcesPlanRows(t *testing.T) {
	rows := []store.RatableUsage{
		{SourceID: "s1", SKU: "k8s.vcpu", Quantity: "1000"},
		{SourceID: "s1", SKU: "plan.s", Quantity: "240"},
		{SourceID: "s1", SKU: "plan.m", Quantity: "504"},
		{SourceID: "s1", SKU: "plan.flexi", Quantity: "10"},
	}
	segs, err := PlanSegments(rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 || segs[0].Slug != "m" || segs[0].Hours.Cmp(hours(504)) != 0 || segs[1].Slug != "s" || segs[1].Hours.Cmp(hours(240)) != 0 {
		t.Fatalf("segments = %+v, want m (504 h) then s (240 h); flexi and the meters are not plans", segs)
	}
	if segs, _ := PlanSegments([]store.RatableUsage{{SKU: "ecs.s7n.small.1", Quantity: "744"}}); len(segs) != 0 {
		t.Fatalf("a cloud source has no plan segment, got %+v", segs)
	}
}

// S with backup TAKEN: the add-on line is priced for the plan-hours the S
// package ran, at the add-on's price, named after the plan.
func TestOptionalAddonTakenOnSIsPriced(t *testing.T) {
	src := store.CostSource{ID: "src-1", Addons: []string{"backup"}}
	ents := map[string][]store.Entitlement{"s": {cell("s", backup, store.EntitlementOptional, ""), cell("s", ssl, store.EntitlementIncluded, "")}}
	res, err := ApplyPackage(src, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("lines = %+v, want the SSL included line and the backup add-on line", res.Lines)
	}
	var addon, included *store.RatedLine
	for i := range res.Lines {
		switch res.Lines[i].SKU {
		case "addon.backup":
			addon = &res.Lines[i]
		case "plan.s.ssl":
			included = &res.Lines[i]
		}
	}
	if addon == nil || included == nil {
		t.Fatalf("lines = %+v", res.Lines)
	}
	// 744 plan-hours × 0.00205479 = 1.528764 — the add-on billed like the plan.
	if string(addon.Quantity) != "744.000000" || addon.Unit != store.PlanUnit || string(addon.UnitPrice) != "0.00205479" || string(addon.Amount) != "1.528764" {
		t.Fatalf("add-on line = %+v, want 744 plan-hour × 0.00205479 = 1.528764", *addon)
	}
	if addon.Description != "Backup — add-on to S plan" || addon.SourceID == nil || *addon.SourceID != "src-1" {
		t.Fatalf("add-on line = %+v", *addon)
	}
	if string(included.Amount) != "0.000000" || string(included.UnitPrice) != "0.00000000" || string(included.Quantity) != "1" || included.Unit != IncludedUnit || included.Description != "Unlimited free SSL — included in S plan" {
		t.Fatalf("included line = %+v, want a 0.000 line naming the feature and the plan", *included)
	}
	if len(res.Unpriced) != 0 || len(res.Included) != 0 {
		t.Fatalf("unpriced %v included %v, want none", res.Unpriced, res.Included)
	}
}

// XL includes backup: the bill carries the 0.000 included line and the
// add-on the Organization still has recorded is NOT billed on top.
func TestIncludedOnXLRendersZeroLineAndNoAddon(t *testing.T) {
	src := store.CostSource{ID: "src-1", Addons: []string{"backup"}}
	ents := map[string][]store.Entitlement{"xl": {cell("xl", backup, store.EntitlementIncluded, ""), cell("xl", ssl, store.EntitlementIncluded, "")}}
	res, err := ApplyPackage(src, []PlanSegment{{Slug: "xl", Hours: hours(744)}}, ents, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	skus := map[string]store.RatedLine{}
	for _, l := range res.Lines {
		skus[l.SKU] = l
	}
	if _, billed := skus["addon.backup"]; billed {
		t.Fatalf("backup is included in XL and must not be billed as an add-on: %+v", res.Lines)
	}
	l, ok := skus["plan.xl.backup"]
	if !ok || string(l.Amount) != "0.000000" || l.Description != "Backup — included in XL plan" {
		t.Fatalf("included backup line = %+v (ok=%v)", l, ok)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("lines = %+v, want exactly one 0.000 line per included feature", res.Lines)
	}
}

// 50 Mbps included on S, 70 Mbps used: 20 Mbps-hours are priced for every
// hour — the included quantity is an ALLOWANCE on the bandwidth SKU, applied
// by the same ApplyTerms that applies a plan's or a contract's allowance.
func TestIncludedBandwidthIsAnAllowanceOnTheMeter(t *testing.T) {
	src := store.CostSource{ID: "src-1"}
	items := showcaseItems()
	ents := map[string][]store.Entitlement{"s": {cell("s", bw, store.EntitlementIncluded, "50")}}

	// One plan-hour: 50 mbps-hours included.
	res, err := ApplyPackage(src, []PlanSegment{{Slug: "s", Hours: hours(1)}}, ents, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 0 {
		t.Fatalf("a quantity feature renders no line of its own, got %+v", res.Lines)
	}
	if got := res.Included["eip.bandwidth_mbps"]; string(got) != "50.000000" {
		t.Fatalf("included allowance = %q, want 50.000000 mbps-hour for one plan-hour", got)
	}
	usage := []store.RatedLine{{SourceID: &src.ID, SKU: "eip.bandwidth_mbps", Quantity: "70", Unit: "mbps-hour", UnitPrice: "0.01716667", Amount: "1.201667"}}
	out, applied, err := ApplyTerms(usage, items, Terms{Included: res.Included})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || string(applied[0].Allowance) != "50.000000" || string(applied[0].AllowanceUsed) != "50.000000" || string(applied[0].Excess) != "20.000000" {
		t.Fatalf("breakdown = %+v, want 50 included, 50 used, 20 in excess", applied)
	}
	// 20 mbps-hours × 0.01716667 = 0.343333
	if string(out[0].Amount) != "0.343333" {
		t.Fatalf("bandwidth rated %s, want 0.343333 = 20 × 0.01716667", out[0].Amount)
	}

	// A whole month on S: 50 Mbps for 744 h = 37,200 mbps-hours included.
	res, err = ApplyPackage(src, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, items)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Included["eip.bandwidth_mbps"]; string(got) != "37200.000000" {
		t.Fatalf("month allowance = %q, want 37200.000000", got)
	}
	// 70 Mbps always on = 52,080 mbps-hours; 14,880 (20 Mbps × 744 h) are priced.
	usage[0].Quantity = "52080"
	out, applied, err = ApplyTerms(usage, items, Terms{Included: res.Included})
	if err != nil {
		t.Fatal(err)
	}
	if string(applied[0].Excess) != "14880.000000" || string(out[0].Amount) != "255.440050" {
		t.Fatalf("month: excess %s rated %s, want 14880 and 255.440050", applied[0].Excess, out[0].Amount)
	}
}

// The package's allowance ADDS to the item's and the contract's, exactly as a
// contract allowance adds to the plan's: one allowance path, three origins.
func TestPackageAllowanceAddsToPlanAndContractAllowances(t *testing.T) {
	item := store.PriceItem{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.01", Allowance: dc("10")}
	contract := &store.Contract{Items: []store.ContractItem{{Kind: store.ContractItemAllowance, SKU: "eip.bandwidth_mbps", Quantity: "20"}}}
	usage := []store.RatedLine{{SKU: "eip.bandwidth_mbps", Quantity: "100", Unit: "mbps-hour", UnitPrice: "0.01", Amount: "1.000000"}}
	_, applied, err := ApplyTerms(usage, map[string]store.PriceItem{item.SKU: item}, Terms{Contract: contract, Included: map[string]store.Decimal{"eip.bandwidth_mbps": "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(applied[0].Allowance) != "60.000000" || string(applied[0].Amount) != "0.400000" {
		t.Fatalf("allowance %s amount %s, want 60 (10 + 20 + 30) and 0.40", applied[0].Allowance, applied[0].Amount)
	}
}

// A plan change inside the period: the add-on is billed for the hours of
// every package that offered it, the included feature named once after the
// longest package, and the bandwidth allowance follows each package's rate.
func TestPlanChangeInsideThePeriod(t *testing.T) {
	src := store.CostSource{ID: "src-1", Addons: []string{"backup"}}
	ents := map[string][]store.Entitlement{
		"s": {cell("s", backup, store.EntitlementOptional, ""), cell("s", ssl, store.EntitlementIncluded, ""), cell("s", bw, store.EntitlementIncluded, "50")},
		"m": {cell("m", backup, store.EntitlementOptional, ""), cell("m", ssl, store.EntitlementIncluded, ""), cell("m", bw, store.EntitlementIncluded, "100")},
	}
	res, err := ApplyPackage(src, []PlanSegment{{Slug: "m", Hours: hours(500)}, {Slug: "s", Hours: hours(244)}}, ents, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	var ssls, addons int
	for _, l := range res.Lines {
		switch l.SKU {
		case "plan.m.ssl":
			ssls++
		case "plan.s.ssl":
			t.Fatalf("SSL named after S, the shorter package: %+v", res.Lines)
		case "addon.backup":
			addons++
			if string(l.Quantity) != "744.000000" || l.Description != "Backup — add-on to M plan" {
				t.Fatalf("add-on = %+v, want 744 plan-hours (500 on M + 244 on S) named after M", l)
			}
		}
	}
	if ssls != 1 || addons != 1 {
		t.Fatalf("lines = %+v, want one SSL line and one add-on line", res.Lines)
	}
	// 100 × 500 + 50 × 244 = 62,200 mbps-hours included.
	if got := res.Included["eip.bandwidth_mbps"]; string(got) != "62200.000000" {
		t.Fatalf("bandwidth allowance = %q, want 62200.000000", got)
	}
}

// An add-on whose SKU the book does not price is reported, never silently
// billed at nothing; a feature the Source did not take is not billed; a
// not-offered cell produces nothing.
func TestAddonEdges(t *testing.T) {
	items := showcaseItems()
	delete(items, "addon.backup")
	src := store.CostSource{ID: "src-1", Addons: []string{"backup", "dedicated_ip"}}
	dedicated := store.Feature{ID: "f-ip", Key: "dedicated_ip", Name: "Dedicated IP address", Kind: store.FeatureKindBoolean, AddonSKU: "addon.dedicated_ip"}
	ents := map[string][]store.Entitlement{"s": {
		cell("s", backup, store.EntitlementOptional, ""),
		cell("s", dedicated, store.EntitlementNotOffered, ""),
	}}
	res, err := ApplyPackage(src, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 0 || strings.Join(res.Unpriced, ",") != "addon.backup" {
		t.Fatalf("lines %+v unpriced %v, want no line and addon.backup reported unpriced", res.Lines, res.Unpriced)
	}
	// Not taken: optional produces nothing.
	res, err = ApplyPackage(store.CostSource{ID: "src-2"}, []PlanSegment{{Slug: "s", Hours: hours(744)}}, map[string][]store.Entitlement{"s": {cell("s", backup, store.EntitlementOptional, "")}}, showcaseItems())
	if err != nil || len(res.Lines) != 0 || len(res.Unpriced) != 0 {
		t.Fatalf("an optional feature not taken bills nothing: %+v %v %v", res.Lines, res.Unpriced, err)
	}
	// No plan at all: nothing.
	res, err = ApplyPackage(src, nil, nil, items)
	if err != nil || len(res.Lines) != 0 {
		t.Fatalf("no plan, no package: %+v %v", res.Lines, err)
	}
}

// The published price per month is one unit for 730 h rounded ONCE at the
// currency's minor unit — 1.500 for the backup add-on, 5.000 for S.
func TestMonthlyAtRoundsOnce(t *testing.T) {
	for _, tc := range []struct{ unit, want string }{
		{"0.00205479", "1.500"},
		{"0.00684932", "5.000"},
		{"0.04109589", "30.000"},
		{"0.01716667", "12.532"},
	} {
		got, err := MonthlyAt(store.Decimal(tc.unit), 3)
		if err != nil || string(got) != tc.want {
			t.Fatalf("MonthlyAt(%s) = %s %v, want %s", tc.unit, got, err, tc.want)
		}
	}
	if got, _ := MonthlyAt("0.01716667", 2); string(got) != "12.53" {
		t.Fatalf("two decimals = %s", got)
	}
}
