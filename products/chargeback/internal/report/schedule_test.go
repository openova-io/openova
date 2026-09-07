package report

import (
	"testing"
	"time"
)

func at(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func TestNextRunDaily(t *testing.T) {
	// Before today's hour → today at the hour.
	if got, want := NextRun(CadenceDaily, 0, 0, 6, at(2026, 9, 7, 4, 30)), at(2026, 9, 7, 6, 0); !got.Equal(want) {
		t.Fatalf("before hour: got %v want %v", got, want)
	}
	// At exactly the hour → tomorrow (never ≤ after).
	if got, want := NextRun(CadenceDaily, 0, 0, 6, at(2026, 9, 7, 6, 0)), at(2026, 9, 8, 6, 0); !got.Equal(want) {
		t.Fatalf("at hour: got %v want %v", got, want)
	}
	// Past the hour → tomorrow.
	if got, want := NextRun(CadenceDaily, 0, 0, 6, at(2026, 9, 7, 9, 0)), at(2026, 9, 8, 6, 0); !got.Equal(want) {
		t.Fatalf("after hour: got %v want %v", got, want)
	}
	// Month + year roll-over.
	if got, want := NextRun(CadenceDaily, 0, 0, 0, at(2026, 12, 31, 23, 59)), at(2027, 1, 1, 0, 0); !got.Equal(want) {
		t.Fatalf("year roll: got %v want %v", got, want)
	}
}

func TestNextRunWeekly(t *testing.T) {
	// 2026-09-07 is a Monday.
	mon := at(2026, 9, 7, 10, 0)
	if mon.Weekday() != time.Monday {
		t.Fatal("fixture: 2026-09-07 must be a Monday")
	}
	// Monday 10:00 asking for Monday 06:00 → next Monday.
	if got, want := NextRun(CadenceWeekly, 1, 0, 6, mon), at(2026, 9, 14, 6, 0); !got.Equal(want) {
		t.Fatalf("same weekday past hour: got %v want %v", got, want)
	}
	// Monday 10:00 asking for Wednesday 06:00 → this Wednesday.
	if got, want := NextRun(CadenceWeekly, 3, 0, 6, mon), at(2026, 9, 9, 6, 0); !got.Equal(want) {
		t.Fatalf("later weekday: got %v want %v", got, want)
	}
	// Monday 04:00 asking for Monday 06:00 → today.
	if got, want := NextRun(CadenceWeekly, 1, 0, 6, at(2026, 9, 7, 4, 0)), at(2026, 9, 7, 6, 0); !got.Equal(want) {
		t.Fatalf("same weekday before hour: got %v want %v", got, want)
	}
	// Sunday (0) from Monday → 6 days on.
	if got, want := NextRun(CadenceWeekly, 0, 0, 6, mon), at(2026, 9, 13, 6, 0); !got.Equal(want) {
		t.Fatalf("sunday: got %v want %v", got, want)
	}
	// Out-of-range dow falls back to Monday.
	if got, want := NextRun(CadenceWeekly, 9, 0, 6, mon), at(2026, 9, 14, 6, 0); !got.Equal(want) {
		t.Fatalf("bad dow: got %v want %v", got, want)
	}
	// Every result is strictly after `after` and on the asked weekday.
	for dow := 0; dow <= 6; dow++ {
		for h := 0; h < 24; h += 5 {
			after := at(2026, 9, 7, h, 30)
			got := NextRun(CadenceWeekly, dow, 0, 6, after)
			if !got.After(after) || int(got.Weekday()) != dow || got.Hour() != 6 {
				t.Fatalf("dow=%d after=%v got %v", dow, after, got)
			}
		}
	}
}

func TestNextRunMonthly(t *testing.T) {
	// 7 Sep asking for the 1st → 1 Oct.
	if got, want := NextRun(CadenceMonthly, 0, 1, 6, at(2026, 9, 7, 10, 0)), at(2026, 10, 1, 6, 0); !got.Equal(want) {
		t.Fatalf("past dom: got %v want %v", got, want)
	}
	// 7 Sep asking for the 15th → 15 Sep.
	if got, want := NextRun(CadenceMonthly, 0, 15, 6, at(2026, 9, 7, 10, 0)), at(2026, 9, 15, 6, 0); !got.Equal(want) {
		t.Fatalf("later dom: got %v want %v", got, want)
	}
	// Exactly at the due instant → next month.
	if got, want := NextRun(CadenceMonthly, 0, 7, 10, at(2026, 9, 7, 10, 0)), at(2026, 10, 7, 10, 0); !got.Equal(want) {
		t.Fatalf("at due: got %v want %v", got, want)
	}
	// Year roll-over: 20 Dec asking for the 1st → 1 Jan next year.
	if got, want := NextRun(CadenceMonthly, 0, 1, 6, at(2026, 12, 20, 0, 0)), at(2027, 1, 1, 6, 0); !got.Equal(want) {
		t.Fatalf("year roll: got %v want %v", got, want)
	}
	// 28 is always valid, February included.
	if got, want := NextRun(CadenceMonthly, 0, 28, 6, at(2027, 2, 1, 0, 0)), at(2027, 2, 28, 6, 0); !got.Equal(want) {
		t.Fatalf("feb 28: got %v want %v", got, want)
	}
	// Out-of-range dom falls back to the 1st.
	if got, want := NextRun(CadenceMonthly, 0, 31, 6, at(2026, 9, 7, 10, 0)), at(2026, 10, 1, 6, 0); !got.Equal(want) {
		t.Fatalf("bad dom: got %v want %v", got, want)
	}
}

func TestNextRunNeverAtOrBeforeAfter(t *testing.T) {
	after := at(2026, 9, 7, 6, 0)
	for _, c := range Cadences() {
		for dow := 0; dow <= 6; dow++ {
			for dom := 1; dom <= 28; dom += 9 {
				got := NextRun(c, dow, dom, 6, after)
				if !got.After(after) {
					t.Fatalf("%s dow=%d dom=%d: %v is not after %v", c, dow, dom, got, after)
				}
				// Idempotent chain: the next run after the next run is later still.
				if again := NextRun(c, dow, dom, 6, got); !again.After(got) {
					t.Fatalf("%s: chained run %v not after %v", c, again, got)
				}
			}
		}
	}
}

func TestWindow(t *testing.T) {
	now := at(2026, 9, 7, 10, 0)
	cases := []struct {
		cadence  string
		from, to time.Time
	}{
		{CadenceDaily, at(2026, 9, 6, 0, 0), at(2026, 9, 7, 0, 0)},
		{CadenceWeekly, at(2026, 8, 31, 0, 0), at(2026, 9, 7, 0, 0)},
		{CadenceMonthly, at(2026, 8, 1, 0, 0), at(2026, 9, 1, 0, 0)},
	}
	for _, c := range cases {
		from, to := Window(c.cadence, now)
		if !from.Equal(c.from) || !to.Equal(c.to) {
			t.Fatalf("%s: got [%v, %v) want [%v, %v)", c.cadence, from, to, c.from, c.to)
		}
	}
	// January's monthly window is December of the previous year.
	from, to := Window(CadenceMonthly, at(2027, 1, 3, 0, 0))
	if !from.Equal(at(2026, 12, 1, 0, 0)) || !to.Equal(at(2027, 1, 1, 0, 0)) {
		t.Fatalf("jan monthly: [%v, %v)", from, to)
	}
}

func TestWindowLabel(t *testing.T) {
	cases := []struct {
		from, to time.Time
		want     string
	}{
		{at(2026, 9, 1, 0, 0), at(2026, 9, 8, 0, 0), "1–7 Sep 2026"},
		{at(2026, 9, 6, 0, 0), at(2026, 9, 7, 0, 0), "6 Sep 2026"},
		{at(2026, 8, 28, 0, 0), at(2026, 9, 4, 0, 0), "28 Aug – 3 Sep 2026"},
		{at(2025, 12, 29, 0, 0), at(2026, 1, 5, 0, 0), "29 Dec 2025 – 4 Jan 2026"},
		{at(2026, 8, 1, 0, 0), at(2026, 9, 1, 0, 0), "1–31 Aug 2026"},
	}
	for _, c := range cases {
		if got := WindowLabel(c.from, c.to); got != c.want {
			t.Errorf("WindowLabel(%v, %v) = %q want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestNormalizeSections(t *testing.T) {
	got := NormalizeSections([]string{"budgets", "summary", "bogus", "summary", "anomalies"})
	want := []string{"summary", "budgets", "anomalies"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if ValidSection("bogus") || !ValidSection("recommendations") {
		t.Fatal("ValidSection")
	}
}

func TestStatementPeriodLabel(t *testing.T) {
	if got := StatementPeriodLabel("2026-08-01", "2026-08-31"); got != "August 2026" {
		t.Fatalf("whole month: %q", got)
	}
	if got := StatementPeriodLabel("2026-08-10", "2026-08-31"); got != "10–31 Aug 2026" {
		t.Fatalf("partial month: %q", got)
	}
	if got := StatementPeriodLabel("2026-02-01", "2026-02-28"); got != "February 2026" {
		t.Fatalf("february: %q", got)
	}
	if got := StatementPeriodLabel("bad", "2026-08-31"); got != "bad – 2026-08-31" {
		t.Fatalf("malformed: %q", got)
	}
}
