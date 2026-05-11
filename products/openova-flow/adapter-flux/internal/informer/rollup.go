// rollup.go — status rollup for synthetic group nodes (region root,
// phase columns). Per the OpenovaFlow brief, group nodes are
// domain-agnostic: their status is derived by worst-of-children walk.
//
// Worst-of ordering (highest precedence first):
//
//	failed > running > pending > succeeded
//
// Canonical pattern reference: same ordering as the existing
// catalyst-api status-projection used by the Sovereign Console
// progress widget (products/catalyst/bootstrap/api/internal/projector
// — worst-status wins for parent-rollup of HelmRelease groups). The
// adapter's rollup is the same worst-of so the canvas's group status
// matches what an operator sees in the Console.
package informer

import (
	"sort"
	"sync"
)

// Status palette ordering (lowest → highest precedence). Higher index
// wins in worst-of rollup.
var rollupPalette = []string{"succeeded", "pending", "running", "failed"}

func rollupRank(s string) int {
	for i, v := range rollupPalette {
		if v == s {
			return i
		}
	}
	// Unknown statuses default to "running" precedence — keeps a noisy
	// upstream from accidentally bubbling up as "succeeded".
	return rollupRank("running")
}

// RollupStatus — worst-of across the supplied child statuses. Empty
// input collapses to "pending" (the group has been declared but has
// no children yet — the operator should see it as not-yet-started).
func RollupStatus(children []string) string {
	if len(children) == 0 {
		return "pending"
	}
	worst := -1
	out := "pending"
	for _, s := range children {
		r := rollupRank(s)
		if r > worst {
			worst = r
			out = s
			if s == "" {
				out = "pending"
			}
		}
	}
	if worst < 0 {
		return "pending"
	}
	// Normalize unknown strings to "running" so the returned value is
	// from the palette.
	if rollupPalette[worst] != out {
		return rollupPalette[worst]
	}
	return out
}

// StatusTracker — concurrent-safe map from group ID → set of child
// statuses keyed by child node ID. The informer calls Record on every
// HR upsert and Forget on every HR delete; Rollup reads the snapshot
// to compute the group's worst-of.
//
// One tracker instance covers ALL group nodes (region + phases) — the
// group ID is the lookup key.
type StatusTracker struct {
	mu       sync.Mutex
	children map[string]map[string]string // groupID → childID → status
}

// NewStatusTracker — fresh tracker.
func NewStatusTracker() *StatusTracker {
	return &StatusTracker{children: map[string]map[string]string{}}
}

// Record — note that childID is a member of groupID with the given
// status. Idempotent.
func (t *StatusTracker) Record(groupID, childID, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.children[groupID]
	if !ok {
		g = map[string]string{}
		t.children[groupID] = g
	}
	g[childID] = status
}

// Forget — drop a child from a group (called on HR delete).
func (t *StatusTracker) Forget(groupID, childID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if g, ok := t.children[groupID]; ok {
		delete(g, childID)
		if len(g) == 0 {
			delete(t.children, groupID)
		}
	}
}

// Rollup — current rolled-up status for groupID. Returns "pending" if
// the group has no recorded children yet.
func (t *StatusTracker) Rollup(groupID string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.children[groupID]
	if !ok || len(g) == 0 {
		return "pending"
	}
	statuses := make([]string, 0, len(g))
	for _, s := range g {
		statuses = append(statuses, s)
	}
	// Deterministic order for the rollup function — RollupStatus
	// doesn't actually depend on order, but tests rely on a stable
	// pass-through.
	sort.Strings(statuses)
	return RollupStatus(statuses)
}

// Groups — currently-tracked group IDs. Used by the informer to know
// which synthetic parents need a re-emit after a child status change.
func (t *StatusTracker) Groups() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.children))
	for k := range t.children {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
