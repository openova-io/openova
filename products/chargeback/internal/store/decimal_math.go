package store

import (
	"math/big"
	"strings"
)

// The explorer sums money across groups and buckets in Go (top-N "Other",
// totals, shares). Those sums use exact rational arithmetic like the rating
// engine does, never float64 — a cost that is a bill preview must add up to
// the bill. Only ratios (share, delta %) are floats, because they are
// display-only and have no exact form.

func ratOf(d Decimal) *big.Rat {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return new(big.Rat)
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

// decOf renders a rational as a 6-decimal Decimal, the scale of every money
// column in this schema.
func decOf(r *big.Rat) Decimal {
	return Decimal(r.FloatString(6))
}

// addDec returns a + b exactly.
func addDec(a, b Decimal) Decimal {
	return decOf(new(big.Rat).Add(ratOf(a), ratOf(b)))
}

// isZeroDec reports whether d is numerically zero (or empty).
func isZeroDec(d Decimal) bool {
	return ratOf(d).Sign() == 0
}

// floatOf is for ratios only.
func floatOf(d Decimal) float64 {
	f, _ := ratOf(d).Float64()
	return f
}

// deltaPct returns (cur - prev) / prev × 100, or nil when prev is zero: a
// change against nothing is not a percentage, and 0 would read as "no change".
func deltaPct(cur, prev Decimal) *float64 {
	p := ratOf(prev)
	if p.Sign() == 0 {
		return nil
	}
	d := new(big.Rat).Sub(ratOf(cur), p)
	d.Quo(d, p)
	d.Mul(d, big.NewRat(100, 1))
	f, _ := d.Float64()
	return &f
}

// shareOf returns part / whole, or 0 when whole is zero.
func shareOf(part, whole Decimal) float64 {
	w := ratOf(whole)
	if w.Sign() == 0 {
		return 0
	}
	f, _ := new(big.Rat).Quo(ratOf(part), w).Float64()
	return f
}
