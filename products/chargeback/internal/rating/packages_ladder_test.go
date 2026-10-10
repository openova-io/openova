package rating

import (
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// THE LADDER (DESIGN.md §22.2, 0.1.61). A HARD CAP applies the allowance,
// reports the excess and bills nothing above it; UNLIMITED carries no
// allowance and bills nothing; a LEVEL bills nothing for the level a package
// is at; its purchasable NEXT LEVEL, taken, is an add-on at 1.500 a month for
// the plan-hours; an ACCESS door bills nothing; and the included 0.000 lines
// can be turned off without touching any of that.

var (
	dr      = store.Feature{ID: "f-dr", Key: "dr", Name: "DR topology", Kind: store.FeatureKindLevel, AddonSKU: "addon.dr", Levels: []string{"single region", "active-passive"}}
	backups = store.Feature{ID: "f-bk", Key: "backups", Name: "Backups", Kind: store.FeatureKindLevel, AddonSKU: "addon.backup_daily", Levels: []string{"weekly · 7 days", "daily · 14 days", "daily · 30 days"}}
	gitea   = store.Feature{ID: "f-git", Key: "gitea_iac", Name: "Gitea + IaC", Kind: store.FeatureKindAccess}
)

func qcell(plan string, f store.Feature, qty, overage string) store.Entitlement {
	e := cell(plan, f, store.EntitlementIncluded, qty)
	e.Overage = overage
	return e
}

func lcell(plan string, f store.Feature, state string, level int) store.Entitlement {
	e := cell(plan, f, state, "")
	e.Level = &level
	return e
}

// 50 Mbps included on S with a HARD CAP, 70 Mbps used: the allowance applies,
// the 20 Mbps-hours above it are reported as the excess — and billed at 0.
func TestHardCapBillsNothingAboveTheAllowanceAndReportsTheExcess(t *testing.T) {
	items := showcaseItems()
	ents := map[string][]store.Entitlement{"s": {qcell("s", bw, "50", store.OverageHardCap)}}
	res, err := ApplyPackage(store.CostSource{ID: "src-1"}, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, items)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Capped["eip.bandwidth_mbps"] || string(res.Included["eip.bandwidth_mbps"]) != "37200.000000" {
		t.Fatalf("capped %v included %v, want the SKU capped with 37,200 mbps-hours allowed", res.Capped, res.Included)
	}
	sid := "src-1"
	usage := []store.RatedLine{{SourceID: &sid, SKU: "eip.bandwidth_mbps", Quantity: "52080", Unit: "mbps-hour", UnitPrice: "0.01716667", Amount: "894.040050"}}
	out, applied, err := ApplyTerms(usage, items, Terms{Included: res.Included, Capped: res.Capped})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !applied[0].Capped || string(applied[0].Allowance) != "37200.000000" || string(applied[0].AllowanceUsed) != "37200.000000" || string(applied[0].Excess) != "14880.000000" {
		t.Fatalf("breakdown = %+v, want capped, 37,200 allowed and used, 14,880 in excess", applied)
	}
	if string(applied[0].Amount) != "0.000000" || string(out[0].Amount) != "0.000000" || string(out[0].UnitPrice) != "0.00000000" {
		t.Fatalf("a hard cap bills nothing: breakdown %s line %s at %s", applied[0].Amount, out[0].Amount, out[0].UnitPrice)
	}
	// Under the cap: nothing in excess, nothing billed, still flagged.
	usage[0].Quantity = "1000"
	_, applied, err = ApplyTerms(usage, items, Terms{Included: res.Included, Capped: res.Capped})
	if err != nil || !applied[0].Capped || string(applied[0].Excess) != "0.000000" || string(applied[0].Amount) != "0.000000" {
		t.Fatalf("under the cap = %+v %v", applied, err)
	}
}

// UNLIMITED on XL: no allowance is computed and the usage bills nothing,
// reported as such. Metered on L, the same usage bills: the policy is per cell.
func TestUnlimitedBillsNothingAndCarriesNoAllowance(t *testing.T) {
	items := showcaseItems()
	ents := map[string][]store.Entitlement{"xl": {qcell("xl", bw, "", store.OverageUnlimited)}}
	res, err := ApplyPackage(store.CostSource{ID: "src-1"}, []PlanSegment{{Slug: "xl", Hours: hours(744)}}, ents, items)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Unlimited["eip.bandwidth_mbps"] || len(res.Included) != 0 || len(res.Lines) != 0 {
		t.Fatalf("unlimited %v included %v lines %v", res.Unlimited, res.Included, res.Lines)
	}
	sid := "src-1"
	usage := []store.RatedLine{{SourceID: &sid, SKU: "eip.bandwidth_mbps", Quantity: "52080", Unit: "mbps-hour", UnitPrice: "0.01716667", Amount: "894.040050"}}
	out, applied, err := ApplyTerms(usage, items, Terms{Unlimited: res.Unlimited})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || !applied[0].Unlimited || string(applied[0].Quantity) != "52080.000000" || string(applied[0].Amount) != "0.000000" || applied[0].Allowance != "" {
		t.Fatalf("breakdown = %+v, want unlimited, the quantity reported, nothing allowed, nothing billed", applied)
	}
	if string(out[0].Amount) != "0.000000" {
		t.Fatalf("unlimited bills nothing, got %s", out[0].Amount)
	}
	ents = map[string][]store.Entitlement{"l": {qcell("l", bw, "250", store.OverageMetered)}}
	res, _ = ApplyPackage(store.CostSource{ID: "src-1"}, []PlanSegment{{Slug: "l", Hours: hours(744)}}, ents, items)
	if res.Capped["eip.bandwidth_mbps"] || res.Unlimited["eip.bandwidth_mbps"] || string(res.Included["eip.bandwidth_mbps"]) != "186000.000000" {
		t.Fatalf("metered on L = capped %v unlimited %v included %v", res.Capped, res.Unlimited, res.Included)
	}
}

// A LEVEL and an ACCESS feature bill nothing — no 0.000 line either: a label
// and a door are not lines. The boolean included feature keeps its line.
func TestLevelAndAccessBillNothing(t *testing.T) {
	ents := map[string][]store.Entitlement{"m": {
		lcell("m", dr, store.EntitlementIncluded, 0),
		lcell("m", backups, store.EntitlementIncluded, 1),
		cell("m", gitea, store.EntitlementIncluded, ""),
		cell("m", ssl, store.EntitlementIncluded, ""),
	}}
	// Even with the level features TAKEN by key: an included level has no
	// next level to sell on this package.
	src := store.CostSource{ID: "src-1", Addons: []string{"dr", "backups"}}
	res, err := ApplyPackage(src, []PlanSegment{{Slug: "m", Hours: hours(744)}}, ents, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0].SKU != "plan.m.ssl" || len(res.Unpriced) != 0 {
		t.Fatalf("lines = %+v unpriced %v, want only the SSL 0.000 line", res.Lines, res.Unpriced)
	}
}

// The NEXT LEVEL of backups on S, purchasable at 1.500 a month and taken:
// an add-on line of 744 plan-hours × 0.00205479 = 1.528764, named after the
// level it raises to. Not taken: nothing. Across a plan change to a package
// already at that level, the S hours only.
func TestNextLevelAddonBillsLikeAnAddon(t *testing.T) {
	items := showcaseItems()
	items["addon.backup_daily"] = store.PriceItem{SKU: "addon.backup_daily", Unit: store.PlanUnit, UnitPrice: "0.00205479"}
	ents := map[string][]store.Entitlement{"s": {lcell("s", backups, store.EntitlementOptional, 0)}}
	res, err := ApplyPackage(store.CostSource{ID: "src-1", Addons: []string{"backups"}}, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 {
		t.Fatalf("lines = %+v, want the next-level add-on line", res.Lines)
	}
	l := res.Lines[0]
	if l.SKU != "addon.backup_daily" || string(l.Quantity) != "744.000000" || l.Unit != store.PlanUnit || string(l.Amount) != "1.528764" || l.Description != "Backups — daily · 14 days, add-on to S plan" {
		t.Fatalf("next-level line = %+v", l)
	}
	res, err = ApplyPackage(store.CostSource{ID: "src-2"}, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, items)
	if err != nil || len(res.Lines) != 0 {
		t.Fatalf("not taken: %+v %v", res.Lines, err)
	}
	ents["m"] = []store.Entitlement{lcell("m", backups, store.EntitlementIncluded, 1)}
	res, err = ApplyPackage(store.CostSource{ID: "src-1", Addons: []string{"backups"}}, []PlanSegment{{Slug: "m", Hours: hours(500)}, {Slug: "s", Hours: hours(244)}}, ents, items)
	if err != nil || len(res.Lines) != 1 || string(res.Lines[0].Quantity) != "244.000000" {
		t.Fatalf("plan change: %+v %v, want 244 plan-hours of the add-on", res.Lines, err)
	}
}

// PACKAGE_INCLUDED_LINES=false: the included 0.000 lines go; the add-on
// line, the allowance, the cap and the included-wins rule stay as they are.
func TestIncludedLinesCanBeTurnedOff(t *testing.T) {
	src := store.CostSource{ID: "src-1", Addons: []string{"backup"}}
	ents := map[string][]store.Entitlement{"s": {
		cell("s", backup, store.EntitlementOptional, ""),
		cell("s", ssl, store.EntitlementIncluded, ""),
		qcell("s", bw, "50", store.OverageHardCap),
	}}
	on, err := ApplyPackageWith(PackageOptions{IncludedLines: true}, src, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	off, err := ApplyPackageWith(PackageOptions{IncludedLines: false}, src, []PlanSegment{{Slug: "s", Hours: hours(744)}}, ents, showcaseItems())
	if err != nil {
		t.Fatal(err)
	}
	if len(on.Lines) != 2 || len(off.Lines) != 1 || off.Lines[0].SKU != "addon.backup" || string(off.Lines[0].Amount) != "1.528764" {
		t.Fatalf("on %+v off %+v", on.Lines, off.Lines)
	}
	if string(off.Included["eip.bandwidth_mbps"]) != "37200.000000" || !off.Capped["eip.bandwidth_mbps"] {
		t.Fatalf("the allowance and the cap are unaffected: %v %v", off.Included, off.Capped)
	}
	ents = map[string][]store.Entitlement{"xl": {cell("xl", backup, store.EntitlementIncluded, "")}}
	off, _ = ApplyPackageWith(PackageOptions{IncludedLines: false}, src, []PlanSegment{{Slug: "xl", Hours: hours(744)}}, ents, showcaseItems())
	if len(off.Lines) != 0 {
		t.Fatalf("included on XL with the lines off: %+v, want nothing", off.Lines)
	}
}
