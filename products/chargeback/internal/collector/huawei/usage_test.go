package huawei

import (
	"math"
	"testing"
	"time"
)

func ts(h, m int) time.Time { return time.Date(2026, 8, 31, h, m, 0, 0, time.UTC) }

func hoursOf(slices []Slice) []float64 {
	out := make([]float64, len(slices))
	for i, s := range slices {
		out[i] = s.Hours()
	}
	return out
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestHourSlicesCreatedInsideWindow(t *testing.T) {
	got := HourSlices(ts(10, 0), ts(12, 0), Lifecycle{Created: ts(10, 30), Status: "ACTIVE"})
	if len(got) != 2 || !approx(got[0].Hours(), 0.5) || !approx(got[1].Hours(), 1) {
		t.Fatalf("slices = %v", hoursOf(got))
	}
	if !got[0].Start.Equal(ts(10, 30)) || !got[0].End.Equal(ts(11, 0)) || !got[1].End.Equal(ts(12, 0)) {
		t.Fatalf("bounds wrong: %+v", got)
	}
	if got[0].Status != "ACTIVE" {
		t.Fatalf("status = %q", got[0].Status)
	}
}

func TestHourSlicesDeletedInsideWindow(t *testing.T) {
	got := HourSlices(ts(10, 0), ts(12, 0), Lifecycle{Created: ts(1, 0), Deleted: ts(11, 15), Status: "ACTIVE"})
	if len(got) != 2 || !approx(got[0].Hours(), 1) || !approx(got[1].Hours(), 0.25) {
		t.Fatalf("slices = %v", hoursOf(got))
	}
}

func TestHourSlicesCreateAndDeleteWithinOneHour(t *testing.T) {
	got := HourSlices(ts(10, 0), ts(12, 0), Lifecycle{Created: ts(10, 10), Deleted: ts(10, 40)})
	if len(got) != 1 || !approx(got[0].Hours(), 0.5) {
		t.Fatalf("slices = %v", hoursOf(got))
	}
}

func TestHourSlicesOutsideWindow(t *testing.T) {
	if got := HourSlices(ts(10, 0), ts(12, 0), Lifecycle{Created: ts(1, 0), Deleted: ts(9, 0)}); len(got) != 0 {
		t.Fatalf("deleted before window produced %v", got)
	}
	if got := HourSlices(ts(10, 0), ts(12, 0), Lifecycle{Created: ts(13, 0)}); len(got) != 0 {
		t.Fatalf("created after window produced %v", got)
	}
	if got := HourSlices(ts(10, 0), ts(10, 0), Lifecycle{}); len(got) != 0 {
		t.Fatalf("empty window produced %v", got)
	}
}

func TestHourSlicesRecomputesFromHourFloor(t *testing.T) {
	// A tick at 10:22 that starts where the previous tick ended (10:07) must
	// recompute the whole 10:00 hour so the upsert keyed by slice start grows
	// the same row instead of creating a second one.
	got := HourSlices(ts(10, 7), ts(10, 22), Lifecycle{Created: ts(1, 0), Status: "ACTIVE"})
	if len(got) != 1 || !got[0].Start.Equal(ts(10, 0)) || !got[0].End.Equal(ts(10, 22)) {
		t.Fatalf("slices = %+v", got)
	}
	// With no known creation time the resource counts from the hour floor.
	got = HourSlices(ts(10, 7), ts(11, 30), Lifecycle{})
	if len(got) != 2 || !got[0].Start.Equal(ts(10, 0)) || !approx(got[1].Hours(), 0.5) {
		t.Fatalf("slices = %+v", got)
	}
}

func TestHourSlicesStoppedPolicySegments(t *testing.T) {
	lc := Lifecycle{
		Created: ts(1, 0),
		Transitions: []Transition{
			{At: ts(1, 0), Status: "ACTIVE", Source: "created"},
			{At: ts(10, 45), Status: "SHUTOFF", Source: "cts"},
			{At: ts(11, 30), Status: "ACTIVE", Source: "cts"},
		},
	}
	got := HourSlices(ts(10, 0), ts(12, 0), lc)
	want := []struct {
		h      float64
		status string
	}{{0.75, "ACTIVE"}, {0.25, "SHUTOFF"}, {0.5, "SHUTOFF"}, {0.5, "ACTIVE"}}
	if len(got) != len(want) {
		t.Fatalf("got %d slices: %+v", len(got), got)
	}
	var running, stopped float64
	for i, w := range want {
		if !approx(got[i].Hours(), w.h) || got[i].Status != w.status {
			t.Errorf("slice %d = %.2fh %s, want %.2fh %s", i, got[i].Hours(), got[i].Status, w.h, w.status)
		}
		if IsStopped(got[i].Status) {
			stopped += got[i].Hours()
		} else {
			running += got[i].Hours()
		}
	}
	if !approx(running, 1.25) || !approx(stopped, 0.75) {
		t.Fatalf("running=%v stopped=%v", running, stopped)
	}
}

func TestHourSlicesFlavorTransitionChangesSKU(t *testing.T) {
	lc := Lifecycle{
		Created: ts(9, 0),
		Transitions: []Transition{
			{At: ts(9, 0), Status: "ACTIVE", Flavor: "s6.large.2", Source: "created"},
			{At: ts(10, 30), Flavor: "s6.xlarge.2", Source: "cts"},
		},
	}
	got := HourSlices(ts(10, 0), ts(11, 0), lc)
	if len(got) != 2 || got[0].Flavor != "s6.large.2" || got[1].Flavor != "s6.xlarge.2" || got[1].Status != "ACTIVE" {
		t.Fatalf("slices = %+v", got)
	}
	if sk := SKUsFor(KindECS, nil, got[1].Flavor); sk[0].Name != "ecs.s6.xlarge.2" {
		t.Fatalf("sku = %+v", sk)
	}
}

func TestSKUsFor(t *testing.T) {
	if s := SKUsFor(KindEVS, map[string]any{"size_gb": 100, "volume_type": "SSD"}, ""); s[0].Name != "evs.ssd.gb" || s[0].Multiplier != 100 || s[0].Unit != "gb-hour" {
		t.Fatalf("evs ssd = %+v", s)
	}
	if s := SKUsFor(KindEVS, map[string]any{"size_gb": float64(40), "volume_type": "SAS"}, ""); s[0].Name != "evs.hdd.gb" || s[0].Multiplier != 40 {
		t.Fatalf("evs sas = %+v", s)
	}
	if s := SKUsFor(KindEIP, map[string]any{"bandwidth_mbps": 5}, ""); len(s) != 2 || s[0].Name != "eip" || s[1].Name != "eip.bandwidth_mbps" || s[1].Multiplier != 5 {
		t.Fatalf("eip = %+v", s)
	}
	if s := SKUsFor(KindEIP, map[string]any{"bandwidth_mbps": 0}, ""); len(s) != 1 {
		t.Fatalf("eip no bandwidth = %+v", s)
	}
	if s := SKUsFor(KindNAT, map[string]any{"spec": "2"}, ""); s[0].Name != "nat.2" || s[0].Unit != "hour" {
		t.Fatalf("nat = %+v", s)
	}
	if s := SKUsFor(KindELB, nil, ""); s[0].Name != "elb" {
		t.Fatalf("elb = %+v", s)
	}
	if s := SKUsFor(KindECS, map[string]any{"flavor": "c7.large.2"}, ""); s[0].Name != "ecs.c7.large.2" || s[0].Unit != "instance-hour" {
		t.Fatalf("ecs = %+v", s)
	}
	if s := SKUsFor("unknown", nil, ""); s != nil {
		t.Fatalf("unknown kind = %+v", s)
	}
	if q := Quantity(0.5, 100); q != 50 {
		t.Fatalf("quantity = %v", q)
	}
	if q := Quantity(1.0/3.0, 1); q != 0.333333 {
		t.Fatalf("quantity rounding = %v", q)
	}
}

func TestMergeTransitionMovesObservedToExactTime(t *testing.T) {
	existing := []Transition{
		{At: ts(9, 0), Status: "ACTIVE", Source: "created"},
		{At: ts(10, 15), Status: "SHUTOFF", Source: "observed"}, // seen at a list tick
	}
	got := MergeTransition(existing, Transition{At: ts(10, 3), Status: "SHUTOFF"}, 30*time.Minute)
	if len(got) != 2 || !got[1].At.Equal(ts(10, 3)) || got[1].Source != "cts" {
		t.Fatalf("merge = %+v", got)
	}
	// An event with no observed counterpart is inserted in order.
	got = MergeTransition(got, Transition{At: ts(9, 30), Status: "ACTIVE"}, 30*time.Minute)
	if len(got) != 3 || !got[1].At.Equal(ts(9, 30)) {
		t.Fatalf("insert = %+v", got)
	}
	// Beyond the tolerance nothing is moved.
	got = MergeTransition(existing, Transition{At: ts(8, 0), Status: "SHUTOFF"}, 30*time.Minute)
	if len(got) != 3 {
		t.Fatalf("out-of-tolerance = %+v", got)
	}
}

func TestAdoptObservedUsesEarlierCTSTime(t *testing.T) {
	existing := []Transition{
		{At: ts(9, 0), Status: "ACTIVE", Source: "created"},
		{At: ts(10, 3), Status: "SHUTOFF", Source: "cts"},
	}
	got := AdoptObserved(existing, Transition{At: ts(10, 15), Status: "SHUTOFF"}, 30*time.Minute)
	if len(got) != 2 {
		t.Fatalf("adopt should not add: %+v", got)
	}
	got = AdoptObserved(existing, Transition{At: ts(12, 0), Status: "ACTIVE"}, 30*time.Minute)
	if len(got) != 3 || got[2].Source != "observed" {
		t.Fatalf("new observation not appended: %+v", got)
	}
	// A pending CTS resize adopts the flavor the next list tick observes.
	pending := []Transition{{At: ts(9, 0), Flavor: "a", Source: "created"}, {At: ts(10, 0), Source: "cts"}}
	got = AdoptObserved(pending, Transition{At: ts(10, 10), Flavor: "b"}, 30*time.Minute)
	if len(got) != 2 || got[1].Flavor != "b" || !got[1].At.Equal(ts(10, 0)) {
		t.Fatalf("resize adopt = %+v", got)
	}
}

func TestParseTimeShapes(t *testing.T) {
	cases := []string{"2026-08-31T10:00:00Z", "2026-08-31T10:00:00.123456", "2026-08-31 10:00:00", "2026-08-31 10:00:00.418723"}
	for _, c := range cases {
		if got := parseTime(c); got.IsZero() || got.Hour() != 10 {
			t.Errorf("parseTime(%q) = %v", c, got)
		}
	}
	if !parseTime("").IsZero() || !parseTime("garbage").IsZero() {
		t.Error("garbage parsed")
	}
}
