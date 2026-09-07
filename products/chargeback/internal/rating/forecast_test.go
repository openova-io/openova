package rating

import (
	"math"
	"testing"
	"time"
)

// days builds consecutive complete days from 2026-09-01 (a Tuesday).
func days(costs ...float64) []DayCost {
	out := make([]DayCost, len(costs))
	for i, c := range costs {
		out[i] = DayCost{Day: time.Date(2026, 9, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Cost: c}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func sum(d []DayCost) float64 {
	s := 0.0
	for _, x := range d {
		s += x.Cost
	}
	return s
}

// checkProjection asserts the invariants every method must keep: one entry
// per remaining calendar day starting today and ending at month end, none
// negative, and the entries summing exactly to MonthEnd − observed.
func checkProjection(t *testing.T, f Forecast, now time.Time, observed float64) {
	t.Helper()
	remaining := f.DaysInMonth - (now.Day() - 1)
	if len(f.Projection) != remaining {
		t.Fatalf("projection has %d days, want %d", len(f.Projection), remaining)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for i, p := range f.Projection {
		if want := today.AddDate(0, 0, i).Format("2006-01-02"); p.Day != want {
			t.Fatalf("projection[%d].Day = %s, want %s", i, p.Day, want)
		}
		if p.Cost < 0 {
			t.Fatalf("projection[%d] = %v is negative", i, p.Cost)
		}
	}
	if !near(observed+sum(f.Projection), f.MonthEnd) {
		t.Fatalf("observed %v + projection %v ≠ month end %v", observed, sum(f.Projection), f.MonthEnd)
	}
}

func TestForecastMonth_NoDays(t *testing.T) {
	if _, ok := ForecastMonth(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), nil); ok {
		t.Fatal("a forecast from nothing must not exist")
	}
}

func TestForecastMonth_MethodByDaysObserved(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{1, "run-rate-1d"}, {6, "run-rate-6d"}, {7, "run-rate-7d+trend"}, {13, "run-rate-7d+trend"}, {14, "weekday-seasonal"}, {28, "weekday-seasonal"}} {
		flat := make([]float64, c.n)
		for i := range flat {
			flat[i] = 10
		}
		f, _ := ForecastMonth(time.Date(2026, 9, c.n+1, 0, 0, 0, 0, time.UTC), days(flat...))
		if f.Method != c.want {
			t.Fatalf("%d days → %s, want %s", c.n, f.Method, c.want)
		}
		if (f.WeekdayFactors != nil) != (c.want == "weekday-seasonal") {
			t.Fatalf("%d days: weekday factors present=%v for %s", c.n, f.WeekdayFactors != nil, f.Method)
		}
	}
}

func TestForecastMonth_RunRateUsesLastSevenDaysOnly(t *testing.T) {
	// 10 complete days: 3 quiet days then 7 at 100/day. The run rate must be
	// 100, not the 10-day mean (79) — a mutant averaging every day fails here.
	now := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	series := days(30, 30, 30, 100, 100, 100, 100, 100, 100, 100)
	f, ok := ForecastMonth(now, series)
	if !ok {
		t.Fatal("expected a forecast")
	}
	if !near(f.RunRateDaily, 100) {
		t.Fatalf("run rate = %v, want 100", f.RunRateDaily)
	}
	if f.Method != "run-rate-7d+trend" || f.DaysObserved != 10 || f.DaysInMonth != 30 {
		t.Fatalf("meta = %+v", f)
	}
	if f.Confidence != "medium" {
		t.Fatalf("confidence = %s, want medium (10 days)", f.Confidence)
	}
	// The step up reads as a rising trend, so every remaining day projects
	// above the flat run rate and month end is above observed + 100 × 20.
	if f.TrendDaily <= 0 {
		t.Fatalf("trend = %v, want positive", f.TrendDaily)
	}
	for _, p := range f.Projection {
		if p.Cost <= 100 {
			t.Fatalf("%s projected %v, want > run rate 100", p.Day, p.Cost)
		}
	}
	if f.MonthEnd <= 790+100*20 {
		t.Fatalf("month end = %v, want > flat 2790", f.MonthEnd)
	}
	checkProjection(t, f, now, 790)
}

func TestForecastMonth_FewDaysIsLowConfidence(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, days(10, 20, 30))
	if f.Method != "run-rate-3d" || f.Confidence != "low" {
		t.Fatalf("got %+v", f)
	}
	// mean 20 × (30 − 3) + 60: below a week the trend is reported, not applied
	if !near(f.MonthEnd, 60+20*27) {
		t.Fatalf("month end = %v", f.MonthEnd)
	}
	if !near(f.TrendDaily, 10) {
		t.Fatalf("trend = %v, want 10 (reported even when not applied)", f.TrendDaily)
	}
	for _, p := range f.Projection {
		if !near(p.Cost, 20) {
			t.Fatalf("%s projected %v, want the flat run rate 20", p.Day, p.Cost)
		}
	}
	checkProjection(t, f, now, 60)
}

func TestForecastMonth_HighConfidenceNeedsTwoWeeksAndStability(t *testing.T) {
	now := time.Date(2026, 9, 16, 0, 0, 0, 0, time.UTC)
	stable := make([]float64, 15)
	for i := range stable {
		stable[i] = 50
	}
	f, _ := ForecastMonth(now, days(stable...))
	if f.Confidence != "high" {
		t.Fatalf("15 flat days should be high, got %s", f.Confidence)
	}
	// Same length, wildly varying last week: not high.
	noisy := append([]float64{}, stable...)
	copy(noisy[8:], []float64{5, 200, 5, 200, 5, 200, 5})
	f, _ = ForecastMonth(now, days(noisy...))
	if f.Confidence == "high" {
		t.Fatalf("a noisy last week must not be high confidence")
	}
}

func TestForecastMonth_NoisyLastWeekIsLowConfidenceEvenAfterAWeek(t *testing.T) {
	// 10 days, last seven alternating 10 / 190: CV ≈ 1 ≥ 0.5 → low, where
	// the day-count rule alone would say medium.
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, days(100, 100, 100, 10, 190, 10, 190, 10, 190, 10))
	if f.Confidence != "low" {
		t.Fatalf("confidence = %s, want low for a noisy last week", f.Confidence)
	}
	// Mild variation (CV ≈ 0.11) over the same ten days stays medium.
	f, _ = ForecastMonth(now, days(100, 100, 100, 90, 110, 90, 110, 90, 110, 90))
	if f.Confidence != "medium" {
		t.Fatalf("confidence = %s, want medium", f.Confidence)
	}
}

