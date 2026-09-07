package rating

import (
	"fmt"
	"math"
	"time"
)

// Month-end forecast (#6867, DESIGN.md §3.1).
//
// A forecast is an estimate, so unlike every rated amount in this package it
// is computed in float64 and labelled with the method and a confidence. It
// never feeds a statement.
//
// The method is picked by how much of the month is observed:
//
//	complete days   method              each remaining day d (k = 1 for today, 2 tomorrow, …)
//	< 7             run-rate-Nd         mean of the N days
//	7 … 13          run-rate-7d+trend   max(0, rr7 + slope × (3 + k))
//	≥ 14            weekday-seasonal    max(0, (mean28 + slope × ((W−1)/2 + k)) × factor(weekday(d)))
//
// where rr7 is the mean of the last 7 complete days, mean28 the mean of the
// last W = min(28, n) complete days, slope the least-squares cost change per
// day, and factor(w) the mean cost on weekday w divided by the overall mean
// (1 for a weekday seen fewer than twice). The slope is applied from the
// CENTRE of the averaging window — (3 + k) and ((W−1)/2 + k) — because a
// window's mean is the fitted line's value at its midpoint, so anchoring the
// trend at the last day would start the projection (W−1)/2 days stale: a
// rising series would be projected below yesterday's observed cost.
//
// The weekly shape is what a flat run rate cannot show and cloud consoles
// do: an org whose batch jobs run Monday–Friday spends half as much at the
// weekend, and a forecast made on a Thursday should not carry Thursday's
// rate into Saturday.

// DayCost is one complete day's cost.
type DayCost struct {
	Day  string  `json:"day"`
	Cost float64 `json:"cost"`
}

// Forecast is the projected month.
type Forecast struct {
	// MonthEnd = cost of the complete days observed + the sum of Projection
	// (today counted as a full projected day, so a partial today is never
	// added twice).
	MonthEnd float64 `json:"month_end"`
	// RunRateDaily is the mean of the last 7 (or all) complete days — the
	// flat rate the run-rate methods project at, reported for every method.
	RunRateDaily float64 `json:"run_rate_daily"`
	// TrendDaily is the least-squares slope the projection applies: cost
	// change per day. Positive = accelerating spend. For weekday-seasonal it
	// is fitted on the deseasonalised days (cost ÷ weekday factor) so the
	// weekly shape itself cannot read as a trend; for the run-rate methods
	// it is the raw slope over every complete day.
	TrendDaily   float64 `json:"trend_daily"`
	Method       string  `json:"method"`
	DaysObserved int     `json:"days_observed"`
	DaysInMonth  int     `json:"days_in_month"`
	Confidence   string  `json:"confidence"` // low | medium | high
	// Projection is one entry per remaining calendar day, today first — the
	// exact values summed into MonthEnd, so a chart drawing them reconciles
	// with the KPI.
	Projection []DayCost `json:"projection"`
	// WeekdayFactors (Mon … Sun) is the weekly shape the weekday-seasonal
	// method applied: cost on that weekday relative to the overall mean.
	// Absent for the other methods.
	WeekdayFactors map[string]float64 `json:"weekday_factors,omitempty"`
}

const (
	// runRateWindow is how many trailing complete days set the run rate — a
	// week smooths the weekday/weekend shape without lagging a real change
	// by a month.
	runRateWindow = 7
	// seasonalMinDays is the least history the weekday-seasonal method
	// needs: two of every weekday.
	seasonalMinDays = 14
	// seasonalWindow caps the days the weekly shape and trend are fitted on,
	// so a change in spend a month ago does not dilute this month's shape.
	seasonalWindow = 28
	// minWeekdaySamples is how often a weekday must be seen before its
	// factor is trusted; below it the day projects at the overall mean.
	minWeekdaySamples = 2
	// noisyCV is the coefficient of variation of the last week above which
	// no projection is better than low confidence, however long the history.
	noisyCV = 0.5
)

