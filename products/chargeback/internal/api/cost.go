package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Cost analysis endpoints (#6867, DESIGN.md §3).
//
// Windows are half-open [from, to) in whole UTC days. The default window is
// the last 30 days. The customer-lens routes force the session's customer
// through the store scope; the operator routes accept a `customer` filter.

const (
	maxExploreBuckets = 400
	maxExploreGroups  = 500
	defaultTopN       = 10
)

func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// parseCostQuery reads the explorer parameters. The second return is the
// 400 message when the request is malformed.
func (h *Handler) parseCostQuery(r *http.Request) (store.CostQuery, string) {
	qs := r.URL.Query()
	today := dateOnly(h.Now())
	q := store.CostQuery{
		From:        today.AddDate(0, 0, -29),
		To:          today.AddDate(0, 0, 1),
		Granularity: "day",
		GroupBy:     "none",
		Metric:      "cost",
		Include:     map[string][]string{},
		Exclude:     map[string][]string{},
		Limit:       defaultTopN,
	}
	parseDay := func(s string) (time.Time, bool) {
		t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
		if err != nil {
			return time.Time{}, false
		}
		return t.UTC(), true
	}
	if v := qs.Get("from"); v != "" {
		t, ok := parseDay(v)
		if !ok {
			return q, "from must be YYYY-MM-DD"
		}
		q.From = t
	}
	if v := qs.Get("to"); v != "" {
		t, ok := parseDay(v)
		if !ok {
			return q, "to must be YYYY-MM-DD"
		}
		q.To = t
	}
	if !q.To.After(q.From) {
		return q, "from must be before to"
	}
	switch v := qs.Get("granularity"); v {
	case "", "day":
		q.Granularity = "day"
	case "month":
		q.Granularity = "month"
	default:
		return q, "granularity must be day or month"
	}
	if n := len(store.Buckets(q.From, q.To, q.Granularity)); n > maxExploreBuckets {
		return q, fmt.Sprintf("window too long: %d buckets, maximum %d", n, maxExploreBuckets)
	}
	switch v := qs.Get("group_by"); v {
	case "", "none":
		q.GroupBy = "none"
	default:
		found := false
		for _, d := range store.CostDimensions() {
			if d == v {
				found = true
			}
		}
		if !found {
			return q, "group_by must be none or one of " + strings.Join(store.CostDimensions(), ", ")
		}
		q.GroupBy = v
	}
	for _, dim := range store.CostDimensions() {
		if vals := splitCSV(qs[dim]); len(vals) > 0 {
			q.Include[dim] = vals
		}
		if vals := splitCSV(qs["exclude_"+dim]); len(vals) > 0 {
			q.Exclude[dim] = vals
		}
	}
	switch v := qs.Get("metric"); v {
	case "", "cost":
		q.Metric = "cost"
	case "usage":
		// Quantities of different SKUs have different units; summing them
		// would print a number with no meaning.
		if q.GroupBy != "sku" && len(q.Include["sku"]) != 1 {
			return q, "metric=usage needs group_by=sku or exactly one sku filter"
		}
		q.Metric = "usage"
	default:
		return q, "metric must be cost or usage"
	}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > maxExploreGroups {
			return q, fmt.Sprintf("limit must be 0..%d", maxExploreGroups)
		}
		q.Limit = n
	}
	return q, ""
}

func splitCSV(vals []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// forecastFor attaches a month-end forecast when the window is the current
// calendar month at day grain; anywhere else a forecast would be a claim
// about a month the window does not show.
func (h *Handler) forecastFor(res store.ExploreResult, from, to time.Time) *rating.Forecast {
	now := h.Now().UTC()
	today := dateOnly(now)
	if res.Granularity != "day" || !from.Equal(monthStart(now)) || to.Before(today) {
		return nil
	}
	var complete []rating.DayCost
	for i, b := range res.Buckets {
		if b >= today.Format("2006-01-02") {
			break
		}
		if !res.BucketHasData[i] {
			continue
		}
		complete = append(complete, rating.DayCost{Day: b, Cost: decFloat(res.TotalsByBucket[i])})
	}
	f, ok := rating.ForecastMonth(now, complete)
	if !ok {
		return nil
	}
	return &f
}

func decFloat(d store.Decimal) float64 {
	f, _ := strconv.ParseFloat(string(d), 64)
	return f
}

// exploreDoc is the wire shape: the store result plus the forecast.
type exploreDoc struct {
	store.ExploreResult
	Forecast *rating.Forecast `json:"forecast"`
}

func (h *Handler) runExplore(w http.ResponseWriter, r *http.Request, scope store.Scope, customerID string) (exploreDoc, bool) {
	q, msg := h.parseCostQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return exploreDoc{}, false
	}
	q.CustomerID = customerID
	res, err := h.Store.Explore(r.Context(), scope, q)
	if err != nil {
		storeErr(w, err)
		return exploreDoc{}, false
	}
	return exploreDoc{ExploreResult: res, Forecast: h.forecastFor(res, q.From, q.To)}, true
}