func TestForecastMonth_TrendSlope(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, days(10, 20, 30, 40, 50, 60, 70))
	if !near(f.TrendDaily, 10) {
		t.Fatalf("slope = %v, want 10/day", f.TrendDaily)
	}
	f, _ = ForecastMonth(now, days(70, 60, 50, 40, 30, 20, 10))
	if !near(f.TrendDaily, -10) {
		t.Fatalf("slope = %v, want -10/day", f.TrendDaily)
	}
}

func TestForecastMonth_TrendContinuesTheLineFromTheWindowCentre(t *testing.T) {
	// 10 days rising 10/day: run rate 70 (mean of 40…100), slope 10. The
	// projection must continue the line — today 110, then 120, 130, … — not
	// restart it from the 7-day mean (a mutant anchoring at the last day
	// projects today at 80, below yesterday's observed 100).
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	series := days(10, 20, 30, 40, 50, 60, 70, 80, 90, 100)
	f, _ := ForecastMonth(now, series)
	if f.Method != "run-rate-7d+trend" || !near(f.RunRateDaily, 70) || !near(f.TrendDaily, 10) {
		t.Fatalf("meta = %+v", f)
	}
	for k, p := range f.Projection {
		if want := 100 + 10*float64(k+1); !near(p.Cost, want) {
			t.Fatalf("projection[%d] (%s) = %v, want %v", k, p.Day, p.Cost, want)
		}
	}
	// 550 observed + Σ_{k=1..20} (100 + 10k) = 550 + 2000 + 2100
	if !near(f.MonthEnd, 550+2000+2100) {
		t.Fatalf("month end = %v, want 4650", f.MonthEnd)
	}
	checkProjection(t, f, now, 550)
}

