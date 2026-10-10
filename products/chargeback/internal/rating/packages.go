package rating

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// PACKAGES ON THE BILL (DESIGN.md §22.2). When a platform Source is on a
// plan, the package's entitlement matrix applies to the period, per KIND:
//
//   - a BOOLEAN feature INCLUDED renders a 0.000 line — "Backup — included
//     in XL plan" — so the invoice shows the value the package carries. One
//     line per period per feature, no quantity to speak of (1 period). The
//     operator may turn these lines off (PACKAGE_INCLUDED_LINES=false,
//     PackageOptions.IncludedLines): the features are then on the document
//     and the console only. OPTIONAL and TAKEN (store.CostSource.Addons) is
//     billed at its add-on SKU's price, for the plan-hours the package ran —
//     the add-on is priced per plan-hour exactly as the plan is;
//   - a QUANTITY feature INCLUDED is an ALLOWANCE of the included quantity on
//     the feature's SKU, through the one allowance path the engine has
//     (Terms.Included → shapeFor → step 1 of the order of operations), never
//     a second one. A per-hour SKU (mbps-hour) includes the quantity for
//     every plan-hour the package ran: 50 Mbps on S for 744 h is 37,200
//     mbps-hours. Its OVERAGE POLICY decides what happens above it: METERED
//     bills the excess at the SKU's price; HARD_CAP reports the excess and
//     bills nothing (Terms.Capped — the platform refuses it); UNLIMITED
//     carries no allowance and bills nothing (Terms.Unlimited);
//   - a LEVEL feature bills nothing for the level a package is at (no 0.000
//     line — a label is not a line). Its NEXT LEVEL, when the cell is
//     optional and the Source has taken the feature by key, is an add-on
//     line at the feature's add-on SKU, per plan-hour like any add-on;
//   - an ACCESS feature bills nothing, ever;
//   - NOT OFFERED produces nothing here; taking it was refused at the Source
//     (store.SetSourceAddons). A boolean add-on taken on a package that has
//     since come to include the feature is not billed twice: the included
//     line wins and the add-on line is not written.
//
// A period in which the Source changed plan carries one segment per plan,
// each with its own plan-hours. An included feature is named once, after the
// plan that ran longest; an add-on is billed for the hours of every segment
// that offered it. The order of operations is unchanged: these lines join the
// metered ones BEFORE the terms, discounts, true-up and tax.

// IncludedUnit is the unit of an included-feature line: one per period, like
// a true-up.
const IncludedUnit = "period"

// IncludedSKU is the SKU of the 0.000 line an included feature renders:
// plan.<slug>.<feature key>, so the line files under the plan it belongs to.
func IncludedSKU(planSlug, featureKey string) string {
	return store.PlanSKU(planSlug) + "." + featureKey
}

// PlanSegment is one plan a Source was on during the period and for how many
// plan-hours.
type PlanSegment struct {
	Slug  string
	Hours *big.Rat
}

// PlanSegments reads the plan.<slug> rows of one Source's usage: one segment
// per plan, the longest first. Empty when the Source ran no plan (flexi, or a
// cloud source).
func PlanSegments(rows []store.RatableUsage) ([]PlanSegment, error) {
	var out []PlanSegment
	for _, u := range rows {
		if !strings.HasPrefix(u.SKU, store.PlanSKUPrefix) {
			continue
		}
		slug := store.NormalizePlanSlug(strings.TrimPrefix(u.SKU, store.PlanSKUPrefix))
		if !store.PlanBillable(slug) || strings.Contains(slug, ".") {
			continue
		}
		q, err := parseRat(string(u.Quantity))
		if err != nil {
			return nil, fmt.Errorf("sku %s: %w", u.SKU, err)
		}
		if q.Sign() <= 0 {
			continue
		}
		found := false
		for i := range out {
			if out[i].Slug == slug {
				out[i].Hours.Add(out[i].Hours, q)
				found = true
			}
		}
		if !found {
			out = append(out, PlanSegment{Slug: slug, Hours: q})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Hours.Cmp(out[j].Hours) > 0 })
	return out, nil
}

