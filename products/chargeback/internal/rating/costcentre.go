package rating

import (
	"math/big"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The cost-centre breakdown of a statement (DESIGN.md §19).
//
// A cost centre is an attribution of an amount ALREADY COMPUTED. The
// waterfall — list → commercial terms → discounts → true-up → tax — does not
// know cost centres exist, and nothing here may change what a customer is
// charged. What this produces is a reading of the same invoice by a second
// dimension.
//
// So the breakdown is not a second rating run: it apportions the statement's
// OWN net, discount and tax across the cost centres, pro rata by the period's
// usage under each, by the largest-remainder method (apportion.go). That
// makes four identities hold by construction rather than by luck:
//
//	sum(net)      == statement subtotal
//	sum(discount) == statement discount total
//	sum(tax)      == statement tax
//	sum(total)    == statement total          (because total = net + tax on every row)
//
// Apportioning NET rather than gross is deliberate: the parts of a
// largest-remainder split are never negative, so a row's net can never come
// out below zero — which apportioning gross and subtracting an apportioned
// discount could do at a 100 % discount.
//
// A charge that is not usage — a contract true-up, a minimum commitment —
// raises the statement's net and is therefore shared out by the same usage
// weights. That is the standard showback treatment of an unattributable
// charge, and it is stated on the page rather than hidden: the Usage column
// on each row is the weight the split used, so a reader can see the basis.

// CostCentreBreakdown apportions a statement's figures across the cost
// centres its period's usage fell into. weights come from
// store.CostCentreWeights, biggest first, and already carry the unassigned
// bucket when anything landed in it.
//
// A period with no usage at all — a statement that is only a true-up, or a
// customer whose records were all unpriced — produces ONE row under the
// unassigned bucket. The figure is never dropped and never spread over
// centres no usage supports.
func CostCentreBreakdown(weights []store.CostCentreWeight, net, discount, tax store.Decimal) ([]store.CostCentreLine, error) {
	rows := orderedWeights(weights)
	amounts := make([]*big.Rat, len(rows))
	total := new(big.Rat)
	for i, w := range rows {
		r, err := parseRat(string(w.Amount))
		if err != nil {
			return nil, err
		}
		if r.Sign() < 0 {
			r = new(big.Rat)
		}
		amounts[i] = r
		total.Add(total, r)
	}
	nets, err := ApportionWeights(net, amounts)
	if err != nil {
		return nil, err
	}
	discounts, err := ApportionWeights(discount, amounts)
	if err != nil {
		return nil, err
	}
	taxes, err := ApportionWeights(tax, amounts)
	if err != nil {
		return nil, err
	}
	out := make([]store.CostCentreLine, 0, len(rows))
	for i, w := range rows {
		name := w.Name
		if name == "" && w.Code == store.CostCentreUnassigned {
			name = store.CostCentreUnassignedName
		}
		out = append(out, store.CostCentreLine{
			Code:     w.Code,
			Name:     name,
			Usage:    store.Decimal(roundRat(amounts[i], MoneyScale)),
			List:     store.Decimal(roundRat(new(big.Rat).Add(nets[i], discounts[i]), MoneyScale)),
			Discount: store.Decimal(roundRat(discounts[i], MoneyScale)),
			Net:      store.Decimal(roundRat(nets[i], MoneyScale)),
			Tax:      store.Decimal(roundRat(taxes[i], MoneyScale)),
			Total:    store.Decimal(roundRat(new(big.Rat).Add(nets[i], taxes[i]), MoneyScale)),
		})
	}
	return out, nil
}

// orderedWeights returns the rows to apportion over: the weights as given,
// except that when NOTHING has a weight the unassigned bucket is put first —
// ApportionWeights puts an unsplittable figure on the first row, and the
// named bucket is the only honest place for it.
func orderedWeights(weights []store.CostCentreWeight) []store.CostCentreWeight {
	positive := false
	unassigned := -1
	for i, w := range weights {
		if r, err := parseRat(string(w.Amount)); err == nil && r.Sign() > 0 {
			positive = true
		}
		if w.Code == store.CostCentreUnassigned {
			unassigned = i
		}
	}
	if positive {
		return weights
	}
	out := make([]store.CostCentreWeight, 0, len(weights)+1)
	if unassigned < 0 {
		out = append(out, store.CostCentreWeight{Code: store.CostCentreUnassigned, Name: store.CostCentreUnassignedName, Amount: "0.000000"})
	} else {
		out = append(out, weights[unassigned])
	}
	for i, w := range weights {
		if i != unassigned {
			out = append(out, w)
		}
	}
	return out
}

// CostCentreTotals sums a breakdown's columns. It is what a screen, an
// export and a test all read, so "the rows add up to the invoice" is checked
// against one definition of adding up.
func CostCentreTotals(lines []store.CostCentreLine) (usage, list, discount, net, tax, total store.Decimal, err error) {
	col := func(pick func(store.CostCentreLine) store.Decimal) (store.Decimal, error) {
		vals := make([]store.Decimal, 0, len(lines))
		for _, l := range lines {
			vals = append(vals, pick(l))
		}
		return Sum(vals...)
	}
	if usage, err = col(func(l store.CostCentreLine) store.Decimal { return l.Usage }); err != nil {
		return
	}
	if list, err = col(func(l store.CostCentreLine) store.Decimal { return l.List }); err != nil {
		return
	}
	if discount, err = col(func(l store.CostCentreLine) store.Decimal { return l.Discount }); err != nil {
		return
	}
	if net, err = col(func(l store.CostCentreLine) store.Decimal { return l.Net }); err != nil {
		return
	}
	if tax, err = col(func(l store.CostCentreLine) store.Decimal { return l.Tax }); err != nil {
		return
	}
	total, err = col(func(l store.CostCentreLine) store.Decimal { return l.Total })
	return
}
