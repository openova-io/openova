// Package report builds, renders, schedules and mails the plain-text cost
// report (#6867 follow-up), and renders the statement-issued notification.
//
// The calendar arithmetic here is pure and unit-tested; the store owns no
// dates beyond persisting next_at, and the API only validates.
package report

import "time"

// Cadences.
const (
	CadenceDaily   = "daily"
	CadenceWeekly  = "weekly"
	CadenceMonthly = "monthly"
)

// Cadences lists the valid cadences in display order.
func Cadences() []string { return []string{CadenceDaily, CadenceWeekly, CadenceMonthly} }

// Sections a report may carry, in the order they are rendered.
const (
	SectionSummary  = "summary"
	SectionServices = "services"
	// SectionCostCentres is the window's cost by the customer's own cost
	// centre (DESIGN.md §19). It renders on a CUSTOMER-scoped schedule only:
	// a code is unique within its customer, so grouping several customers'
	// spend by code would add together two centres that merely share a name.
	SectionCostCentres     = "cost-centres"
	SectionCustomers       = "customers"
	SectionBudgets         = "budgets"
	SectionAnomalies       = "anomalies"
	SectionRecommendations = "recommendations"
)

// Sections lists every known section in render order.
func Sections() []string {
	return []string{SectionSummary, SectionServices, SectionCostCentres, SectionCustomers, SectionBudgets, SectionAnomalies, SectionRecommendations}
}

// ValidSection reports whether name is a known section.
func ValidSection(name string) bool {
	for _, s := range Sections() {
		if s == name {
			return true
		}
	}
	return false
}

// NormalizeSections returns the known sections of in, in render order,
// without duplicates. Unknown names are dropped (the API rejects them
// before they get here; a row edited by hand still renders deterministically).
func NormalizeSections(in []string) []string {
	want := map[string]bool{}
	for _, s := range in {
		want[s] = true
	}
	out := make([]string, 0, len(in))
	for _, s := range Sections() {
		if want[s] {
			out = append(out, s)
		}
	}
	return out
}

// Defaults when a schedule leaves the day unset.
const (
	DefaultDayOfWeek  = 1 // Monday
	DefaultDayOfMonth = 1
	DefaultHourUTC    = 6
)

func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// NextRun is the first due instant strictly after `after` for a cadence:
//
//	daily    every day at hourUTC
//	weekly   the next dow (0 = Sunday … 6 = Saturday) at hourUTC
//	monthly  the next dom (1..28) at hourUTC, rolling over the year
//
// A dow or dom outside its range falls back to the default (Monday, the
// 1st); hourUTC is clamped to 0..23. The result is always > after, so a
// schedule created at exactly its due time is next due one period later,
// never immediately.
func NextRun(cadence string, dow, dom, hourUTC int, after time.Time) time.Time {
	after = after.UTC()
	if hourUTC < 0 || hourUTC > 23 {
		hourUTC = DefaultHourUTC
	}
	day := dateOnly(after)
	at := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), hourUTC, 0, 0, 0, time.UTC)
	}
	switch cadence {
	case CadenceWeekly:
		if dow < 0 || dow > 6 {
			dow = DefaultDayOfWeek
		}
		for i := 0; i <= 7; i++ {
			c := at(day.AddDate(0, 0, i))
			if int(c.Weekday()) == dow && c.After(after) {
				return c
			}
		}
		// Unreachable: eight consecutive days hold every weekday, and the
		// eighth is a full week past `after`.
		return at(day.AddDate(0, 0, 7))
	case CadenceMonthly:
		if dom < 1 || dom > 28 {
			dom = DefaultDayOfMonth
		}
		ms := monthStart(after)
		for i := 0; i <= 12; i++ {
			m := ms.AddDate(0, i, 0)
			c := time.Date(m.Year(), m.Month(), dom, hourUTC, 0, 0, 0, time.UTC)
			if c.After(after) {
				return c
			}
		}
		m := ms.AddDate(0, 1, 0)
		return time.Date(m.Year(), m.Month(), dom, hourUTC, 0, 0, 0, time.UTC)
	default: // daily
		c := at(day)
		if !c.After(after) {
			c = at(day.AddDate(0, 0, 1))
		}
		return c
	}
}

// Window is the half-open [from, to) day window a report sent at `now`
// covers: yesterday for daily, the last 7 complete days for weekly, the
// previous calendar month for monthly. Today is never included — its cost is
// still being collected.
func Window(cadence string, now time.Time) (from, to time.Time) {
	today := dateOnly(now)
	switch cadence {
	case CadenceWeekly:
		return today.AddDate(0, 0, -7), today
	case CadenceMonthly:
		ms := monthStart(now)
		return ms.AddDate(0, -1, 0), ms
	default:
		return today.AddDate(0, 0, -1), today
	}
}

// WindowLabel names an inclusive day range the way a person writes it:
// "1–7 Sep 2026", "28 Aug – 3 Sep 2026", "29 Dec 2025 – 4 Jan 2026", or a
// single day "7 Sep 2026". `to` is the half-open end.
func WindowLabel(from, to time.Time) string {
	from, to = dateOnly(from), dateOnly(to)
	last := to.AddDate(0, 0, -1)
	if !last.After(from) {
		return from.Format("2 Jan 2006")
	}
	switch {
	case from.Year() != last.Year():
		return from.Format("2 Jan 2006") + " – " + last.Format("2 Jan 2006")
	case from.Month() != last.Month():
		return from.Format("2 Jan") + " – " + last.Format("2 Jan 2006")
	default:
		return from.Format("2") + "–" + last.Format("2 Jan 2006")
	}
}