// PackageOptions is what the operator may vary about how a package reaches
// the bill.
type PackageOptions struct {
	// IncludedLines writes a 0.000 line per boolean feature a package
	// includes (PACKAGE_INCLUDED_LINES, default true).
	IncludedLines bool
}

// PackageResult is what the matrix did for one Source in one period.
type PackageResult struct {
	// Lines are the included 0.000 lines and the priced add-on lines.
	Lines []store.RatedLine
	// Included is the allowance per SKU the package carries into the period,
	// for Terms.Included.
	Included map[string]store.Decimal
	// Capped names the SKUs whose overage is a hard cap: the excess above
	// the allowance is reported and billed at nothing (Terms.Capped).
	Capped map[string]bool
	// Unlimited names the SKUs the package leaves unlimited: no allowance,
	// nothing billed (Terms.Unlimited).
	Unlimited map[string]bool
	// Unpriced names the add-on SKUs the book does not price — an add-on the
	// Organization took that cannot be billed, reported like any unpriced SKU.
	Unpriced []string
}

// ApplyPackage is ApplyPackageWith under the default options (the included
// 0.000 lines on).
func ApplyPackage(src store.CostSource, segments []PlanSegment, ents map[string][]store.Entitlement, items map[string]store.PriceItem) (PackageResult, error) {
	return ApplyPackageWith(PackageOptions{IncludedLines: true}, src, segments, ents, items)
}

