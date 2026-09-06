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

// DayCost is one complete day's cost.
type DayCost struct {
	Day  string  `json:"day"`
	Cost float64 `json:"cost"`
}

// Forecast is the projected month.
type Forecast struct {
	// MonthEnd = cost of the complete days observed + RunRateDaily × the days
	// left (today counted as a full run-rate day, so a partial today is never
	// added twice).
	MonthEnd     float64 `json:"month_end"`
	RunRateDaily float64 `json:"run_rate_daily"`
	// TrendDaily is the least-squares slope over the complete days: cost
	// change per day. Positive = accelerating spend.
	TrendDaily   float64 `json:"trend_daily"`
	Method       string  `json:"method"`
	DaysObserved int     `json:"days_observed"`
	DaysInMonth  int     `json:"days_in_month"`
	Confidence   string  `json:"confidence"` // low | medium | high
}

// runRateWindow is how many trailing complete days set the run rate — a week
// smooths the weekday/weekend shape without lagging a real change by a month.
const runRateWindow = 7

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
	f := Forecast{
		MonthEnd:     observed + rate*float64(remaining),
		RunRateDaily: rate,
		TrendDaily:   slope(completeDays),
		Method:       fmt.Sprintf("run-rate-%dd", w),
		DaysObserved: n,
		DaysInMonth:  daysInMonth,
	}
	switch {
	case n >= 14 && cv(last) < 0.15:
		f.Confidence = "high"
	case n >= runRateWindow:
		f.Confidence = "medium"
	default:
		f.Confidence = "low"
	}
	return f, true
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
