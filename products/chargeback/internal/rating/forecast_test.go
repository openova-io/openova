package rating

import (
	"math"
	"testing"
	"time"
)

func days(costs ...float64) []DayCost {
	out := make([]DayCost, len(costs))
	for i, c := range costs {
		out[i] = DayCost{Day: time.Date(2026, 9, i+1, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Cost: c}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestForecastMonth_NoDays(t *testing.T) {
	if _, ok := ForecastMonth(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC), nil); ok {
		t.Fatal("a forecast from nothing must not exist")
	}
}

func TestForecastMonth_RunRateUsesLastSevenDaysOnly(t *testing.T) {
	// 10 complete days: 3 quiet days then 7 at 100/day. The run rate must be
	// 100, not the 10-day mean (79) — a mutant averaging every day fails here.
	now := time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC)
	f, ok := ForecastMonth(now, days(30, 30, 30, 100, 100, 100, 100, 100, 100, 100))
	if !ok {
		t.Fatal("expected a forecast")
	}
	if !near(f.RunRateDaily, 100) {
		t.Fatalf("run rate = %v, want 100", f.RunRateDaily)
	}
	// observed 790 + 100 × (30 − 10 elapsed) = 2790
	if !near(f.MonthEnd, 790+100*20) {
		t.Fatalf("month end = %v, want 2790", f.MonthEnd)
	}
	if f.Method != "run-rate-7d" || f.DaysObserved != 10 || f.DaysInMonth != 30 {
		t.Fatalf("meta = %+v", f)
	}
	if f.Confidence != "medium" {
		t.Fatalf("confidence = %s, want medium (10 days)", f.Confidence)
	}
}

func TestForecastMonth_FewDaysIsLowConfidence(t *testing.T) {
	now := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, days(10, 20, 30))
	if f.Method != "run-rate-3d" || f.Confidence != "low" {
		t.Fatalf("got %+v", f)
	}
	// mean 20 × (30 − 3) + 60
	if !near(f.MonthEnd, 60+20*27) {
		t.Fatalf("month end = %v", f.MonthEnd)
	}
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

func TestForecastMonth_LastDayOfMonthHasNoRemainingDays(t *testing.T) {
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	f, _ := ForecastMonth(now, days(1, 1, 1, 1, 1, 1, 1))
	// elapsed = 29, remaining = 1 (today)
	if !near(f.MonthEnd, 7+1) {
		t.Fatalf("month end = %v, want 8", f.MonthEnd)
	}
}
