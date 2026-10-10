package rating

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The package is a prepaid monthly commitment (DESIGN.md §22.11). These
// tests pin Commitment, the pure function behind every package line, on the
// founder's cases: the first statement after a checkout, a steady month, an
// upgrade and a downgrade inside a month, a cancellation, a suspension, a
// resumption, and a flat month of 28 days. Prices are the workbook's: S
// 2.490, M 4.490, L 7.990 a month (annual 29.88 / 53.88 / 95.88).

func planItems() map[string]store.PriceItem {
	item := func(sku, annual string) store.PriceItem {
		a := store.Decimal(annual)
		up, _ := UnitPrice(annual, 8760)
		return store.PriceItem{SKU: sku, Unit: store.PlanUnit, UnitPrice: up, AnnualPrice: &a}
	}
	return map[string]store.PriceItem{
		"plan.s": item("plan.s", "29.88"),
		"plan.m": item("plan.m", "53.88"),
		"plan.l": item("plan.l", "95.88"),
	}
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func span(slug string, from, to time.Time) store.PlanSpan {
	return store.PlanSpan{Slug: slug, Start: from, End: to, Hours: store.Decimal(fmt.Sprintf("%d", int(to.Sub(from).Hours())))}
}

func commitIn(from time.Time, spans ...store.PlanSpan) CommitmentInput {
	return CommitmentInput{
		Source: store.CostSource{ID: "src", Layer: store.LayerPlatform, Status: "verified"},
		From:   from, To: from.AddDate(0, 1, 0), Timeline: spans,
		Active: true, Items: planItems(), Divisor: 8760, Currency: "OMR",
	}
}

type wantLine struct{ sku, qty, unit, unitPrice, amount, svcFrom, svcTo, desc string }

func checkLines(t *testing.T, got []store.RatedLine, want []wantLine) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d lines %+v, want %d", len(got), got, len(want))
	}
	for i, w := range want {
		l := got[i]
		svc := func(p *time.Time) string {
			if p == nil {
				return ""
			}
			return p.Format("2006-01-02")
		}
		if l.SKU != w.sku || string(l.Quantity) != w.qty || l.Unit != w.unit || string(l.UnitPrice) != w.unitPrice || string(l.Amount) != w.amount ||
			svc(l.ServiceFrom) != w.svcFrom || svc(l.ServiceTo) != w.svcTo || !strings.Contains(l.Description, w.desc) {
			t.Fatalf("line %d = %s %s %s × %s = %s [%s..%s] %q\nwant %+v", i, l.SKU, l.Quantity, l.Unit, l.UnitPrice, l.Amount, svc(l.ServiceFrom), svc(l.ServiceTo), l.Description, w)
		}
	}
}

// The flat monthly price is the annual price ÷ 12, whatever the month's
// length: M 53.88 ÷ 12 = 4.490 in October (744 h) and February (672 h).
func TestMonthlyPriceIsFlat(t *testing.T) {
	m, err := MonthlyPrice(planItems()["plan.m"], 8760)
	if err != nil || m.FloatString(6) != "4.490000" {
		t.Fatalf("M monthly = %v %v, want 4.490000", m, err)
	}
	// An item stored without its annual price: 0.00512557 × 8760 ÷ 12 =
	// 3.741666… → 3.741666 (rounded once).
	m, _ = MonthlyPrice(store.PriceItem{UnitPrice: "0.00512557"}, 8760)
	if m.FloatString(6) != "3.741666" {
		t.Fatalf("unit-priced monthly = %s", m.FloatString(6))
	}
}

