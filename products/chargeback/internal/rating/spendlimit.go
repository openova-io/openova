package rating

import (
	"fmt"
	"math/big"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// THE SPEND LIMIT (DESIGN.md §22.11, founder decision 2026-10-10). A grow
// Source's spend_limit_month caps the USAGE charges ABOVE the package — the
// compute overage lines and the disk and bandwidth excess — never the whole
// bill: the package's own line and its add-ons are the prepaid commitment
// and are always billed. When the usage above the package rates to more
// than the limit, the statement carries ONE named line that takes the
// difference off, so the bill shows what was used, what it rated to, and
// what the limit waived — never a silent adjustment.
//
// It is a cap on the BILL, applied when the period is rated. Nothing here
// stops the usage while it happens: the quota stays at the grow ceiling.

// SpendLimitSKU / SpendLimitUnit name the line.
const (
	SpendLimitSKU  = "overage.spend_limit"
	SpendLimitUnit = "period"
)

// overageSKUs are the SKUs whose lines on a grow platform Source are usage
// above the package: the two compute overage lines GrowOverage writes and
// the two quantity meters whose allowance the package carries (after the
// terms, their amount is the excess alone).
var overageSKUs = map[string]bool{store.SKUVCPU: true, store.SKUMem: true, store.SKUPVC: true, store.SKUBandwidth: true}

// SpendLimitLine returns the line that brings a grow Source's usage charges
// above the package down to its spend limit, and false when there is none
// to write (not grow, no limit, or the usage within it). lines are the
// period's lines AFTER the terms.
func SpendLimitLine(src store.CostSource, lines []store.RatedLine, currency string) (store.RatedLine, bool, error) {
	if src.OverageMode != store.OverageModeGrow || src.SpendLimitMonth == nil {
		return store.RatedLine{}, false, nil
	}
	limit, err := parseRat(string(*src.SpendLimitMonth))
	if err != nil {
		return store.RatedLine{}, false, fmt.Errorf("spend limit: %w", err)
	}
	used := new(big.Rat)
	for _, l := range lines {
		if l.SourceID == nil || *l.SourceID != src.ID || !overageSKUs[l.SKU] {
			continue
		}
		a, err := parseRat(string(l.Amount))
		if err != nil {
			return store.RatedLine{}, false, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		used.Add(used, a)
	}
	over := new(big.Rat).Sub(used, limit)
	if over.Sign() <= 0 {
		return store.RatedLine{}, false, nil
	}
	amount := store.Decimal(roundRat(new(big.Rat).Neg(over), 6))
	sid := src.ID
	return store.RatedLine{
		SourceID:  &sid,
		SKU:       SpendLimitSKU,
		Quantity:  "1",
		Unit:      SpendLimitUnit,
		UnitPrice: store.Decimal(roundRat(new(big.Rat).Neg(over), 8)),
		Amount:    amount,
		Description: fmt.Sprintf("Spend limit — the usage above the package rated %s %s this period, capped at the %s %s limit; %s not billed",
			trimRat(used), currency, trimRat(limit), currency, trimRat(over)),
	}, true, nil
}
