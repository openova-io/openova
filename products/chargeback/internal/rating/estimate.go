package rating

import (
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Public estimates (DESIGN.md §11). An estimate is a month of usage a
// prospect describes in the public calculator, priced EXACTLY as a statement
// would price the same month: every line becomes one RatableUsage row — the
// aggregate the ledger would hold after a month of hourly records — and goes
// through Rate and Totals, the two functions the statement run calls. There
// is no pricing logic in this file: it aggregates, hands over, and reads the
// result. TestPriceEstimateEqualsRate and the API integration test pin that
// an estimate line equals the rated usage line for the same SKU, quantity
// and hours.

// HoursPerMonth is the normalised month of an estimate: 8760 / 12, the same
// figure the platform books derive a monthly price from (monthly = unit_price
// × 730 exactly as their divisor implies).
const HoursPerMonth = "730"

// EstimateLine is one requested line: an SKU of the catalog, how many of it,
// how many hours a month each runs (default HoursPerMonth) and, for a plan
// line, how many months (default 1; an SKU line is always 1).
type EstimateLine struct {
	SKU      string
	Quantity store.Decimal
	Hours    store.Decimal
	Months   int
}

// EstimateResult is what pricing a request produced. Lines are in request
// order, one per known SKU; Unknown names the SKUs the catalog does not
// price, sorted, so the API can refuse the request naming them.
//
// Subtotal / Tax / Total cover the whole term of every line (a plan line
// over 3 months counts three times). Monthly is the recurring month —
// every line divided by its months, then taxed — so an estimate whose lines
// are all one month has Monthly == Total exactly; Yearly is 12 × Monthly.
type EstimateResult struct {
	Lines    []store.RatedLine
	Subtotal store.Decimal
	Tax      store.Decimal
	Total    store.Decimal
	Monthly  store.Decimal
	Yearly   store.Decimal
	Unknown  []string
}

// PriceEstimate prices lines against a catalog's items through Rate — the
// same function, with the same stopped-instance policy, that prices a
// customer's month — and totals them through Totals at taxRate. The catalog
// is whatever items the caller assembled (the public book, the plans book,
// the pay-per-use book): choosing the book is the caller's decision, pricing
// is not.
func PriceEstimate(items map[string]store.PriceItem, policy string, lines []EstimateLine, taxRate store.Decimal) (EstimateResult, error) {
	var res EstimateResult
	unknown := map[string]bool{}
	usage := make([]store.RatableUsage, 0, len(lines))
	months := make([]int, 0, len(lines))
	for i, l := range lines {
		sku := strings.TrimSpace(l.SKU)
		item, ok := items[sku]
		if !ok {
			unknown[sku] = true
			continue
		}
		qty, err := ratedQuantity(l)
		if err != nil {
			return res, fmt.Errorf("line %d (%s): %w", i+1, sku, err)
		}
		m := l.Months
		if m <= 0 {
			m = 1
		}
		usage = append(usage, store.RatableUsage{SKU: sku, Unit: item.Unit, Quantity: qty, ResourceCount: resourceCount(l.Quantity)})
		months = append(months, m)
	}
	res.Unknown = sortedSet(unknown)
	rated, unpriced, err := Rate(usage, items, policy)
	if err != nil {
		return res, err
	}
	if len(unpriced) > 0 {
		// Cannot happen — every usage row above has an item — but a silent
		// gap here would be a second pricing path by omission.
		return res, fmt.Errorf("rating left %v unpriced", unpriced)
	}
	res.Lines = rated
	if res.Subtotal, res.Tax, res.Total, err = Totals(rated, taxRate); err != nil {
		return res, err
	}
	// The recurring month: each line over its term, then taxed exactly as
	// Totals taxes the term, so all-monthly lines give Monthly == Total.
	perMonth := make([]store.Decimal, len(rated))
	for i, l := range rated {
		if perMonth[i], err = divInt(l.Amount, months[i]); err != nil {
			return res, err
		}
	}
	monthlySub, err := Sum(perMonth...)
	if err != nil {
		return res, err
	}
	monthlyTax, err := Tax(monthlySub, taxRate)
	if err != nil {
		return res, err
	}
	if res.Monthly, err = Sum(monthlySub, monthlyTax); err != nil {
		return res, err
	}
	res.Yearly, err = Amount(res.Monthly, "12")
	return res, err
}

// ratedQuantity is what the ledger would hold after the month: quantity ×
// hours × months, exact, at the 6 decimals usage_records carries.
func ratedQuantity(l EstimateLine) (store.Decimal, error) {
	q, err := parseRat(string(l.Quantity))
	if err != nil {
		return "", fmt.Errorf("quantity: %w", err)
	}
	if q.Sign() <= 0 {
		return "", fmt.Errorf("quantity must be positive")
	}
	hours := string(l.Hours)
	if strings.TrimSpace(hours) == "" {
		hours = HoursPerMonth
	}
	hr, err := parseRat(hours)
	if err != nil {
		return "", fmt.Errorf("hours: %w", err)
	}
	if hr.Sign() <= 0 {
		return "", fmt.Errorf("hours must be positive")
	}
	m := l.Months
	if m <= 0 {
		m = 1
	}
	total := new(big.Rat).Mul(q, hr)
	total.Mul(total, big.NewRat(int64(m), 1))
	return store.Decimal(roundRat(total, 6)), nil
}

// resourceCount is the integral part of the requested quantity — the
// resource_count a statement line would carry for that many instances.
func resourceCount(q store.Decimal) int {
	r, err := parseRat(string(q))
	if err != nil || r.Sign() <= 0 {
		return 0
	}
	n := new(big.Int).Quo(r.Num(), r.Denom())
	if !n.IsInt64() || n.Int64() > 1<<31-1 {
		return 0
	}
	return int(n.Int64())
}

// divInt is a ÷ n at 6 decimals.
func divInt(a store.Decimal, n int) (store.Decimal, error) {
	if n <= 0 {
		n = 1
	}
	x, err := parseRat(string(a))
	if err != nil {
		return "", err
	}
	return store.Decimal(roundRat(new(big.Rat).Quo(x, big.NewRat(int64(n), 1)), 6)), nil
}

func sortedSet(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