var weekdayNames = [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// ForecastMonth projects the calendar month containing now from its complete
// days so far (days strictly before now's date). ok is false when there is
// nothing to project from.
func ForecastMonth(now time.Time, completeDays []DayCost) (Forecast, bool) {
	now = now.UTC()
	n := len(completeDays)
	if n == 0 {
		return Forecast{}, false
	}
	daysInMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	observed := 0.0
	for _, d := range completeDays {
		observed += d.Cost
	}
	w := runRateWindow
	if n < w {
		w = n
	}
	last := completeDays[n-w:]
	rate := mean(last)
	// Days still to come: the whole month minus the complete days already
	// elapsed by the calendar (not by the data — a gap in the ledger is not
	// a day the cloud stopped charging for).
	elapsed := now.Day() - 1
	remaining := daysInMonth - elapsed
	if remaining < 0 {
		remaining = 0
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	f := Forecast{
		RunRateDaily: rate,
		DaysObserved: n,
		DaysInMonth:  daysInMonth,
		Projection:   make([]DayCost, 0, remaining),
	}
	// level(k) is the projected cost of the k-th remaining day before the
	// weekly shape is applied; factor(d) is that shape (1 when none).
	var level func(k int) float64
	factor := func(time.Time) float64 { return 1 }
	switch {
	case n >= seasonalMinDays:
		f.Method = "weekday-seasonal"
		ws := seasonalWindow
		if n < ws {
			ws = n
		}
		window := completeDays[n-ws:]
		m := mean(window)
		f.WeekdayFactors = weekdayFactors(window, m)
		f.TrendDaily = slope(deseasonalise(window, f.WeekdayFactors, m))
		centre := float64(ws-1) / 2
		level = func(k int) float64 { return m + f.TrendDaily*(centre+float64(k)) }
		factor = func(d time.Time) float64 { return f.WeekdayFactors[weekdayNames[d.Weekday()]] }
	case n >= runRateWindow:
		f.Method = fmt.Sprintf("run-rate-%dd+trend", w)
		f.TrendDaily = slope(completeDays)
		centre := float64(w-1) / 2
		level = func(k int) float64 { return rate + f.TrendDaily*(centre+float64(k)) }
	default:
		f.Method = fmt.Sprintf("run-rate-%dd", w)
		f.TrendDaily = slope(completeDays)
		level = func(int) float64 { return rate }
	}

	projected := 0.0
	for k := 1; k <= remaining; k++ {
		d := today.AddDate(0, 0, k-1)
		c := math.Max(0, level(k)*factor(d))
		projected += c
		f.Projection = append(f.Projection, DayCost{Day: d.Format("2006-01-02"), Cost: c})
	}
	f.MonthEnd = observed + projected

	lastCV := cv(last)
	switch {
	case lastCV >= noisyCV:
		f.Confidence = "low"
	case n >= seasonalMinDays && lastCV < 0.15:
		f.Confidence = "high"
	case n >= runRateWindow:
		f.Confidence = "medium"
	default:
		f.Confidence = "low"
	}
	return f, true
}

// weekdayFactors is the weekly shape of d: for each weekday, its mean cost
// over the overall mean m. A weekday seen fewer than minWeekdaySamples times
// — or every weekday when m is not positive — gets 1, i.e. no shape.
func weekdayFactors(d []DayCost, m float64) map[string]float64 {
	out := make(map[string]float64, len(weekdayNames))
	for _, name := range weekdayNames {
		out[name] = 1
	}
	if m <= 0 {
		return out
	}
	var sum [7]float64
	var cnt [7]int
	for _, x := range d {
		wd, ok := weekdayOf(x.Day)
		if !ok {
			continue
		}
		sum[wd] += x.Cost
		cnt[wd]++
	}
	for wd, name := range weekdayNames {
		if cnt[wd] >= minWeekdaySamples {
			out[name] = sum[wd] / float64(cnt[wd]) / m
		}
	}
	return out
}

// deseasonalise divides each day by its weekday factor so the least-squares
// slope sees the level, not the weekly shape (three low weekends in 21 days
// otherwise read as a ~1 %/day decline). A day whose factor is not positive,
// or whose date does not parse, is replaced by the mean m — neutral.
func deseasonalise(d []DayCost, factors map[string]float64, m float64) []DayCost {
	out := make([]DayCost, len(d))
	for i, x := range d {
		out[i] = DayCost{Day: x.Day, Cost: m}
		wd, ok := weekdayOf(x.Day)
		if !ok {
			continue
		}
		if f := factors[weekdayNames[wd]]; f > 0 {
			out[i].Cost = x.Cost / f
		}
	}
	return out
}

func weekdayOf(day string) (time.Weekday, bool) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return 0, false
	}
	return t.Weekday(), true
}

func mean(d []DayCost) float64 {
	if len(d) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range d {
		s += x.Cost
	}
	return s / float64(len(d))
}

// cv is the coefficient of variation (σ / mean); 0 when the mean is 0.
func cv(d []DayCost) float64 {
	m := mean(d)
	if m == 0 || len(d) < 2 {
		return 0
	}
	ss := 0.0
	for _, x := range d {
		ss += (x.Cost - m) * (x.Cost - m)
	}
	return math.Sqrt(ss/float64(len(d)-1)) / m
}

// slope is the least-squares slope of cost against day index.
func slope(d []DayCost) float64 {
	n := float64(len(d))
	if n < 3 {
		return 0
	}
	var sx, sy, sxx, sxy float64
	for i, x := range d {
		xi := float64(i)
		sx += xi
		sy += x.Cost
		sxx += xi * xi
		sxy += xi * x.Cost
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / den
}