func TestForecastMonth_FallingTrendFloorsAtZero(t *testing.T) {
	// 10 days falling 10/day to 10: the line crosses zero on the first
	// projected day; nothing may project negative and month end can never
	// be below what was already spent.
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	series := days(100, 90, 80, 70, 60, 50, 40, 30, 20, 10)
	f, _ := ForecastMonth(now, series)
	if !near(f.TrendDaily, -10) {
		t.Fatalf("slope = %v", f.TrendDaily)
	}
	for _, p := range f.Projection {
		if p.Cost < 0 {
			t.Fatalf("%s projected negative: %v", p.Day, p.Cost)
		}
	}
	if !near(f.MonthEnd, 550) {
		t.Fatalf("month end = %v, want 550 (observed only; every remaining day floors at 0)", f.MonthEnd)
	}
	checkProjection(t, f, now, 550)
}

// weekShape builds n consecutive days from 2026-09-01 with weekday cost wd
// and weekend cost we.
func weekShape(n int, wd, we float64) []DayCost {
	out := make([]DayCost, n)
	for i := range out {
		d := time.Date(2026, 9, i+1, 0, 0, 0, 0, time.UTC)
		c := wd
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			c = we
		}
		out[i] = DayCost{Day: d.Format("2006-01-02"), Cost: c}
	}
	return out
}

func byDay(f Forecast) map[string]float64 {
	m := map[string]float64{}
	for _, p := range f.Projection {
		m[p.Day] = p.Cost
	}
	return m
}

func TestForecastMonth_WeekdaySeasonalFollowsTheWeeklyShape(t *testing.T) {
	// Three full weeks (Sep 1–21, 2026) of 100 on weekdays and 50 at the
	// weekend, forecast on Tuesday Sep 22. A flat run rate would project
	// every remaining day at the last week's mean (600/7 ≈ 85.7); the
	// seasonal method must project Saturday at half of Wednesday.
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	series := weekShape(21, 100, 50)
	f, ok := ForecastMonth(now, series)
	if !ok || f.Method != "weekday-seasonal" || f.DaysObserved != 21 {
		t.Fatalf("meta = %+v", f)
	}
	p := byDay(f)
	sat, wed := p["2026-09-26"], p["2026-09-23"]
	if !near(sat, 0.5*wed) {
		t.Fatalf("Saturday %v, Wednesday %v: want Saturday = ½ Wednesday", sat, wed)
	}
	if !near(wed, 100) || !near(sat, 50) {
		t.Fatalf("Wednesday %v Saturday %v, want 100 / 50", wed, sat)
	}
	// Sep 22–30 = Tue Wed Thu Fri Sat Sun Mon Tue Wed: 7 weekdays + 1 weekend
	observed := 15*100 + 6*50.0
	if !near(f.MonthEnd, observed+7*100+2*50) {
		t.Fatalf("month end = %v, want %v", f.MonthEnd, observed+800)
	}
	flat := observed + f.RunRateDaily*9
	if near(f.MonthEnd, flat) {
		t.Fatalf("month end %v equals the flat run-rate projection %v — the weekly shape was not applied", f.MonthEnd, flat)
	}
	// Factors are relative to the overall mean (1800/21 ≈ 85.7): weekdays
	// 100/85.7 ≈ 1.17, the weekend 50/85.7 ≈ 0.58 — half of a weekday.
	m := mean(series)
	for _, d := range []string{"Mon", "Tue", "Wed", "Thu", "Fri"} {
		if !near(f.WeekdayFactors[d], 100/m) {
			t.Fatalf("factor %s = %v, want %v", d, f.WeekdayFactors[d], 100/m)
		}
	}
	for _, d := range []string{"Sat", "Sun"} {
		if !near(f.WeekdayFactors[d], 50/m) {
			t.Fatalf("factor %s = %v, want %v", d, f.WeekdayFactors[d], 50/m)
		}
	}
	if len(f.WeekdayFactors) != 7 {
		t.Fatalf("factors = %v, want all seven weekdays", f.WeekdayFactors)
	}
	// A periodic series has no trend. The raw least-squares slope of this
	// one is ≈ −0.97/day (each week ends low); fitting on the deseasonalised
	// days must see 0.
	if !near(f.TrendDaily, 0) {
		t.Fatalf("trend = %v, want 0 for a purely weekly series", f.TrendDaily)
	}
	if f.Confidence != "medium" {
		// CV of the last week (5×100, 2×50) ≈ 0.26: not high, not noisy.
		t.Fatalf("confidence = %s, want medium", f.Confidence)
	}
	checkProjection(t, f, now, observed)
}

