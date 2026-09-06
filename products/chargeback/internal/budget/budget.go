// Package budget evaluates monthly budgets (#6867, DESIGN.md §3.5): the pure
// status arithmetic (Evaluate), the month window + forecast plumbing on top of
// the cost engine (StatusFor), and the hourly threshold evaluator.
//
// Percentages are compared exactly: actual / amount is a rational, never a
// float64, so a threshold of 7% on a 0.030 budget is crossed at exactly
// 0.0021 and not one ulp later. Only the emitted pct fields are floats,
// because they are display-only and have no exact form. The forecast is an
// estimate to begin with (rating.ForecastMonth), so its percentage is float.
package budget

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Status values.
const (
	StatusOK       = "ok"
	StatusWarning  = "warning"
	StatusExceeded = "exceeded"
)

// Input is the budget-shaped input to Evaluate. Period is the calendar month
// being evaluated (YYYY-MM), not the budget's cadence ("monthly").
type Input struct {
	ID           string
	Name         string
	CustomerID   *string
	CustomerName *string
	Amount       store.Decimal
	Currency     string
	Period       string
	Thresholds   []int
}

// Alert is a recorded crossing of one threshold in the evaluated period.
type Alert struct {
	Threshold int
	At        time.Time
}

// Threshold is one line of Status.Thresholds (types.ts BudgetThreshold).
type Threshold struct {
	Pct       int        `json:"pct"`
	Crossed   bool       `json:"crossed"`
	AlertedAt *time.Time `json:"alerted_at"`
}

// Status is the wire shape of a budget's standing in a period (types.ts
// BudgetStatus). Every field is always present so the UI never branches on
// a missing key.
type Status struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	CustomerID   *string       `json:"customer_id"`
	CustomerName *string       `json:"customer_name"`
	Amount       store.Decimal `json:"amount"`
	Currency     string        `json:"currency"`
	Period       string        `json:"period"`
	Actual       store.Decimal `json:"actual"`
	Forecast     *float64      `json:"forecast"`
	PctActual    float64       `json:"pct_actual"`
	PctForecast  *float64      `json:"pct_forecast"`
	Status       string        `json:"status"`
	Thresholds   []Threshold   `json:"thresholds"`
}

var hundred = big.NewRat(100, 1)

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

// FromBudget adapts a stored budget for the given period.
func FromBudget(b store.Budget, period string) Input {
	return Input{
		ID: b.ID, Name: b.Name, CustomerID: b.CustomerID, CustomerName: b.CustomerName,
		Amount: b.Amount, Currency: b.Currency, Period: period, Thresholds: b.Thresholds,
	}
}

// NormalizeThresholds returns the thresholds sorted ascending without
// duplicates. The API stores them this way; Evaluate applies it again so a
// row written by hand still evaluates deterministically.
func NormalizeThresholds(in []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(in))
	for _, t := range in {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Ints(out)
	return out
}

// Evaluate computes the status of a budget from the period's actual cost,
// the month-end forecast (nil outside the current month) and the crossings
// already recorded.
//
//	pct_actual   = actual / amount × 100   (0 when amount is 0)
//	pct_forecast = forecast / amount × 100 (nil when no forecast or amount is 0)
//	exceeded     when pct_actual ≥ 100
//	warning      when pct_actual ≥ the lowest threshold, or pct_forecast ≥ 100
//	ok           otherwise
//
// A threshold is crossed when pct_actual ≥ its pct; alerted_at is the time
// the crossing was recorded, nil until the evaluator records it.
func Evaluate(in Input, actual store.Decimal, forecast *float64, alerts []Alert) Status {
	amount := ratOf(in.Amount)
	act := ratOf(actual)

	pct := new(big.Rat)
	if amount.Sign() > 0 {
		pct.Quo(act, amount)
		pct.Mul(pct, hundred)
	}
	pctActual, _ := pct.Float64()

	var pctForecast *float64
	if forecast != nil && amount.Sign() > 0 {
		af, _ := amount.Float64()
		v := *forecast / af * 100
		pctForecast = &v
	}

	alertedAt := map[int]time.Time{}
	for _, a := range alerts {
		if prev, ok := alertedAt[a.Threshold]; !ok || a.At.Before(prev) {
			alertedAt[a.Threshold] = a.At
		}
	}

	thresholds := NormalizeThresholds(in.Thresholds)
	rows := make([]Threshold, 0, len(thresholds))
	for _, t := range thresholds {
		row := Threshold{Pct: t, Crossed: pct.Cmp(big.NewRat(int64(t), 1)) >= 0}
		if at, ok := alertedAt[t]; ok {
			at := at.UTC()
			row.AlertedAt = &at
		}
		rows = append(rows, row)
	}

	status := StatusOK
	switch {
	case pct.Cmp(hundred) >= 0:
		status = StatusExceeded
	case len(thresholds) > 0 && pct.Cmp(big.NewRat(int64(thresholds[0]), 1)) >= 0:
		status = StatusWarning
	case pctForecast != nil && *pctForecast >= 100:
		status = StatusWarning
	}

	if strings.TrimSpace(string(actual)) == "" {
		actual = "0"
	}
	return Status{
		ID: in.ID, Name: in.Name, CustomerID: in.CustomerID, CustomerName: in.CustomerName,
		Amount: in.Amount, Currency: in.Currency, Period: in.Period,
		Actual: actual, Forecast: forecast, PctActual: pctActual, PctForecast: pctForecast,
		Status: status, Thresholds: rows,
	}
}

