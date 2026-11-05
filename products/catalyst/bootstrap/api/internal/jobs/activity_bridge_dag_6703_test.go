// activity_bridge_dag_6703_test.go — the step contract is a dependsOn DAG,
// not a linear order (epic #6703 P4).
//
// Before: ActivityStep carried only Order and the bridge FABRICATED one edge
// per step to its predecessor. That drew eleven cutover edges of which
// several asserted a dependency no step has (harbor-projects does not need
// gitea-mirror) — the same "plausible tree, wrong at the edge level" class
// #6093 killed for the alphabetical sort.
//
// After: a step may DECLARE its upstream set. Declared wins; declared-empty
// is a root; undeclared (nil) keeps the linear predecessor so sources that
// only know a sequence are unchanged. Forward, unknown and self references
// are dropped, so whatever a source declares the projected graph is acyclic
// and every edge is one the sequential engine honours.
package jobs

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// stepEdges reads the projected cutover-step rows back out of the store
// and returns JobName -> DependsOn (sorted for stable comparison).
func stepEdges(t *testing.T, st *Store, depID string) map[string][]string {
	t.Helper()
	all, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	out := map[string][]string{}
	for _, j := range all {
		if j.Kind != KindStep {
			continue
		}
		deps := append([]string{}, j.DependsOn...) // never nil: a root compares as {}
		sort.Strings(deps)
		out[j.JobName] = deps
	}
	return out
}

func stepName(slug string) string { return ActivityStepJobName(GroupCutover, slug) }

// The headline: declared edges are projected BY VALUE — exactly the declared
// set, nothing fabricated from Order — and read back from the store.
func TestActivityBridge_DeclaredDependsOnProjectsTheDAGNotTheChain_6703(t *testing.T) {
	st, ab, depID := newActivityFixture(t)

	// A diamond: mirror and projects are independent roots; prewarm needs
	// projects; pivot needs prewarm; patch needs BOTH mirror and pivot.
	// Execution order is 1..5 (a valid topological order), but a linear
	// chain would have drawn projects->mirror and patch->pivot only.
	steps := []ActivityStep{
		{Slug: "gitea-mirror", Order: 1, DependsOn: []string{}},
		{Slug: "harbor-projects", Order: 2, DependsOn: []string{}},
		{Slug: "harbor-prewarm", Order: 3, DependsOn: []string{"harbor-projects"}},
		{Slug: "registry-pivot", Order: 4, DependsOn: []string{"harbor-prewarm"}},
		{Slug: "flux-gitrepository-patch", Order: 5, DependsOn: []string{"gitea-mirror", "registry-pivot"}},
	}
	if err := ab.SeedSteps(steps); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}
	got := stepEdges(t, st, depID)
	want := map[string][]string{
		stepName("gitea-mirror"):             {},
		stepName("harbor-projects"):          {}, // NOT gitea-mirror — a declared root
		stepName("harbor-prewarm"):           {stepName("harbor-projects")},
		stepName("registry-pivot"):           {stepName("harbor-prewarm")},
		stepName("flux-gitrepository-patch"): {stepName("gitea-mirror"), stepName("registry-pivot")}, // TWO edges
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected edges\n got: %v\nwant: %v", got, want)
	}
}

// Undeclared steps keep the pre-P4 contract: the linear predecessor. Mixed
// sets resolve per step, so a source that declares only some steps gets the
// declared edges for those and the chain for the rest.
func TestActivityBridge_UndeclaredStepsStillChainOnOrder_6703(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	steps := []ActivityStep{
		{Slug: "a", Order: 1},                                // nil → root (first)
		{Slug: "b", Order: 2},                                // nil → a
		{Slug: "c", Order: 3, DependsOn: []string{"a"}},      // declared → a only
		{Slug: "d", Order: 4},                                // nil → c (predecessor in ORDER, not in DAG)
		{Slug: "e", Order: 5, DependsOn: []string{"b", "d"}}, // declared
	}
	if err := ab.SeedSteps(steps); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}
	got := stepEdges(t, st, depID)
	want := map[string][]string{
		stepName("a"): {},
		stepName("b"): {stepName("a")},
		stepName("c"): {stepName("a")},
		stepName("d"): {stepName("c")},
		stepName("e"): {stepName("b"), stepName("d")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected edges\n got: %v\nwant: %v", got, want)
	}
}

