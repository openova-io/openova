// Package inventory is the inventory controller of the machine plane. It reads each
// MachineSource, enumerates it and keeps one Machine object per machine it saw.
//
// This file holds the inventory rules as pure functions, so that every rule is a
// table test in rules_test.go: rule 1 (identity), rule 3 (complete or nothing),
// rule 4 (stale by the source's own listing), rule 5 (hold), rule 6 (revive),
// rule 12 (no address, no run) and the retire and purge times of spec.stale.
package inventory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/machines/source"
)

// MachineName is rule 1. A Machine of class node is named after its Node. Any other
// Machine is "<source name>-<first 10 hex digits of sha256(externalId)>". Together
// with its namespace and spec.sourceRef.name, the name stands for one externalId.
func MachineName(sourceName, class, externalID string) string {
	if class == v1alpha1.ClassNode {
		return externalID
	}
	sum := sha256.Sum256([]byte(externalID))
	return sourceName + "-" + hex.EncodeToString(sum[:])[:10]
}

// MayMarkStale is rule 3. Only an enumeration that ended without an error, says it
// is complete and read every scope may mark a machine stale.
func MayMarkStale(e source.Enumeration, err error) bool {
	if err != nil || !e.Complete || len(e.Scopes) == 0 {
		return false
	}
	for _, s := range e.Scopes {
		if !s.Readable {
			return false
		}
	}
	return true
}

