package report

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/anomaly"
	"github.com/openova-io/openova/products/chargeback/internal/budget"
	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/recommend"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Reader is what Build needs from the store. Every read is scope-filtered
// by the store itself; Build only chooses the scope from the schedule.
type Reader interface {
	Explore(ctx context.Context, scope store.Scope, q store.CostQuery) (store.ExploreResult, error)
	ListBudgets(ctx context.Context, scope store.Scope) ([]store.Budget, error)
	ListBudgetAlerts(ctx context.Context, budgetID string) ([]store.BudgetAlert, error)
	DailyCostByCustomerKind(ctx context.Context, scope store.Scope, customerID string, from, to time.Time) ([]store.DailyKindCost, error)
	CustomerBooks(ctx context.Context, scope store.Scope, customerID string) ([]store.CustomerBook, error)
	LiveResources(ctx context.Context, scope store.Scope, customerID string) ([]store.LiveResource, error)
	SourceHealths(ctx context.Context, scope store.Scope, customerID string) ([]store.SourceHealth, error)
	UnpricedUsageByCustomer(ctx context.Context, scope store.Scope, customerID string, from, to time.Time) ([]store.CustomerUnpricedSKU, error)
	CPUUtilMeans(ctx context.Context, scope store.Scope, customerID string, from, to time.Time) ([]store.CPUUtilMean, error)
	EIPTrafficWindows(ctx context.Context, scope store.Scope, customerID string, from, to time.Time) ([]store.EIPTrafficWindow, error)
}

// Windows the recommendation rules read (as the API's handler uses).
const (
	unpricedWindow = 30 * 24 * time.Hour
	cpuWindow      = 7 * 24 * time.Hour
	trafficWindow  = 7 * 24 * time.Hour
	// topRecommendations is how many recommendation lines the mail lists.
	topRecommendations = 3
	// topAnomalies is how many flagged days are kept (the mail prints one).
	topAnomalies = 3
)

// ScopeFor is the read scope of a schedule: a customer schedule reads as
// that customer (the store forces the customer id on every query), a global
// one as the operator. The second return is the customer filter.
func ScopeFor(s store.ReportSchedule) (store.Scope, string) {
	if s.CustomerID != nil && *s.CustomerID != "" {
		return store.CustomerScope(*s.CustomerID), *s.CustomerID
	}
	return store.OperatorScope, ""
}

// Build gathers a schedule's report for the window [from, to) as seen at
// now — NOT through HTTP: the same store calls the explorer, budgets,
// anomalies and recommendations endpoints make, so the mail can never
// disagree with the screens. Sections the schedule does not carry are not
// gathered at all.
func Build(ctx context.Context, r Reader, sched store.ReportSchedule, from, to, now time.Time, publicURL string) (Input, error) {
	now = now.UTC()
	scope, customerID := ScopeFor(sched)
	in := Input{
		Name:      sched.Name,
		Operator:  scope.Operator,
		Cadence:   sched.Cadence,
		From:      dateOnly(from),
		To:        dateOnly(to),
		Sections:  NormalizeSections(sched.Sections),
		PublicURL: publicURL,
	}
	if sched.CustomerName != nil && *sched.CustomerName != "" {
		in.Scope = *sched.CustomerName
	} else if customerID != "" {
		in.Scope = "customer " + customerID
	} else {
		in.Scope = "All customers"
	}
	base := func(from, to time.Time, gran, groupBy string, limit int) store.CostQuery {
		return store.CostQuery{From: from, To: to, Granularity: gran, GroupBy: groupBy, Metric: "cost", Limit: limit, CustomerID: customerID}
	}

	// The window total is always read: the subject carries it.
	total, err := r.Explore(ctx, scope, base(in.From, in.To, "day", "none", 0))
	if err != nil {
		return in, err
	}
	// Explore reports in the reporting currency (store/currency.go); what
	// it could not convert is listed, never summed.
	in.Currency = total.Currency
	in.Total, in.Previous, in.DeltaPct, in.Resources = total.Total.Current, total.Total.Previous, total.Total.DeltaPct, total.Total.Resources
	in.Unpriced = total.Unpriced
	in.Unconverted = total.Unconverted

	if in.has(SectionSummary) {
		ms := monthStart(now)
		mtd, err := r.Explore(ctx, scope, base(ms, ms.AddDate(0, 1, 0), "day", "none", 0))
		if err != nil {
			return in, err
		}
		in.MTDMonth = ms.Format("Jan 2006")
		in.MTD = mtd.Total.Current
		in.Forecast = monthForecast(now, mtd)
		if in.Currency == "" {
			in.Currency = mtd.Currency
		}
	}
	if in.has(SectionServices) {
		res, err := r.Explore(ctx, scope, base(in.From, in.To, "month", "kind", TopN))
		if err != nil {
			return in, err
		}
		in.Services, in.ServicesOther = groupsOf(res)
	}
	// DESIGN.md §19 — the window by COST CENTRE, for a customer schedule.
	// It is the explorer's own dimension, read exactly as the services
	// section reads the kind, so the mail can never disagree with the page.
	if in.has(SectionCostCentres) && customerID != "" {
		res, err := r.Explore(ctx, scope, base(in.From, in.To, "month", store.CostCentreDimension, TopN))
		if err != nil {
			return in, err
		}
		in.CostCentres, in.CostCentresOther = groupsOf(res)
	}
	if in.has(SectionCustomers) && scope.Operator {
		res, err := r.Explore(ctx, scope, base(in.From, in.To, "month", "customer", TopN))
		if err != nil {
			return in, err
		}
		in.Customers, in.CustomersOther = groupsOf(res)
	}
	if in.has(SectionBudgets) {
		bs, err := r.ListBudgets(ctx, scope)
		if err != nil {
			return in, err
		}
		in.Budgets = []budget.Status{}
		for _, b := range bs {
			if !b.Active {
				continue
			}
			st, err := budget.StatusFor(ctx, r, scope, b, now, now)
			if err != nil {
				return in, err
			}
			in.Budgets = append(in.Budgets, st)
		}
	}
	if in.has(SectionAnomalies) {
		count, rows, err := detectAnomalies(ctx, r, scope, customerID, in.From, in.To)
		if err != nil {
			return in, err
		}
		in.AnomalyCount, in.Anomalies = count, rows
	}
	if in.has(SectionRecommendations) {
		// Savings in the reporting currency, like every other figure here.
		rin := recommend.Input{Now: now, ReportingCurrency: in.Currency}
		if rin.Books, err = r.CustomerBooks(ctx, scope, customerID); err != nil {
			return in, err
		}
		if rin.Resources, err = r.LiveResources(ctx, scope, customerID); err != nil {
			return in, err
		}
		if rin.Sources, err = r.SourceHealths(ctx, scope, customerID); err != nil {
			return in, err
		}
		if rin.Unpriced, err = r.UnpricedUsageByCustomer(ctx, scope, customerID, now.Add(-unpricedWindow), now); err != nil {
			return in, err
		}
		if rin.CPUUtil, err = r.CPUUtilMeans(ctx, scope, customerID, now.Add(-cpuWindow), now); err != nil {
			return in, err
		}
		if rin.EIPTraffic, err = r.EIPTrafficWindows(ctx, scope, customerID, now.Add(-trafficWindow), now); err != nil {
			return in, err
		}
		rows := recommend.Evaluate(rin)
		in.RecommendationCount = len(rows)
		in.RecommendationCurrency = recommend.Currency(rows, rin.Books)
		if rin.ReportingCurrency != "" {
			in.RecommendationCurrency = rin.ReportingCurrency
		}
		in.RecommendationSaving = recommend.TotalIn(rows, in.RecommendationCurrency)
		if len(rows) > topRecommendations {
			rows = rows[:topRecommendations]
		}
		in.Recommendations = rows
	}
	return in, nil
}

