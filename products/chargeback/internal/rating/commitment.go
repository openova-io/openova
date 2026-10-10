package rating

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// THE PACKAGE IS A PREPAID MONTHLY COMMITMENT (DESIGN.md §22.11, founder
// direction 2026-10-10). The statement issued for period N carries:
//
//   - the package for period N+1 IN ADVANCE — one line, quantity 1, unit
//     "month", at the package's FLAT monthly price (the book's annual price ÷
//     12: M 53.88 ÷ 12 = 4.490 in a month of 28, 30 or 31 days alike), its
//     service period stamped on the line. Written only when the Source was
//     on a package at the end of N and its customer is active when N is
//     rated: a suspended or cancelled Organization is not billed ahead;
//   - for period N itself, only what was NOT already paid in advance:
//
//     prepaid on N−1's statement   nothing, unless the Source moved UP:
//     an upgrade bills the monthly DIFFERENCE prorated by the hour for the
//     hours left on the higher package; a downgrade bills nothing and
//     refunds nothing (the commitment for N was made; the lower package
//     is billed from N+1 on, in advance)
//
//     not prepaid (the first period, a period resumed after a suspension,
//     or the first statement of a Source rated before this model)  each
//     plan span at its monthly price prorated by the hour — or, when one
//     package ran the whole period, the flat monthly price
//
//   - on the Source's FIRST period, a credit for what the checkout already
//     collected (spec.commerce.prepaidAmount), at most the package lines of
//     this statement, so the package is never charged twice;
//   - the usage above the package for period N, in arrears (GrowOverage and
//     the allowance path, unchanged).
//
// PRORATION IS BY THE HOUR: the monthly price over the hours of the month
// (744 in October, 672 in February) is the unit price of a prorated line,
// rounded to 8 decimals like every unit price in BSS, and its quantity is
// the plan-hours the collector wrote. The flat lines are never hourly.

// Units of the commitment lines.
const (
	// CommitmentUnitMonth is the unit of a flat package line.
	CommitmentUnitMonth = "month"
	// CommitmentUnitHour is the unit of a prorated package line.
	CommitmentUnitHour = "hour"
	// CheckoutCreditSKU is the SKU of the credit for what the checkout
	// collected.
	CheckoutCreditSKU = "checkout.credit"
)

// CommitmentInput is everything the commitment lines of one platform Source
// in one period are computed from. The caller loads it; Commitment is pure.
type CommitmentInput struct {
	Source store.CostSource
	// From / To bound the period N, [From, To).
	From, To time.Time
	// Timeline is the Source's plan spans inside N, in time order.
	Timeline []store.PlanSpan
	// Prepaid is the advance line an earlier statement wrote for N, nil
	// when N was not paid in advance.
	Prepaid *store.PrepaidPeriod
	// FirstPeriod is true when the Source has no plan-hour before From.
	FirstPeriod bool
	// Active is whether the customer is active when N is rated — the
	// condition for billing N+1 in advance.
	Active bool
	// Items are the Source's book items; Divisor its annual divisor.
	Items   map[string]store.PriceItem
	Divisor int
	// Currency is the book's, named on the checkout credit.
	Currency string
}

// MonthlyPrice is a book item's flat monthly price: its annual price ÷ 12,
// or, for an item stored without an annual price, unit price × divisor ÷ 12,
// rounded to 6 decimals once.
func MonthlyPrice(item store.PriceItem, divisor int) (*big.Rat, error) {
	var annual *big.Rat
	if item.AnnualPrice != nil && strings.TrimSpace(string(*item.AnnualPrice)) != "" {
		a, err := parseRat(string(*item.AnnualPrice))
		if err != nil {
			return nil, err
		}
		annual = a
	} else {
		up, err := parseRat(string(item.UnitPrice))
		if err != nil {
			return nil, err
		}
		annual = new(big.Rat).Mul(up, big.NewRat(int64(divisor), 1))
	}
	m, _ := parseRat(roundRat(new(big.Rat).Quo(annual, big.NewRat(12, 1)), 6))
	return m, nil
}

