package rating

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// COMMERCIAL TERMS — the three rating shapes and the true-up (DESIGN.md §15).
//
// These are the industry's shapes, under the industry's names. AWS sells free
// tiers and tiered S3 storage and Savings Plans; Azure sells included
// quantities, graduated meters and reservations; Stripe calls the two tier
// modes `graduated` and `volume`; Zuora calls them tiered and volume pricing.
// Nothing below is invented, and nothing below is a second pricing path:
// every figure is produced by THIS file and then handed to the ONE discount
// engine (discount.go) and the ONE partner waterfall (partners.go).
//
// THE ORDER OF OPERATIONS, stated once and pinned by tests:
//
//	 1. ALLOWANCE   usage up to the included quantity rates to ZERO
//	 2. TIERS       the remainder is priced by the item's bands (or its flat
//	                unit price when it has none)
//	 3. COMMITMENT  the committed quantity — the head of the remaining volume
//	                — is repriced at the committed rate; the excess keeps the
//	                rate step 2 gave it
//	 4. DISCOUNTS   customer discounts, and a partner tier, through
//	                ApplyDiscounts / DiscountBySKU
//	 5. TRUE-UP     a period below the contract's monthly minimum carries a
//	                named `true-up` line for the shortfall
//	 6. TAX         on the net subtotal, the true-up included
//
// The order matters and the tests prove it: an allowance applied after the
// tiers would consume the CHEAPEST band instead of the dearest; a discount
// applied before the tiers would move the volume into a different band.
//
// All arithmetic is exact (big.Rat), rounded once at the edge, like the rest
// of this package. Money is never computed in float64.

// TrueUpSKU is the SKU of the shortfall line a minimum commitment produces.
// It is a line of the statement, never a silent adjustment of the totals: a
// customer who is charged for usage it did not have must be able to read why.
const TrueUpSKU = "true-up"

// TrueUpUnit is the unit of a true-up line: one per billing period.
const TrueUpUnit = "period"

// band is one volume tier, normalised: everything above `from` and up to
// `upTo` rates at `price`. upTo nil is the last, unbounded band.
type band struct {
	from  *big.Rat
	upTo  *big.Rat
	price *big.Rat
}

// shape is the commercial shape of ONE SKU for ONE billing period: the
// item's price (flat or banded), the quantity included, and the quantity
// committed at a negotiated rate.
type shape struct {
	sku            string
	unitPrice      *big.Rat
	mode           string
	bands          []band
	allowance      *big.Rat
	committed      *big.Rat
	committedPrice *big.Rat
}

// inert reports whether the shape does nothing a flat unit price would not
// already do — the case for every item of every book written before §15.
func (s shape) inert() bool {
	return len(s.bands) == 0 && (s.allowance == nil || s.allowance.Sign() == 0) && (s.committed == nil || s.committed.Sign() == 0)
}

// Breakdown is what the shapes did to one SKU's quantity in one period. It is
// what the run reports and what the money tests assert on.
type Breakdown struct {
	SKU string `json:"sku"`
	// Quantity is the metered total; Allowance what the plan and the
	// contract included; AllowanceUsed how much of it the usage consumed.
	Quantity      store.Decimal `json:"quantity"`
	Allowance     store.Decimal `json:"allowance,omitempty"`
	AllowanceUsed store.Decimal `json:"allowance_used,omitempty"`
	// Committed is the quantity that rated at CommittedPrice; Excess the
	// quantity above it, which rated at the item's price or its bands.
	Committed      store.Decimal `json:"committed,omitempty"`
	CommittedPrice store.Decimal `json:"committed_price,omitempty"`
	Excess         store.Decimal `json:"excess,omitempty"`
	TierMode       string        `json:"tier_mode,omitempty"`
	// Amount is what the SKU rated to, and EffectiveUnitPrice the amount
	// divided by the metered quantity — the rate the customer actually paid,
	// which is the number to put in front of a customer.
	Amount             store.Decimal `json:"amount"`
	EffectiveUnitPrice store.Decimal `json:"effective_unit_price"`
}

