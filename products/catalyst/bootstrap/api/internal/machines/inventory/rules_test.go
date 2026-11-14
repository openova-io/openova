package inventory

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/source"
)

var t0 = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

func TestRule1Identity(t *testing.T) {
	for _, c := range []struct{ name, source, class, id, want string }{
		{"rule1: a node is named after its Node", "nodes", "node", "cp-1", "cp-1"},
		{"rule1: another class is the source and 10 hex of the id", "sovereign-cloud", "virtual-machine", "srv-1", "sovereign-cloud-" + sha10("srv-1")},
		{"rule1: one id gives one name", "sovereign-cloud", "host", "srv-1", "sovereign-cloud-" + sha10("srv-1")},
	} {
		if got := MachineName(c.source, c.class, c.id); got != c.want {
			t.Errorf("%s: MachineName = %q, want %q", c.name, got, c.want)
		}
	}
	if MachineName("s", "host", "a") == MachineName("s", "host", "b") {
		t.Error("rule1: two ids gave one name")
	}
}

func sha10(id string) string {
	// sha256("srv-1"), computed outside the code under test.
	if id == "srv-1" {
		return "ce956e9f24"
	}
	return ""
}

func complete(ids ...string) source.Enumeration {
	e := source.Enumeration{ID: "E1", Complete: true, Scopes: []source.ScopeResult{{Name: "nodes", Readable: true, Count: len(ids)}}}
	for _, id := range ids {
		e.Sightings = append(e.Sightings, source.Sighting{ExternalID: id})
	}
	return e
}

