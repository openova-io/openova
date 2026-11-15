// Package operations holds the operations controller of the machine plane: a scheduler
// that creates one MachineRun for every time of a ScanPolicy's schedule, and a run
// reconciler that starts one runner Job per target, reads each Job's result document,
// writes the hardening report and the bill-of-materials reference of the Machine, and
// deletes a finished run when its retention time has passed.
//
// Nothing in this package waits in a loop with a fixed period. A reconcile that must
// happen later asks for it at the exact time, with ctrl.Result.RequeueAfter.
package operations

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a five-field cron expression, read in UTC: minute (0-59), hour (0-23),
// day of the month (1-31), month (1-12) and day of the week (0-7, 0 and 7 are Sunday).
// A field is *, a number, a range a-b, a step */n or a-b/n, or a comma-separated list
// of those. When both day fields are restricted, a day that matches either one matches.
type Schedule struct {
	minute, hour, dom, month, dow uint64
	domAny, dowAny                bool
}

var cronFields = []struct {
	name        string
	least, most int
}{{"minute", 0, 59}, {"hour", 0, 23}, {"day of the month", 1, 31}, {"month", 1, 12}, {"day of the week", 0, 7}}

// ParseSchedule reads a five-field cron expression.
func ParseSchedule(expr string) (Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != len(cronFields) {
		return Schedule{}, fmt.Errorf("schedule %q must have five fields", expr)
	}
	var bits [5]uint64
	for i, field := range fields {
		f := cronFields[i]
		for _, part := range strings.Split(field, ",") {
			b, err := cronPart(part, f.least, f.most)
			if err != nil {
				return Schedule{}, fmt.Errorf("schedule %q, field %s: %w", expr, f.name, err)
			}
			bits[i] |= b
		}
	}
	if bits[4]&(1<<7) != 0 {
		bits[4] |= 1
	}
	return Schedule{minute: bits[0], hour: bits[1], dom: bits[2], month: bits[3], dow: bits[4],
		domAny: fields[2] == "*", dowAny: fields[4] == "*"}, nil
}

// cronPart reads one list item of a field and returns its values as a bit set.
func cronPart(part string, least, most int) (uint64, error) {
	span, stepText, hasStep := strings.Cut(part, "/")
	step := 1
	if hasStep {
		n, err := strconv.Atoi(stepText)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%q has no valid step", part)
		}
		step = n
	}
	lo, hi := least, most
	if span != "*" {
		from, to, isRange := strings.Cut(span, "-")
		a, err := strconv.Atoi(from)
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", from)
		}
		lo, hi = a, a
		if isRange {
			if hi, err = strconv.Atoi(to); err != nil {
				return 0, fmt.Errorf("%q is not a number", to)
			}
		} else if hasStep {
			hi = most
		}
	}
	if lo < least || hi > most || lo > hi {
		return 0, fmt.Errorf("%q is outside %d-%d", part, least, most)
	}
	var out uint64
	for v := lo; v <= hi; v += step {
		out |= 1 << uint(v) // #nosec G115 -- v is between 0 and 59
	}
	return out, nil
}

func has(set uint64, v int) bool { return set&(1<<uint(v)) != 0 } // #nosec G115 -- v is between 0 and 59

// Next returns the first time of the schedule that is after t, in UTC with zero seconds.
// It returns the zero time when the schedule has no time within five years of t.
func (s Schedule) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	for limit := t.AddDate(5, 0, 0); t.Before(limit); {
		switch {
		case !has(s.month, int(t.Month())):
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		case !s.dayMatches(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !has(s.hour, t.Hour()):
			t = t.Truncate(time.Hour).Add(time.Hour)
		case !has(s.minute, t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}

func (s Schedule) dayMatches(t time.Time) bool {
	dom, dow := has(s.dom, t.Day()), has(s.dow, int(t.Weekday()))
	if s.domAny || s.dowAny {
		return (s.domAny || dom) && (s.dowAny || dow)
	}
	return dom || dow
}