// Month 1 — the order is placed on 10 October and the checkout collects the
// M package, 4.490. The statement for October carries:
//
//	plan.m  10 to 31 October, 528 of 744 h × (4.490 ÷ 744 = 0.00603495)  = 3.186454
//	plan.m  November, in advance, 1 month × 4.490                        = 4.490000
//	checkout.credit  the 4.490 the checkout collected                    = −4.490000
//	                                                     package lines  = 3.186454
func TestFirstStatementCreditsTheCheckout(t *testing.T) {
	in := commitIn(day(2026, 10, 1), span("m", day(2026, 10, 10), day(2026, 11, 1)))
	in.FirstPeriod = true
	paid := store.Decimal("4.490")
	in.Source.PrepaidAmount = &paid
	lines, unpriced, err := Commitment(in)
	if err != nil || len(unpriced) != 0 {
		t.Fatalf("err %v unpriced %v", err, unpriced)
	}
	checkLines(t, lines, []wantLine{
		{"plan.m", "528.000000", "hour", "0.00603495", "3.186454", "2026-10-10", "2026-10-31", "M package — 10 to 31 October 2026, 528 of 744 hours (the 4.490 monthly price prorated by the hour)"},
		{"plan.m", "1", "month", "4.49000000", "4.490000", "2026-11-01", "2026-11-30", "M package — November 2026, billed in advance"},
		{CheckoutCreditSKU, "1", "period", "-4.49000000", "-4.490000", "", "", "Paid at checkout — 4.490 OMR for the M package, credited"},
	})
	// Not the first period: no credit, whatever the Source carries.
	in.FirstPeriod = false
	lines, _, _ = Commitment(in)
	if len(lines) != 2 {
		t.Fatalf("a later period credited the checkout again: %+v", lines)
	}
}

// A steady month: November was paid on October's statement, so November's
// statement carries December in advance and nothing for November.
func TestSteadyMonthBillsOnlyTheNextMonthInAdvance(t *testing.T) {
	in := commitIn(day(2026, 11, 1), span("m", day(2026, 11, 1), day(2026, 12, 1)))
	in.Prepaid = &store.PrepaidPeriod{PlanSlug: "m", Amount: "4.490000"}
	lines, _, err := Commitment(in)
	if err != nil {
		t.Fatal(err)
	}
	checkLines(t, lines, []wantLine{
		{"plan.m", "1", "month", "4.49000000", "4.490000", "2026-12-01", "2026-12-31", "M package — December 2026, billed in advance"},
	})
}

// An upgrade takes effect at once: M paid for December, L from 16 December.
// The difference only, prorated by the hour for what is left of the month:
//
//	plan.l  upgrade, 384 of 744 h × (3.500 ÷ 744 = 0.00470430)  = 1.806451
//	plan.l  January, in advance                                 = 7.990000
func TestUpgradeBillsTheDifferenceForTheRestOfTheMonth(t *testing.T) {
	in := commitIn(day(2026, 12, 1), span("m", day(2026, 12, 1), day(2026, 12, 16)), span("l", day(2026, 12, 16), day(2027, 1, 1)))
	in.Prepaid = &store.PrepaidPeriod{PlanSlug: "m", Amount: "4.490000"}
	lines, _, err := Commitment(in)
	if err != nil {
		t.Fatal(err)
	}
	checkLines(t, lines, []wantLine{
		{"plan.l", "384.000000", "hour", "0.00470430", "1.806451", "2026-12-16", "2026-12-31", "Upgrade M → L — 16 to 31 December 2026, 384 of 744 hours at the 3.500 monthly difference"},
		{"plan.l", "1", "month", "7.99000000", "7.990000", "2027-01-01", "2027-01-31", "L package — January 2027, billed in advance"},
	})
	// Up, down and up again inside one month: the second L was paid already.
	in.Timeline = []store.PlanSpan{span("m", day(2026, 12, 1), day(2026, 12, 11)), span("l", day(2026, 12, 11), day(2026, 12, 16)),
		span("m", day(2026, 12, 16), day(2026, 12, 21)), span("l", day(2026, 12, 21), day(2027, 1, 1))}
	lines, _, _ = Commitment(in)
	if len(lines) != 2 || lines[0].Quantity != "120.000000" {
		t.Fatalf("up-down-up = %+v, want one 120 h difference and the advance", lines)
	}
}

// A downgrade takes effect at the next period: L paid for January, S from
// 11 January. Nothing is refunded, nothing is billed for January, and
// February is billed in advance at S.
func TestDowngradeTakesEffectAtTheNextPeriod(t *testing.T) {
	in := commitIn(day(2027, 1, 1), span("l", day(2027, 1, 1), day(2027, 1, 11)), span("s", day(2027, 1, 11), day(2027, 2, 1)))
	in.Prepaid = &store.PrepaidPeriod{PlanSlug: "l", Amount: "7.990000"}
	lines, _, err := Commitment(in)
	if err != nil {
		t.Fatal(err)
	}
	checkLines(t, lines, []wantLine{
		{"plan.s", "1", "month", "2.49000000", "2.490000", "2027-02-01", "2027-02-28", "S package — February 2027, billed in advance"},
	})
}