// StaleCandidates is rule 4: the Active machines of the source that the enumeration
// did not list, by externalId, sorted. Time since a machine was last seen plays no part.
func StaleCandidates(active []string, listed map[string]bool) []string {
	var out []string
	for _, id := range active {
		if !listed[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Settings are the stale rules of one MachineSource, with the defaults applied.
type Settings struct {
	HoldPercent    int
	HoldMinimum    int
	RetireAfter    time.Duration
	PurgeAfter     time.Duration
	ReleaseHoldFor string
}

// SettingsOf reads spec.stale. An absent field takes its default: holdPercent 20,
// holdMinimum 10, retireAfter 720h, purgeAfter 2160h, releaseHoldFor "".
func SettingsOf(stale *v1alpha1.StaleSpec) (Settings, error) {
	s := Settings{HoldPercent: v1alpha1.DefaultHoldPercent, HoldMinimum: v1alpha1.DefaultHoldMinimum}
	retire, purge := v1alpha1.DefaultRetireAfter, v1alpha1.DefaultPurgeAfter
	if stale != nil {
		if stale.HoldPercent != nil {
			s.HoldPercent = int(*stale.HoldPercent)
		}
		if stale.HoldMinimum != nil {
			s.HoldMinimum = int(*stale.HoldMinimum)
		}
		if stale.RetireAfter != "" {
			retire = stale.RetireAfter
		}
		if stale.PurgeAfter != "" {
			purge = stale.PurgeAfter
		}
		s.ReleaseHoldFor = stale.ReleaseHoldFor
	}
	if s.HoldPercent < 1 || s.HoldPercent > 100 {
		return Settings{}, fmt.Errorf("spec.stale.holdPercent %d is not between 1 and 100", s.HoldPercent)
	}
	if s.HoldMinimum < 0 {
		return Settings{}, fmt.Errorf("spec.stale.holdMinimum %d is below 0", s.HoldMinimum)
	}
	var err error
	if s.RetireAfter, err = time.ParseDuration(retire); err != nil || s.RetireAfter <= 0 {
		return Settings{}, fmt.Errorf("spec.stale.retireAfter %q is not a positive duration", retire)
	}
	if s.PurgeAfter, err = time.ParseDuration(purge); err != nil || s.PurgeAfter <= 0 {
		return Settings{}, fmt.Errorf("spec.stale.purgeAfter %q is not a positive duration", purge)
	}
	return s, nil
}

// Trips is the threshold of rule 5: marking wouldMark of the active machines is
// more than HoldPercent of them and at least HoldMinimum machines.
func (s Settings) Trips(active, wouldMark int) bool {
	return wouldMark >= s.HoldMinimum && wouldMark*100 > s.HoldPercent*active
}

// Sweep is what one enumeration does to the machines it did not list.
type Sweep struct {
	Mark []string       // externalIds that become Stale
	Held *v1alpha1.Held // the hold to record, or nil when no sweep is held
}

// DecideSweep applies rules 3, 4 and 5 to one enumeration. active holds the
// externalIds of the source's Active machines and held the hold recorded so far.
//
//   - An enumeration that may not mark (rule 3) marks nothing and keeps the hold as it is.
//   - A sweep under the threshold marks every candidate and ends the hold.
//   - A sweep over the threshold marks nothing and is held. A hold that exists keeps its
//     enumerationId and its since; wouldMarkStale and live take the new numbers.
//   - When spec.stale.releaseHoldFor equals the enumerationId of the recorded hold, a
//     sweep over the threshold marks every candidate and ends the hold.
func DecideSweep(s Settings, e source.Enumeration, err error, active []string, held *v1alpha1.Held, now time.Time) Sweep {
	if !MayMarkStale(e, err) {
		return Sweep{Held: held}
	}
	listed := make(map[string]bool, len(e.Sightings))
	for _, sight := range e.Sightings {
		listed[sight.ExternalID] = true
	}
	candidates := StaleCandidates(active, listed)
	if !s.Trips(len(active), len(candidates)) {
		return Sweep{Mark: candidates}
	}
	if held != nil && s.ReleaseHoldFor != "" && s.ReleaseHoldFor == held.EnumerationID {
		return Sweep{Mark: candidates}
	}
	next := &v1alpha1.Held{EnumerationID: e.ID, Since: metav1.NewTime(now)}
	if held != nil {
		next.EnumerationID, next.Since = held.EnumerationID, held.Since
	}
	next.WouldMarkStale, next.Live = int32(len(candidates)), int32(len(active)) // #nosec G115 -- counts of objects in one namespace
	return Sweep{Held: next}
}

// Revive is rule 6. A Stale or Retired machine that a sighting lists again becomes
// Active; firstSeen is kept. It reports whether the phase changed.
func Revive(st *v1alpha1.MachineStatus) bool {
	if st.Phase != v1alpha1.PhaseStale && st.Phase != v1alpha1.PhaseRetired {
		return false
	}
	st.Phase, st.StaleSince = v1alpha1.PhaseActive, nil
	return true
}

// Targetable is rule 12: a machine with no primary address is never the target of a
// run. Class node is exempt, because node mode uses no address.
func Targetable(m *v1alpha1.Machine) bool {
	return m.Spec.Class == v1alpha1.ClassNode || m.Status.PrimaryAddress != ""
}

// Step is what the stale times ask for one machine.
type Step string

// The steps.
const (
	Keep   Step = "Keep"
	Retire Step = "Retire"
	Purge  Step = "Purge"
)

// Age applies the retire and purge times. A Stale machine is retired once it has been
// stale for RetireAfter; a Retired machine is deleted once RetireAfter plus PurgeAfter
// have passed since staleSince. The time returned is when the next step falls due;
// it is zero when no step will fall due (an Active machine, or no staleSince).
func Age(s Settings, st v1alpha1.MachineStatus, now time.Time) (Step, time.Time) {
	if st.StaleSince == nil {
		return Keep, time.Time{}
	}
	retireAt := st.StaleSince.Add(s.RetireAfter)
	purgeAt := retireAt.Add(s.PurgeAfter)
	switch st.Phase {
	case v1alpha1.PhaseStale:
		if !now.Before(retireAt) {
			if !now.Before(purgeAt) {
				return Purge, time.Time{}
			}
			return Retire, purgeAt
		}
		return Keep, retireAt
	case v1alpha1.PhaseRetired:
		if !now.Before(purgeAt) {
			return Purge, time.Time{}
		}
		return Keep, purgeAt
	}
	return Keep, time.Time{}
}