// ApplyPackageWith computes the package lines, the included allowances and
// the overage policies of one Source from its plan segments, the matrix
// cells of each plan (ents, keyed by plan slug) and the book's items. It is
// pure: the caller loads the cells.
func ApplyPackageWith(opts PackageOptions, src store.CostSource, segments []PlanSegment, ents map[string][]store.Entitlement, items map[string]store.PriceItem) (PackageResult, error) {
	res := PackageResult{Included: map[string]store.Decimal{}, Capped: map[string]bool{}, Unlimited: map[string]bool{}}
	if len(segments) == 0 {
		return res, nil
	}
	taken := map[string]bool{}
	for _, k := range src.Addons {
		taken[k] = true
	}
	sid := src.ID
	includedLines := map[string]bool{}         // feature key → the package includes it (boolean)
	addonHours := map[string]*big.Rat{}        // add-on SKU → plan-hours billed
	addonFeature := map[string]store.Feature{} // add-on SKU → the feature (for the description)
	addonLevel := map[string]int{}             // add-on SKU → the level the add-on raises to (level features)
	addonPlan := map[string]string{}           // add-on SKU → the plan named on the line (longest segment)
	var addonOrder []string
	allowance := map[string]*big.Rat{}
	var unpriced []string
	seenUnpriced := map[string]bool{}
	addon := func(f store.Feature, seg PlanSegment, level int) {
		if f.AddonSKU == "" {
			return
		}
		if _, ok := items[f.AddonSKU]; !ok {
			if !seenUnpriced[f.AddonSKU] {
				seenUnpriced[f.AddonSKU] = true
				unpriced = append(unpriced, f.AddonSKU)
			}
			return
		}
		if addonHours[f.AddonSKU] == nil {
			addonHours[f.AddonSKU] = new(big.Rat)
			addonFeature[f.AddonSKU] = f
			addonLevel[f.AddonSKU] = level
			addonPlan[f.AddonSKU] = seg.Slug
			addonOrder = append(addonOrder, f.AddonSKU)
		}
		addonHours[f.AddonSKU].Add(addonHours[f.AddonSKU], seg.Hours)
	}
	for _, seg := range segments {
		for _, e := range ents[seg.Slug] {
			f := e.Feature
			switch f.Kind {
			case store.FeatureKindBoolean:
				switch {
				case e.State == store.EntitlementIncluded:
					if includedLines[f.Key] {
						continue
					}
					includedLines[f.Key] = true
					if !opts.IncludedLines {
						continue
					}
					res.Lines = append(res.Lines, store.RatedLine{
						SourceID:    &sid,
						SKU:         IncludedSKU(seg.Slug, f.Key),
						Quantity:    "1",
						Unit:        IncludedUnit,
						UnitPrice:   "0.00000000",
						Amount:      "0.000000",
						Description: fmt.Sprintf("%s — included in %s plan", f.Name, store.PlanName(seg.Slug)),
					})
				case e.State == store.EntitlementOptional && taken[f.Key]:
					addon(f, seg, -1)
				}
			case store.FeatureKindQuantity:
				if e.State != store.EntitlementIncluded || f.AddonSKU == "" {
					continue
				}
				overage := e.Overage
				if overage == "" {
					overage = store.OverageMetered
				}
				if overage == store.OverageUnlimited {
					res.Unlimited[f.AddonSKU] = true
					continue
				}
				if overage == store.OverageHardCap {
					res.Capped[f.AddonSKU] = true
				}
				if e.IncludedQuantity == nil {
					continue
				}
				item, ok := items[f.AddonSKU]
				if !ok {
					// Nothing to allow against: the book does not price the
					// meter, so its usage is unpriced whatever is included.
					continue
				}
				q, err := parseRat(string(*e.IncludedQuantity))
				if err != nil {
					return res, fmt.Errorf("feature %s: included quantity: %w", f.Key, err)
				}
				if strings.HasSuffix(strings.ToLower(item.Unit), "-hour") {
					// A rate (50 Mbps) is included for every plan-hour the
					// package ran; a plain quantity (a GB of storage kept) is
					// included once per period.
					q = new(big.Rat).Mul(q, seg.Hours)
				}
				if allowance[f.AddonSKU] == nil {
					allowance[f.AddonSKU] = new(big.Rat)
				}
				allowance[f.AddonSKU].Add(allowance[f.AddonSKU], q)
			case store.FeatureKindLevel:
				// The level a package is at bills nothing. The next level,
				// purchasable and taken, is an add-on.
				if e.State == store.EntitlementOptional && taken[f.Key] && e.Level != nil {
					addon(f, seg, *e.Level+1)
				}
			}
			// An access feature bills nothing, ever.
		}
	}
	for _, sku := range addonOrder {
		f := addonFeature[sku]
		if f.Kind == store.FeatureKindBoolean && includedLines[f.Key] {
			// The package the Source spent most of the period on includes
			// the feature: it is on the bill at 0.000 already.
			continue
		}
		item := items[sku]
		qty := store.Decimal(roundRat(addonHours[sku], 6))
		amount, err := Amount(qty, item.UnitPrice)
		if err != nil {
			return res, fmt.Errorf("add-on %s: %w", sku, err)
		}
		desc := fmt.Sprintf("%s — add-on to %s plan", f.Name, store.PlanName(addonPlan[sku]))
		if lvl := addonLevel[sku]; f.Kind == store.FeatureKindLevel && lvl >= 0 && lvl < len(f.Levels) {
			desc = fmt.Sprintf("%s — %s, add-on to %s plan", f.Name, f.Levels[lvl], store.PlanName(addonPlan[sku]))
		}
		res.Lines = append(res.Lines, store.RatedLine{
			SourceID:    &sid,
			SKU:         sku,
			Quantity:    qty,
			Unit:        item.Unit,
			UnitPrice:   item.UnitPrice,
			Amount:      amount,
			Description: desc,
		})
	}
	for sku, q := range allowance {
		if q.Sign() > 0 {
			res.Included[sku] = store.Decimal(roundRat(q, 6))
		}
	}
	sort.Strings(unpriced)
	res.Unpriced = unpriced
	return res, nil
}

// MonthlyAt is one unit of an item for HoursPerMonth hours, rounded ONCE to
// the given number of decimals — the published price per month of a package
// or an add-on, at the currency's minor unit.
func MonthlyAt(unitPrice store.Decimal, digits int) (store.Decimal, error) {
	up, err := parseRat(string(unitPrice))
	if err != nil {
		return "", err
	}
	hours, _ := parseRat(HoursPerMonth)
	if digits < 0 {
		digits = 0
	}
	return store.Decimal(roundRat(new(big.Rat).Mul(up, hours), digits)), nil
}
