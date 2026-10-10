package rating

import (
	"fmt"
	"math/big"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// GROW OVERAGE (DESIGN.md §22.11). A package's vCPU and memory headline is
// an allowance like its disk and bandwidth, but it is not a quantity
// feature of the matrix: the headline is the package's SHAPE and it is a
// LIMIT (the ResourceQuota's limits.* term). So the compute overage of a
// Source in grow mode is measured on the LIMIT meters the collector writes
// for an Organization on a package (store.SKUVCPULimit, store.SKUMemLimit),
// hour by hour, and computed here:
//
//	allowance = Σ over the plan segments of headline × plan-hours
//	excess    = max(0, limit-hours − allowance)
//	line      = excess × the package's own overage rate per unit-hour
//
// The rate is the PACKAGE's (package_settings.overage_vcpu_month /
// overage_mem_gb_month — its unit price plus 10 %), converted exactly as
// every book item is: annual = monthly × 12, unit = annual ÷ the book's
// divisor, rounded to 8 decimals. A period with a plan change rates the
// whole excess at the package the Source spent most of the period on (the
// segments come longest first), and adds each segment's allowance.
//
// The lines carry the meter SKUs a customer recognises — k8s.vcpu per
// vcpu-hour and k8s.mem_gb per gib-hour — and are placed on the statement of
// the period they were used in, in arrears. In capped mode there is no
// compute line at all: the quota refuses anything above the headline.

// GrowInput is what GrowOverage needs for one Source.
type GrowInput struct {
	// Limits is the period's total of each limit meter (SKU → unit-hours).
	Limits map[string]*big.Rat
	// Packages are the limits and rates of each package the Source ran,
	// keyed by plan slug.
	Packages map[string]store.PackageLimits
	// Divisor is the book's annual divisor.
	Divisor int
	// Currency is the book's, named on the line.
	Currency string
}

// GrowOverage returns the compute overage lines of one Source in grow mode,
// and the SKUs it could not price (a package that allows no grow rate).
func GrowOverage(src store.CostSource, segments []PlanSegment, in GrowInput) ([]store.RatedLine, []string, error) {
	if src.OverageMode != store.OverageModeGrow || len(segments) == 0 {
		return nil, nil, nil
	}
	sid := src.ID
	type dim struct {
		limitSKU, sku, unit, what, unitName string
		head                                func(store.PackageLimits) *store.Decimal
		rate                                func(store.PackageLimits) *store.Decimal
	}
	dims := []dim{
		{store.SKUVCPULimit, store.SKUVCPU, store.UnitVCPU, "vCPU", "vCPU",
			func(l store.PackageLimits) *store.Decimal { return l.Headline.VCPU },
			func(l store.PackageLimits) *store.Decimal { return l.OverageVCPUMonth }},
		{store.SKUMemLimit, store.SKUMem, store.UnitMem, "Memory", "GB",
			func(l store.PackageLimits) *store.Decimal { return l.Headline.MemoryGB },
			func(l store.PackageLimits) *store.Decimal { return l.OverageMemGBMonth }},
	}
	var lines []store.RatedLine
	var unpriced []string
	main := in.Packages[segments[0].Slug]
	for _, d := range dims {
		used := in.Limits[d.limitSKU]
		if used == nil || used.Sign() <= 0 {
			continue
		}
		allowance := new(big.Rat)
		var parts []string
		for _, seg := range segments {
			h := d.head(in.Packages[seg.Slug])
			if h == nil {
				continue
			}
			head, err := parseRat(string(*h))
			if err != nil {
				return nil, nil, fmt.Errorf("package %s: %s headline: %w", seg.Slug, d.what, err)
			}
			allowance.Add(allowance, new(big.Rat).Mul(head, seg.Hours))
			parts = append(parts, fmt.Sprintf("%s %s × %s h on %s", trimRat(head), d.unitName, trimRat(seg.Hours), store.PlanName(seg.Slug)))
		}
		excess := new(big.Rat).Sub(used, allowance)
		if excess.Sign() <= 0 {
			continue
		}
		rate := d.rate(main)
		if rate == nil || strings.TrimSpace(string(*rate)) == "" {
			unpriced = append(unpriced, d.sku)
			continue
		}
		monthly, err := parseRat(string(*rate))
		if err != nil {
			return nil, nil, fmt.Errorf("package %s: %s overage rate: %w", main.PlanSlug, d.what, err)
		}
		unitPrice, err := UnitPrice(roundRat(new(big.Rat).Mul(monthly, big.NewRat(12, 1)), 8), in.Divisor)
		if err != nil {
			return nil, nil, err
		}
		qty := store.Decimal(roundRat(excess, 6))
		amount, err := Amount(qty, unitPrice)
		if err != nil {
			return nil, nil, err
		}
		lines = append(lines, store.RatedLine{
			SourceID:  &sid,
			SKU:       d.sku,
			Quantity:  qty,
			Unit:      d.unit,
			UnitPrice: unitPrice,
			Amount:    amount,
			Description: fmt.Sprintf("%s above the %s package, grow — %s %s used, %s included (%s), at %s %s per %s a month",
				d.what, store.PlanName(main.PlanSlug), trimRat(used), d.unit, trimRat(allowance), strings.Join(parts, " + "), string(*rate), in.Currency, d.unitName),
		})
	}
	return lines, unpriced, nil
}

// trimRat renders an exact quantity with at most six decimals and no
// trailing zeros.
func trimRat(r *big.Rat) string {
	s := roundRat(r, 6)
	if strings.Contains(s, ".") {
		s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
	}
	return s
}
