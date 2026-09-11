package store

import (
	"strings"
	"testing"
	"time"
)

// The window split (#6926, DESIGN.md §20) without a database: which whole
// UTC days the daily rollup may serve, and what is left for the live ledger.
// The property under test is that the two together cover the window EXACTLY
// — no instant in two branches (which would double-count) and none in
// neither (which would drop rows).

func ts(y, m, d, h int) time.Time { return time.Date(y, time.Month(m), d, h, 0, 0, 0, time.UTC) }

// coveredExcept is the state table's answer for a window whose days are all
// recorded and fresh apart from the ones named: those are stale, and so are
// absent from the covered set exactly as a day the table has never heard of.
func coveredExcept(from, to time.Time, stale ...time.Time) map[int64]bool {
	out := map[int64]bool{}
	for d := utcDay(from); d.Before(to); d = d.AddDate(0, 0, 1) {
		out[d.Unix()] = true
	}
	for _, d := range stale {
		delete(out, d.UTC().Unix())
	}
	return out
}

// assertCoversExactly walks the window minute by minute at day boundaries and
// the half-hours around them, and insists every instant falls in exactly one
// branch.
func assertCoversExactly(t *testing.T, w usageWindow, from, to time.Time) {
	t.Helper()
	in := func(rs []costRange, at time.Time) int {
		n := 0
		for _, r := range rs {
			if !at.Before(r.from) && at.Before(r.to) {
				n++
			}
		}
		return n
	}
	for at := from; at.Before(to); at = at.Add(30 * time.Minute) {
		if got := in(w.rollup, at) + in(w.live, at); got != 1 {
			t.Fatalf("%s is in %d branches, want exactly 1 (rollup=%v live=%v)", at, got, w.rollup, w.live)
		}
	}
	// Nothing may reach outside the window at either end.
	for _, rs := range [][]costRange{w.rollup, w.live} {
		for _, r := range rs {
			if r.from.Before(from) || r.to.After(to) {
				t.Fatalf("range %s..%s leaves the window %s..%s", r.from, r.to, from, to)
			}
		}
	}
	// A rollup range is always whole UTC days: a part-day would carry hours
	// the window did not ask for.
	for _, r := range w.rollup {
		if !r.from.Equal(utcDay(r.from)) || !r.to.Equal(utcDay(r.to)) {
			t.Fatalf("rollup range %s..%s is not day-aligned", r.from, r.to)
		}
	}
}

func TestSplitWindowCoversTheWindowExactly(t *testing.T) {
	cases := []struct {
		name           string
		from, to       time.Time
		covered        map[int64]bool
		wantRollupDays int
	}{
		{"whole month, nothing stale", ts(2026, 9, 1, 0), ts(2026, 10, 1, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 10, 1, 0)), 30},
		{"today is stale", ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), ts(2026, 9, 10, 0)), 9},
		{"a hole in the middle", ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), ts(2026, 9, 5, 0)), 9},
		{"two holes", ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), ts(2026, 9, 3, 0), ts(2026, 9, 7, 0)), 8},
		{"part-day at the start", ts(2026, 9, 1, 11), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0)), 9},
		{"part-day at the end", ts(2026, 9, 1, 0), ts(2026, 9, 10, 17), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0)), 9},
		{"part-days at both ends", ts(2026, 9, 1, 11), ts(2026, 9, 10, 17), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0)), 8},
		{"part-day and a hole", ts(2026, 9, 1, 11), ts(2026, 9, 10, 17), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), ts(2026, 9, 4, 0)), 7},
		{"shorter than a day", ts(2026, 9, 1, 3), ts(2026, 9, 1, 19), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 2, 0)), 0},
		{"exactly one day", ts(2026, 9, 1, 0), ts(2026, 9, 2, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 2, 0)), 1},
		{"everything stale", ts(2026, 9, 1, 0), ts(2026, 9, 4, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 4, 0), ts(2026, 9, 1, 0), ts(2026, 9, 2, 0), ts(2026, 9, 3, 0)), 0},
		// The state table says nothing at all. Absence is NOT emptiness: a
		// day it has never heard of may hold usage the rollup does not, so
		// every day goes to the live ledger.
		{"the state table knows nothing", ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), nil, 0},
		// It knows the first three days and nothing about the rest.
		{"the state table knows a prefix", ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 4, 0)), 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := splitWindow(c.from, c.to, c.covered)
			assertCoversExactly(t, w, c.from, c.to)
			days := 0
			for _, r := range w.rollup {
				days += int(r.to.Sub(r.from) / (24 * time.Hour))
			}
			if days != c.wantRollupDays {
				t.Fatalf("rollup covers %d days, want %d (%v)", days, c.wantRollupDays, w.rollup)
			}
			if c.wantRollupDays == 0 && len(w.rollup) != 0 {
				t.Fatalf("no whole day is servable but the split still names %v", w.rollup)
			}
		})
	}
}

