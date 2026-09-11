package rating

import (
	"math"
	"testing"
	"time"
)

// RunRate is ForecastMonth's arithmetic exposed for capacity: on the same
// series it must report exactly the run rate the forecast reports, and its
// trend must be the slope the run-rate+trend method projects with.
func TestRunRateMatchesForecastMonth(t *testing.T) {
	var days []DayCost
	for d := 1; d <= 10; d++ {
		// A series that grows by 2 a day: 100, 102, …, 118.
		days = append(days, DayCost{Day: time.Date(2026, 9, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), Cost: 100 + 2*float64(d-1)})
	}
	f, ok := ForecastMonth(time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC), days)
	if !ok {
		t.Fatal("forecast")
	}
	rate, trend, ok := RunRate(days)
	if !ok {
		t.Fatal("RunRate must succeed on 10 days")
	}
	if rate != f.RunRateDaily {
		t.Fatalf("rate = %v, forecast run rate = %v", rate, f.RunRateDaily)
	}
	// Mean of the last 7 days (106 … 118) is 112; the slope of a series
	// rising by 2 a day is 2.
	if math.Abs(rate-112) > 1e-9 || math.Abs(trend-2) > 1e-9 {
		t.Fatalf("rate = %v, trend = %v; want 112, 2", rate, trend)
	}
}

// Fewer than three days cannot carry a slope; a flat series has none.
func TestRunRateEdges(t *testing.T) {
	if _, _, ok := RunRate(nil); ok {
		t.Fatal("empty series must not report a run rate")
	}
	if _, _, ok := RunRate([]DayCost{{Day: "2026-09-01", Cost: 5}, {Day: "2026-09-02", Cost: 7}}); ok {
		t.Fatal("two days cannot carry a slope")
	}
	rate, trend, ok := RunRate([]DayCost{{Day: "2026-09-01", Cost: 40}, {Day: "2026-09-02", Cost: 40}, {Day: "2026-09-03", Cost: 40}})
	if !ok || rate != 40 || trend != 0 {
		t.Fatalf("flat series: rate = %v, trend = %v, ok = %v", rate, trend, ok)
	}
	// A falling series has a negative trend: capacity reads that as "not
	// growing" and reports no exhaustion date.
	_, trend, _ = RunRate([]DayCost{{Day: "2026-09-01", Cost: 30}, {Day: "2026-09-02", Cost: 20}, {Day: "2026-09-03", Cost: 10}})
	if trend >= 0 {
		t.Fatalf("falling series trend = %v, want negative", trend)
	}
}