func TestRule3CompleteOrNothing(t *testing.T) {
	partial := complete("a")
	partial.Scopes = append(partial.Scopes, source.ScopeResult{Name: "net-2", Readable: false, Error: "403"})
	notComplete := complete("a")
	notComplete.Complete = false
	for _, c := range []struct {
		name string
		e    source.Enumeration
		err  error
		want bool
	}{
		{"rule3: a complete enumeration may mark", complete("a"), nil, true},
		{"rule3: a failed enumeration marks nothing", source.Enumeration{}, errors.New("timeout"), false},
		{"rule3: an error with a list marks nothing", complete("a"), errors.New("cancelled"), false},
		{"rule3: an unreadable scope marks nothing", partial, nil, false},
		{"rule3: an enumeration not complete marks nothing", notComplete, nil, false},
		{"rule3: an enumeration without scopes marks nothing", source.Enumeration{Complete: true}, nil, false},
	} {
		if got := MayMarkStale(c.e, c.err); got != c.want {
			t.Errorf("%s: MayMarkStale = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRule4StaleByListing(t *testing.T) {
	for _, c := range []struct {
		name   string
		active []string
		listed map[string]bool
		want   []string
	}{
		{"rule4: an active machine not listed becomes a candidate", []string{"b", "a", "c"}, map[string]bool{"a": true}, []string{"b", "c"}},
		{"rule4: every active machine listed gives none", []string{"a"}, map[string]bool{"a": true, "x": true}, nil},
		{"rule4: no active machine gives none", nil, map[string]bool{}, nil},
	} {
		if got := StaleCandidates(c.active, c.listed); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: StaleCandidates = %v, want %v", c.name, got, c.want)
		}
	}
}

func ids(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return out
}

func defaults(t *testing.T) Settings {
	t.Helper()
	s, err := SettingsOf(nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRule5Hold(t *testing.T) {
	s := defaults(t)
	sweep := func(total, absent int, err error, held *v1alpha1.Held, set Settings) Sweep {
		active := ids("m", total)
		return DecideSweep(set, complete(active[absent:]...), err, active, held, t0)
	}
	if got := sweep(40, 9, nil, nil, s); len(got.Mark) != 9 || got.Held != nil {
		t.Errorf("rule5: 9 of 40 absent is marked: got %d marked, held %v", len(got.Mark), got.Held)
	}
	got := sweep(40, 10, nil, nil, s)
	if len(got.Mark) != 0 || got.Held == nil {
		t.Fatalf("rule5: 10 of 40 absent is held: got %d marked, held %v", len(got.Mark), got.Held)
	}
	if *got.Held != (v1alpha1.Held{EnumerationID: "E1", WouldMarkStale: 10, Live: 40, Since: metav1.NewTime(t0)}) {
		t.Errorf("rule5: the hold records the enumeration, the counts and the time: %+v", *got.Held)
	}
	if got := sweep(100, 10, nil, nil, s); len(got.Mark) != 10 || got.Held != nil {
		t.Errorf("rule5: 10 of 100 absent is marked: got %d marked, held %v", len(got.Mark), got.Held)
	}
	prior := &v1alpha1.Held{EnumerationID: "E0", WouldMarkStale: 10, Live: 40, Since: metav1.NewTime(t0.Add(-time.Hour))}
	if got := sweep(40, 30, errors.New("timeout"), prior, s); len(got.Mark) != 0 || got.Held != prior {
		t.Errorf("rule5: a failed enumeration marks nothing and keeps the hold: %+v", got)
	}
	if got := sweep(40, 12, nil, prior, s); len(got.Mark) != 0 || got.Held.EnumerationID != "E0" || got.Held.WouldMarkStale != 12 || !got.Held.Since.Equal(&prior.Since) {
		t.Errorf("rule5: a later sweep over the threshold keeps the held enumeration and takes the new count: %+v", got.Held)
	}
	if got := sweep(40, 2, nil, prior, s); len(got.Mark) != 2 || got.Held != nil {
		t.Errorf("rule5: a later complete enumeration under the threshold ends the hold: %+v", got)
	}
	release := s
	release.ReleaseHoldFor = "E0"
	if got := sweep(40, 10, nil, prior, release); len(got.Mark) != 10 || got.Held != nil {
		t.Errorf("rule5: releaseHoldFor equal to the held enumeration applies the marking: %+v", got)
	}
	release.ReleaseHoldFor = "E9"
	if got := sweep(40, 10, nil, prior, release); len(got.Mark) != 0 || got.Held == nil {
		t.Errorf("rule5: releaseHoldFor of another enumeration releases nothing: %+v", got)
	}
}

func TestRule6Revive(t *testing.T) {
	since := metav1.NewTime(t0)
	for _, c := range []struct {
		name  string
		phase string
		want  bool
	}{
		{"rule6: a stale machine listed again is active", v1alpha1.PhaseStale, true},
		{"rule6: a retired machine listed again is active", v1alpha1.PhaseRetired, true},
		{"rule6: an active machine stays active", v1alpha1.PhaseActive, false},
	} {
		first := metav1.NewTime(t0.Add(-48 * time.Hour))
		st := v1alpha1.MachineStatus{Phase: c.phase, FirstSeen: &first}
		if c.phase != v1alpha1.PhaseActive {
			st.StaleSince = &since
		}
		changed := Revive(&st)
		if changed != c.want || st.Phase != v1alpha1.PhaseActive || st.StaleSince != nil || !st.FirstSeen.Equal(&first) {
			t.Errorf("%s: changed %v, status %+v", c.name, changed, st)
		}
	}
}

func TestRule12NoAddressNoRun(t *testing.T) {
	for _, c := range []struct {
		name, class, address string
		want                 bool
	}{
		{"rule12: a machine with an address is a target", "virtual-machine", "10.0.0.4", true},
		{"rule12: a machine without an address is never a target", "virtual-machine", "", false},
		{"rule12: a node without an address is a target", "node", "", true},
	} {
		m := &v1alpha1.Machine{Spec: v1alpha1.MachineSpec{Class: c.class}, Status: v1alpha1.MachineStatus{PrimaryAddress: c.address}}
		if got := Targetable(m); got != c.want {
			t.Errorf("%s: Targetable = %v", c.name, got)
		}
	}
}

func TestSettingsOf(t *testing.T) {
	s := defaults(t)
	if s != (Settings{HoldPercent: 20, HoldMinimum: 10, RetireAfter: 720 * time.Hour, PurgeAfter: 2160 * time.Hour}) {
		t.Errorf("defaults: %+v", s)
	}
	p, m := int32(50), int32(0)
	got, err := SettingsOf(&v1alpha1.StaleSpec{HoldPercent: &p, HoldMinimum: &m, RetireAfter: "1h", PurgeAfter: "2h", ReleaseHoldFor: "E1"})
	if err != nil || got != (Settings{HoldPercent: 50, HoldMinimum: 0, RetireAfter: time.Hour, PurgeAfter: 2 * time.Hour, ReleaseHoldFor: "E1"}) {
		t.Errorf("given values: %+v, %v", got, err)
	}
	zero, over := int32(0), int32(101)
	for _, bad := range []*v1alpha1.StaleSpec{{HoldPercent: &zero}, {HoldPercent: &over}, {RetireAfter: "soon"}, {PurgeAfter: "-1h"}} {
		if _, err := SettingsOf(bad); err == nil {
			t.Errorf("SettingsOf(%+v) accepted a value out of range", *bad)
		}
	}
}

func TestStaleTimes(t *testing.T) {
	s := defaults(t)
	since := metav1.NewTime(t0)
	retireAt, purgeAt := t0.Add(720*time.Hour), t0.Add(2880*time.Hour)
	for _, c := range []struct {
		name  string
		phase string
		since *metav1.Time
		now   time.Time
		step  Step
		due   time.Time
	}{
		{"stale: before retireAfter it is kept until then", v1alpha1.PhaseStale, &since, t0.Add(time.Hour), Keep, retireAt},
		{"stale: at retireAfter it is retired", v1alpha1.PhaseStale, &since, retireAt, Retire, purgeAt},
		{"stale: past retireAfter and purgeAfter it is deleted", v1alpha1.PhaseStale, &since, purgeAt, Purge, time.Time{}},
		{"retired: before purgeAfter it is kept until then", v1alpha1.PhaseRetired, &since, retireAt.Add(time.Hour), Keep, purgeAt},
		{"retired: at purgeAfter it is deleted", v1alpha1.PhaseRetired, &since, purgeAt, Purge, time.Time{}},
		{"active: nothing falls due", v1alpha1.PhaseActive, nil, purgeAt, Keep, time.Time{}},
	} {
		step, due := Age(s, v1alpha1.MachineStatus{Phase: c.phase, StaleSince: c.since}, c.now)
		if step != c.step || !due.Equal(c.due) {
			t.Errorf("%s: Age = %s, %v; want %s, %v", c.name, step, due, c.step, c.due)
		}
	}
}

func TestNewULID(t *testing.T) {
	a, b := NewULID(t0), NewULID(t0)
	if len(a) != 26 || a == b || a[:10] != b[:10] {
		t.Fatalf("ULIDs %q and %q: want 26 characters, one time part, different random parts", a, b)
	}
	if later := NewULID(t0.Add(time.Millisecond)); later[:10] <= a[:10] {
		t.Fatalf("a later ULID %q does not sort after %q", later, a)
	}
	if a[:10] != "01M8RDA4M0" {
		t.Fatalf("time part %q, want 01M8RDA4M0 for 2026-12-01T09:00:00Z", a[:10])
	}
}