// A cancellation stops at the period end: February was paid, the
// Organization is gone (its customer suspended) when February is rated — no
// refund, and no March in advance. The same when the Organization ran to the
// last hour of February.
func TestCancellationStopsAtThePeriodEnd(t *testing.T) {
	for _, end := range []time.Time{day(2027, 2, 20), day(2027, 3, 1)} {
		in := commitIn(day(2027, 2, 1), span("m", day(2027, 2, 1), end))
		in.Prepaid = &store.PrepaidPeriod{PlanSlug: "m", Amount: "4.490000"}
		in.Active = false
		lines, _, err := Commitment(in)
		if err != nil || len(lines) != 0 {
			t.Fatalf("cancelled at %s: %+v %v, want nothing", end, lines, err)
		}
	}
}

// A suspended Source is not billed in advance: March was paid, the Source
// was suspended on 10 March and is still suspended when March is rated.
// When it resumes on 15 April, April was not paid in advance, so April's
// statement bills the hours it ran and May in advance:
//
//	plan.m  15 to 30 April, 384 of 720 h × (4.490 ÷ 720 = 0.00623611)  = 2.394666
//	plan.m  May, in advance                                           = 4.490000
func TestSuspendedSourceIsNotBilledInAdvance(t *testing.T) {
	in := commitIn(day(2027, 3, 1), span("m", day(2027, 3, 1), day(2027, 3, 10)))
	in.Prepaid = &store.PrepaidPeriod{PlanSlug: "m", Amount: "4.490000"}
	lines, _, _ := Commitment(in)
	if len(lines) != 0 {
		t.Fatalf("suspended from 10 March: %+v, want nothing (no April in advance)", lines)
	}
	// Still active per the customer, but the Source disabled: no advance.
	in = commitIn(day(2027, 3, 1), span("m", day(2027, 3, 1), day(2027, 4, 1)))
	in.Prepaid = &store.PrepaidPeriod{PlanSlug: "m", Amount: "4.490000"}
	in.Source.Status = store.StatusDisabled
	if lines, _, _ := Commitment(in); len(lines) != 0 {
		t.Fatalf("disabled Source: %+v", lines)
	}
	in = commitIn(day(2027, 4, 1), span("m", day(2027, 4, 15), day(2027, 5, 1)))
	lines, _, err := Commitment(in)
	if err != nil {
		t.Fatal(err)
	}
	checkLines(t, lines, []wantLine{
		{"plan.m", "384.000000", "hour", "0.00623611", "2.394666", "2027-04-15", "2027-04-30", "M package — 15 to 30 April 2027, 384 of 720 hours"},
		{"plan.m", "1", "month", "4.49000000", "4.490000", "2027-05-01", "2027-05-31", "M package — May 2027, billed in advance"},
	})
}

// A whole month not paid in advance — the first statement of a Source rated
// before this model — is the FLAT price, never hours × an hourly rate:
// February (672 h) bills M at exactly 4.490, and March in advance.
func TestWholeMonthNotPaidInAdvanceIsFlat(t *testing.T) {
	in := commitIn(day(2027, 2, 1), span("m", day(2027, 2, 1), day(2027, 3, 1)))
	lines, _, err := Commitment(in)
	if err != nil {
		t.Fatal(err)
	}
	checkLines(t, lines, []wantLine{
		{"plan.m", "1", "month", "4.49000000", "4.490000", "2027-02-01", "2027-02-28", "M package — February 2027 (not billed in advance)"},
		{"plan.m", "1", "month", "4.49000000", "4.490000", "2027-03-01", "2027-03-31", "M package — March 2027, billed in advance"},
	})
}

// A plan the book does not price is reported, never billed at nothing.
func TestCommitmentReportsAnUnpricedPlan(t *testing.T) {
	in := commitIn(day(2026, 10, 1), span("xl", day(2026, 10, 1), day(2026, 11, 1)))
	lines, unpriced, err := Commitment(in)
	if err != nil || len(lines) != 0 || len(unpriced) != 1 || unpriced[0] != "plan.xl" {
		t.Fatalf("lines %+v unpriced %v err %v", lines, unpriced, err)
	}
}
