package rating

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// AppliedDiscount records what one discount actually took off a statement, so
// a bill can show list price, discount and net rather than one opaque number
// (#6862).
type AppliedDiscount struct {
	DiscountID string        `json:"discount_id"`
	Name       string        `json:"name"`
	Kind       string        `json:"kind"`
	Value      store.Decimal `json:"value"`
	SKU        string        `json:"sku,omitempty"`
	Amount     store.Decimal `json:"amount"`
	// Stackable echoes the discount's flag, so the statement can say why a
	// second percentage applied on top of the winner (DESIGN.md §2.11).
	Stackable bool `json:"stackable,omitempty"`
	// SupersededBy is the id of the discount that beat this one on every
	// line it matched, under the most-specific and highest rules. Amount is
	// then 0 and the statement shows "not applied: superseded by <name>".
	SupersededBy string `json:"superseded_by,omitempty"`
}

// ApplyDiscounts computes the total reduction for a set of rated lines under
// the given combination rule (store.DiscountRule*, DESIGN.md §2.11). An
// empty rule is the default; an unknown one is an error, never a silent
// fallback — the rule is printed on the bill.
//
// Arithmetic is exact (big.Rat), matching the rest of this package. Money must
// not be computed in float64: 0.1+0.2 is not 0.3, and a cent lost per line is a
// reconciliation failure nobody can explain later.
//
// PERCENT discounts are decided per SKU, because a discount applies to a meter
// and the lines of one meter share every applicable discount:
//
//   - most-specific: the one non-stackable percent with the narrowest scope
//     wins — a SKU-scoped discount beats a whole-bill one; at the same scope
//     the higher percent wins. Stackable discounts are added on top of the
//     winner, against the untouched base.
//   - highest: the highest non-stackable percent wins regardless of scope
//     (a scope tie-break, then input order); stackable ones add on top.
//   - stack: every applicable percent is computed against the untouched base
//     and summed — two 10 % discounts are 20 %, not 19 %.
//   - compound: percents multiply — 10 % then 20 % is 1 − 0.9 × 0.8 = 28 %.
//     The breakdown attributes each percentage to what remained after the
//     narrower ones before it; the total does not depend on that order.
//
// FIXED amounts come off what remains after the percentages, in every rule,
// and are clamped so the run cannot overdraw.
//
// The result is clamped at the gross: an over-generous campaign takes the bill
// to zero and no further. A negative invoice is not a credit note, it is a bug
// that reads as money owed to the customer.
func ApplyDiscounts(lines []store.RatedLine, discounts []store.Discount, rule string) (store.Decimal, []AppliedDiscount, error) {
	if rule == "" {
		rule = store.DefaultDiscountRule
	}
	if !store.ValidDiscountRule(rule) {
		return store.Decimal("0"), nil, fmt.Errorf("unknown discount rule %q", rule)
	}
	if len(lines) == 0 || len(discounts) == 0 {
		return store.Decimal("0"), nil, nil
	}

	// Bases: one per SKU (a discount applies to a meter, not to one source's
	// share of it) plus the gross. SKUs are visited in a fixed order so the
	// breakdown is reproducible.
	bySKU := map[string]*big.Rat{}
	var skus []string
	gross := new(big.Rat)
	for _, l := range lines {
		a, err := parseRat(string(l.Amount))
		if err != nil {
			return store.Decimal("0"), nil, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		if bySKU[l.SKU] == nil {
			bySKU[l.SKU] = new(big.Rat)
			skus = append(skus, l.SKU)
		}
		bySKU[l.SKU].Add(bySKU[l.SKU], a)
		gross.Add(gross, a)
	}
	sort.Strings(skus)

	// Percent candidates, parsed once. idx points back into discounts so the
	// breakdown keeps the caller's order and two discounts with one id (a
	// test fixture, never the database) cannot merge.
	type pctDiscount struct {
		idx int
		d   store.Discount
		pct *big.Rat
	}
	var pcts []pctDiscount
	for i, d := range discounts {
		if d.Kind != "percent" {
			continue
		}
		pct, err := parseRat(string(d.Value))
		if err != nil {
			return store.Decimal("0"), nil, fmt.Errorf("discount %s: %w", d.Name, err)
		}
		if pct.Sign() <= 0 {
			continue
		}
		pcts = append(pcts, pctDiscount{idx: i, d: d, pct: pct})
	}

	hundred := big.NewRat(100, 1)
	acc := make([]*big.Rat, len(discounts))     // what each discount took, across lines
	matched := make([]bool, len(discounts))     // applied to at least one line
	supersededBy := make([]int, len(discounts)) // index of the winner on the largest line lost
	supersededBase := make([]*big.Rat, len(discounts))
	for i := range supersededBy {
		supersededBy[i] = -1
	}
	take := func(idx int, base, pct *big.Rat) {
		amt := new(big.Rat).Quo(new(big.Rat).Mul(base, pct), hundred)
		if acc[idx] == nil {
			acc[idx] = new(big.Rat)
		}
		acc[idx].Add(acc[idx], amt)
	}
	lost := func(idx, winner int, base *big.Rat) {
		if supersededBase[idx] == nil || base.Cmp(supersededBase[idx]) > 0 {
			supersededBy[idx] = winner
			supersededBase[idx] = new(big.Rat).Set(base)
		}
	}

	for _, sku := range skus {
		base := bySKU[sku]
		if base.Sign() <= 0 {
			continue
		}
		var cands []pctDiscount
		for _, p := range pcts {
			if p.d.SKU == "" || p.d.SKU == sku {
				cands = append(cands, p)
			}
		}
		if len(cands) == 0 {
			continue
		}
		for _, c := range cands {
			matched[c.idx] = true
		}
		switch rule {
		case store.DiscountRuleStack:
			for _, c := range cands {
				take(c.idx, base, c.pct)
			}
		case store.DiscountRuleCompound:
			// Narrowest first, then highest; a stable sort keeps input order
			// for full ties. Only the attribution depends on this order.
			sort.SliceStable(cands, func(i, j int) bool {
				if si, sj := scopeRank(cands[i].d), scopeRank(cands[j].d); si != sj {
					return si > sj
				}
				return cands[i].pct.Cmp(cands[j].pct) > 0
			})
			remaining := new(big.Rat).Set(base)
			for _, c := range cands {
				amt := new(big.Rat).Quo(new(big.Rat).Mul(remaining, c.pct), hundred)
				if acc[c.idx] == nil {
					acc[c.idx] = new(big.Rat)
				}
				acc[c.idx].Add(acc[c.idx], amt)
				remaining.Sub(remaining, amt)
			}
		default: // most-specific, highest
			winner := -1
			for i, c := range cands {
				if c.d.Stackable {
					continue
				}
				if winner < 0 || beats(rule, c.d, c.pct, cands[winner].d, cands[winner].pct) {
					winner = i
				}
			}
			for i, c := range cands {
				if c.d.Stackable || i == winner {
					take(c.idx, base, c.pct)
					continue
				}
				lost(c.idx, cands[winner].idx, base)
			}
		}
	}

	applied := []AppliedDiscount{}
	total := new(big.Rat)
	for i, d := range discounts {
		if d.Kind != "percent" {
			continue
		}
		entry := AppliedDiscount{DiscountID: d.ID, Name: d.Name, Kind: d.Kind, Value: d.Value, SKU: d.SKU, Stackable: d.Stackable}
		switch {
		case acc[i] != nil && acc[i].Sign() > 0:
			total.Add(total, acc[i])
			entry.Amount = store.Decimal(roundRat(acc[i], 6))
			applied = append(applied, entry)
		case matched[i] && supersededBy[i] >= 0:
			// Matched a line but a better discount took it — on the bill, so
			// the customer sees why the campaign did not add up.
			entry.Amount = store.Decimal(roundRat(new(big.Rat), 6))
			entry.SupersededBy = discounts[supersededBy[i]].ID
			applied = append(applied, entry)
		}
	}

	// Fixed amounts, off what remains, in every rule.
	for _, d := range discounts {
		if d.Kind != "fixed" {
			continue
		}
		v, err := parseRat(string(d.Value))
		if err != nil {
			return store.Decimal("0"), nil, fmt.Errorf("discount %s: %w", d.Name, err)
		}
		remaining := new(big.Rat).Sub(gross, total)
		if remaining.Sign() <= 0 {
			break
		}
		amt := v
		if amt.Cmp(remaining) > 0 {
			amt = remaining
		}
		if amt.Sign() <= 0 {
			continue
		}
		total.Add(total, amt)
		applied = append(applied, AppliedDiscount{
			DiscountID: d.ID, Name: d.Name, Kind: d.Kind, Value: d.Value, SKU: d.SKU,
			Amount: store.Decimal(roundRat(amt, 6)),
		})
	}

	if total.Cmp(gross) > 0 {
		total = new(big.Rat).Set(gross)
	}
	return store.Decimal(roundRat(total, 6)), applied, nil
}

// scopeRank orders discount scopes from widest to narrowest: a SKU-scoped
// discount is more specific than a whole-bill one. The customer dimension
// does not enter — a customer's own discount and an all-customer campaign
// both already apply to this customer's statement.
func scopeRank(d store.Discount) int {
	if d.SKU != "" {
		return 1
	}
	return 0
}

// beats reports whether candidate a should replace the current winner b
// under rule. A full tie keeps b, so input order settles it.
func beats(rule string, a store.Discount, aPct *big.Rat, b store.Discount, bPct *big.Rat) bool {
	sa, sb := scopeRank(a), scopeRank(b)
	pc := aPct.Cmp(bPct)
	if rule == store.DiscountRuleHighest {
		if pc != 0 {
			return pc > 0
		}
		return sa > sb
	}
	// most-specific
	if sa != sb {
		return sa > sb
	}
	return pc > 0
}
