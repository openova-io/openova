package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// Wire contract between lanes (#6867).
//
// The overview rendered every KPI as zero on hw307 because the API wrote
// `customers` and `last_period` while the page read `customers_by_status`
// and `rated_total_last_period` — two lanes, no test across the boundary.
//
// This test writes the summary and explore documents the Go side produces
// into ui/src/api/fixtures/*.json. The UI's vitest suite parses THOSE files
// through its own readers and asserts non-zero KPIs, so a renamed key fails
// a test on whichever side moved. Regenerate with:
//
//	go test ./internal/api -run TestWireContractFixtures -update

var update = flag.Bool("update", false, "rewrite the UI wire-contract fixtures")

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "ui", "src", "api", "fixtures", name)
}

func sampleExplore() store.ExploreResult {
	pct := func(v float64) *float64 { return &v }
	return store.ExploreResult{
		From: "2026-09-01", To: "2026-09-08", Granularity: "day", GroupBy: "kind", Metric: "cost", Currency: "OMR",
		Buckets:       []string{"2026-09-01", "2026-09-02", "2026-09-03", "2026-09-04", "2026-09-05", "2026-09-06", "2026-09-07"},
		BucketHasData: []bool{true, true, true, true, true, true, true},
		Groups: []store.CostGroup{
			{Key: "ecs", Label: "Elastic Cloud Server", Total: "84.000000", Previous: "42.000000", DeltaPct: pct(100), Share: 0.8065, Resources: 2,
				Values: []store.Decimal{"12.000000", "12.000000", "12.000000", "12.000000", "12.000000", "12.000000", "12.000000"}},
			{Key: "evs", Label: "Block storage (EVS)", Total: "16.800000", Previous: "0.000000", DeltaPct: nil, Share: 0.1613, Resources: 1,
				Values: []store.Decimal{"2.400000", "2.400000", "2.400000", "2.400000", "2.400000", "2.400000", "2.400000"}},
		},
		Other: &store.CostGroup{Key: "other", Label: "Other", Total: "3.360000", Previous: "0.000000", Share: 0.0323, Resources: 1,
			Values: []store.Decimal{"0.480000", "0.480000", "0.480000", "0.480000", "0.480000", "0.480000", "0.480000"}},
		Total:          store.CostTotal{Current: "104.160000", Previous: "42.000000", DeltaPct: pct(148), Resources: 4},
		TotalsByBucket: []store.Decimal{"14.880000", "14.880000", "14.880000", "14.880000", "14.880000", "14.880000", "14.880000"},
		Unpriced:       []store.UnpricedSKU{{SKU: "k8s.vcpu", Unit: "vcpu-hour", Quantity: "84.000000", Resources: 1}},
		Compare:        store.CompareWindow{From: "2026-08-25", To: "2026-09-01", Label: store.CompareLabelPrevious},
	}
}