// ---------------------------------------------------------------------------
// Month window + forecast on top of the cost engine
// ---------------------------------------------------------------------------

// Reader is what computing a status needs from the store.
type Reader interface {
	Explore(ctx context.Context, scope store.Scope, q store.CostQuery) (store.ExploreResult, error)
	ListBudgetAlerts(ctx context.Context, budgetID string) ([]store.BudgetAlert, error)
}

// MonthStart is the first instant (UTC) of the calendar month containing t.
func MonthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ParsePeriod reads YYYY-MM into the month's first instant.
func ParsePeriod(s string) (time.Time, error) {
	t, err := time.Parse("2006-01", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, fmt.Errorf("period must be YYYY-MM")
	}
	return t.UTC(), nil
}

func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func decFloat(d store.Decimal) float64 {
	f, _ := strconv.ParseFloat(string(d), 64)
	return f
}

// monthForecast mirrors the API explorer's forecastFor: a month-end estimate
// only when the window IS the current calendar month at day grain, from the
// complete days before today that have data. Anywhere else a forecast would
// be a claim about a month the window does not show.
func monthForecast(now time.Time, res store.ExploreResult, from, to time.Time) *float64 {
	now = now.UTC()
	today := dateOnly(now)
	if res.Granularity != "day" || !from.Equal(MonthStart(now)) || to.Before(today) {
		return nil
	}
	var complete []rating.DayCost
	for i, b := range res.Buckets {
		if b >= today.Format("2006-01-02") {
			break
		}
		if i >= len(res.BucketHasData) || !res.BucketHasData[i] {
			continue
		}
		complete = append(complete, rating.DayCost{Day: b, Cost: decFloat(res.TotalsByBucket[i])})
	}
	f, ok := rating.ForecastMonth(now, complete)
	if !ok {
		return nil
	}
	return &f.MonthEnd
}

// StatusFor evaluates b for the calendar month containing month, as seen at
// now. The cost is the engine's window total [monthStart, nextMonthStart)
// narrowed to the budget's customer (a global budget sums every customer);
// the scope still applies, so a customer principal can only ever see its own
// spend through a budget.
func StatusFor(ctx context.Context, r Reader, scope store.Scope, b store.Budget, month, now time.Time) (Status, error) {
	from := MonthStart(month)
	to := from.AddDate(0, 1, 0)
	period := from.Format("2006-01")
	q := store.CostQuery{From: from, To: to, Granularity: "day", GroupBy: "none", Metric: "cost"}
	if b.CustomerID != nil {
		q.CustomerID = *b.CustomerID
	}
	res, err := r.Explore(ctx, scope, q)
	if err != nil {
		return Status{}, err
	}
	forecast := monthForecast(now, res, from, to)
	recorded, err := r.ListBudgetAlerts(ctx, b.ID)
	if err != nil {
		return Status{}, err
	}
	var alerts []Alert
	for _, a := range recorded {
		if a.Period == period {
			alerts = append(alerts, Alert{Threshold: a.Threshold, At: a.At})
		}
	}
	return Evaluate(FromBudget(b, period), res.Total.Current, forecast, alerts), nil
}
