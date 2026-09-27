package store

import (
	"strings"
	"testing"
	"time"
)

// The batch (#6867) without a database: which questions share a statement,
// what the statement holds, and where a window filter may and may not go.

// summaryQuestions are the six explorer questions the overview asks, as
// gatherSummary builds them for a clock inside September 2026.
func summaryQuestions() []CostQuery {
	ms, nextMs, lastMs := ts(2026, 9, 1, 0), ts(2026, 10, 1, 0), ts(2026, 8, 1, 0)
	today := ts(2026, 9, 27, 0)
	base := func(from, to time.Time, gran, groupBy string, limit int) CostQuery {
		return CostQuery{From: from, To: to, Granularity: gran, GroupBy: groupBy, Metric: "cost", Limit: limit}
	}
	return []CostQuery{
		base(ms, nextMs, "day", "none", 0),
		base(today.AddDate(0, 0, -29), today.AddDate(0, 0, 1), "day", "none", 0),
		base(lastMs, ms, "month", "none", 0),
		base(lastMs, lastMs.AddDate(0, 0, 27), "month", "none", 0),
		base(ms, nextMs, "month", "customer", 10),
		base(ms, nextMs, "month", "kind", 10),
	}
}

func TestCostBatchSharesOneStatementPerFilterSet(t *testing.T) {
	b := (&Store{}).NewCostBatch()
	for _, q := range summaryQuestions() {
		if _, err := b.Explore(OperatorScope, q); err != nil {
			t.Fatal(err)
		}
	}
	// A customer-scoped budget's month, and the anomaly detector's series.
	budget := CostQuery{From: ts(2026, 9, 1, 0), To: ts(2026, 10, 1, 0), Granularity: "day", GroupBy: "none", Metric: "cost", CustomerID: "c-1"}
	if _, err := b.Explore(OperatorScope, budget); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DailyCostByCustomerKind(OperatorScope, "", ts(2026, 9, 7, 0), ts(2026, 9, 28, 0)); err != nil {
		t.Fatal(err)
	}
	if len(b.order) != 2 {
		t.Fatalf("groups = %d, want 2 (the operator's questions and the customer budget)", len(b.order))
	}
	g := b.groups[b.order[0]]
	// Six questions × (rows, previous rows, unpriced, unconverted, count) is
	// 30 aggregates; the three month-to-date questions share one unpriced
	// list, one unconverted list and one count, so 24 — plus the series.
	if len(g.branches) != 25 {
		t.Fatalf("operator group holds %d branches, want 25", len(g.branches))
	}
	kinds := map[branchKind]int{}
	for _, br := range g.branches {
		kinds[br.kind]++
	}
	if kinds[branchRows] != 12 || kinds[branchUnpriced] != 4 || kinds[branchUnconverted] != 4 || kinds[branchCount] != 4 || kinds[branchDaily] != 1 {
		t.Fatalf("branch kinds = %v", kinds)
	}
	if g.hull.from != ts(2026, 7, 1, 0) || g.hull.to != ts(2026, 10, 1, 0) {
		t.Fatalf("hull = %s..%s, want July through September (last month's compare window through month end)", g.hull.from, g.hull.to)
	}
	sqlText, args, err := renderGroup(g, liveWindow(g.hull.from, g.hull.to))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(sqlText, "WITH u AS ("); n != 1 {
		t.Fatalf("statement builds the usage CTE %d times, want once", n)
	}
	if n := strings.Count(sqlText, "UNION ALL"); n != 25 {
		t.Fatalf("statement has %d UNION ALL arms, want 25 (24 aggregates + the series after the currency row)", n)
	}
	// Every branch of a whole-day hull keeps its own window as a filter, and
	// the customer budget's statement carries the customer predicate the
	// operator's must not.
	a := &costArgs{}
	for _, br := range g.branches {
		if s := br.render(a, g); !strings.Contains(s, "window_start >= $") || !strings.Contains(s, "AND window_start < $") {
			t.Fatalf("branch %s has no window filter:\n%s", br.tag, s)
		}
	}
	if strings.Contains(sqlText, "customer_id::text = ANY(") {
		t.Fatal("the operator's statement filters by customer")
	}
	if len(args) == 0 {
		t.Fatal("no bound arguments")
	}
	budgetSQL, _, err := renderGroup(b.groups[b.order[1]], liveWindow(ts(2026, 8, 1, 0), ts(2026, 10, 1, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(budgetSQL, "u.customer_id::text = ANY(") {
		t.Fatal("the customer budget's statement does not filter by customer")
	}
}

func TestCostBatchUnalignedWindowGetsItsOwnExactRead(t *testing.T) {
	b := (&Store{}).NewCostBatch()
	// Noon to noon: the rollup's whole-day rows cannot serve it and a filter
	// on the day-truncated window_start would admit the morning.
	q := CostQuery{From: ts(2026, 9, 1, 12), To: ts(2026, 9, 8, 12), Granularity: "day", GroupBy: "none", Metric: "cost"}
	if _, err := b.Explore(OperatorScope, q); err != nil {
		t.Fatal(err)
	}
	if len(b.order) != 2 {
		t.Fatalf("groups = %d, want 2: the window and its (also unaligned) compare window each read exactly", len(b.order))
	}
	for _, key := range b.order {
		g := b.groups[key]
		if !g.exact {
			t.Fatalf("group %q is not exact", key)
		}
		a := &costArgs{}
		for _, br := range g.branches {
			if s := br.render(a, g); strings.Contains(s, "window_start >=") {
				t.Fatalf("exact group's branch %s filters on window_start:\n%s", br.tag, s)
			}
		}
	}
}

func TestCostBatchHourGrainReadsTheLedgerOnlyForTheChart(t *testing.T) {
	b := (&Store{}).NewCostBatch()
	q := CostQuery{From: ts(2026, 9, 1, 0), To: ts(2026, 9, 3, 0), Granularity: "hour", GroupBy: "none", Metric: "cost"}
	if _, err := b.Explore(OperatorScope, q); err != nil {
		t.Fatal(err)
	}
	if len(b.order) != 2 {
		t.Fatalf("groups = %d, want 2: the hourly chart and the day-grain totals", len(b.order))
	}
	grains := map[string]int{}
	for _, key := range b.order {
		g := b.groups[key]
		grains[g.grain] = len(g.branches)
	}
	if grains[grainHour] != 1 || grains[grainDay] != 4 {
		t.Fatalf("branches per grain = %v, want 1 hourly (the chart) and 4 daily", grains)
	}
}

func TestCostBatchRejectsBeforeAsking(t *testing.T) {
	b := (&Store{}).NewCostBatch()
	if _, err := b.Explore(OperatorScope, CostQuery{From: ts(2026, 9, 2, 0), To: ts(2026, 9, 1, 0)}); err == nil {
		t.Fatal("empty window accepted")
	}
	if _, err := b.Explore(OperatorScope, CostQuery{From: ts(2026, 9, 1, 0), To: ts(2026, 9, 2, 0), GroupBy: "nope"}); err == nil {
		t.Fatal("unknown group_by accepted")
	}
	if _, err := b.Explore(CustomerScope("c-1"), CostQuery{From: ts(2026, 9, 1, 0), To: ts(2026, 9, 2, 0), CustomerID: "c-2"}); err == nil {
		t.Fatal("another customer's rows accepted")
	}
	if len(b.order) != 0 {
		t.Fatalf("a refused question left %d groups behind", len(b.order))
	}
}
