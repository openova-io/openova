// Package anomaly flags days whose cost breaks out of their own recent past
// (#6867, DESIGN.md §3.6).
//
// Every day is compared with the trailing window of days before it: the
// baseline mean and sample standard deviation give a z-score, and a day is
// flagged when it is far out statistically (z ≥ 3), far out proportionally
// (≥ 1.3 × the mean — a z-score alone flags a 0.1 % wobble on a perfectly
// flat series) and far out in money (≥ MinImpact — a 1.3 × jump on a
// series worth cents is not worth a row).
//
// The package is pure: it sees a series of (day, value) points and returns
// flags. It is float64 throughout because a z-score is a statistic, not a
// bill; the ledger amounts the API attaches next to a flag stay exact.
package anomaly

import (
	"math"
	"sort"
	"time"
)

// DayValue is one day's total. A series holds only the days that have data:
// an absent day is unknown and never enters a baseline, whereas a present
// zero is a fact (the ledger says nothing was spent).
type DayValue struct {
	Day   string // YYYY-MM-DD
	Value float64
}

// Options tune the detector. Zero values take the defaults below, which are
// the DESIGN.md §3.6 contract.
type Options struct {
	// Lookback is the number of calendar days before a day that form its
	// baseline. Default 14.
	Lookback int
	// MinBaseline is the smallest number of baseline points with data a day
	// needs before it can be judged at all. Default 5.
	MinBaseline int
	// ZThreshold is the minimum z-score. Default 3.
	ZThreshold float64
	// Ratio is the minimum actual / mean. Default 1.3.
	Ratio float64
	// MinImpact is the minimum actual − mean, in currency units. Default 1.
	MinImpact float64
}

// Defaults are the contract values.
const (
	DefaultLookback    = 14
	DefaultMinBaseline = 5
	DefaultZThreshold  = 3.0
	DefaultRatio       = 1.3
	DefaultMinImpact   = 1.0
)

// ScoreCap bounds the reported score. A baseline with no variance makes the
// z-score infinite, and an infinite float64 cannot be encoded as JSON, so
// the score is capped here: 99 reads as "off the chart" and still sorts
// above every finite score.
const ScoreCap = 99.0

// Flag is one flagged day.
type Flag struct {
	Day      string
	Expected float64 // baseline mean
	Actual   float64
	Impact   float64 // Actual − Expected
	Score    float64 // z-score, capped at ScoreCap
}

func (o Options) withDefaults() Options {
	if o.Lookback <= 0 {
		o.Lookback = DefaultLookback
	}
	if o.MinBaseline <= 0 {
		o.MinBaseline = DefaultMinBaseline
	}
	if o.ZThreshold <= 0 {
		o.ZThreshold = DefaultZThreshold
	}
	if o.Ratio <= 0 {
		o.Ratio = DefaultRatio
	}
	if o.MinImpact <= 0 {
		o.MinImpact = DefaultMinImpact
	}
	return o
}

type point struct {
	day   string
	t     time.Time
	value float64
}

// Detect judges every day of the series against the days before it and
// returns the flagged days in ascending day order. Days that do not parse
// as YYYY-MM-DD are ignored.
func Detect(series []DayValue, opts Options) []Flag {
	opts = opts.withDefaults()
	pts := make([]point, 0, len(series))
	for _, d := range series {
		t, err := time.Parse("2006-01-02", d.Day)
		if err != nil {
			continue
		}
		pts = append(pts, point{day: d.Day, t: t, value: d.Value})
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].t.Before(pts[j].t) })

	var flags []Flag
	for i, p := range pts {
		// Baseline: the points with a day in [day − Lookback, day).
		start := p.t.AddDate(0, 0, -opts.Lookback)
		var base []float64
		for j := i - 1; j >= 0; j-- {
			if pts[j].t.Before(start) {
				break
			}
			if pts[j].t.Equal(p.t) {
				// A duplicate day is the caller's bug; do not let a day be
				// its own baseline.
				continue
			}
			base = append(base, pts[j].value)
		}
		if len(base) < opts.MinBaseline {
			continue
		}
		mean, sd := meanStddev(base)
		z := zScore(p.value, mean, sd, opts.Ratio)
		impact := p.value - mean
		if z < opts.ZThreshold || p.value < opts.Ratio*mean || impact < opts.MinImpact {
			continue
		}
		flags = append(flags, Flag{Day: p.day, Expected: mean, Actual: p.value, Impact: impact, Score: math.Min(z, ScoreCap)})
	}
	return flags
}

// meanStddev returns the mean and the sample (n−1) standard deviation.
func meanStddev(xs []float64) (float64, float64) {
	n := float64(len(xs))
	if n == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	mean := sum / n
	if n < 2 {
		return mean, 0
	}
	ss := 0.0
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	return mean, math.Sqrt(ss / (n - 1))
}

// zScore is (v − mean) / sd. A baseline with no variance has no scale to
// measure against: a value clearly above it (more than ratio × mean) is
// infinitely surprising, anything else is not surprising at all.
func zScore(v, mean, sd, ratio float64) float64 {
	if sd == 0 {
		if v > ratio*mean {
			return math.Inf(1)
		}
		return 0
	}
	return (v - mean) / sd
}
