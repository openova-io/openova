package report

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// topStatementLines is how many rated lines the notification lists.
const topStatementLines = 5

// StatementPeriodLabel names a statement's inclusive period: "August 2026"
// for a whole calendar month, else the day range.
func StatementPeriodLabel(periodStart, periodEnd string) string {
	from, err1 := time.Parse("2006-01-02", strings.TrimSpace(periodStart))
	end, err2 := time.Parse("2006-01-02", strings.TrimSpace(periodEnd))
	if err1 != nil || err2 != nil || end.Before(from) {
		return periodStart + " – " + periodEnd
	}
	lastOfMonth := from.AddDate(0, 1, -from.Day())
	if from.Day() == 1 && end.Equal(lastOfMonth) {
		return from.Format("January 2006")
	}
	return WindowLabel(from, end.AddDate(0, 0, 1))
}

// RenderStatement is the plain-text notification mailed when a statement is
// issued: the period, the waterfall (list → discount → net → tax → total),
// the biggest lines and a link to the statement. Amounts are the frozen
// statement figures, rendered at the currency's minor unit; list subtotal is
// net + discount computed exactly.
func RenderStatement(st store.Statement, link string) (subject, body string) {
	cur := st.Currency
	period := StatementPeriodLabel(st.PeriodStart, st.PeriodEnd)
	customer := strings.TrimSpace(st.CustomerName)
	if customer == "" {
		customer = "your account"
	}
	subject = clip(fmt.Sprintf("Statement for %s — %s: %s %s", clip(customer, 30), period, money(st.Total, cur), cur), MaxLineWidth)

	list := addDec(st.Subtotal, st.DiscountTotal)
	var w writer
	w.line("Your statement for %s has been issued.", period)
	w.blank()
	w.line("Customer:  %s", clip(customer, 60))
	w.line("Period:    %s (%s to %s)", period, st.PeriodStart, st.PeriodEnd)
	if st.IssuedAt != nil {
		w.line("Issued:    %s", st.IssuedAt.UTC().Format("2 Jan 2006 15:04 UTC"))
	}
	w.blank()
	w.line("  %-22s %16s %s", "List subtotal", money(list, cur), cur)
	w.line("  %-22s %16s %s", "Discounts", money(negate(st.DiscountTotal), cur), cur)
	w.line("  %-22s %16s %s", "Net subtotal", money(st.Subtotal, cur), cur)
	w.line("  %-22s %16s %s", "Tax ("+pctF(taxPct(st.TaxRate), false)+")", money(st.Tax, cur), cur)
	w.line("  %-22s %16s %s", "TOTAL", money(st.Total, cur), cur)

	if len(st.Lines) > 0 {
		lines := append([]store.RatedLine(nil), st.Lines...)
		sort.SliceStable(lines, func(i, j int) bool {
			c := ratOf(lines[i].Amount).Cmp(ratOf(lines[j].Amount))
			if c != 0 {
				return c > 0
			}
			return lines[i].SKU < lines[j].SKU
		})
		w.blank()
		if len(lines) > topStatementLines {
			w.line("Largest lines (%d of %d):", topStatementLines, len(lines))
			lines = lines[:topStatementLines]
		} else {
			w.line("Lines:")
		}
		for _, l := range lines {
			w.line("  %-28s %13s %-14s %13s", clip(l.SKU, 28), trimDec(l.Quantity), clip(l.Unit, 14), money(l.Amount, cur))
		}
	}
	if link != "" {
		w.blank()
		w.line("View the statement:")
		w.raw(link)
	}
	return subject, w.String()
}

func ratOf(d store.Decimal) *big.Rat {
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

// addDec adds two decimals exactly, at 6 decimals like the ledger.
func addDec(a, b store.Decimal) store.Decimal {
	sum := new(big.Rat).Add(ratOf(a), ratOf(b))
	return store.Decimal(roundRat(sum, 6))
}

func negate(d store.Decimal) store.Decimal {
	r := ratOf(d)
	if r.Sign() == 0 {
		return "0"
	}
	return store.Decimal(roundRat(r.Neg(r), 6))
}

// taxPct turns a rate (0.05) into a percentage (5).
func taxPct(rate store.Decimal) float64 {
	f, _ := new(big.Rat).Mul(ratOf(rate), big.NewRat(100, 1)).Float64()
	return f
}