func TestForecastMonth_FlatSeriesIsIdenticalToRunRate(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	flat := make([]float64, 20)
	for i := range flat {
		flat[i] = 40
	}
	f, _ := ForecastMonth(now, days(flat...))
	if f.Method != "weekday-seasonal" {
		t.Fatalf("method = %s", f.Method)
	}
	if !near(f.MonthEnd, 800+40*10) || !near(f.TrendDaily, 0) {
		t.Fatalf("month end = %v trend = %v, want 1200 / 0", f.MonthEnd, f.TrendDaily)
	}
	for d, v := range f.WeekdayFactors {
		if !near(v, 1) {
			t.Fatalf("factor %s = %v, want 1", d, v)
		}
	}
	for _, p := range f.Projection {
		if !near(p.Cost, 40) {
			t.Fatalf("%s projected %v, want 40", p.Day, p.Cost)
		}
	}
	if f.Confidence != "high" {
		t.Fatalf("confidence = %s", f.Confidence)
	}
	checkProjection(t, f, now, 800)
}

func TestForecastMonth_WeekdaySeenOnceKeepsNoShape(t *testing.T) {
	// 14 complete days Sep 2–16 with Sep 8 missing: every weekday twice
	// except Tuesday (only Sep 15) and Wednesday (2, 9, 16). The one
	// Tuesday is cheap; with a single sample its factor must stay 1, so the
	// remaining Tuesdays project at the overall level, not at a tenth of it.
	var series []DayCost
	for d := 2; d <= 16; d++ {
		if d == 8 {
			continue
		}
		day := time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC)
		c := 100.0
		if day.Weekday() == time.Tuesday {
			c = 10
		}
		series = append(series, DayCost{Day: day.Format("2006-01-02"), Cost: c})
	}
	if len(series) != 14 {
		t.Fatalf("test setup: %d days", len(series))
	}
	now := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, series)
	if f.Method != "weekday-seasonal" {
		t.Fatalf("method = %s", f.Method)
	}
	if !near(f.WeekdayFactors["Tue"], 1) {
		t.Fatalf("Tue factor = %v from one sample, want 1", f.WeekdayFactors["Tue"])
	}
	if !near(f.WeekdayFactors["Wed"], 100/mean(series)) {
		t.Fatalf("Wed factor = %v, want %v", f.WeekdayFactors["Wed"], 100/mean(series))
	}
	p := byDay(f)
	if tue, wed := p["2026-09-22"], p["2026-09-23"]; tue < 0.9*wed {
		t.Fatalf("Tuesday %v projected far below Wednesday %v from a single sample", tue, wed)
	}
	checkProjection(t, f, now, sum(series))
}

func TestForecastMonth_SeasonalWindowIsTheLastTwentyEightDays(t *testing.T) {
	// 30 complete days of a 31-day month: two ancient expensive days then
	// 28 flat. The shape and level come from the last 28 only, so every
	// remaining day projects at 10 — the two outliers count in observed
	// but not in the projection.
	series := make([]DayCost, 30)
	for i := range series {
		c := 10.0
		if i < 2 {
			c = 1000
		}
		series[i] = DayCost{Day: time.Date(2026, 10, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Cost: c}
	}
	now := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, series)
	if len(f.Projection) != 1 || !near(f.Projection[0].Cost, 10) || !near(f.TrendDaily, 0) {
		t.Fatalf("projection = %+v trend = %v, want one day at 10 and no trend", f.Projection, f.TrendDaily)
	}
	checkProjection(t, f, now, 2000+280)
}

func TestForecastMonth_LastDayOfMonthHasNoRemainingDays(t *testing.T) {
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, days(1, 1, 1, 1, 1, 1, 1))
	// elapsed = 29, remaining = 1 (today)
	if !near(f.MonthEnd, 7+1) {
		t.Fatalf("month end = %v, want 8", f.MonthEnd)
	}
	if len(f.Projection) != 1 || f.Projection[0].Day != "2026-09-30" {
		t.Fatalf("projection = %+v, want today only", f.Projection)
	}
}
