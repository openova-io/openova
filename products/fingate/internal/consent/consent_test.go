package consent

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/openova-io/openova/products/fingate/internal/profile"
)

const rw400Dir = "../../profiles/rw-4.0.0"

var (
	allStates = []State{None, Awaiting, Authorised, Rejected, Revoked, Expired, Consumed}
	allEvents = []Event{Create, Authorise, Reject, Revoke, Expire, Consume}
	allActors = []Actor{ThirdParty, Customer, Bank, System}
)

// allowed is the transition table written out once more, independently of the package:
// family, then "from event", then the actors allowed and the state after the event.
type outcome struct {
	to     State
	actors []Actor
}

func allowed() map[Family]map[string]outcome {
	common := func() map[string]outcome {
		return map[string]outcome{
			" create":            {Awaiting, []Actor{ThirdParty}},
			"awaiting authorise": {Authorised, []Actor{Customer}},
			"awaiting reject":    {Rejected, []Actor{Customer, System}},
		}
	}
	m := map[Family]map[string]outcome{}
	for _, f := range []Family{AccountAccess, FundsConfirmation, VRP} {
		m[f] = common()
		m[f]["authorised revoke"] = outcome{Revoked, []Actor{Customer, Bank, ThirdParty}}
		m[f]["authorised expire"] = outcome{Expired, []Actor{System}}
	}
	m[Payment] = common()
	m[Payment]["authorised consume"] = outcome{Consumed, []Actor{System}}
	return m
}

// TestNextTable checks Next for every family, every state and every event, and for an
// unknown family, an unknown state and an unknown event, against the table written out above.
func TestNextTable(t *testing.T) {
	want := allowed()
	families := append(Families(), Family("unknown"))
	states := append(append([]State{}, allStates...), State("unknown"))
	events := append(append([]Event{}, allEvents...), Event("unknown"))
	accepted := 0
	for _, f := range families {
		for _, from := range states {
			for _, e := range events {
				got, err := Next(f, from, e)
				o, ok := want[f][string(from)+" "+string(e)]
				switch {
				case ok && (err != nil || got != o.to):
					t.Errorf("Next(%s, %q, %s) = %q, %v; want %q", f, from, e, got, err, o.to)
				case !ok && !errors.Is(err, ErrTransition):
					t.Errorf("Next(%s, %q, %s) = %q, %v; want ErrTransition", f, from, e, got, err)
				case ok:
					accepted++
				}
			}
		}
	}
	if accepted != 19 {
		t.Errorf("Next accepted %d transitions, want 19 (5 for each of three families, 4 for payment)", accepted)
	}
}

// TestFamilies checks the four families, against the families of the profile's consentStatus.
func TestFamilies(t *testing.T) {
	p, err := profile.LoadDir(rw400Dir)
	if err != nil {
		t.Fatalf("LoadDir(%s): %v", rw400Dir, err)
	}
	var got, inProfile []string
	for _, f := range Families() {
		got = append(got, string(f))
	}
	for f := range p.ConsentStatus {
		inProfile = append(inProfile, f)
	}
	sort.Strings(got)
	sort.Strings(inProfile)
	if !reflect.DeepEqual(got, inProfile) {
		t.Errorf("Families() = %q, the profile has %q", got, inProfile)
	}
	for _, s := range allStates {
		if want := s == Rejected || s == Revoked || s == Expired || s == Consumed; Terminal(s) != want {
			t.Errorf("Terminal(%q) = %v, want %v", s, Terminal(s), want)
		}
	}
}

// walkEvents calls visit for every sequence of events of length 1 to depth that starts at
// None, with the state after each prefix as Next leaves it, until Next refuses an event.
// It returns the number of sequences visited.
func walkEvents(t *testing.T, f Family, depth int, visit func(seq []Event, before, after State, err error)) int {
	t.Helper()
	n := 0
	var walk func(seq []Event, s State)
	walk = func(seq []Event, s State) {
		if len(seq) == depth {
			return
		}
		for _, e := range allEvents {
			next := append(append([]Event{}, seq...), e)
			after, err := Next(f, s, e)
			n++
			visit(next, s, after, err)
			if err == nil {
				walk(next, after)
			} else {
				walk(next, s)
			}
		}
	}
	walk(nil, None)
	return n
}

// TestPropertyTerminalHasNoExit is the first consent property: for every family, no sequence of
// events leaves a terminal state. Every sequence of up to six events from no consent is tried.
func TestPropertyTerminalHasNoExit(t *testing.T) {
	for _, f := range Families() {
		t.Run(string(f), func(t *testing.T) {
			reached := map[State]bool{}
			n := walkEvents(t, f, 6, func(seq []Event, before, after State, err error) {
				if err == nil {
					reached[after] = true
				}
				if Terminal(before) && (err == nil || !errors.Is(err, ErrTransition)) {
					t.Fatalf("%s %v: left the terminal state %q to %q, err %v", f, seq, before, after, err)
				}
				if err == nil && after == before {
					t.Fatalf("%s %v: an accepted event kept the state %q", f, seq, before)
				}
			})
			terminals := 0
			for s := range reached {
				if Terminal(s) {
					terminals++
				}
			}
			want := 3
			if f == Payment {
				want = 2
			}
			if terminals != want {
				t.Fatalf("%s: %d terminal states reached in %d sequences, want %d", f, terminals, n, want)
			}
		})
	}
}