// bandsOf normalises an item's tiers into contiguous, ascending bands. A
// band whose upper bound is at or below its predecessor's is an error: an
// overlapping ladder has no single answer, and guessing one would be a
// pricing bug nobody could see.
func bandsOf(it store.PriceItem) ([]band, error) {
	if !it.HasTiers() {
		return nil, nil
	}
	out := make([]band, 0, len(it.Tiers))
	prev := new(big.Rat)
	sawOpen := false
	for i, t := range it.Tiers {
		if sawOpen {
			return nil, fmt.Errorf("sku %s: tier %d comes after the unbounded band; the band with no upper bound must be last", it.SKU, i+1)
		}
		price, err := parseRat(string(t.Price))
		if err != nil {
			return nil, fmt.Errorf("sku %s: tier %d price: %w", it.SKU, i+1, err)
		}
		if price.Sign() < 0 {
			return nil, fmt.Errorf("sku %s: tier %d price is negative", it.SKU, i+1)
		}
		b := band{from: new(big.Rat).Set(prev), price: price}
		if t.UpTo == nil || strings.TrimSpace(string(*t.UpTo)) == "" {
			sawOpen = true
		} else {
			up, err := parseRat(string(*t.UpTo))
			if err != nil {
				return nil, fmt.Errorf("sku %s: tier %d up_to: %w", it.SKU, i+1, err)
			}
			if up.Cmp(prev) <= 0 {
				return nil, fmt.Errorf("sku %s: tier %d ends at %s, which is not above the band before it (%s)", it.SKU, i+1, up.FloatString(6), prev.FloatString(6))
			}
			b.upTo = up
			prev = up
		}
		out = append(out, b)
	}
	if !sawOpen {
		// A ladder that stops needs a top band, or volume above the last
		// bound would rate at nothing. The last band's price carries on.
		last := out[len(out)-1]
		out = append(out, band{from: new(big.Rat).Set(last.upTo), price: new(big.Rat).Set(last.price)})
	}
	return out, nil
}

// priceAt is the band price the given volume reaches — what `all_units` uses
// for the WHOLE volume.
func priceAt(bands []band, volume *big.Rat) *big.Rat {
	for _, b := range bands {
		if b.upTo == nil || volume.Cmp(b.upTo) <= 0 {
			return b.price
		}
	}
	return bands[len(bands)-1].price
}

// tierAmount prices the volume interval (lo, hi] — step 2 of the order of
// operations. lo is what a commitment already took off the bottom.
func (s shape) tierAmount(lo, hi *big.Rat) *big.Rat {
	if hi.Cmp(lo) <= 0 {
		return new(big.Rat)
	}
	if len(s.bands) == 0 {
		return new(big.Rat).Mul(new(big.Rat).Sub(hi, lo), s.unitPrice)
	}
	if s.mode == store.TierModeAllUnits {
		// The whole volume rates at the band the TOTAL reaches — the band is
		// chosen by hi (everything being rated this period), not by the slice
		// left after a commitment.
		return new(big.Rat).Mul(new(big.Rat).Sub(hi, lo), priceAt(s.bands, hi))
	}
	total := new(big.Rat)
	for _, b := range s.bands {
		from := b.from
		if from.Cmp(lo) < 0 {
			from = lo
		}
		to := hi
		if b.upTo != nil && b.upTo.Cmp(hi) < 0 {
			to = b.upTo
		}
		if to.Cmp(from) <= 0 {
			continue
		}
		total.Add(total, new(big.Rat).Mul(new(big.Rat).Sub(to, from), b.price))
	}
	return total
}

// rate applies the order of operations to one SKU's metered quantity.
func (s shape) rate(qty *big.Rat) (*big.Rat, Breakdown) {
	br := Breakdown{SKU: s.sku, Quantity: store.Decimal(roundRat(qty, 6)), TierMode: s.mode}
	if qty.Sign() <= 0 {
		br.Amount, br.EffectiveUnitPrice = "0.000000", "0.00000000"
		return new(big.Rat), br
	}
	// 1. the allowance comes off the top of the quantity.
	used := new(big.Rat)
	if s.allowance != nil && s.allowance.Sign() > 0 {
		used.Set(s.allowance)
		if used.Cmp(qty) > 0 {
			used.Set(qty)
		}
		br.Allowance = store.Decimal(roundRat(s.allowance, 6))
		br.AllowanceUsed = store.Decimal(roundRat(used, 6))
	}
	billable := new(big.Rat).Sub(qty, used)
	// 3. the commitment covers the head of what remains.
	committed := new(big.Rat)
	if s.committed != nil && s.committed.Sign() > 0 && s.committedPrice != nil {
		committed.Set(s.committed)
		if committed.Cmp(billable) > 0 {
			committed.Set(billable)
		}
		br.Committed = store.Decimal(roundRat(committed, 6))
		br.CommittedPrice = store.Decimal(roundRat(s.committedPrice, 8))
	}
	amount := new(big.Rat)
	if committed.Sign() > 0 {
		amount.Mul(committed, s.committedPrice)
	}
	// 2. the rest at the item's bands, or at its flat unit price.
	amount.Add(amount, s.tierAmount(committed, billable))
	br.Excess = store.Decimal(roundRat(new(big.Rat).Sub(billable, committed), 6))
	br.Amount = store.Decimal(roundRat(amount, 6))
	br.EffectiveUnitPrice = store.Decimal(roundRat(new(big.Rat).Quo(amount, qty), 8))
	return amount, br
}