func (h *Handler) explore(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	doc, ok := h.runExplore(w, r, s.Scope(), "")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) customerExplore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	doc, ok := h.runExplore(w, r, s.Scope(), id)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func writeExploreCSV(w http.ResponseWriter, doc exploreDoc) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cost-%s-%s-%s.csv"`, doc.GroupBy, doc.From, doc.To))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"bucket", "group_by", "group_key", "group_label", doc.Metric, "currency"})
	groups := doc.Groups
	if doc.Other != nil {
		groups = append(groups, *doc.Other)
	}
	for _, g := range groups {
		for i, b := range doc.Buckets {
			if i >= len(g.Values) {
				break
			}
			_ = cw.Write([]string{b, doc.GroupBy, g.Key, g.Label, string(g.Values[i]), doc.Currency})
		}
	}
	cw.Flush()
}

func (h *Handler) exploreCSV(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	doc, ok := h.runExplore(w, r, s.Scope(), "")
	if !ok {
		return
	}
	writeExploreCSV(w, doc)
}

func (h *Handler) customerExploreCSV(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	doc, ok := h.runExplore(w, r, s.Scope(), id)
	if !ok {
		return
	}
	writeExploreCSV(w, doc)
}

func (h *Handler) costDimensions(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	h.writeDimensions(w, r, s.Scope(), "")
}

func (h *Handler) customerCostDimensions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	h.writeDimensions(w, r, s.Scope(), id)
}