// TestPropertyEveryTransitionAppendsHistory is the second consent property: for every family,
// every accepted event appends exactly one history entry that names the transition, and a
// refused event changes nothing. Every sequence of up to four (event, actor) pairs is tried.
func TestPropertyEveryTransitionAppendsHistory(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	want := allowed()
	for _, f := range Families() {
		t.Run(string(f), func(t *testing.T) {
			pairs := map[string]bool{}
			var walk func(r Record, depth int)
			walk = func(r Record, depth int) {
				if depth == 0 {
					return
				}
				for _, e := range allEvents {
					for _, a := range allActors {
						before := Record{Family: r.Family, State: r.State, History: slices.Clone(r.History)}
						after := Record{Family: r.Family, State: r.State, History: slices.Clone(r.History)}
						reason := fmt.Sprintf("%s by %s", e, a)
						err := after.Apply(e, a, reason, at)
						o, ok := want[f][string(before.State)+" "+string(e)]
						ok = ok && slices.Contains(o.actors, a)
						if !ok {
							if !errors.Is(err, ErrTransition) || !reflect.DeepEqual(after, before) {
								t.Fatalf("%s from %q: %s by %s was not refused cleanly: err %v, record %+v", f, before.State, e, a, err, after)
							}
							continue
						}
						if err != nil {
							t.Fatalf("%s from %q: %s by %s refused: %v", f, before.State, e, a, err)
						}
						pairs[string(before.State)+" "+string(e)+" "+string(a)] = true
						if len(after.History) != len(before.History)+1 || !slices.Equal(after.History[:len(before.History)], before.History) {
							t.Fatalf("%s from %q: %s by %s did not append exactly one entry: %+v", f, before.State, e, a, after.History)
						}
						last := after.History[len(after.History)-1]
						entry := HistoryEntry{Seq: len(after.History), From: before.State, To: o.to, Actor: a, Reason: reason, At: at}
						if last != entry || after.State != o.to {
							t.Fatalf("%s from %q: %s by %s: state %q, entry %+v; want state %q, entry %+v", f, before.State, e, a, after.State, last, o.to, entry)
						}
						walk(after, depth-1)
					}
				}
			}
			walk(Record{Family: f}, 4)
			wantPairs := 0
			for _, o := range want[f] {
				wantPairs += len(o.actors)
			}
			if len(pairs) != wantPairs {
				t.Fatalf("%s: %d allowed (state, event, actor) triples were taken, want %d", f, len(pairs), wantPairs)
			}
		})
	}
}

// TestApplyContinuesSeq checks that Seq follows the last entry when History does not start at 1.
func TestApplyContinuesSeq(t *testing.T) {
	r := Record{Family: AccountAccess, State: Awaiting, History: []HistoryEntry{{Seq: 7, From: None, To: Awaiting, Actor: ThirdParty}}}
	if err := r.Apply(Authorise, Customer, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got := r.History[len(r.History)-1].Seq; got != 8 {
		t.Fatalf("Seq = %d, want 8", got)
	}
}

// TestWireRW400 checks Wire against profile rw-4.0.0: the states reachable in a family are
// exactly the states the profile gives a code for that family, each code is the expected one
// and differs from the others of its family, and every other state has no code.
func TestWireRW400(t *testing.T) {
	p, err := profile.LoadDir(rw400Dir)
	if err != nil {
		t.Fatalf("LoadDir(%s): %v", rw400Dir, err)
	}
	codes := map[Family]map[State]string{
		AccountAccess:     {Awaiting: "AWAU", Authorised: "AUTH", Rejected: "RJCT", Revoked: "CANC", Expired: "EXPD"},
		Payment:           {Awaiting: "AWAU", Authorised: "AUTH", Rejected: "RJCT", Consumed: "COND"},
		FundsConfirmation: {Awaiting: "AWAU", Authorised: "AUTH", Rejected: "RJCT", Revoked: "CANC", Expired: "EXPD"},
		VRP:               {Awaiting: "AWAU", Authorised: "AUTH", Rejected: "RJCT", Revoked: "CANC", Expired: "EXPD"},
	}
	for _, f := range Families() {
		reachable := map[State]bool{}
		walkEvents(t, f, 4, func(_ []Event, _, after State, err error) {
			if err == nil {
				reachable[after] = true
			}
		})
		if len(reachable) != len(p.ConsentStatus[string(f)]) {
			t.Errorf("%s: %d states reachable, the profile has codes for %d", f, len(reachable), len(p.ConsentStatus[string(f)]))
		}
		seen := map[string]State{}
		for _, s := range allStates {
			got, err := Wire(*p, f, s)
			want, has := codes[f][s]
			if has != reachable[s] {
				t.Errorf("%s: state %q reachable %v, has a code %v", f, s, reachable[s], has)
			}
			if !has {
				if !errors.Is(err, ErrNoCode) {
					t.Errorf("Wire(%s, %q) = %q, %v; want ErrNoCode", f, s, got, err)
				}
				continue
			}
			if err != nil || got != want {
				t.Errorf("Wire(%s, %q) = %q, %v; want %q", f, s, got, err, want)
			}
			if other, dup := seen[got]; dup {
				t.Errorf("%s: the states %q and %q have the same code %s", f, other, s, got)
			}
			seen[got] = s
		}
	}
	if _, err := Wire(*p, Family("unknown"), Awaiting); !errors.Is(err, ErrNoCode) {
		t.Errorf("Wire of an unknown family: %v, want ErrNoCode", err)
	}
}