// monthForecast mirrors the API explorer's forecastFor: a month-end
// estimate from the complete days before today that have data, or nil.
func monthForecast(now time.Time, res store.ExploreResult) *rating.Forecast {
	today := dateOnly(now)
	var complete []rating.DayCost
	for i, b := range res.Buckets {
		if b >= today.Format("2006-01-02") {
			break
		}
		if i >= len(res.BucketHasData) || !res.BucketHasData[i] || i >= len(res.TotalsByBucket) {
			continue
		}
		f, _ := strconv.ParseFloat(string(res.TotalsByBucket[i]), 64)
		complete = append(complete, rating.DayCost{Day: b, Cost: f})
	}
	f, ok := rating.ForecastMonth(now, complete)
	if !ok {
		return nil
	}
	return &f
}

// detectAnomalies flags the days in [from, to) per (customer, kind) the way
// the API's /anomalies does (internal/anomaly on DailyCostByCustomerKind
// with the 14 baseline days before the window), returning the count and the
// biggest few by impact.
func detectAnomalies(ctx context.Context, r Reader, scope store.Scope, customerID string, from, to time.Time) (int, []Anomaly, error) {
	daily, err := r.DailyCostByCustomerKind(ctx, scope, customerID, from.AddDate(0, 0, -anomaly.DefaultLookback), to)
	if err != nil {
		return 0, nil, err
	}
	type pair struct{ customer, kind string }
	series := map[pair][]anomaly.DayValue{}
	actual := map[pair]map[string]store.Decimal{}
	names := map[string]string{}
	var order []pair
	for _, d := range daily {
		p := pair{d.CustomerID, d.ResourceKind}
		if _, ok := series[p]; !ok {
			order = append(order, p)
			actual[p] = map[string]store.Decimal{}
		}
		f, _ := strconv.ParseFloat(string(d.Cost), 64)
		series[p] = append(series[p], anomaly.DayValue{Day: d.Day, Value: f})
		actual[p][d.Day] = d.Cost
		names[d.CustomerID] = d.CustomerName
	}
	fromDay, toDay := from.Format("2006-01-02"), to.Format("2006-01-02")
	rows := []Anomaly{}
	for _, p := range order {
		for _, f := range anomaly.Detect(series[p], anomaly.Options{}) {
			if f.Day < fromDay || f.Day >= toDay {
				continue
			}
			rows = append(rows, Anomaly{
				Day: f.Day, CustomerName: names[p.customer], Label: store.KindLabel(p.kind),
				Actual: actual[p][f.Day], Expected: f.Expected, Impact: f.Impact, Score: math.Round(f.Score*100) / 100,
			})
		}
	}
	sortAnomalies(rows)
	count := len(rows)
	if len(rows) > topAnomalies {
		rows = rows[:topAnomalies]
	}
	return count, rows, nil
}
