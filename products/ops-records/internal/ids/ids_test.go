package ids

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// fixedClock returns a clock that always reads t.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestEncodeKnownValues(t *testing.T) {
	var zero, ones [10]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	cases := []struct {
		name string
		ms   uint64
		r    [10]byte
		want string
	}{
		{"all zero", 0, zero, "00000000000000000000000000"},
		{"largest", maxMillis, ones, "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{"one millisecond", 1, zero, "00000000010000000000000000"},
		{"lowest random bit", 0, [10]byte{9: 1}, "00000000000000000000000001"},
	}
	for _, c := range cases {
		if got := encode(c.ms, c.r); got != c.want {
			t.Errorf("%s: encode = %s, want %s", c.name, got, c.want)
		}
	}
}

func TestNextHasTheTimeOfTheClock(t *testing.T) {
	at := time.Date(2026, 12, 1, 9, 0, 4, 123_000_000, time.UTC)
	g := NewGenerator(fixedClock(at), bytes.NewReader(make([]byte, 10)))
	id, err := g.Next()
	if err != nil {
		t.Fatal(err)
	}
	if !Valid(id) {
		t.Fatalf("%q is not valid", id)
	}
	got, err := Time(id)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(at) {
		t.Fatalf("Time(%s) = %v, want %v", id, got, at)
	}
	if !strings.HasSuffix(id, "0000000000000000") {
		t.Fatalf("random part of %s does not come from the entropy given", id)
	}
}

func TestNextRisesWithinOneMillisecond(t *testing.T) {
	g := NewGenerator(fixedClock(time.UnixMilli(1_700_000_000_000)), nil)
	prev := ""
	for i := 0; i < 1000; i++ {
		id, err := g.Next()
		if err != nil {
			t.Fatal(err)
		}
		if id <= prev {
			t.Fatalf("identifier %d (%s) is not greater than %s", i, id, prev)
		}
		prev = id
	}
}

func TestNextRisesWhenTheClockStepsBack(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_500)
	g := NewGenerator(func() time.Time { return now }, nil)
	first, err := g.Next()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Second)
	second, err := g.Next()
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("after the clock stepped back %s is not greater than %s", second, first)
	}
}

func TestNextRisesOverTime(t *testing.T) {
	now := time.UnixMilli(1_700_000_000_000)
	g := NewGenerator(func() time.Time { return now }, nil)
	a, _ := g.Next()
	now = now.Add(time.Millisecond)
	b, _ := g.Next()
	if b <= a {
		t.Fatalf("a later millisecond gave %s, not greater than %s", b, a)
	}
}

func TestNextOverflowFails(t *testing.T) {
	ones := bytes.Repeat([]byte{0xFF}, 10)
	g := NewGenerator(fixedClock(time.UnixMilli(5)), bytes.NewReader(ones))
	if _, err := g.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Next(); !errors.Is(err, ErrOverflow) {
		t.Fatalf("second identifier in the same millisecond after all ones: err = %v, want ErrOverflow", err)
	}
}

func TestNextRefusesTimeOutOfRange(t *testing.T) {
	g := NewGenerator(fixedClock(time.UnixMilli(-1)), nil)
	if _, err := g.Next(); err == nil {
		t.Fatal("a time before 1970 gave no error")
	}
}

func TestNextFailsWhenEntropyFails(t *testing.T) {
	g := NewGenerator(nil, bytes.NewReader(nil))
	if _, err := g.Next(); err == nil {
		t.Fatal("an empty entropy source gave no error")
	}
}

func TestPrefixes(t *testing.T) {
	inc := NewIncident()
	if !strings.HasPrefix(inc, "INC-") || !ValidIncident(inc) || len(inc) != 4+Length {
		t.Fatalf("NewIncident() = %q", inc)
	}
	prb := NewProblem()
	if !strings.HasPrefix(prb, "PRB-") || !ValidProblem(prb) {
		t.Fatalf("NewProblem() = %q", prb)
	}
	if ValidIncident(prb) || ValidProblem(inc) {
		t.Fatal("a prefix is accepted for the other kind")
	}
	if ValidIncident(New()) {
		t.Fatal("a ULID without prefix is accepted as an incident identifier")
	}
}

func TestValidRefuses(t *testing.T) {
	good := New()
	bad := []string{
		"",
		good[:25],
		good + "0",
		strings.ToLower(good),
		"8" + good[1:],
		good[:10] + "I" + good[11:],
		good[:10] + "L" + good[11:],
		good[:10] + "O" + good[11:],
		good[:10] + "U" + good[11:],
	}
	if !Valid(good) {
		t.Fatalf("%q is refused", good)
	}
	for _, s := range bad {
		if Valid(s) {
			t.Errorf("%q is accepted", s)
		}
	}
	if _, err := Time("not a ulid"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Time of an invalid string: err = %v", err)
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := New()
		if seen[id] {
			t.Fatalf("%s made twice", id)
		}
		seen[id] = true
	}
}