// Terms is what a customer's period is rated under besides its price books:
// the contract in force and the allowance carried in from the period before.
type Terms struct {
	Contract *store.Contract
	// CarryIn is the unused allowance carried into this period per SKU, for
	// the items and contract lines that roll over (DESIGN.md §15.1).
	CarryIn map[string]store.Decimal
}

// shapeFor builds one SKU's shape from its price-book item, the contract's
// lines and the carried-in allowance.
func (t Terms) shapeFor(it store.PriceItem) (shape, error) {
	s := shape{sku: it.SKU, mode: it.TierMode}
	up, err := parseRat(string(it.UnitPrice))
	if err != nil {
		return s, fmt.Errorf("sku %s: %w", it.SKU, err)
	}
	s.unitPrice = up
	if s.bands, err = bandsOf(it); err != nil {
		return s, err
	}
	if len(s.bands) > 0 && s.mode == store.TierModeNone {
		s.mode = store.TierModeGraduated
	}
	allowance := new(big.Rat)
	if it.Allowance != nil {
		a, err := parseRat(string(*it.Allowance))
		if err != nil {
			return s, fmt.Errorf("sku %s: allowance: %w", it.SKU, err)
		}
		allowance.Add(allowance, a)
	}
	if t.Contract != nil {
		for _, ci := range t.Contract.Items {
			if ci.SKU != it.SKU {
				continue
			}
			q, err := parseRat(string(ci.Quantity))
			if err != nil {
				return s, fmt.Errorf("contract line %s: quantity: %w", ci.SKU, err)
			}
			switch ci.Kind {
			case store.ContractItemAllowance:
				// A negotiated allowance is ON TOP of the plan's: the
				// contract adds to what the plan includes, it does not
				// silently replace it.
				allowance.Add(allowance, q)
			case store.ContractItemCommitment:
				s.committed = q
				price, err := commitmentPrice(ci, s, q)
				if err != nil {
					return s, err
				}
				s.committedPrice = price
			}
		}
	}
	if c, ok := t.CarryIn[it.SKU]; ok {
		r, err := parseRat(string(c))
		if err != nil {
			return s, fmt.Errorf("sku %s: carried allowance: %w", it.SKU, err)
		}
		allowance.Add(allowance, r)
	}
	if allowance.Sign() > 0 {
		s.allowance = allowance
	}
	return s, nil
}

// commitmentPrice is the rate a committed-use line rates at: the negotiated
// price when one is written, else the item's list price less the agreed
// percentage. For a tiered item the "list price" a percentage comes off is
// the price of the band the committed quantity itself reaches — a commitment
// is priced against the rate that quantity would otherwise have earned.
func commitmentPrice(ci store.ContractItem, s shape, qty *big.Rat) (*big.Rat, error) {
	if ci.CommittedPrice != nil && strings.TrimSpace(string(*ci.CommittedPrice)) != "" {
		p, err := parseRat(string(*ci.CommittedPrice))
		if err != nil {
			return nil, fmt.Errorf("contract line %s: committed price: %w", ci.SKU, err)
		}
		return p, nil
	}
	if ci.DiscountPct == nil {
		return nil, fmt.Errorf("contract line %s: a committed-use line needs a committed price or a discount percentage", ci.SKU)
	}
	pct, err := parseRat(string(*ci.DiscountPct))
	if err != nil {
		return nil, fmt.Errorf("contract line %s: discount percentage: %w", ci.SKU, err)
	}
	list := s.unitPrice
	if len(s.bands) > 0 {
		list = priceAt(s.bands, qty)
	}
	keep := new(big.Rat).Sub(big.NewRat(1, 1), new(big.Rat).Quo(pct, big.NewRat(100, 1)))
	if keep.Sign() < 0 {
		keep = new(big.Rat)
	}
	return new(big.Rat).Mul(list, keep), nil
}

