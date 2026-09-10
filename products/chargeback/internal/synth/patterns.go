package synth

import (
	"math"
	"time"
)

// Shape functions the scenario composes. All are pure and take UTC times;
// "local" below means Oman (UTC+4), so an evening peak at 20:00 local is
// 16:00 UTC.

// WeekdayFactor is the Omani working-week rhythm: Sunday–Thursday full load
// (mid-week a touch above), Friday the deep dip, Saturday partial.
func WeekdayFactor(t time.Time) float64 {
	switch t.UTC().Weekday() {
	case time.Friday:
		return 0.65
	case time.Saturday:
		return 0.85
	case time.Tuesday, time.Wednesday:
		return 1.05
	}
	return 1.0
}

// DailyTraffic is the retail traffic curve: 20 % at the 04:00 UTC trough,
// 100 % at the 16:00 UTC (20:00 local) evening peak, a smooth cosine between.
func DailyTraffic(t time.Time) float64 {
	h := float64(t.UTC().Hour()) + float64(t.UTC().Minute())/60
	c := (1 + math.Cos(2*math.Pi*(h-16)/24)) / 2 // 1 at 16:00, 0 at 04:00
	return 0.2 + 0.8*math.Pow(c, 1.3)
}

// OfficeTraffic is the daytime curve of an institution: 15 % overnight,
// 100 % across 08:00–14:00 local (04:00–10:00 UTC), tapering after.
func OfficeTraffic(t time.Time) float64 {
	h := float64(t.UTC().Hour())
	c := (1 + math.Cos(2*math.Pi*(h-7)/24)) / 2 // peak at 07:00 UTC (11:00 local)
	return 0.15 + 0.85*math.Pow(c, 2)
}

// Linear interpolates v0 → v1 across [from, to], clamped outside.
func Linear(t, from, to time.Time, v0, v1 float64) float64 {
	if !to.After(from) || !t.After(from) {
		return v0
	}
	if !t.Before(to) {
		return v1
	}
	f := float64(t.Sub(from)) / float64(to.Sub(from))
	return v0 + (v1-v0)*f
}

// Step is a value taking effect at a time.
type Step struct {
	At    time.Time
	Value float64
}

// StepAt returns the value of the latest step whose At ≤ t (steps ascending),
// or v0 before the first.
func StepAt(t time.Time, v0 float64, steps []Step) float64 {
	v := v0
	for _, s := range steps {
		if t.Before(s.At) {
			break
		}
		v = s.Value
	}
	return v
}

// InRange reports from ≤ t < to.
func InRange(t, from, to time.Time) bool {
	return !t.Before(from) && t.Before(to)
}

// HourIn reports whether t's UTC hour is in [h0, h1).
func HourIn(t time.Time, h0, h1 int) bool {
	h := t.UTC().Hour()
	return h >= h0 && h < h1
}

// DayFraction is a deterministic value in [0, 1) per (seed, key, UTC day):
// the per-day randomness behind batch sizes and volume churn.
func DayFraction(seed uint64, key string, t time.Time) float64 {
	return Fraction(seed, key, t.UTC().Format("2006-01-02"))
}

// Fraction is a deterministic value in [0, 1) for (seed, parts…) — the same
// hash DayFraction uses, over any key. It carries no time of its own, so a
// value drawn from it is a property of the thing, not of the hour: that is
// what the landlord backfill's fixed volume sizes need.
func Fraction(seed uint64, parts ...string) float64 {
	h := Hash64(seed, parts...)
	return float64(h>>11) / float64(uint64(1)<<53)
}

// PlanAt returns the plan slug in force at t given ascending switches, or ""
// before the first.
func PlanAt(t time.Time, switches []PlanSwitch) string {
	slug := ""
	for _, s := range switches {
		if t.Before(s.At) {
			break
		}
		slug = s.Slug
	}
	return slug
}

// RoundInt rounds to the nearest integer, never below min.
func RoundInt(v float64, min int) int {
	n := int(math.Round(v))
	if n < min {
		return min
	}
	return n
}