func sampleParts() summaryParts {
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	mtd := sampleExplore()
	mtd.GroupBy = "none"
	mtd.Groups = nil
	mtd.Other = nil
	daily := mtd
	byCustomer := sampleExplore()
	byCustomer.GroupBy = "customer"
	byCustomer.Groups = []store.CostGroup{{Key: "c-1", Label: "Acme", Total: "100.800000", Previous: "42.000000", Share: 0.97, Resources: 3}, {Key: "c-2", Label: "Bravo", Total: "3.360000", Previous: "0.000000", Share: 0.03, Resources: 1}}
	byCustomer.Other = nil
	last := mtd
	last.Granularity = "month"
	last.Buckets = []string{"2026-08"}
	last.BucketHasData = []bool{true}
	last.TotalsByBucket = []store.Decimal{"42.000000"}
	last.Total = store.CostTotal{Current: "42.000000", Previous: "0.000000"}
	issued := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	return summaryParts{
		Now: now, Profile: "sovereign",
		MTD: mtd, Daily30: daily, LastMonth: last, PrevMTD: last,
		ByCustomer: byCustomer, ByKind: sampleExplore(),
		Customers:   map[string]int{"active": 2, "pending": 0, "suspended": 0},
		Sources:     map[string]int{"verified": 3, "pending": 0, "failed": 0},
		Resources:   126,
		LastCollect: &now,
		Statements: []store.Statement{
			{ID: "s-1", CustomerID: "c-1", CustomerName: "Acme", PeriodStart: "2026-08-01", PeriodEnd: "2026-08-31", Currency: "OMR", Subtotal: "42.000000", TaxRate: "0.05", Tax: "2.100000", Total: "44.100000", Status: "issued", IssuedAt: &issued, CreatedAt: issued},
			{ID: "s-2", CustomerID: "c-1", CustomerName: "Acme", PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30", Currency: "OMR", Subtotal: "100.800000", TaxRate: "0.05", Tax: "5.040000", Total: "105.840000", Status: "draft", CreatedAt: issued},
		},
	}
}

func TestWireContractFixtures(t *testing.T) {
	parts := sampleParts()
	// The seam the budgets/anomalies lanes fill: the fixture carries one row
	// of each so the UI parses the shape, not just the empty array.
	parts.Budgets = []map[string]any{{"id": "b-1", "name": "September", "customer_id": nil, "amount": "3000.000000", "currency": "OMR", "actual": "104.160000", "forecast": 2712.5, "pct_actual": 3.47, "pct_forecast": 90.4, "status": "warning", "thresholds": []map[string]any{{"pct": 50, "crossed": false}, {"pct": 80, "crossed": false}, {"pct": 100, "crossed": false}}}}
	parts.Anomalies = []map[string]any{{"day": "2026-09-03", "customer_id": "c-1", "customer_name": "Acme", "dimension": "kind", "key": "ecs", "label": "Elastic Cloud Server", "expected": 12.0, "actual": 24.0, "impact": 12.0, "score": 4.2, "drivers": []map[string]any{{"kind": "resource", "key": "vm-2", "label": "batch-2", "delta": 12.0}}}}

	f, _ := rating.ForecastMonth(parts.Now, []rating.DayCost{{Day: "2026-09-01", Cost: 14.88}, {Day: "2026-09-02", Cost: 14.88}, {Day: "2026-09-03", Cost: 14.88}, {Day: "2026-09-04", Cost: 14.88}, {Day: "2026-09-05", Cost: 14.88}, {Day: "2026-09-06", Cost: 14.88}, {Day: "2026-09-07", Cost: 14.88}})
	docs := map[string]any{
		"summary.json": composeSummary(parts),
		"explore.json": exploreDoc{ExploreResult: sampleExplore(), Forecast: &f},
	}
	for name, doc := range docs {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			t.Fatal(err)
		}
		path := fixturePath(t, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (run with -update)", name, err)
		}
		if !bytes.Equal(want, buf.Bytes()) {
			t.Fatalf("%s drifted from the Go document; regenerate with -update and re-run the UI tests", name)
		}
	}
	// The summary must carry the keys the page reads — pinned here by name so
	// a rename on the Go side fails before the fixture is even compared.
	sum := composeSummary(parts)
	for _, k := range []string{"currency", "mtd", "forecast", "last_month", "prev_mtd", "mom_delta_pct", "avg_daily_30d", "resources_live", "unpriced_skus", "customers", "sources", "last_collected_at", "daily", "by_customer", "by_kind", "budgets", "anomalies", "statements"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("summary lacks %q", k)
		}
	}
	if sum["mtd"].(map[string]any)["cost"] != store.Decimal("104.160000") {
		t.Fatalf("mtd cost = %v", sum["mtd"])
	}
	if sum["mom_delta_pct"].(*float64) == nil {
		t.Fatal("mom delta must be computed when last month is non-zero")
	}
}