// ApplyTerms reshapes a period's rated lines by the commercial terms — step 1
// to 3 of the order of operations — and reports what each shape did.
//
// The shapes are per SKU per BILLING PERIOD, not per source: an allowance of
// 50 GB is 50 GB of the customer's month, however many projects reported it.
// So the SKU's whole quantity is rated at once and the amount is allocated
// back across its lines in proportion to their quantity, by largest
// remainder, so the lines of a SKU sum exactly to the SKU's figure.
//
// items names the shape of each SKU: a customer whose sources sit on two
// books takes the shape from the first source in source order that prices the
// SKU, because a shape belongs to the agreement, not to one project.
//
// A line whose SKU has no shape is returned UNCHANGED, byte for byte, which
// is every line of every book written before §15.
func ApplyTerms(lines []store.RatedLine, items map[string]store.PriceItem, t Terms) ([]store.RatedLine, []Breakdown, error) {
	if len(lines) == 0 {
		return lines, nil, nil
	}
	idx := map[string][]int{}
	var skus []string
	for i, l := range lines {
		if _, seen := idx[l.SKU]; !seen {
			skus = append(skus, l.SKU)
		}
		idx[l.SKU] = append(idx[l.SKU], i)
	}
	sort.Strings(skus)
	out := make([]store.RatedLine, len(lines))
	copy(out, lines)
	var applied []Breakdown
	for _, sku := range skus {
		item, ok := items[sku]
		if !ok {
			continue
		}
		sh, err := t.shapeFor(item)
		if err != nil {
			return nil, nil, err
		}
		if sh.inert() {
			continue
		}
		is := idx[sku]
		qty := new(big.Rat)
		weights := make([]*big.Rat, len(is))
		for k, i := range is {
			weights[k] = ratOf(lines[i].Quantity)
			qty.Add(qty, weights[k])
		}
		amount, br := sh.rate(qty)
		applied = append(applied, br)
		parts := allocate(amount, weights, qty)
		for k, i := range is {
			out[i].Amount = parts[k]
			// The unit price the line SHOWS is the rate it actually paid:
			// quantity × unit price = amount still holds on every line, so
			// an invoice can be read across.
			q := weights[k]
			if q.Sign() > 0 {
				out[i].UnitPrice = store.Decimal(roundRat(new(big.Rat).Quo(ratOf(parts[k]), q), 8))
			}
		}
	}
	return out, applied, nil
}

// LoadTerms reads the commercial terms governing a customer's period: the
// contract active on the first day of it, and the allowance carried in from
// the period before for the SKUs that roll over.
//
// CARRY-OVER IS ONE PERIOD DEEP and does not compound: what a period did not
// use is available in the next one, and then it is gone. That is the rule the
// console states and the tests pin — an unbounded bank would have to be
// materialised, and a silent bank nobody can see is worse than no bank.
func LoadTerms(ctx context.Context, st *store.Store, customerID string, from time.Time, items map[string]store.PriceItem) (Terms, error) {
	var t Terms
	day := from.Format("2006-01-02")
	contract, found, err := st.ActiveContractAt(ctx, customerID, day)
	if err != nil {
		return t, fmt.Errorf("contract: %w", err)
	}
	if found {
		t.Contract = &contract
	}
	// Which SKUs roll over: the plan's items and the contract's allowance
	// lines each carry the flag.
	rollover := map[string]*big.Rat{}
	for sku, it := range items {
		if !it.AllowanceRollover || it.Allowance == nil {
			continue
		}
		a, err := parseRat(string(*it.Allowance))
		if err != nil {
			return t, fmt.Errorf("sku %s: allowance: %w", sku, err)
		}
		rollover[sku] = a
	}
	if t.Contract != nil {
		for _, ci := range t.Contract.Items {
			if ci.Kind != store.ContractItemAllowance || !ci.Rollover {
				continue
			}
			q, err := parseRat(string(ci.Quantity))
			if err != nil {
				return t, fmt.Errorf("contract line %s: quantity: %w", ci.SKU, err)
			}
			if rollover[ci.SKU] == nil {
				rollover[ci.SKU] = new(big.Rat)
			}
			rollover[ci.SKU].Add(rollover[ci.SKU], q)
		}
	}
	if len(rollover) == 0 {
		return t, nil
	}
	prevFrom := from.AddDate(0, -1, 0)
	prev, err := st.UsageForRating(ctx, customerID, prevFrom, from)
	if err != nil {
		return t, fmt.Errorf("previous period usage: %w", err)
	}
	usedBySKU := map[string]*big.Rat{}
	for _, u := range prev {
		if usedBySKU[u.SKU] == nil {
			usedBySKU[u.SKU] = new(big.Rat)
		}
		usedBySKU[u.SKU].Add(usedBySKU[u.SKU], ratOf(u.Quantity))
	}
	t.CarryIn = map[string]store.Decimal{}
	for sku, allowance := range rollover {
		unused := new(big.Rat).Set(allowance)
		if used := usedBySKU[sku]; used != nil {
			unused.Sub(unused, used)
		}
		if unused.Sign() <= 0 {
			continue
		}
		t.CarryIn[sku] = store.Decimal(roundRat(unused, 6))
	}
	if len(t.CarryIn) == 0 {
		t.CarryIn = nil
	}
	return t, nil
}

