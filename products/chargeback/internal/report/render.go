package report

import (
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/budget"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/recommend"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// MaxLineWidth is the widest line Render emits: plain-text mail that a
// 78-column reader shows without wrapping (RFC 5322 §2.1.1 recommends 78).
const MaxLineWidth = 78

// TopN is how many services / customers a report lists.
const TopN = 5

// Group is one ranked line (a service kind or a customer) of the window.
type Group struct {
	Key       string
	Label     string
	Total     store.Decimal
	Previous  store.Decimal
	DeltaPct  *float64
	Share     float64
	Resources int
}

// Anomaly is one flagged day, reduced to what the mail states.
type Anomaly struct {
	Day          string
	CustomerName string
	Label        string // service kind label
	Actual       store.Decimal
	Expected     float64
	Impact       float64
	Score        float64
}

// Input is everything Render reads. It is a plain struct so the golden test
// needs no database; Build fills it from the store.
type Input struct {
	Name     string
	Scope    string // "All customers" or the customer's name
	Operator bool   // operator-scoped: the customers section may render
	Cadence  string
	From, To time.Time // half-open window
	Currency string
	Sections []string

	// Summary.
	Total     store.Decimal
	Previous  store.Decimal
	DeltaPct  *float64
	Resources int
	MTDMonth  string // "Sep 2026"
	MTD       store.Decimal
	Forecast  *rating.Forecast
	Unpriced  []store.UnpricedSKU

	Services       []Group
	ServicesOther  *Group
	Customers      []Group
	CustomersOther *Group

	Budgets []budget.Status

	AnomalyCount int
	Anomalies    []Anomaly // biggest first; Render prints the first

	RecommendationCount    int
	RecommendationSaving   store.Decimal
	RecommendationCurrency string
	Recommendations        []recommend.Recommendation // top few by saving

	// PublicURL is the console's base URL for the footer link; empty = none.
	PublicURL string
}

func (in Input) has(section string) bool {
	for _, s := range in.Sections {
		if s == section {
			return true
		}
	}
	return false
}

// Render produces the mail subject and plain-text body. Sections absent
// from in.Sections are omitted entirely; the header and footer always
// render. No line exceeds MaxLineWidth.
func Render(in Input) (subject, body string) {
	label := WindowLabel(in.From, in.To)
	cur := in.Currency
	name := clip(strings.TrimSpace(in.Name), 30)
	if name == "" {
		name = "Cost report"
	}
	subject = clip(strings.TrimSpace(fmt.Sprintf("Cost report: %s — %s: %s %s", name, label, money(in.Total, cur), cur)), MaxLineWidth)

	var w writer
	w.line("Cost report: %s", name)
	w.line("Window: %s (%s)", label, in.Cadence)
	scope := in.Scope
	if scope == "" {
		scope = "All customers"
	}
	if cur != "" {
		w.line("Scope: %s · Currency: %s", scope, cur)
	} else {
		w.line("Scope: %s", scope)
	}

	if in.has(SectionSummary) {
		w.blank()
		w.line("SUMMARY")
		prevLabel := WindowLabel(in.From.Add(-in.To.Sub(in.From)), in.From)
		w.line("  %-26s %12s %-3s  %s vs %s", "Total for the period", money(in.Total, cur), cur, pct(in.DeltaPct, true), prevLabel)
		if in.MTDMonth != "" {
			w.line("  %-26s %12s %-3s", "Month to date ("+in.MTDMonth+")", money(in.MTD, cur), cur)
		}
		if in.Forecast != nil {
			w.line("  %-26s %12s %-3s  %s, %s confidence", "Forecast month end", moneyF(in.Forecast.MonthEnd, cur), cur, in.Forecast.Method, in.Forecast.Confidence)
		} else if in.MTDMonth != "" {
			w.line("  %-26s %12s", "Forecast month end", "n/a")
		}
		w.line("  %-26s %12d", "Resources with cost", in.Resources)
		if len(in.Unpriced) > 0 {
			verb := "carry"
			if len(in.Unpriced) == 1 {
				verb = "carries"
			}
			w.line("  Unpriced usage (%d SKU%s %s no rate in the price book):", len(in.Unpriced), plural(len(in.Unpriced)), verb)
			for _, u := range in.Unpriced {
				w.line("    - %s: %s %s across %d resource%s", clip(u.SKU, 24), trimDec(u.Quantity), clip(u.Unit, 14), u.Resources, plural(u.Resources))
			}
		}
	}

	if in.has(SectionServices) {
		w.blank()
		w.line("TOP SERVICES")
		renderGroups(&w, in.Services, in.ServicesOther, cur, "No priced usage in the window.")
	}

	if in.has(SectionCustomers) && in.Operator {
		w.blank()
		w.line("TOP CUSTOMERS")
		renderGroups(&w, in.Customers, in.CustomersOther, cur, "No priced usage in the window.")
	}

	if in.has(SectionBudgets) {
		w.blank()
		w.line("BUDGETS")
		if len(in.Budgets) == 0 {
			w.line("  No active budget.")
		}
		for _, b := range in.Budgets {
			scope := "all customers"
			if b.CustomerName != nil && *b.CustomerName != "" {
				scope = *b.CustomerName
			}
			fc := ""
			if b.PctForecast != nil {
				fc = fmt.Sprintf(", forecast %s", pctF(*b.PctForecast, false))
			}
			w.line("  %-9s %-30s %6s of %s %s%s", b.Status, clip(b.Name+" · "+scope, 30), pctF(b.PctActual, false), trimDec(b.Amount), b.Currency, fc)
		}
	}

	if in.has(SectionAnomalies) {
		w.blank()
		w.line("ANOMALIES")
		if in.AnomalyCount == 0 {
			w.line("  No anomalous day in the window.")
		} else {
			w.line("  %d anomalous day%s in the window.", in.AnomalyCount, plural(in.AnomalyCount))
			if len(in.Anomalies) > 0 {
				a := in.Anomalies[0]
				who := a.Label
				if a.CustomerName != "" {
					who = a.CustomerName + " · " + a.Label
				}
				w.line("  Biggest: %s, %s", a.Day, clip(who, 50))
				w.line("    actual %s %s vs expected %s (impact %s, score %.1f)", money(a.Actual, cur), cur, moneyF(a.Expected, cur), signedF(a.Impact), a.Score)
			}
		}
	}

	if in.has(SectionRecommendations) {
		w.blank()
		w.line("RECOMMENDATIONS")
		if in.RecommendationCount == 0 {
			w.line("  Nothing to recommend.")
		} else {
			rc := in.RecommendationCurrency
			if rc == "" {
				rc = cur
			}
			w.line("  %d recommendation%s, potential saving %s %s per month.", in.RecommendationCount, plural(in.RecommendationCount), money(in.RecommendationSaving, rc), rc)
			for _, r := range in.Recommendations {
				who := r.CustomerName
				if r.ResourceName != "" {
					who = r.ResourceName + " (" + r.CustomerName + ")"
				}
				w.line("    - %s: %s", clip(r.Title, 40), clip(who, 26))
				w.line("      %s %s/month", money(r.MonthlySaving, r.Currency), r.Currency)
			}
		}
	}

	if in.PublicURL != "" {
		w.blank()
		w.line("Open the console:")
		w.raw(strings.TrimRight(in.PublicURL, "/") + "/reports")
	}
	return subject, w.String()
}

func renderGroups(w *writer, groups []Group, other *Group, cur, empty string) {
	if len(groups) == 0 && other == nil {
		w.line("  %s", empty)
		return
	}
	w.line("  %-30s %16s %7s %9s", "", "Cost", "Share", "vs prev")
	for _, g := range groups {
		w.line("  %-30s %16s %7s %9s", clip(g.Label, 30), money(g.Total, cur), pctF(g.Share*100, false), pct(g.DeltaPct, true))
	}
	if other != nil {
		w.line("  %-30s %16s %7s %9s", "Other", money(other.Total, cur), pctF(other.Share*100, false), pct(other.DeltaPct, true))
	}
}

// writer accumulates lines, each clipped to MaxLineWidth.
type writer struct {
	sb strings.Builder
}

func (w *writer) line(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	s = strings.TrimRight(s, " ")
	w.sb.WriteString(clip(s, MaxLineWidth))
	w.sb.WriteString("\n")
}

func (w *writer) blank()         { w.sb.WriteString("\n") }
func (w *writer) String() string { return w.sb.String() }

// raw writes a line unclipped. Only URLs go through here: a link cut at 78
// columns is a dead link, and mail readers wrap long URLs themselves.
func (w *writer) raw(s string) {
	w.sb.WriteString(s)
	w.sb.WriteString("\n")
}

// clip truncates s to n runes, marking the cut with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// threeDecimalCurrencies are the ISO 4217 currencies with a 3-decimal minor
// unit; everything else renders with 2.
var threeDecimalCurrencies = map[string]bool{"OMR": true, "BHD": true, "KWD": true, "JOD": true, "IQD": true, "LYD": true, "TND": true}

func scaleFor(cur string) int {
	if threeDecimalCurrencies[strings.ToUpper(cur)] {
		return 3
	}
	return 2
}

// money renders an exact decimal rounded half-up to the currency's minor
// unit. Rounding is display-only: the values come from the ledger and are
// never summed here.
func money(d store.Decimal, cur string) string {
	s := strings.TrimSpace(string(d))
	if s == "" {
		s = "0"
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return s
	}
	return roundRat(r, scaleFor(cur))
}

// moneyF renders a float estimate (forecast, expected) at the same scale.
func moneyF(f float64, cur string) string {
	return fmt.Sprintf("%.*f", scaleFor(cur), f)
}

func signedF(f float64) string {
	if f >= 0 {
		return fmt.Sprintf("+%.2f", f)
	}
	return fmt.Sprintf("%.2f", f)
}

// pct renders a percentage change: "+5.8%", "-3.1%", "n/a" when nil.
func pct(p *float64, signed bool) string {
	if p == nil {
		return "n/a"
	}
	return pctF(*p, signed)
}

func pctF(v float64, signed bool) string {
	if signed && v > 0 {
		return fmt.Sprintf("+%.1f%%", v)
	}
	return fmt.Sprintf("%.1f%%", v)
}

// roundRat renders r rounded half-up to scale decimals (mirrors
// rating.roundRat, which is unexported).
func roundRat(r *big.Rat, scale int) string {
	neg := r.Sign() < 0
	abs := new(big.Rat).Abs(r)
	pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	scaled := new(big.Rat).Mul(abs, new(big.Rat).SetInt(pow))
	scaled.Add(scaled, big.NewRat(1, 2))
	floor := new(big.Int).Quo(scaled.Num(), scaled.Denom())
	s := floor.String()
	if scale > 0 {
		if len(s) <= scale {
			s = strings.Repeat("0", scale-len(s)+1) + s
		}
		s = s[:len(s)-scale] + "." + s[len(s)-scale:]
	}
	if neg && strings.Trim(s, "0.") != "" {
		s = "-" + s
	}
	return s
}

// trimDec drops trailing zeros from a decimal string for prose:
// 200.000000 → 200, 12.500000 → 12.5.
func trimDec(d store.Decimal) string {
	s := strings.TrimSpace(string(d))
	if s == "" {
		return "0"
	}
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// groupsOf converts explorer groups to report lines, keeping the store's
// order (total descending).
func groupsOf(res store.ExploreResult) ([]Group, *Group) {
	out := make([]Group, 0, len(res.Groups))
	for _, g := range res.Groups {
		out = append(out, Group{Key: g.Key, Label: g.Label, Total: g.Total, Previous: g.Previous, DeltaPct: g.DeltaPct, Share: g.Share, Resources: g.Resources})
	}
	var other *Group
	if res.Other != nil {
		o := res.Other
		other = &Group{Key: o.Key, Label: o.Label, Total: o.Total, Previous: o.Previous, DeltaPct: o.DeltaPct, Share: o.Share, Resources: o.Resources}
	}
	return out, other
}

// sortAnomalies orders by impact descending, then day descending.
func sortAnomalies(rows []Anomaly) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Impact != rows[j].Impact {
			return rows[i].Impact > rows[j].Impact
		}
		return rows[i].Day > rows[j].Day
	})
}
