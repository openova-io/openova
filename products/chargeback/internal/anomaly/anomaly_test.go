package anomaly

import (
	"math"
	"testing"
	"time"
)

// series builds consecutive days from 2026-08-01 with the given values.
func series(values ...float64) []DayValue {
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	out := make([]DayValue, len(values))
	for i, v := range values {
		out[i] = DayValue{Day: start.AddDate(0, 0, i).Format("2006-01-02"), Value: v}
	}
	return out
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestFlatSeriesHasNoAnomaly(t *testing.T) {
	if got := Detect(series(repeat(10, 30)...), Options{}); len(got) != 0 {
		t.Fatalf("flat series flagged %+v", got)
	}
	if got := Detect(series(repeat(0, 30)...), Options{}); len(got) != 0 {
		t.Fatalf("all-zero series flagged %+v", got)
	}
}

func TestSingleSpikeIsExactlyOneFlagWithTheRightNumbers(t *testing.T) {
	vals := append(repeat(10, 20), 25)
	vals = append(vals, repeat(10, 5)...)
	s := series(vals...)
	// Order of the input must not matter.
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	got := Detect(s, Options{})
	if len(got) != 1 {
		t.Fatalf("want exactly one flag, got %+v", got)
	}
	f := got[0]
	if f.Day != "2026-08-21" {
		t.Fatalf("day = %s", f.Day)
	}
	if !near(f.Expected, 10) || !near(f.Actual, 25) || !near(f.Impact, 15) {
		t.Fatalf("expected/actual/impact = %v/%v/%v", f.Expected, f.Actual, f.Impact)
	}
	// A flat baseline has no variance: the score is the cap, never ±Inf
	// (which would not survive JSON encoding).
	if f.Score != ScoreCap || math.IsInf(f.Score, 0) {
		t.Fatalf("score = %v", f.Score)
	}
	// The days after the spike are judged against a baseline that now
	// contains the spike; being lower than the mean they never flag.
}

func TestSteadyRampIsNotAnAnomaly(t *testing.T) {
	vals := make([]float64, 30)
	for i := range vals {
		vals[i] = 10 + float64(i)
	}
	if got := Detect(series(vals...), Options{}); len(got) != 0 {
		t.Fatalf("linear ramp flagged %+v", got)
	}
	// Compounding growth of 8 %/day either.
	v := 10.0
	for i := range vals {
		vals[i] = v
		v *= 1.08
	}
	if got := Detect(series(vals...), Options{}); len(got) != 0 {
		t.Fatalf("geometric ramp flagged %+v", got)
	}
}

// Pins z ≥ 3: a day 2.5 σ above a noisy baseline is 1.5 × the mean and
// worth more than a currency unit, so only the z gate rejects it. A
// detector that flagged at z ≥ 2 fails here.
func TestTwoAndAHalfSigmaIsNotFlagged(t *testing.T) {
	var vals []float64
	for i := 0; i < 14; i++ {
		if i%2 == 0 {
			vals = append(vals, 8)
		} else {
			vals = append(vals, 12)
		}
	}
	// mean 10, sample σ = sqrt(56/13) ≈ 2.0755 → 15.19 is z ≈ 2.50.
	vals = append(vals, 15.19)
	if got := Detect(series(vals...), Options{}); len(got) != 0 {
		t.Fatalf("z≈2.5 flagged %+v", got)
	}
	// And the same day with a 3.5 σ value is flagged, with that score.
	vals[len(vals)-1] = 10 + 3.5*math.Sqrt(56.0/13.0)
	got := Detect(series(vals...), Options{})
	if len(got) != 1 || !near(got[0].Score, 3.5) || !near(got[0].Expected, 10) {
		t.Fatalf("z=3.5 → %+v", got)
	}
}

// Pins the 14-day baseline: a week at 20 then a week at 10 and a day at 14.
// Against the last 14 days (mean 15, σ ≈ 5.2) the 14 is below the mean;
// against only the last 7 (flat 10) it would be an infinite-z, 1.4 × spike.
func TestBaselineIsFourteenDaysNotSeven(t *testing.T) {
	vals := append(repeat(20, 7), repeat(10, 7)...)
	vals = append(vals, 14)
	if got := Detect(series(vals...), Options{}); len(got) != 0 {
		t.Fatalf("a 7-day baseline would flag this; 14 days must not: %+v", got)
	}
}

func TestNeedsFiveBaselinePoints(t *testing.T) {
	// Four prior points: not judged at all.
	if got := Detect(series(10, 10, 10, 10, 30), Options{}); len(got) != 0 {
		t.Fatalf("judged with 4 baseline points: %+v", got)
	}
	// Five: judged and flagged.
	if got := Detect(series(10, 10, 10, 10, 10, 30), Options{}); len(got) != 1 || got[0].Day != "2026-08-06" {
		t.Fatalf("5 baseline points → %+v", got)
	}
	// Only days WITH data count: a gap of absent days is not a baseline.
	s := series(10, 10, 10, 10)
	s = append(s, DayValue{Day: "2026-08-20", Value: 30})
	if got := Detect(s, Options{}); len(got) != 0 {
		t.Fatalf("absent days counted as baseline: %+v", got)
	}
}

func TestMinImpactAndRatioGates(t *testing.T) {
	// 0.5 → 1.2 is 2.4 × and infinitely surprising, but 0.7 currency units.
	s := series(append(repeat(0.5, 10), 1.2)...)
	if got := Detect(s, Options{}); len(got) != 0 {
		t.Fatalf("sub-unit impact flagged: %+v", got)
	}
	if got := Detect(s, Options{MinImpact: 0.5}); len(got) != 1 || !near(got[0].Impact, 0.7) {
		t.Fatalf("MinImpact 0.5 → %+v", got)
	}
	// 10 ± 0.01 noise then 12: z is enormous, impact is 2, but 1.2 × < 1.3 ×.
	var vals []float64
	for i := 0; i < 14; i++ {
		vals = append(vals, 10+0.01*float64(i%3-1))
	}
	vals = append(vals, 12)
	if got := Detect(series(vals...), Options{}); len(got) != 0 {
		t.Fatalf("1.2× flagged: %+v", got)
	}
	// Flat 10 then 12: σ = 0 and below the ratio → z is 0, not +Inf.
	if got := Detect(series(append(repeat(10, 10), 12)...), Options{}); len(got) != 0 {
		t.Fatalf("flat 10 → 12 flagged: %+v", got)
	}
	// From nothing to something is an anomaly (mean 0, ratio trivially met).
	if got := Detect(series(append(repeat(0, 10), 5)...), Options{}); len(got) != 1 || !near(got[0].Impact, 5) {
		t.Fatalf("0 → 5 → %+v", got)
	}
}

func TestMalformedDaysAreIgnored(t *testing.T) {
	s := series(append(repeat(10, 10), 30)...)
	s = append(s, DayValue{Day: "yesterday", Value: 1e9})
	got := Detect(s, Options{})
	if len(got) != 1 || got[0].Day != "2026-08-11" {
		t.Fatalf("got %+v", got)
	}
}

func TestMeanStddevIsSample(t *testing.T) {
	m, sd := meanStddev([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	// Population σ would be 2; sample σ is sqrt(32/7).
	if !near(m, 5) || !near(sd, math.Sqrt(32.0/7.0)) {
		t.Fatalf("mean/sd = %v/%v", m, sd)
	}
	if _, sd := meanStddev([]float64{3}); sd != 0 {
		t.Fatalf("single point sd = %v", sd)
	}
}