// Commitment returns the package lines of one Source for period N and the
// plan SKUs its book does not price.
func Commitment(in CommitmentInput) ([]store.RatedLine, []string, error) {
	if len(in.Timeline) == 0 && in.Prepaid == nil {
		return nil, nil, nil
	}
	sid := in.Source.ID
	periodHours := new(big.Rat).SetFrac64(int64(in.To.Sub(in.From)/time.Second), 3600)
	var unpriced []string
	seenUnpriced := map[string]bool{}
	monthly := func(slug string) (*big.Rat, bool, error) {
		item, ok := in.Items[store.PlanSKU(slug)]
		if !ok {
			if !seenUnpriced[slug] {
				seenUnpriced[slug] = true
				unpriced = append(unpriced, store.PlanSKU(slug))
			}
			return nil, false, nil
		}
		m, err := MonthlyPrice(item, in.Divisor)
		return m, err == nil, err
	}
	var lines []store.RatedLine
	day := func(t time.Time) *time.Time { d := t.UTC().Truncate(24 * time.Hour); return &d }
	lastDay := func(end time.Time) *time.Time { return day(end.Add(-time.Nanosecond)) }

	// Period N itself: what the advance line of N−1 did not already pay.
	committed := new(big.Rat)
	committedSlug := ""
	if in.Prepaid != nil {
		a, err := parseRat(string(in.Prepaid.Amount))
		if err != nil {
			return nil, nil, fmt.Errorf("prepaid amount: %w", err)
		}
		committed, committedSlug = a, in.Prepaid.PlanSlug
	}
	for _, sp := range in.Timeline {
		price, ok, err := monthly(sp.Slug)
		if err != nil {
			return nil, nil, fmt.Errorf("plan %s: %w", sp.Slug, err)
		}
		if !ok {
			continue
		}
		diff := new(big.Rat).Sub(price, committed)
		if diff.Sign() <= 0 {
			// The same package, or a lower one: paid already, nothing is
			// refunded.
			continue
		}
		hours, err := parseRat(string(sp.Hours))
		if err != nil {
			return nil, nil, fmt.Errorf("plan %s hours: %w", sp.Slug, err)
		}
		whole := committed.Sign() == 0 && !sp.Start.After(in.From) && !sp.End.Before(in.To) && hours.Cmp(periodHours) >= 0
		if whole {
			amount := store.Decimal(roundRat(price, 6))
			lines = append(lines, store.RatedLine{
				SourceID: &sid, SKU: store.PlanSKU(sp.Slug), Quantity: "1", Unit: CommitmentUnitMonth,
				UnitPrice: store.Decimal(roundRat(price, 8)), Amount: amount,
				Description: fmt.Sprintf("%s package — %s (not billed in advance)", store.PlanName(sp.Slug), monthName(in.From)),
				ServiceFrom: day(in.From), ServiceTo: lastDay(in.To),
			})
		} else {
			unitPrice := store.Decimal(roundRat(new(big.Rat).Quo(diff, periodHours), 8))
			qty := store.Decimal(roundRat(hours, 6))
			amount, err := Amount(qty, unitPrice)
			if err != nil {
				return nil, nil, err
			}
			desc := fmt.Sprintf("%s package — %s, %s of %s hours (the %s monthly price prorated by the hour)",
				store.PlanName(sp.Slug), dayRange(sp.Start, sp.End), trimRat(hours), trimRat(periodHours), roundRat(price, 3))
			if committedSlug != "" {
				desc = fmt.Sprintf("Upgrade %s → %s — %s, %s of %s hours at the %s monthly difference",
					store.PlanName(committedSlug), store.PlanName(sp.Slug), dayRange(sp.Start, sp.End), trimRat(hours), trimRat(periodHours), roundRat(diff, 3))
			}
			lines = append(lines, store.RatedLine{
				SourceID: &sid, SKU: store.PlanSKU(sp.Slug), Quantity: qty, Unit: CommitmentUnitHour,
				UnitPrice: unitPrice, Amount: amount, Description: desc,
				ServiceFrom: day(sp.Start), ServiceTo: lastDay(sp.End),
			})
		}
		committed, committedSlug = price, sp.Slug
	}

	// Period N+1, in advance: the package the Source was on at the end of N.
	if n := len(in.Timeline); n > 0 && in.Active && in.Source.Status != store.StatusDisabled && !in.Timeline[n-1].End.Before(in.To) {
		slug := in.Timeline[n-1].Slug
		price, ok, err := monthly(slug)
		if err != nil {
			return nil, nil, fmt.Errorf("plan %s: %w", slug, err)
		}
		if ok {
			next := in.To.AddDate(0, 1, 0)
			lines = append(lines, store.RatedLine{
				SourceID: &sid, SKU: store.PlanSKU(slug), Quantity: "1", Unit: CommitmentUnitMonth,
				UnitPrice: store.Decimal(roundRat(price, 8)), Amount: store.Decimal(roundRat(price, 6)),
				Description: fmt.Sprintf("%s package — %s, billed in advance", store.PlanName(slug), monthName(in.To)),
				ServiceFrom: day(in.To), ServiceTo: lastDay(next),
			})
		}
	}

	// The checkout's credit, on the first period only, never more than the
	// package lines above.
	if in.FirstPeriod && len(in.Timeline) > 0 && in.Source.PrepaidAmount != nil {
		paid, err := parseRat(string(*in.Source.PrepaidAmount))
		if err != nil {
			return nil, nil, fmt.Errorf("prepaid amount: %w", err)
		}
		lineSum := new(big.Rat)
		for _, l := range lines {
			a, _ := parseRat(string(l.Amount))
			lineSum.Add(lineSum, a)
		}
		credit := paid
		if credit.Cmp(lineSum) > 0 {
			credit = lineSum
		}
		if credit.Sign() > 0 {
			neg := new(big.Rat).Neg(credit)
			lines = append(lines, store.RatedLine{
				SourceID: &sid, SKU: CheckoutCreditSKU, Quantity: "1", Unit: IncludedUnit,
				UnitPrice: store.Decimal(roundRat(neg, 8)), Amount: store.Decimal(roundRat(neg, 6)),
				Description: fmt.Sprintf("Paid at checkout — %s %s for the %s package, credited", roundRat(paid, 3), in.Currency, store.PlanName(in.Timeline[0].Slug)),
			})
		}
	}
	return lines, unpriced, nil
}

// monthName renders the month a period starts in: "October 2026".
func monthName(t time.Time) string { return t.UTC().Format("January 2006") }

// dayRange renders [start, end) as days: "10 to 31 October 2026", or a
// single day. end is exclusive.
func dayRange(start, end time.Time) string {
	s, e := start.UTC(), end.UTC().Add(-time.Nanosecond)
	if s.Year() == e.Year() && s.Month() == e.Month() {
		if s.Day() == e.Day() {
			return s.Format("2 January 2006")
		}
		return fmt.Sprintf("%d to %s", s.Day(), e.Format("2 January 2006"))
	}
	return fmt.Sprintf("%s to %s", s.Format("2 January 2006"), e.Format("2 January 2006"))
}