// A run of fresh days is ONE range, not one per day: the reader's predicate
// is a range scan, and a month of separate equality tests would be a worse
// plan for no gain.
func TestSplitWindowMergesAdjacentDays(t *testing.T) {
	w := splitWindow(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), ts(2026, 9, 6, 0)))
	if len(w.rollup) != 2 || len(w.live) != 1 {
		t.Fatalf("rollup=%v live=%v", w.rollup, w.live)
	}
	if !w.rollup[0].from.Equal(ts(2026, 9, 1, 0)) || !w.rollup[0].to.Equal(ts(2026, 9, 6, 0)) {
		t.Fatalf("first range = %v", w.rollup[0])
	}
	if !w.live[0].from.Equal(ts(2026, 9, 6, 0)) || !w.live[0].to.Equal(ts(2026, 9, 7, 0)) {
		t.Fatalf("live range = %v", w.live[0])
	}
}

// The two branches of the usage CTE must project the same columns in the
// same order, aggregate the live half by the UTC clock, and keep the sampled
// measurements out — three things a UNION ALL would otherwise mis-align or
// silently admit.
func TestUsageBranchesAreOneShape(t *testing.T) {
	a := &costArgs{}
	w := splitWindow(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), coveredExcept(ts(2026, 9, 1, 0), ts(2026, 9, 11, 0), ts(2026, 9, 10, 0)))
	sqlText := usageBranches(a, w, grainDay)
	if !strings.Contains(sqlText, "FROM cost_usage_daily") || !strings.Contains(sqlText, "FROM usage_records") {
		t.Fatalf("both branches must be present: %s", sqlText)
	}
	if strings.Count(sqlText, costUsageProjection) != 2 {
		t.Fatalf("the two branches do not share one projection: %s", sqlText)
	}
	if !strings.Contains(sqlText, utcDayExpr) {
		t.Fatalf("the live branch must file records on the UTC clock: %s", sqlText)
	}
	if !strings.Contains(sqlText, metricSKUFilter) {
		t.Fatalf("the live branch must exclude the sampled measurements: %s", sqlText)
	}
	// Every window bound is a bind parameter, never text.
	if strings.Contains(sqlText, "2026-09") {
		t.Fatalf("a window bound was spliced into the SQL: %s", sqlText)
	}
	// One merged rollup range and one live range, two bounds each.
	if len(a.args) != 4 {
		t.Fatalf("args = %d: %v", len(a.args), a.args)
	}

	// Hour grain never reads the rollup, and files records by the hour.
	hourly := usageBranches(&costArgs{}, liveWindow(ts(2026, 9, 1, 0), ts(2026, 9, 2, 0)), grainHour)
	if strings.Contains(hourly, "cost_usage_daily") {
		t.Fatalf("hour grain must not read the daily rollup: %s", hourly)
	}
	if !strings.Contains(hourly, utcHourExpr) {
		t.Fatalf("hour grain must file records by the hour: %s", hourly)
	}

	// An empty split still yields a well-typed, empty relation rather than
	// invalid SQL.
	if empty := usageBranches(&costArgs{}, usageWindow{}, grainDay); !strings.Contains(empty, "WHERE false") {
		t.Fatalf("empty window = %s", empty)
	}
}

// The rollup builder aggregates by the SAME expression and the same key as
// the live branch. Two spellings of "a day" would be two ledgers.
func TestRollupBuildMatchesTheLiveAggregation(t *testing.T) {
	if !strings.Contains(costRollupBuildSQL, costUsageProjection) {
		t.Fatal("the builder does not use the shared projection")
	}
	if !strings.Contains(costRollupBuildSQL, utcDayExpr) {
		t.Fatal("the builder does not file records on the UTC clock")
	}
	if !strings.Contains(costRollupBuildSQL, metricSKUFilter) {
		t.Fatal("the builder does not exclude the sampled measurements")
	}
	if !strings.Contains(costRollupBuildSQL, "GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9") {
		t.Fatal("the builder groups by a different key than the live branch")
	}
}