// TestForecastWireKeys pins the forecast object's keys by name (#6867
// follow-up): the projection the chart draws its hatched tail from, and the
// weekday factors the overview tooltip lists. `weekday_factors` is present
// only for the weekday-seasonal method (≥ 14 complete days); `projection` is
// always there and sums to month_end − observed.
func TestForecastWireKeys(t *testing.T) {
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	var long []rating.DayCost
	for d := 1; d <= 21; d++ {
		day := time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC)
		c := 100.0
		if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
			c = 50
		}
		long = append(long, rating.DayCost{Day: day.Format("2006-01-02"), Cost: c})
	}
	roundTrip := func(f rating.Forecast) map[string]any {
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	seasonal, _ := rating.ForecastMonth(now, long)
	m := roundTrip(seasonal)
	for _, k := range []string{"month_end", "run_rate_daily", "trend_daily", "method", "days_observed", "days_in_month", "confidence", "projection", "weekday_factors"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("seasonal forecast lacks %q: %v", k, m)
		}
	}
	proj := m["projection"].([]any)
	if len(proj) != 9 || proj[0].(map[string]any)["day"] != "2026-09-22" || proj[8].(map[string]any)["day"] != "2026-09-30" {
		t.Fatalf("projection = %v, want Sep 22–30", proj)
	}
	if wf := m["weekday_factors"].(map[string]any); len(wf) != 7 || wf["Sat"].(float64) >= wf["Wed"].(float64) {
		t.Fatalf("weekday_factors = %v", wf)
	}
	short, _ := rating.ForecastMonth(time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC), long[:7])
	if m := roundTrip(short); m["weekday_factors"] != nil {
		t.Fatalf("a %s forecast must not carry weekday_factors: %v", short.Method, m["weekday_factors"])
	} else if len(m["projection"].([]any)) != 23 {
		t.Fatalf("projection = %v", m["projection"])
	}
}

func TestParseCostQueryDefaultsAndValidation(t *testing.T) {
	h := &Handler{Deps: Deps{Now: func() time.Time { return time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC) }}}
	q, msg := h.parseCostQuery(mustReq("/api/v1/cost/explore"))
	if msg != "" || q.From.Format("2006-01-02") != "2026-08-10" || q.To.Format("2006-01-02") != "2026-09-09" || q.GroupBy != "none" || q.Limit != 10 {
		t.Fatalf("defaults = %+v %q", q, msg)
	}
	q, msg = h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-08&group_by=kind&kind=ecs,evs&exclude_customer=c-2&limit=3&granularity=month"))
	if msg != "" || q.GroupBy != "kind" || len(q.Include["kind"]) != 2 || q.Exclude["customer"][0] != "c-2" || q.Limit != 3 || q.Granularity != "month" {
		t.Fatalf("parsed = %+v %q", q, msg)
	}
	for _, bad := range []string{"/x?from=2026-09-08&to=2026-09-01", "/x?group_by=colour", "/x?metric=usage", "/x?metric=usage&group_by=kind", "/x?granularity=week", "/x?limit=-1", "/x?from=2025-01-01&to=2026-09-01"} {
		if _, msg := h.parseCostQuery(mustReq(bad)); msg == "" {
			t.Fatalf("%s must be rejected", bad)
		}
	}
	if _, msg := h.parseCostQuery(mustReq("/x?metric=usage&sku=ecs.m7n.xlarge.8")); msg != "" {
		t.Fatalf("usage with one sku filter is valid: %s", msg)
	}
}

// Hourly grain is bounded by days, not only by the bucket ceiling: 15 days is
// 360 buckets — under 400 — and is still refused, with the limit in the message.
func TestParseCostQueryHourlyWindowLimit(t *testing.T) {
	h := &Handler{Deps: Deps{Now: func() time.Time { return time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC) }}}
	q, msg := h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-15&granularity=hour"))
	if msg != "" || q.Granularity != "hour" {
		t.Fatalf("14 hourly days must parse: %+v %q", q, msg)
	}
	if n := len(store.Buckets(q.From, q.To, q.Granularity)); n != 336 {
		t.Fatalf("14 days = %d hour buckets, want 336", n)
	}
	q, msg = h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-02&granularity=hour"))
	if msg != "" || len(store.Buckets(q.From, q.To, q.Granularity)) != 24 {
		t.Fatalf("one hourly day = %v %q", store.Buckets(q.From, q.To, q.Granularity), msg)
	}
	_, msg = h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-16&granularity=hour"))
	if msg == "" || !strings.Contains(msg, "14 days") || !strings.Contains(msg, "15 requested") {
		t.Fatalf("15 hourly days must be refused naming the limit: %q", msg)
	}
	// The default 30-day window at hour grain is refused the same way.
	if _, msg := h.parseCostQuery(mustReq("/x?granularity=hour")); msg == "" || !strings.Contains(msg, "14 days") {
		t.Fatalf("default window at hour grain: %q", msg)
	}
	// Day and month grain keep the 400-bucket ceiling as their only bound.
	if _, msg := h.parseCostQuery(mustReq("/x?from=2025-09-01&to=2026-09-01&granularity=day")); msg != "" {
		t.Fatalf("365 daily buckets are fine: %q", msg)
	}
}

