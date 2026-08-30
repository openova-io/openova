// Package window is the hour-slice window math shared by every collector
// (ADR-0014 D3a). It was born in the Huawei cloud collector and is exported
// here so the OpenOva platform collector slices usage windows EXACTLY the
// same way: contiguous, single-status intervals that never cross a UTC hour
// boundary, recomputed idempotently per touched hour. The huawei package
// re-exports these names so its callers and tests are unchanged.
package window

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Transition is a point in a resource's life after which the given status
// (and, when set, flavor) applies. Source is "observed" when the collector
// noticed the change at a list tick, "cts" when the audit trail gave the
// exact time, "created" for the initial state.
type Transition struct {
	At     time.Time `json:"at"`
	Status string    `json:"status,omitempty"`
	Flavor string    `json:"flavor,omitempty"`
	Source string    `json:"source,omitempty"`
}

// Lifecycle is everything the window math needs about one resource.
type Lifecycle struct {
	Created     time.Time    // zero = unknown (treated as before the window)
	Deleted     time.Time    // zero = still alive
	Status      string       // fallback when Transitions is empty
	Flavor      string       // fallback flavor (ECS)
	Transitions []Transition // sorted by At; the first one carries the initial state
}

// Slice is one contiguous, single-status, hour-bounded billing interval.
type Slice struct {
	Start, End time.Time
	Status     string
	Flavor     string
}

// Hours is the slice length in hours.
func (s Slice) Hours() float64 {
	return s.End.Sub(s.Start).Hours()
}

// HourSlices splits the resource's life inside [from, to) into slices that
// never cross a UTC hour boundary or a status/flavor transition. The window
// starts at the hour containing from, so a re-run over a later tick
// recomputes each touched hour from scratch (idempotent upserts keyed by
// slice start).
func HourSlices(from, to time.Time, lc Lifecycle) []Slice {
	from = from.UTC()
	to = to.UTC()
	lo := from.Truncate(time.Hour)
	if !lc.Created.IsZero() && lc.Created.After(lo) {
		lo = lc.Created.UTC()
	}
	hi := to
	if !lc.Deleted.IsZero() && lc.Deleted.Before(hi) {
		hi = lc.Deleted.UTC()
	}
	if !hi.After(lo) {
		return nil
	}
	trs := append([]Transition(nil), lc.Transitions...)
	sort.SliceStable(trs, func(i, j int) bool { return trs[i].At.Before(trs[j].At) })

	points := []time.Time{lo}
	for h := lo.Truncate(time.Hour).Add(time.Hour); h.Before(hi); h = h.Add(time.Hour) {
		points = append(points, h)
	}
	for _, t := range trs {
		if t.At.After(lo) && t.At.Before(hi) {
			points = append(points, t.At.UTC())
		}
	}
	points = append(points, hi)
	sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })

	var out []Slice
	for i := 0; i+1 < len(points); i++ {
		s, e := points[i], points[i+1]
		if !e.After(s) {
			continue
		}
		status, flavor := StateAt(s, lc, trs)
		out = append(out, Slice{Start: s, End: e, Status: status, Flavor: flavor})
	}
	return out
}

// StateAt returns the status/flavor in force at t: the last transition at or
// before t; before the first transition, the first transition's state; with
// no transitions, the lifecycle fallbacks.
func StateAt(t time.Time, lc Lifecycle, trs []Transition) (string, string) {
	status, flavor := lc.Status, lc.Flavor
	if len(trs) == 0 {
		return status, flavor
	}
	if trs[0].Status != "" {
		status = trs[0].Status
	}
	if trs[0].Flavor != "" {
		flavor = trs[0].Flavor
	}
	for _, tr := range trs {
		if tr.At.After(t) {
			break
		}
		if tr.Status != "" {
			status = tr.Status
		}
		if tr.Flavor != "" {
			flavor = tr.Flavor
		}
	}
	return status, flavor
}

// IsStopped reports whether an ECS status means the instance is powered off.
func IsStopped(status string) bool {
	switch strings.ToUpper(status) {
	case "SHUTOFF", "STOPPED", "SHUTDOWN":
		return true
	}
	return false
}

// Quantity rounds hours × multiplier to the 6 decimals usage_records store.
func Quantity(hours, multiplier float64) float64 {
	return math.Round(hours*multiplier*1e6) / 1e6
}

// MergeTransition folds an exact-time transition (from CTS) into the observed
// list: an observed transition of the same kind within tolerance after the
// event is moved to the exact time; otherwise the event is inserted. The
// result stays sorted.
func MergeTransition(existing []Transition, ev Transition, tolerance time.Duration) []Transition {
	out := append([]Transition(nil), existing...)
	replaced := false
	for i := range out {
		if out[i].Source == "cts" {
			continue
		}
		same := (ev.Status != "" && strings.EqualFold(out[i].Status, ev.Status)) ||
			(ev.Status == "" && ev.Flavor != "" && out[i].Flavor != "")
		if !same {
			continue
		}
		if !out[i].At.Before(ev.At) && out[i].At.Sub(ev.At) <= tolerance {
			out[i].At = ev.At
			out[i].Source = "cts"
			if ev.Flavor != "" {
				out[i].Flavor = ev.Flavor
			}
			replaced = true
			break
		}
	}
	if !replaced {
		ev.Source = "cts"
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// AdoptObserved is the reverse of MergeTransition: when the collector observes
// a change at a list tick and a CTS transition with the same target status (or
// a pending flavor) already sits within tolerance before it, the observation
// is attributed to that exact time instead of adding a second transition.
func AdoptObserved(existing []Transition, obs Transition, tolerance time.Duration) []Transition {
	for i := range existing {
		e := existing[i]
		if e.Source != "cts" || e.At.After(obs.At) || obs.At.Sub(e.At) > tolerance {
			continue
		}
		if (obs.Status != "" && strings.EqualFold(e.Status, obs.Status)) || (obs.Flavor != "" && e.Status == "" && e.Flavor == "") {
			out := append([]Transition(nil), existing...)
			if obs.Flavor != "" {
				out[i].Flavor = obs.Flavor
			}
			return out
		}
	}
	obs.Source = "observed"
	out := append(append([]Transition(nil), existing...), obs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}
