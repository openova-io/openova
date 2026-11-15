package operations

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestScheduleNext(t *testing.T) {
	for _, c := range []struct{ name, expr, after, want string }{
		{"daily, before the time", "0 2 * * *", "2026-12-01 01:00", "2026-12-01 02:00"},
		{"daily, at the time: the next day", "0 2 * * *", "2026-12-01 02:00", "2026-12-02 02:00"},
		{"step of minutes", "*/15 * * * *", "2026-12-01 10:16", "2026-12-01 10:30"},
		{"list and range", "5,35 8-9 * * *", "2026-12-01 09:36", "2026-12-02 08:05"},
		{"first of the month, over the year end", "0 0 1 * *", "2026-12-01 00:00", "2027-01-01 00:00"},
		{"weekdays skip the weekend", "30 3 * * 1-5", "2026-12-04 03:30", "2026-12-07 03:30"},
		{"7 is Sunday", "0 0 * * 7", "2026-12-01 00:00", "2026-12-06 00:00"},
		{"day of the month or day of the week", "0 0 13 * 5", "2026-12-05 00:00", "2026-12-11 00:00"},
		{"29 February waits for a leap year", "0 0 29 2 *", "2026-12-01 00:00", "2028-02-29 00:00"},
		{"a number with a step runs to the end", "50/5 * * * *", "2026-12-01 10:51", "2026-12-01 10:55"},
	} {
		s, err := ParseSchedule(c.expr)
		if err != nil {
			t.Errorf("%s: ParseSchedule(%q): %v", c.name, c.expr, err)
			continue
		}
		if got := s.Next(at(c.after)); !got.Equal(at(c.want)) {
			t.Errorf("%s: Next(%s) of %q = %s, want %s", c.name, c.after, c.expr, got.Format("2006-01-02 15:04"), c.want)
		}
	}
}

func TestScheduleRefusesWhatIsNotCron(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8", "*/0 * * * *", "a * * * *", "5-1 * * * *", "1-x * * * *", "*/x * * * *"} {
		if s, err := ParseSchedule(expr); err == nil {
			t.Errorf("ParseSchedule(%q) = %+v, want an error", expr, s)
		}
	}
	never, err := ParseSchedule("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := never.Next(at("2026-12-01 00:00")); !got.IsZero() {
		t.Errorf("31 February came at %s", got)
	}
}