// TrueUp is step 5: the shortfall line a period below the contract's monthly
// MINIMUM COMMITMENT carries.
//
// The comparison is against the NET of the period — the rated lines less the
// discounts, which is what the customer would otherwise pay — so a discount
// cannot be used to slide under a minimum that was agreed in the same
// contract. The line brings the net subtotal up to exactly the minimum, and
// tax is then charged on that, because the true-up is a charge for the
// service like any other.
//
// ok is false when the period is at or above the minimum: there is nothing to
// invoice, and a zero line on a bill is noise.
func TrueUp(lines []store.RatedLine, discount store.Decimal, minimum store.Decimal) (store.RatedLine, bool, error) {
	min, err := parseRat(string(minimum))
	if err != nil {
		return store.RatedLine{}, false, fmt.Errorf("minimum commitment: %w", err)
	}
	if min.Sign() <= 0 {
		return store.RatedLine{}, false, nil
	}
	net := new(big.Rat)
	for _, l := range lines {
		a, err := parseRat(string(l.Amount))
		if err != nil {
			return store.RatedLine{}, false, fmt.Errorf("line %s: %w", l.SKU, err)
		}
		net.Add(net, a)
	}
	d, err := parseRat(string(discount))
	if err != nil {
		return store.RatedLine{}, false, fmt.Errorf("discount: %w", err)
	}
	net.Sub(net, d)
	shortfall := new(big.Rat).Sub(min, net)
	if shortfall.Sign() <= 0 {
		return store.RatedLine{}, false, nil
	}
	amount := store.Decimal(roundRat(shortfall, 6))
	return store.RatedLine{
		SKU:       TrueUpSKU,
		Quantity:  "1",
		Unit:      TrueUpUnit,
		UnitPrice: amount,
		Amount:    amount,
	}, true, nil
}

// ValidateTiers checks an item's bands the way the engine reads them and
// returns them normalised. The console and the API call it so an operator
// hears about an overlapping or out-of-order ladder while typing, and never
// discovers it on an issued bill.
func ValidateTiers(it store.PriceItem) ([]store.PriceTier, error) {
	if _, err := bandsOf(it); err != nil {
		return nil, err
	}
	return it.Tiers, nil
}

// ExplainItem is the item's effective price in words — the sentence the price
// book editor prints under a tiered or allowance-bearing row, and the one a
// statement footnote can reuse. It states exactly what the engine does, in
// the engine's order, so the words and the arithmetic cannot drift.
func ExplainItem(it store.PriceItem, currency string) string {
	var parts []string
	if it.Allowance != nil && ratOf(*it.Allowance).Sign() > 0 {
		roll := "; unused allowance lapses at the end of the period"
		if it.AllowanceRollover {
			roll = "; unused allowance carries into the next period, once"
		}
		parts = append(parts, fmt.Sprintf("the first %s %s each period are included%s", trimZeros(string(*it.Allowance)), it.Unit, roll))
	}
	if it.HasTiers() {
		bands, err := bandsOf(it)
		if err != nil {
			return err.Error()
		}
		var words []string
		for _, b := range bands {
			rate := fmt.Sprintf("%s %s per %s", trimZeros(b.price.FloatString(8)), currency, it.Unit)
			switch {
			case b.upTo == nil && b.from.Sign() == 0:
				words = append(words, "every unit at "+rate)
			case b.upTo == nil:
				words = append(words, fmt.Sprintf("above %s at %s", trimZeros(b.from.FloatString(6)), rate))
			case b.from.Sign() == 0:
				words = append(words, fmt.Sprintf("up to %s at %s", trimZeros(b.upTo.FloatString(6)), rate))
			default:
				words = append(words, fmt.Sprintf("%s-%s at %s", trimZeros(b.from.FloatString(6)), trimZeros(b.upTo.FloatString(6)), rate))
			}
		}
		if it.TierMode == store.TierModeAllUnits {
			parts = append(parts, "the WHOLE billable quantity rates at the band it reaches: "+strings.Join(words, ", "))
		} else {
			parts = append(parts, "each band rates at its own price: "+strings.Join(words, ", "))
		}
	} else {
		parts = append(parts, fmt.Sprintf("the rest at %s %s per %s", trimZeros(string(it.UnitPrice)), currency, it.Unit))
	}
	return capitalise(strings.Join(parts, "; ")) + "."
}

func trimZeros(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
