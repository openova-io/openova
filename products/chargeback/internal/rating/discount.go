package rating

import (
	"fmt"
	"math/big"

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
}

// ApplyDiscounts computes the total reduction for a set of rated lines.
//
// Arithmetic is exact (big.Rat), matching the rest of this package. Money must
// not be computed in float64: 0.1+0.2 is not 0.3, and a cent lost per line is a
// reconciliation failure nobody can explain later.
//
// PERCENT discounts are always computed against the UNTOUCHED base, never
// against a partially discounted amount. Two consequences worth stating
// because they are the whole commercial meaning:
//
//   - stacking two 10% discounts gives 20%, not 19% — they do not compound;
//   - the order percent/fixed are evaluated in cannot change the total, which
//     is why there is no test asserting an ordering. An earlier version of
//     this comment claimed order was load-bearing; it is not, and the test
//     that "proved" it passed identically with the passes swapped.
//
// FIXED amounts come off what remains after the percentages, and are clamped
// so the run cannot overdraw.
//
// The result is clamped at the gross: an over-generous campaign takes the bill
// to zero and no further. A negative invoice is not a credit note, it is a bug
// that reads as money owed to the customer.
func ApplyDiscounts(lines []store.RatedLine, discounts []store.Discount) (store.Decimal, []AppliedDiscount, error) {
	if len(lines) == 0 || len(discounts) == 0 {
		return store.Decimal("0"), nil, nil
	}
	bySKU := map[string]*big.Rat{}
	gross := new(big.Rat)
	for _, l := range lines {
		a, err := parseRat(string(l.Amount))
		if err != nil {
			return store.Decimal("0"), nil, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		if bySKU[l.SKU] == nil {
			bySKU[l.SKU] = new(big.Rat)
		}
		bySKU[l.SKU].Add(bySKU[l.SKU], a)
		gross.Add(gross, a)
	}
	base := func(d store.Discount) *big.Rat {
		if d.SKU == "" {
			return new(big.Rat).Set(gross)
		}
		if v := bySKU[d.SKU]; v != nil {
			return new(big.Rat).Set(v)
		}
		return new(big.Rat)
	}

	applied := []AppliedDiscount{}
	total := new(big.Rat)
	hundred := big.NewRat(100, 1)

	// Pass 1 — percentages, against the untouched base.
	for _, d := range discounts {
		if d.Kind != "percent" {
			continue
		}
		pct, err := parseRat(string(d.Value))
		if err != nil {
			return store.Decimal("0"), nil, fmt.Errorf("discount %s: %w", d.Name, err)
		}
		amt := new(big.Rat).Quo(new(big.Rat).Mul(base(d), pct), hundred)
		if amt.Sign() <= 0 {
			continue
		}
		total.Add(total, amt)
		applied = append(applied, AppliedDiscount{
			DiscountID: d.ID, Name: d.Name, Kind: d.Kind, Value: d.Value, SKU: d.SKU,
			Amount: store.Decimal(roundRat(amt, 6)),
		})
	}

	// Pass 2 — fixed amounts, off what remains.
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