func (h *Handler) writeDimensions(w http.ResponseWriter, r *http.Request, scope store.Scope, customerID string) {
	q, msg := h.parseCostQuery(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	q.CustomerID = customerID
	vals, err := h.Store.DimensionValues(r.Context(), scope, q)
	if err != nil {
		storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": q.From.Format("2006-01-02"), "to": q.To.Format("2006-01-02"), "dimensions": vals})
}

// ---------------------------------------------------------------------------
// Summary (the overview document, DESIGN.md §3.2)
// ---------------------------------------------------------------------------

// summaryParts is everything the summary is composed from; gathered by
// gatherSummary against the store, composed by composeSummary (pure, so the
// wire contract can be pinned without a database).
type summaryParts struct {
	Now         time.Time
	MTD         store.ExploreResult // current month, day grain, no grouping
	Daily30     store.ExploreResult // last 30 days, day grain, no grouping
	LastMonth   store.ExploreResult // previous calendar month, month grain
	PrevMTD     store.ExploreResult // previous month, same number of days
	ByCustomer  store.ExploreResult // current month grouped by customer
	ByKind      store.ExploreResult // current month grouped by kind
	Customers   map[string]int
	Sources     map[string]int
	Resources   int
	LastCollect *time.Time
	Statements  []store.Statement
	Profile     string
	// Filled by their own lanes (budgets, anomalies); nil renders as [].
	Budgets   any
	Anomalies any
}

func (h *Handler) gatherSummary(r *http.Request, scope store.Scope, customerID string) (summaryParts, error) {
	ctx := r.Context()
	now := h.Now().UTC()
	today := dateOnly(now)
	ms := monthStart(now)
	nextMs := ms.AddDate(0, 1, 0)
	lastMs := ms.AddDate(0, -1, 0)
	p := summaryParts{Now: now, Profile: h.Config.Profile}
	base := func(from, to time.Time, gran, groupBy string, limit int) store.CostQuery {
		return store.CostQuery{From: from, To: to, Granularity: gran, GroupBy: groupBy, Metric: "cost", Limit: limit, CustomerID: customerID}
	}
	var err error
	if p.MTD, err = h.Store.Explore(ctx, scope, base(ms, nextMs, "day", "none", 0)); err != nil {
		return p, err
	}
	if p.Daily30, err = h.Store.Explore(ctx, scope, base(today.AddDate(0, 0, -29), today.AddDate(0, 0, 1), "day", "none", 0)); err != nil {
		return p, err
	}
	if p.LastMonth, err = h.Store.Explore(ctx, scope, base(lastMs, ms, "month", "none", 0)); err != nil {
		return p, err
	}
	// Same day-count of last month, so MoM compares like with like on the 7th.
	elapsed := int(today.Sub(ms).Hours()/24) + 1
	prevTo := lastMs.AddDate(0, 0, elapsed)
	if prevTo.After(ms) {
		prevTo = ms
	}
	if p.PrevMTD, err = h.Store.Explore(ctx, scope, base(lastMs, prevTo, "month", "none", 0)); err != nil {
		return p, err
	}
	if p.ByCustomer, err = h.Store.Explore(ctx, scope, base(ms, nextMs, "month", "customer", 10)); err != nil {
		return p, err
	}
	if p.ByKind, err = h.Store.Explore(ctx, scope, base(ms, nextMs, "month", "kind", 10)); err != nil {
		return p, err
	}
	if scope.Operator {
		if p.Customers, err = h.Store.CustomerCountsByStatus(ctx); err != nil {
			return p, err
		}
		if p.Sources, err = h.Store.SourceStatusCounts(ctx); err != nil {
			return p, err
		}
		if p.Statements, err = h.Store.ListAllStatements(ctx, ""); err != nil {
			return p, err
		}
	} else {
		srcs, err := h.Store.ListSources(ctx, scope, customerID)
		if err != nil {
			return p, err
		}
		p.Sources = map[string]int{"verified": 0, "pending": 0, "failed": 0}
		for _, s := range srcs {
			p.Sources[s.Status]++
		}
		if p.Statements, err = h.Store.ListStatements(ctx, scope, customerID); err != nil {
			return p, err
		}
	}
	if p.Resources, err = h.Store.LiveResourceCount(ctx, scope, customerID); err != nil {
		return p, err
	}
	if p.LastCollect, err = h.Store.LastCollectedAt(ctx, scope, customerID); err != nil {
		return p, err
	}
	return p, nil
}

// composeSummary turns the parts into the overview document. Every key the
// UI reads is written here and pinned by TestWireContractFixtures.
func composeSummary(p summaryParts) map[string]any {
	now := p.Now.UTC()
	today := dateOnly(now)
	ms := monthStart(now)
	elapsed := int(today.Sub(ms).Hours()/24) + 1

	sumTotals := func(r store.ExploreResult) store.Decimal { return r.Total.Current }
	mtd := sumTotals(p.MTD)
	prevMTD := sumTotals(p.PrevMTD)
	lastMonth := sumTotals(p.LastMonth)

	// Forecast from the complete days of the month.
	var complete []rating.DayCost
	for i, b := range p.MTD.Buckets {
		if b >= today.Format("2006-01-02") {
			break
		}
		if i < len(p.MTD.BucketHasData) && p.MTD.BucketHasData[i] {
			complete = append(complete, rating.DayCost{Day: b, Cost: decFloat(p.MTD.TotalsByBucket[i])})
		}
	}
	var forecast any
	if f, ok := rating.ForecastMonth(now, complete); ok {
		forecast = f
	}

	daily := make([]map[string]any, 0, len(p.Daily30.Buckets))
	var last30 store.Decimal = "0"
	daysWithData := 0
	for i, b := range p.Daily30.Buckets {
		v := p.Daily30.TotalsByBucket[i]
		has := i < len(p.Daily30.BucketHasData) && p.Daily30.BucketHasData[i]
		daily = append(daily, map[string]any{"day": b, "cost": v, "has_data": has})
		if has {
			daysWithData++
		}
		last30 = addDecimal(last30, v)
	}
	var avgDaily float64
	if daysWithData > 0 {
		avgDaily = decFloat(last30) / float64(daysWithData)
	}

	groupsOf := func(r store.ExploreResult, withID bool) []map[string]any {
		out := make([]map[string]any, 0, len(r.Groups)+1)
		for _, g := range r.Groups {
			row := map[string]any{"key": g.Key, "label": g.Label, "cost": g.Total, "previous": g.Previous, "delta_pct": g.DeltaPct, "share": g.Share, "resources": g.Resources}
			if withID {
				row["id"] = g.Key
				row["name"] = g.Label
			}
			out = append(out, row)
		}
		if r.Other != nil {
			out = append(out, map[string]any{"key": "other", "label": "Other", "cost": r.Other.Total, "previous": r.Other.Previous, "delta_pct": r.Other.DeltaPct, "share": r.Other.Share, "resources": r.Other.Resources})
		}
		return out
	}

	drafts, issued := 0, 0
	latest := []store.Statement{}
	for _, st := range p.Statements {
		switch st.Status {
		case "draft":
			drafts++
		case "issued":
			issued++
		}
		if len(latest) < 5 {
			latest = append(latest, st)
		}
	}

	currency := p.MTD.Currency
	if currency == "" {
		currency = p.Daily30.Currency
	}
	if currency == "" {
		currency = p.LastMonth.Currency
	}
	budgets := p.Budgets
	if budgets == nil {
		budgets = []any{}
	}
	anomalies := p.Anomalies
	if anomalies == nil {
		anomalies = []any{}
	}
	customers := p.Customers
	if customers == nil {
		customers = map[string]int{}
	}
	sources := p.Sources
	if sources == nil {
		sources = map[string]int{}
	}
	return map[string]any{
		"profile":        p.Profile,
		"now":            now.Format(time.RFC3339),
		"currency":       currency,
		"mixed_currency": p.MTD.MixedCurrency || p.Daily30.MixedCurrency,
		"mtd": map[string]any{
			"cost": mtd, "from": ms.Format("2006-01-02"), "to": today.Format("2006-01-02"), "days": elapsed,
			"resources": p.MTD.Total.Resources,
		},
		"forecast":          forecast,
		"last_month":        map[string]any{"period": ms.AddDate(0, -1, 0).Format("2006-01"), "cost": lastMonth},
		"prev_mtd":          map[string]any{"cost": prevMTD, "days": elapsed},
		"mom_delta_pct":     deltaPercent(mtd, prevMTD),
		"avg_daily_30d":     avgDaily,
		"last_30d":          map[string]any{"cost": last30, "days_with_data": daysWithData},
		"resources_live":    p.Resources,
		"unpriced_skus":     p.MTD.Unpriced,
		"customers":         customers,
		"sources":           sources,
		"last_collected_at": p.LastCollect,
		"daily":             daily,
		"by_customer":       groupsOf(p.ByCustomer, true),
		"by_kind":           groupsOf(p.ByKind, false),
		"budgets":           budgets,
		"anomalies":         anomalies,
		"statements":        map[string]any{"draft": drafts, "issued": issued, "latest": latest},
	}
}

func addDecimal(a, b store.Decimal) store.Decimal {
	x, _ := rating.Sum(a, b)
	return x
}

func deltaPercent(cur, prev store.Decimal) *float64 {
	p := decFloat(prev)
	if p == 0 {
		return nil
	}
	d := (decFloat(cur) - p) / p * 100
	return &d
}

func (h *Handler) summary(w http.ResponseWriter, r *http.Request) {
	s, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	h.writeSummary(w, r, s.Scope(), "")
}

func (h *Handler) customerSummary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := h.requireCustomer(w, r, id, false)
	if !ok {
		return
	}
	h.writeSummary(w, r, s.Scope(), id)
}

func (h *Handler) writeSummary(w http.ResponseWriter, r *http.Request, scope store.Scope, customerID string) {
	parts, err := h.gatherSummary(r, scope, customerID)
	if err != nil {
		storeErr(w, err)
		return
	}
	h.enrichSummary(r, scope, customerID, &parts)
	writeJSON(w, http.StatusOK, composeSummary(parts))
}