// A declaration the sequential engine cannot honour is not drawn: forward
// references (a step that runs LATER), unknown slugs and self references are
// dropped, duplicates collapse, and the result stays acyclic.
func TestActivityBridge_InvalidDeclarationsAreDroppedAndTheGraphStaysAcyclic_6703(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	steps := []ActivityStep{
		{Slug: "a", Order: 1, DependsOn: []string{"c"}},                    // forward ref → dropped → root
		{Slug: "b", Order: 2, DependsOn: []string{"b", "ghost", "a", "a"}}, // self + unknown + dup → [a]
		{Slug: "c", Order: 3, DependsOn: []string{" a ", "b"}},             // whitespace tolerated
	}
	if err := ab.SeedSteps(steps); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}
	got := stepEdges(t, st, depID)
	want := map[string][]string{
		stepName("a"): {},
		stepName("b"): {stepName("a")},
		stepName("c"): {stepName("a"), stepName("b")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected edges\n got: %v\nwant: %v", got, want)
	}
	if err := checkAcyclicUniqueRoot(got, stepName("a")); err != nil {
		t.Fatalf("graph must be acyclic with a as the single root: %v", err)
	}
}

// Re-seeding with a DECLARED set repairs a store that was first seeded with
// the linear chain — including turning a chained step back into a root,
// which only an authoritative write can do (#6131).
func TestActivityBridge_ReseedWithDeclaredDAGRepairsAChainedStore_6703(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	linear := []ActivityStep{
		{Slug: "gitea-mirror", Order: 1},
		{Slug: "harbor-projects", Order: 2},
		{Slug: "harbor-prewarm", Order: 3},
	}
	if err := ab.SeedSteps(linear); err != nil {
		t.Fatalf("SeedSteps(linear): %v", err)
	}
	before := stepEdges(t, st, depID)
	if got := before[stepName("harbor-projects")]; len(got) != 1 || got[0] != stepName("gitea-mirror") {
		t.Fatalf("setup: linear seed should chain harbor-projects on gitea-mirror, got %v", got)
	}

	declared := []ActivityStep{
		{Slug: "gitea-mirror", Order: 1, DependsOn: []string{}},
		{Slug: "harbor-projects", Order: 2, DependsOn: []string{}},
		{Slug: "harbor-prewarm", Order: 3, DependsOn: []string{"harbor-projects"}},
	}
	if err := ab.SeedSteps(declared); err != nil {
		t.Fatalf("SeedSteps(declared): %v", err)
	}
	after := stepEdges(t, st, depID)
	if got := after[stepName("harbor-projects")]; len(got) != 0 {
		t.Fatalf("harbor-projects must be a root after the declared re-seed, still carries %v", got)
	}
	if got := after[stepName("harbor-prewarm")]; len(got) != 1 || got[0] != stepName("harbor-projects") {
		t.Fatalf("harbor-prewarm edge = %v, want [%s]", got, stepName("harbor-projects"))
	}
}

// A step transition arriving AFTER the declared seed must not erase the
// declared edges: StartStep/FinishStep recompute DependsOn from the same
// registered set, so they re-assert the declaration rather than the chain.
func TestActivityBridge_TransitionsKeepDeclaredEdges_6703(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	declared := []ActivityStep{
		{Slug: "a", Order: 1, DependsOn: []string{}},
		{Slug: "b", Order: 2, DependsOn: []string{}},
		{Slug: "c", Order: 3, DependsOn: []string{"a", "b"}},
	}
	if err := ab.SeedSteps(declared); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}
	// The engine re-emits the step with its declaration (activityStepFromCutover
	// passes it through) — but even a bare transition must not regress.
	if err := ab.StartStep(ActivityStep{Slug: "b", Order: 2}, "", time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	if err := ab.FinishStep(ActivityStep{Slug: "b", Order: 2}, ActivityResultSuccess, "", time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("FinishStep: %v", err)
	}
	if err := ab.StartStep(ActivityStep{Slug: "c", Order: 3, DependsOn: []string{"a", "b"}}, "", time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("StartStep(c): %v", err)
	}
	got := stepEdges(t, st, depID)
	if deps := got[stepName("b")]; len(deps) != 0 {
		t.Fatalf("b must stay a declared root across its transitions, got %v", deps)
	}
	if deps := got[stepName("c")]; !reflect.DeepEqual(deps, []string{stepName("a"), stepName("b")}) {
		t.Fatalf("c must keep both declared edges, got %v", deps)
	}
}
