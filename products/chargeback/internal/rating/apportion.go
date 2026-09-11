package rating

import (
	"fmt"
	"math/big"
	"sort"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The largest-remainder apportionment, in ONE place.
//
// Splitting a figure across groups pro rata is the arithmetic behind two
// separate things in this product: the discount across tax rules (DESIGN.md
// §17.3) and the invoice across cost centres (DESIGN.md §19). Both need the
// same guarantee — that the parts sum to the whole EXACTLY, so
//
//	sum(parts) == whole
//
// is an identity and not a rounding coincidence. Two copies of this would be
// two answers that can disagree, so there is one: ApportionWeights is the
// method, and each caller supplies its own weights.

// MoneyScale is the number of decimals every money column carries
// (NUMERIC(20,6)). An apportioned part is a whole number of units at this
// scale, which is what lets the parts be made to sum exactly.
const MoneyScale = 6

// moneyUnit is 10^-MoneyScale as an exact rational.
var moneyUnit = big.NewRat(1, 1000000)

// ApportionWeights splits total across the weights pro rata. Each part is
// floored to the money scale and the remainder is handed out one unit at a
// time, largest fractional part first — so the parts sum to total EXACTLY,
// and a weight of zero receives nothing.
//
// Two boundaries the callers rely on:
//
//   - total ≤ 0 gives every part zero. Apportioning a figure nobody owes is
//     not an error; it is nothing to share out.
//   - every weight zero puts the whole of total on the FIRST group. There is
//     no proportion to go by, and spreading it evenly would invent one; the
//     caller orders the groups so that the first is the one that should
//     carry it.
func ApportionWeights(total store.Decimal, weights []*big.Rat) ([]*big.Rat, error) {
	want, err := parseRat(string(total))
	if err != nil {
		return nil, fmt.Errorf("apportion: %w", err)
	}
	shares := make([]*big.Rat, len(weights))
	for i := range shares {
		shares[i] = new(big.Rat)
	}
	if len(weights) == 0 || want.Sign() <= 0 {
		return shares, nil
	}
	sum := new(big.Rat)
	for _, w := range weights {
		sum.Add(sum, w)
	}
	if sum.Sign() <= 0 {
		shares[0] = want
		return shares, nil
	}
	inUnits := new(big.Rat).Quo(want, moneyUnit)
	totalUnits := new(big.Int).Quo(inUnits.Num(), inUnits.Denom())
	type rem struct {
		i    int
		frac *big.Rat
	}
	var rems []rem
	assigned := new(big.Int)
	for i, w := range weights {
		exact := new(big.Rat).Quo(new(big.Rat).Mul(new(big.Rat).SetInt(totalUnits), w), sum)
		floor := new(big.Int).Quo(exact.Num(), exact.Denom())
		shares[i] = new(big.Rat).Mul(new(big.Rat).SetInt(floor), moneyUnit)
		assigned.Add(assigned, floor)
		rems = append(rems, rem{i: i, frac: new(big.Rat).Sub(exact, new(big.Rat).SetInt(floor))})
	}
	left := new(big.Int).Sub(totalUnits, assigned)
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac.Cmp(rems[b].frac) > 0 })
	for k := 0; left.Sign() > 0 && k < len(rems); k++ {
		shares[rems[k].i].Add(shares[rems[k].i], moneyUnit)
		left.Sub(left, big.NewInt(1))
	}
	// A total that is not a whole number of units (it always is — every
	// money column is NUMERIC(20,6)) would leave a sliver; it goes to the
	// largest weight so the identity still holds.
	if slack := new(big.Rat).Sub(want, sumRats(shares)); slack.Sign() != 0 {
		biggest := 0
		for i := range weights {
			if weights[i].Cmp(weights[biggest]) > 0 {
				biggest = i
			}
		}
		shares[biggest].Add(shares[biggest], slack)
	}
	return shares, nil
}
