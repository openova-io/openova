package api

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
