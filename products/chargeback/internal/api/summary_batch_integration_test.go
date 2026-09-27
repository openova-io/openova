package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The summary is one read of the priced ledger (#6867, store.CostBatch).
// Before the batch an overview issued 41 copies of the priced CTE and 41
// freshness reads (hw307); this pins the ceiling so it cannot creep back,
// counting statements through the driver (testdb.OpenCounted) — the
// statements themselves are the ones the service issues, unchanged.

// seedSummaryLedger writes 45 days of hourly usage up to now for two
// customers on a priced book, one global budget and one budget of A's, and
// builds the rollup with one day re-opened, so the summary crosses the
// rollup/live seam exactly as a live Sovereign's does.
func seedSummaryLedger(t *testing.T, st *store.Store, now time.Time) (a, b store.Customer) {
	t.Helper()
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{
		{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"},
		{SKU: "evs.ssd.gb", Unit: "gb-hour", UnitPrice: "0.001"},
		{SKU: "eip", Unit: "hour", UnitPrice: "0.02"},
	}, true); err != nil {
		t.Fatal(err)
	}
	if a, err = st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", StartDate: "2026-06-01"}); err != nil {
		t.Fatal(err)
	}
	if b, err = st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", StartDate: "2026-06-01"}); err != nil {
		t.Fatal(err)
	}
	srcA, _, err := st.UpsertSource(ctx, a.ID, "huawei-project", "me-east-1", "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	srcB, _, err := st.UpsertSource(ctx, b.ID, "huawei-project", "me-east-1", "proj-b")
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{srcA.ID, srcB.ID} {
		if err := st.SetSourcePriceBook(ctx, src, book.ID); err != nil {
			t.Fatal(err)
		}
	}
	var recs []store.UsageRecord
	rec := func(c store.Customer, src store.CostSource, res, kind, sku, unit string, qty float64, at time.Time, labels map[string]any) {
		lb, _ := json.Marshal(labels)
		recs = append(recs, store.UsageRecord{CustomerID: c.ID, SourceID: src.ID, ResourceID: res, ResourceKind: kind, SKU: sku,
			Quantity: store.Decimal(strconv.FormatFloat(qty, 'f', 6, 64)), Unit: unit, WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
	}
	start := dateOnly(now).AddDate(0, 0, -45)
	for at := start; at.Before(now); at = at.Add(time.Hour) {
		rec(a, srcA, "vm-1", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, at, map[string]any{"name": "web-1", "status": "ACTIVE"})
		rec(a, srcA, "vol-1", "evs", "evs.ssd.gb", "gb-hour", 100, at, map[string]any{"name": "vol-1"})
		rec(b, srcB, "eip-1", "eip", "eip", "hour", 1, at, map[string]any{"name": "1.2.3.4"})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.BudgetInput{
		{Name: "all", Amount: "1000", Currency: "OMR", Period: "monthly", Thresholds: []int{80, 100}, Active: true},
		{Name: "acme", CustomerID: &a.ID, Amount: "100", Currency: "OMR", Period: "monthly", Thresholds: []int{80, 100}, Active: true},
	} {
		if _, err := st.CreateBudget(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	for range 5 {
		parts, err := st.StaleCostRollupPartitions(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) == 0 {
			break
		}
		for _, p := range parts {
			if _, _, err := st.BuildCostRollupPartition(ctx, p); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A late record re-opens one day of A: that day is served live, the
	// rest from the rollup, inside one window.
	recs = recs[:0]
	rec(a, srcA, "vm-2", "ecs", "ecs.m7n.xlarge.8", "instance-hour", 1, dateOnly(now).AddDate(0, 0, -3).Add(5*time.Hour), map[string]any{"name": "batch-2", "status": "ACTIVE"})
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestIntegrationSummaryIsOneLedgerRead(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 30, 0, 0, time.UTC)
	st, log := testdb.OpenCounted(t)
	h, mail := newAPIAt(t, st, now)
	a, _ := seedSummaryLedger(t, st, now)
	op := &client{t: t, h: h}
	op.signIn(opEmail, mail)

	// The month-to-date figure the summary must carry, read on its own.
	ms := monthStart(now)
	direct, err := st.Explore(context.Background(), store.OperatorScope, store.CostQuery{From: ms, To: ms.AddDate(0, 1, 0), Granularity: "day", GroupBy: "none", Metric: "cost"})
	if err != nil {
		t.Fatal(err)
	}

	// ledgerReads returns the ledger statements a request issued and fails
	// the test when they exceed the ceiling.
	ledgerReads := func(path string, want int) []string {
		t.Helper()
		log.Reset()
		sum := op.must("GET", path, 200)
		got := log.Ledger()
		if len(got) > 6 || len(got) != want {
			var heads []string
			for _, q := range got {
				heads = append(heads, strings.SplitN(strings.TrimSpace(q), "\n", 2)[0])
			}
			t.Fatalf("%s issued %d ledger statements (want %d, ceiling 6) among %d in all:\n  %s", path, len(got), want, len(log.Statements()), strings.Join(heads, "\n  "))
		}
		t.Logf("%s: %d ledger statements among %d in all", path, len(got), len(log.Statements()))
		if mtd := sum["mtd"].(map[string]any)["cost"].(float64); mtd != f(direct.Total.Current) {
			t.Fatalf("%s mtd.cost = %v, want %s (the explorer's own answer)", path, mtd, direct.Total.Current)
		}
		return got
	}
	// The operator's overview: the freshness read, the operator's statement
	// (six documents, the global budget, the anomaly series) and the
	// customer budget's statement — a second filter set.
	for _, path := range []string{"/api/v1/cost/summary", "/api/v1/overview"} {
		reads := ledgerReads(path, 3)
		if !strings.Contains(reads[0], "cost_rollup_state") {
			t.Fatalf("%s: first ledger statement is not the freshness read:\n%s", path, reads[0])
		}
		if n := strings.Count(strings.Join(reads[1:], "\n"), "WITH u AS ("); n != 2 {
			t.Fatalf("%s: %d priced CTEs, want 2", path, n)
		}
	}
	// The customer lens: everything the page shows is one filter set.
	log.Reset()
	sum := op.must("GET", "/api/v1/customers/"+a.ID+"/cost/summary", 200)
	if got := log.Ledger(); len(got) != 2 {
		t.Fatalf("customer summary issued %d ledger statements, want 2 (freshness read + one statement)", len(got))
	}
	if budgets := sum["budgets"].([]any); len(budgets) != 1 {
		t.Fatalf("customer summary budgets = %v, want A's alone", budgets)
	}
	// Turned off, the rollup costs nothing to ask about: no freshness read.
	st.SetCostRollupEnabled(false)
	ledgerReads("/api/v1/cost/summary", 2)
}

func f(d store.Decimal) float64 {
	v, _ := strconv.ParseFloat(string(d), 64)
	return v
}
