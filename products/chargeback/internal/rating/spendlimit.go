package rating

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// THE SPEND LIMIT (DESIGN.md §22.11, founder direction 2026-10-10). A grow
// Source's spend_limit_month caps the USAGE charges ABOVE the package — the
// compute overage lines and the disk and bandwidth excess — never the
// package itself. It is enforced while the period runs, not forgiven on the
// bill: the spend-limit evaluator (internal/spendlimit) rates the period so
// far every hour, and when the accrued usage above the package reaches the
// limit it holds the Organization at its package headline (capped) until the
// next period start. So the usage above the package STOPS at the limit.
//
// On the statement the usage above the package is billed EXACTLY up to the
// limit. The rule for the hour that crosses it: the lines are taken in a
// fixed order — vCPU, memory, disk, bandwidth — and the line that crosses
// the limit is billed only for the part that fits under it (its amount is
// the remainder, a compute line's quantity that remainder over its unit
// price, to 6 decimals); every line after it is billed at 0. Usage measured
// after the stop — the minutes between the evaluator's tick and the quota
// landing — is therefore never billed. No negative line is written: each
// line says what it billed.

// overageOrder is the fixed order the usage above the package is billed in
// against the limit.
var overageOrder = []string{store.SKUVCPU, store.SKUMem, store.SKUPVC, store.SKUBandwidth}

// overageSKUs are the SKUs whose lines on a grow platform Source are usage
// above the package: the two compute overage lines GrowOverage writes and
// the two quantity meters whose allowance the package carries (after the
// terms, their amount is the excess alone).
var overageSKUs = map[string]bool{store.SKUVCPU: true, store.SKUMem: true, store.SKUPVC: true, store.SKUBandwidth: true}

// OverageOf sums a Source's usage above the package in lines AFTER the terms.
func OverageOf(sourceID string, lines []store.RatedLine) (*big.Rat, error) {
	used := new(big.Rat)
	for _, l := range lines {
		if l.SourceID == nil || *l.SourceID != sourceID || !overageSKUs[l.SKU] {
			continue
		}
		a, err := parseRat(string(l.Amount))
		if err != nil {
			return nil, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		used.Add(used, a)
	}
	return used, nil
}

// ClipToSpendLimit bills a grow Source's usage above the package at most up
// to its spend limit, in place, and reports whether any line was clipped.
// lines are the period's lines AFTER the terms.
func ClipToSpendLimit(src store.CostSource, lines []store.RatedLine, currency string) (bool, error) {
	if src.OverageMode != store.OverageModeGrow || src.SpendLimitMonth == nil {
		return false, nil
	}
	limit, err := parseRat(string(*src.SpendLimitMonth))
	if err != nil {
		return false, fmt.Errorf("spend limit: %w", err)
	}
	used, err := OverageOf(src.ID, lines)
	if err != nil {
		return false, err
	}
	if used.Cmp(limit) <= 0 {
		return false, nil
	}
	remaining := new(big.Rat).Set(limit)
	for _, sku := range overageOrder {
		for i := range lines {
			l := &lines[i]
			if l.SourceID == nil || *l.SourceID != src.ID || l.SKU != sku {
				continue
			}
			a, err := parseRat(string(l.Amount))
			if err != nil {
				return false, fmt.Errorf("line %s: %w", l.SKU, err)
			}
			if a.Cmp(remaining) <= 0 {
				remaining.Sub(remaining, a)
				continue
			}
			billed := new(big.Rat).Set(remaining)
			if l.SKU == store.SKUVCPU || l.SKU == store.SKUMem {
				// A compute overage line is excess × rate: its quantity is
				// what the billed amount buys.
				if up, err := parseRat(string(l.UnitPrice)); err == nil && up.Sign() > 0 {
					l.Quantity = store.Decimal(roundRat(new(big.Rat).Quo(billed, up), 6))
				}
			}
			l.Description = spendLimitNote(l.Description, a, billed, limit, currency)
			l.Amount = store.Decimal(roundRat(billed, 6))
			remaining.SetInt64(0)
		}
	}
	return true, nil
}

// AccruedOverage rates [from, now) of one customer exactly as the statement
// run rates a period, up to the terms, and returns each grow Source's usage
// above its package so far — what the spend-limit evaluator compares with
// the limit. Only Sources in grow mode with a spend limit are in the map.
func AccruedOverage(ctx context.Context, st *store.Store, c store.Customer, from, now time.Time) (map[string]*big.Rat, error) {
	var detail rateDetail
	pl, err := ratePeriodLines(ctx, st, c, from, now, &detail)
	if err != nil {
		return nil, err
	}
	out := map[string]*big.Rat{}
	for _, src := range pl.sources {
		if src.OverageMode != store.OverageModeGrow || src.SpendLimitMonth == nil {
			continue
		}
		used, err := OverageOf(src.ID, pl.lines)
		if err != nil {
			return nil, err
		}
		out[src.ID] = used
	}
	return out, nil
}

func spendLimitNote(desc string, rated, billed, limit *big.Rat, currency string) string {
	note := fmt.Sprintf("billed up to the %s %s spend limit: %s of %s", trimRat(limit), currency, trimRat(billed), trimRat(rated))
	if billed.Sign() == 0 {
		note = fmt.Sprintf("not billed — the %s %s spend limit was reached (%s rated)", trimRat(limit), currency, trimRat(rated))
	}
	if desc == "" {
		return note
	}
	return desc + "; " + note
}
