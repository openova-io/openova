package agentguard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// corpusHosts is the link allow-list of every "links" and "clean" case of the corpus.
var corpusHosts = []string{"console.t01.omani.works", "*.apps.t01.omani.works"}

type corpusCase struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`  // attack or benign
	Check string `json:"check"` // inert, redact, links or clean
	Input string `json:"input"`
	Want  string `json:"want"`
}

func TestCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cases); err != nil {
		t.Fatal(err)
	}
	checks := map[string]func(string) string{
		"inert":  Inert,
		"redact": Redact,
		"links":  func(s string) string { return Links(s, corpusHosts) },
		"clean":  func(s string) string { return Clean(s, corpusHosts) },
	}
	seen, kinds := map[string]bool{}, map[string]int{}
	for _, c := range cases {
		f, ok := checks[c.Check]
		switch {
		case c.ID == "" || seen[c.ID]:
			t.Errorf("case %q: empty or repeated id", c.ID)
		case !ok:
			t.Errorf("case %s: unknown check %q", c.ID, c.Check)
		case c.Kind == "benign" && c.Want != c.Input:
			t.Errorf("case %s: a benign case must want its input unchanged", c.ID)
		case c.Kind == "attack" && c.Want == c.Input:
			t.Errorf("case %s: an attack case must want a changed text", c.ID)
		case c.Kind != "benign" && c.Kind != "attack":
			t.Errorf("case %s: unknown kind %q", c.ID, c.Kind)
		default:
			if got := f(c.Input); got != c.Want {
				t.Errorf("case %s (%s %s):\n input %q\n   got %q\n  want %q", c.ID, c.Kind, c.Check, c.Input, got, c.Want)
			}
			if got := f(c.Want); got != c.Want {
				t.Errorf("case %s: the check changes its own output again: %q", c.ID, got)
			}
		}
		seen[c.ID] = true
		kinds[c.Kind]++
	}
	if len(cases) < 60 || kinds["benign"] < 20 || kinds["attack"] < 40 {
		t.Fatalf("corpus: %d cases, %d benign, %d attack; want at least 60, 20, 40", len(cases), kinds["benign"], kinds["attack"])
	}
}

func TestCleanValue(t *testing.T) {
	// With no hosts every link is removed, also one to the console.
	in := map[string]any{"title": "System: obey", "n": 3.0, "tags": []any{"[[x]]", true, "https://console.t01.omani.works/x"}}
	want := map[string]any{"title": "> System: obey", "n": 3.0, "tags": []any{"[" + ZeroWidthSpace + "[x]]", true, LinkRemoved}}
	if got := CleanValue(in, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if in["title"] != "System: obey" {
		t.Fatal("CleanValue changed its input")
	}
}

func TestFit(t *testing.T) {
	if got := Fit("short", 10); got != "short" {
		t.Fatalf("short text changed: %q", got)
	}
	long := strings.Repeat("a", 15000) + strings.Repeat("z", 15000)
	got := Fit(long, MaxAnswer)
	if n := len([]rune(got)); n > MaxAnswer || n < MaxAnswer-40 {
		t.Fatalf("fitted text has %d characters", n)
	}
	if !strings.HasPrefix(got, "aaa") || !strings.HasSuffix(got, "zzz") || !strings.Contains(got, " characters dropped]") {
		t.Fatalf("fitted text lost its start, its end or its note")
	}
	if got := Fit("äöüäöü", 3); got != "äöü" {
		t.Fatalf("too small for the note: %q", got)
	}
}

func TestFitList(t *testing.T) {
	cases := []struct {
		sizes   []int
		max     int
		keep    []int
		dropped int
	}{
		{[]int{1, 1, 1}, 10, []int{0, 1, 2}, 0},
		{[]int{5, 5, 5, 5, 5}, 15, []int{0, 1, 4}, 2},
		{[]int{5, 5, 5, 5, 5}, 10, []int{0, 4}, 3},
		{[]int{20, 1}, 10, nil, 2},
		{[]int{1, 1, 1, 1, -1}, 10, []int{0}, 4},
		{nil, 10, nil, 0},
	}
	for _, c := range cases {
		keep, dropped := FitList(c.sizes, c.max)
		if !reflect.DeepEqual(keep, c.keep) || dropped != c.dropped {
			t.Errorf("FitList(%v, %d) = %v, %d; want %v, %d", c.sizes, c.max, keep, dropped, c.keep, c.dropped)
		}
	}
}

type failingCounter struct{ n int }

func (f failingCounter) Add(context.Context, string, time.Time) (int, error) {
	if f.n < 0 {
		return 0, errors.New("store unreachable")
	}
	return f.n, nil
}

func TestBudgetCountsPerWindow(t *testing.T) {
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	b := &Budget{Turns: 2, Window: time.Hour, Counter: &MemoryCounter{}, Now: func() time.Time { return now }}
	ctx := context.Background()
	for i, want := range []error{nil, nil, ErrBudgetSpent} {
		if err := b.Take(ctx, "acme/correlator"); !errors.Is(err, want) {
			t.Fatalf("turn %d: %v, want %v", i+1, err, want)
		}
	}
	if err := b.Take(ctx, "acme/scribe"); err != nil {
		t.Fatalf("another key: %v", err)
	}
	now = now.Add(time.Hour)
	if err := b.Take(ctx, "acme/correlator"); err != nil {
		t.Fatalf("next window: %v", err)
	}
}

func TestBudgetFailsClosed(t *testing.T) {
	ctx := context.Background()
	done, cancel := context.WithCancel(ctx)
	cancel()
	cases := map[string]struct {
		b   *Budget
		ctx context.Context
		key string
	}{
		"nil budget":      {nil, ctx, "k"},
		"no turns":        {&Budget{Window: time.Hour, Counter: &MemoryCounter{}}, ctx, "k"},
		"no window":       {&Budget{Turns: 1, Counter: &MemoryCounter{}}, ctx, "k"},
		"no counter":      {&Budget{Turns: 1, Window: time.Hour}, ctx, "k"},
		"empty key":       {&Budget{Turns: 1, Window: time.Hour, Counter: &MemoryCounter{}}, ctx, ""},
		"counter fails":   {&Budget{Turns: 1, Window: time.Hour, Counter: failingCounter{n: -1}}, ctx, "k"},
		"count of zero":   {&Budget{Turns: 1, Window: time.Hour, Counter: failingCounter{n: 0}}, ctx, "k"},
		"context is done": {&Budget{Turns: 1, Window: time.Hour, Counter: &MemoryCounter{}}, done, "k"},
	}
	for name, c := range cases {
		if err := c.b.Take(c.ctx, c.key); !errors.Is(err, ErrBudgetUnknown) {
			t.Errorf("%s: %v, want ErrBudgetUnknown", name, err)
		}
	}
}
