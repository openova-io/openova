package collections

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The aging buckets are exact at every boundary day (DESIGN.md §9.6).
func TestBucketBoundariesAreExact(t *testing.T) {
	cases := []struct {
		days int
		want string
	}{
		{-10, BucketCurrent}, {-1, BucketCurrent}, {0, BucketCurrent},
		{1, Bucket1to30}, {15, Bucket1to30}, {30, Bucket1to30},
		{31, Bucket31to60}, {45, Bucket31to60}, {60, Bucket31to60},
		{61, Bucket61to90}, {90, Bucket61to90},
		{91, BucketOver90}, {365, BucketOver90},
	}
	for _, c := range cases {
		if got := Bucket(c.days); got != c.want {
			t.Errorf("Bucket(%d) = %s, want %s", c.days, got, c.want)
		}
	}
}

// Days past due are whole UTC calendar days: an invoice due at 09:00 today
// is 0 days past due at 23:59 and 1 day past due at 00:00 tomorrow.
func TestDaysPastDueCountsCalendarDays(t *testing.T) {
	due := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	if d := DaysPastDue(due, time.Date(2026, 3, 10, 23, 59, 0, 0, time.UTC)); d != 0 {
		t.Fatalf("same day = %d, want 0", d)
	}
	if d := DaysPastDue(due, time.Date(2026, 3, 11, 0, 0, 1, 0, time.UTC)); d != 1 {
		t.Fatalf("next midnight = %d, want 1", d)
	}
	if d := DaysPastDue(due, time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)); d != -3 {
		t.Fatalf("three days ahead = %d, want -3", d)
	}
	if d := DaysPastDue(due, time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)); d != 31 {
		t.Fatalf("a month on = %d, want 31", d)
	}
}

// BuildAging folds invoices into per-customer rows, exact at each boundary
// and sorted by what is overdue.
func TestBuildAgingFoldsInvoicesExactlyAtTheBoundaries(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	due := func(daysAgo int) time.Time { return now.AddDate(0, 0, -daysAgo) }
	open := []store.OpenInvoice{
		{StatementID: "a1", CustomerID: "a", CustomerName: "Alpha", Currency: "OMR", Outstanding: "10.000000", DueAt: due(0)},   // current
		{StatementID: "a2", CustomerID: "a", CustomerName: "Alpha", Currency: "OMR", Outstanding: "20.000000", DueAt: due(30)},  // 1-30
		{StatementID: "a3", CustomerID: "a", CustomerName: "Alpha", Currency: "OMR", Outstanding: "30.000000", DueAt: due(31)},  // 31-60
		{StatementID: "a4", CustomerID: "a", CustomerName: "Alpha", Currency: "OMR", Outstanding: "40.000000", DueAt: due(90)},  // 61-90
		{StatementID: "a5", CustomerID: "a", CustomerName: "Alpha", Currency: "OMR", Outstanding: "50.500000", DueAt: due(91)},  // over 90
		{StatementID: "b1", CustomerID: "b", CustomerName: "Bravo", Currency: "OMR", Outstanding: "999.000000", DueAt: due(-5)}, // not yet due
		{StatementID: "z1", CustomerID: "z", CustomerName: "Zero", Currency: "OMR", Outstanding: "0.000000", DueAt: due(200)},   // nothing left
	}
	rep := BuildAging(open, now, map[string]customerFacts{"a": {credit: "5.000000"}}, "internal")
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (a zero-outstanding invoice is not aged)", len(rep.Rows))
	}
	a := rep.Rows[0]
	if a.CustomerID != "a" {
		t.Fatalf("the most overdue customer sorts first: %+v", rep.Rows)
	}
	want := map[string]string{BucketCurrent: "10.000000", Bucket1to30: "20.000000", Bucket31to60: "30.000000", Bucket61to90: "40.000000", BucketOver90: "50.500000"}
	for b, v := range want {
		if string(a.Buckets[b]) != v {
			t.Errorf("Alpha %s = %s, want %s", b, a.Buckets[b], v)
		}
	}
	if string(a.Total) != "150.500000" || string(a.Overdue) != "140.500000" || a.OldestDays != 91 || a.Invoices != 5 || string(a.AvailableCredit) != "5.000000" {
		t.Fatalf("Alpha row = %+v", a)
	}
	b := rep.Rows[1]
	if string(b.Buckets[BucketCurrent]) != "999.000000" || string(b.Overdue) != "0.000000" || b.OldestDays != 0 {
		t.Fatalf("Bravo row = %+v", b)
	}
	if string(rep.Total) != "1149.500000" || string(rep.Overdue) != "140.500000" || string(rep.Totals[BucketOver90]) != "50.500000" || len(rep.Invoices) != 6 {
		t.Fatalf("report totals = %+v", rep)
	}
	if rep.Owner != "internal" {
		t.Fatalf("owner = %s", rep.Owner)
	}
}