func TestParseCostQueryCompareWindow(t *testing.T) {
	h := &Handler{Deps: Deps{Now: func() time.Time { return time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC) }}}
	q, msg := h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-08"))
	if msg != "" || !q.CompareFrom.IsZero() || !q.CompareTo.IsZero() {
		t.Fatalf("no compare params must leave the automatic window: %+v %q", q, msg)
	}
	q, msg = h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-08&compare_from=2026-08-01&compare_to=2026-08-08"))
	if msg != "" || q.CompareFrom.Format("2006-01-02") != "2026-08-01" || q.CompareTo.Format("2006-01-02") != "2026-08-08" {
		t.Fatalf("compare window = %v..%v %q", q.CompareFrom, q.CompareTo, msg)
	}
	// A different length than the window, and overlap with it, are both allowed.
	if _, msg := h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-08&compare_from=2026-08-01&compare_to=2026-09-01")); msg != "" {
		t.Fatalf("31-day compare for a 7-day window is valid: %q", msg)
	}
	if _, msg := h.parseCostQuery(mustReq("/x?from=2026-09-01&to=2026-09-08&compare_from=2026-09-04&compare_to=2026-09-08")); msg != "" {
		t.Fatalf("overlapping compare window is valid: %q", msg)
	}
	for _, bad := range []string{
		"/x?compare_from=2026-08-01",                              // one side only
		"/x?compare_to=2026-08-08",                                // one side only
		"/x?compare_from=2026-08-08&compare_to=2026-08-01",        // reversed
		"/x?compare_from=2026-08-01&compare_to=2026-08-01",        // empty
		"/x?compare_from=01-08-2026&compare_to=2026-08-08",        // malformed
		"/x?compare_from=2026-08-01&compare_to=2026-08-08T00:00Z", // not a day
	} {
		if _, msg := h.parseCostQuery(mustReq(bad)); msg == "" {
			t.Fatalf("%s must be rejected", bad)
		}
	}
}

func TestExploreCSVNameCarriesCustomCompare(t *testing.T) {
	doc := exploreDoc{ExploreResult: sampleExplore()}
	if got := exploreCSVName(doc); got != "cost-kind-2026-09-01-2026-09-08.csv" {
		t.Fatalf("automatic compare must not change the name: %q", got)
	}
	doc.Compare = store.CompareWindow{From: "2026-08-01", To: "2026-08-08", Label: store.CompareLabelCustom}
	if got := exploreCSVName(doc); got != "cost-kind-2026-09-01-2026-09-08-vs-2026-08-01-2026-08-08.csv" {
		t.Fatalf("custom compare name = %q", got)
	}
	rec := httptest.NewRecorder()
	writeExploreCSV(rec, doc)
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "-vs-2026-08-01-2026-08-08.csv") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	// Rows stay per bucket: header + 7 buckets × (2 groups + other).
	if lines := strings.Count(strings.TrimSpace(rec.Body.String()), "\n") + 1; lines != 1+7*3 {
		t.Fatalf("csv lines = %d", lines)
	}
}

func TestForecastOnlyForTheCurrentMonthWindow(t *testing.T) {
	h := &Handler{Deps: Deps{Now: func() time.Time { return time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC) }}}
	res := sampleExplore()
	if f := h.forecastFor(res, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); f == nil || f.DaysObserved != 7 {
		t.Fatalf("current-month window must forecast: %+v", f)
	}
	if f := h.forecastFor(res, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)); f != nil {
		t.Fatal("last month must not forecast")
	}
	res.Granularity = "month"
	if f := h.forecastFor(res, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); f != nil {
		t.Fatal("month grain must not forecast")
	}
}

func mustReq(url string) *http.Request { return httptest.NewRequest(http.MethodGet, url, nil) }
