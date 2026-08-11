// activity_bridge_root_edge_6131_test.go — the cutover execution tree
// must have a ROOT, and re-seeding must be able to say so (issue #6131).
//
// #6099 fixed the ORDER the eleven self-sovereign cutover steps are
// seeded in, and ten of the eleven edges corrected themselves on the
// next /jobs read. The eleventh — the root — did not, because the only
// way to express "this node has no predecessor" is an empty DependsOn,
// and mergeJob treats empty as "this write carries no edge information"
// and inherits the stored value instead. On hw293 that left
// cutover-step-gitea-mirror (bp.openova.io/cutover-order=1) pointing at
// cutover-step-flux-gitrepository-patch — its predecessor under the OLD
// alphabetical seed — closing a five-node cycle and denying the tree a
// root.
//
// These tests assert on EDGE VALUES and the resulting root, never on a
// count: a count passes on any permutation, which is exactly how the
// alphabetical tree shipped green in the first place.
package jobs

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

// cutoverStepOrder6131 is the true execution sequence, taken from the
// `bp.openova.io/cutover-order` label on each cutover-step ConfigMap in
// platform/self-sovereign-cutover/chart/templates/. That label is the
// authoritative source of order — the durable status ConfigMap's data
// is an unordered map and cannot carry a sequence at all.
var cutoverStepOrder6131 = []string{
	"gitea-mirror",              // cutover-order=1  (01-gitea-mirror-job.yaml)
	"harbor-projects",           // cutover-order=2  (02-harbor-projects-job.yaml)
	"harbor-prewarm",            // cutover-order=3  (03-harbor-prewarm-job.yaml)
	"registry-pivot",            // cutover-order=4  (04-registry-pivot-daemonset.yaml)
	"flux-gitrepository-patch",  // cutover-order=5  (05-flux-gitrepository-patch-job.yaml)
	"helmrepository-patches",    // cutover-order=6  (06-helmrepository-patches-job.yaml)
	"catalyst-api-env-patch",    // cutover-order=7  (07-catalyst-api-env-patch-job.yaml)
	"egress-block-test",         // cutover-order=8  (08-egress-block-test-job.yaml)
	"gitea-token-mint",          // cutover-order=9  (09-gitea-token-mint-job.yaml)
	"vcluster-registry-pivot",   // cutover-order=10 (10-vcluster-registry-pivot-job.yaml)
	"crossplane-provider-pivot", // cutover-order=11 (11-crossplane-provider-pivot-job.yaml)
}

// alphabeticalCutoverOrder6131 reproduces the pre-#6099 seed: the read
// path took its sequence from listStepNamesFromStatus, which ends in
// sort.Strings. Computed rather than hardcoded so a step rename makes
// the reproduction fail loudly instead of silently ceasing to reproduce.
func alphabeticalCutoverOrder6131() []string {
	out := append([]string(nil), cutoverStepOrder6131...)
	sort.Strings(out)
	return out
}

// seedCutoverSteps6131 seeds a store through ActivityBridge exactly the
// way projectCutoverResumeSeed does: full ordered set, Order = i+1.
func seedCutoverSteps6131(t *testing.T, st *Store, depID string, slugs []string) {
	t.Helper()
	steps := make([]ActivityStep, 0, len(slugs))
	for i, s := range slugs {
		steps = append(steps, ActivityStep{Slug: s, DisplayName: s, Order: i + 1})
	}
	ab := NewActivityBridge(st, depID, GroupCutover, "Cutover")
	if err := ab.SeedSteps(steps); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}
}

// cutoverEdges6131 reads the persisted dependsOn edges of the cutover
// step leaves back out of the store, keyed by JobName.
func cutoverEdges6131(t *testing.T, st *Store, depID string) map[string][]string {
	t.Helper()
	all, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	edges := map[string][]string{}
	for _, j := range all {
		if j.Type == JobTypeGroup {
			continue
		}
		edges[j.JobName] = j.DependsOn
	}
	return edges
}

// checkAcyclicUniqueRoot verifies that the rendered graph is acyclic and
// that wantRoot is its ONE root (the only node with no outgoing
// dependsOn edge). Returns an error rather than failing the test so the
// vacuity check below can prove the assertion is capable of failing.
func checkAcyclicUniqueRoot(edges map[string][]string, wantRoot string) error {
	// Cycle detection runs FIRST so a cyclic graph is reported as the
	// cycle it is. A cycle also starves the graph of roots, and reporting
	// "0 roots" would describe the symptom rather than the defect.
	//
	// Depth-first cycle hunt over the dependsOn edges.
	const (
		white = 0 // unvisited
		grey  = 1 // on the current stack
		black = 2 // fully explored
	)
	colour := make(map[string]int, len(edges))
	names := make([]string, 0, len(edges))
	for n := range edges {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic reporting
	var walk func(n string, stack []string) error
	walk = func(n string, stack []string) error {
		switch colour[n] {
		case black:
			return nil
		case grey:
			return fmt.Errorf("cycle through %v -> %s", stack, n)
		}
		colour[n] = grey
		for _, d := range edges[n] {
			if _, known := edges[d]; !known {
				continue // edge out of the projected set
			}
			if err := walk(d, append(stack, n)); err != nil {
				return err
			}
		}
		colour[n] = black
		return nil
	}
	for _, n := range names {
		if err := walk(n, nil); err != nil {
			return err
		}
	}

	roots := make([]string, 0, 1)
	for name, deps := range edges {
		if len(deps) == 0 {
			roots = append(roots, name)
		}
	}
	sort.Strings(roots)
	if len(roots) != 1 {
		return fmt.Errorf("graph has %d roots %v, want exactly 1 (%s)", len(roots), roots, wantRoot)
	}
	if roots[0] != wantRoot {
		return fmt.Errorf("root is %s, want %s", roots[0], wantRoot)
	}
	return nil
}

// TestActivityBridge_ReseedRepairsRootEdgeOnAPoisonedStore_6131 is the
// headline. A store first seeded under the pre-#6099 alphabetical build
// is re-seeded with the label-derived order — what every /jobs read does
// after #6099 — and every edge, including the root's, must end up
// correct.
func TestActivityBridge_ReseedRepairsRootEdgeOnAPoisonedStore_6131(t *testing.T) {
	st := newTestStore(t)
	const depID = "dep-6131"

	// Control on the INPUT: the reproduction is only a reproduction if
	// the alphabetical seed really does hand gitea-mirror the
	// flux-gitrepository-patch predecessor that was measured live on
	// hw293. If a step is ever renamed this fails loudly rather than
	// quietly ceasing to reproduce the defect.
	alpha := alphabeticalCutoverOrder6131()
	gotAlphaPred := ""
	for i, s := range alpha {
		if s == "gitea-mirror" && i > 0 {
			gotAlphaPred = alpha[i-1]
		}
	}
	if gotAlphaPred != "flux-gitrepository-patch" {
		t.Fatalf("input control: alphabetical predecessor of gitea-mirror is %q, want %q — this test no longer reproduces the hw293 store",
			gotAlphaPred, "flux-gitrepository-patch")
	}

	// 1. Poison the store the way the pre-#6099 build did.
	seedCutoverSteps6131(t, st, depID, alpha)
	poisoned := cutoverEdges6131(t, st, depID)
	rootJob := ActivityStepJobName(GroupCutover, "gitea-mirror")
	if got, want := poisoned[rootJob], []string{ActivityStepJobName(GroupCutover, "flux-gitrepository-patch")}; len(got) != 1 || got[0] != want[0] {
		t.Fatalf("setup: poisoned store should carry the stale alphabetical edge on %s, got %v want %v", rootJob, got, want)
	}

	// 2. Re-seed with the authoritative label order — the post-#6099
	//    read path, on a fresh catalyst-api process.
	seedCutoverSteps6131(t, st, depID, cutoverStepOrder6131)

	// 3. Assert the WHOLE edge map by value.
	edges := cutoverEdges6131(t, st, depID)
	for i, slug := range cutoverStepOrder6131 {
		name := ActivityStepJobName(GroupCutover, slug)
		got := edges[name]
		if i == 0 {
			if len(got) != 0 {
				t.Errorf("%s is cutover-order=1 and must be the ROOT, got dependsOn %v", name, got)
			}
			continue
		}
		want := ActivityStepJobName(GroupCutover, cutoverStepOrder6131[i-1])
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s dependsOn = %v, want [%s]", name, got, want)
		}
	}

	// 4. Cycle assertion: the rendered graph must be acyclic and
	//    gitea-mirror must be its unique root.
	if err := checkAcyclicUniqueRoot(edges, rootJob); err != nil {
		t.Errorf("rendered execution tree: %v", err)
	}
}

// TestCheckAcyclicUniqueRoot_VacuityOnThePoisonedGraph_6131 proves the
// assertion in step 4 above is capable of failing. Fed the exact graph
// measured on hw293 — every #6099-corrected edge plus the one surviving
// stale root edge — it must report the five-node cycle rather than pass.
func TestCheckAcyclicUniqueRoot_VacuityOnThePoisonedGraph_6131(t *testing.T) {
	edges := map[string][]string{}
	for i, slug := range cutoverStepOrder6131 {
		name := ActivityStepJobName(GroupCutover, slug)
		if i == 0 {
			edges[name] = nil
			continue
		}
		edges[name] = []string{ActivityStepJobName(GroupCutover, cutoverStepOrder6131[i-1])}
	}
	rootJob := ActivityStepJobName(GroupCutover, "gitea-mirror")

	// Sanity: corrected, this graph passes.
	if err := checkAcyclicUniqueRoot(edges, rootJob); err != nil {
		t.Fatalf("corrected graph should pass, got %v", err)
	}

	// Branch 1 — the CYCLE detector. Re-introduce the single surviving
	// stale edge and the graph is the hw293 one.
	cyclic := cloneEdges6131(edges)
	cyclic[rootJob] = []string{ActivityStepJobName(GroupCutover, "flux-gitrepository-patch")}
	err := checkAcyclicUniqueRoot(cyclic, rootJob)
	if err == nil {
		t.Fatal("vacuity check FAILED: the assertion passed on the known-bad hw293 graph, so it cannot catch this defect")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("vacuity check FAILED: the hw293 graph is a five-node cycle but the assertion reported %q — the cycle detector never fired", err)
	}
	t.Logf("vacuity ok (cycle branch) — assertion rejects the hw293 graph: %v", err)

	// Branch 2 — the ROOT-uniqueness check, which the cycle branch would
	// otherwise mask. An acyclic graph with a second root must also fail,
	// or "unique root" is a claim nothing tests.
	twoRoots := cloneEdges6131(edges)
	twoRoots[ActivityStepJobName(GroupCutover, "egress-block-test")] = nil
	err = checkAcyclicUniqueRoot(twoRoots, rootJob)
	if err == nil {
		t.Fatal("vacuity check FAILED: the assertion passed on an acyclic graph with TWO roots")
	}
	if !strings.Contains(err.Error(), "roots") {
		t.Fatalf("vacuity check FAILED: expected a root-count complaint, got %q", err)
	}
	t.Logf("vacuity ok (root branch) — assertion rejects a two-root graph: %v", err)
}

// TestActivityBridge_ReseedWithoutAnOrderBearingSourceStaysAcyclic_6131
// pins the one remaining case where the platform cannot know the true
// order: a completed cutover whose step ConfigMaps have been reaped, so
// listCutoverSteps returns nothing and the sequence can only come from
// the durable record — which is alphabetical by construction, because a
// ConfigMap's data is an unordered map. #6099 accepted that trade.
//
// The order is then wrong, and nothing here can fix it. But the tree must
// still be a TREE. Before #6131 this case produced a CYCLE: the ten
// non-root steps were rewritten alphabetically while the previously-
// correct root kept its old edge, because that edge was the empty list
// the preservation refused to overwrite. So this is a case #6131 quietly
// improves, and it is asserted rather than assumed.
func TestActivityBridge_ReseedWithoutAnOrderBearingSourceStaysAcyclic_6131(t *testing.T) {
	st := newTestStore(t)
	const depID = "dep-6131-no-order-source"

	// A correctly-ordered store, from when the ConfigMaps still existed.
	seedCutoverSteps6131(t, st, depID, cutoverStepOrder6131)
	if err := checkAcyclicUniqueRoot(cutoverEdges6131(t, st, depID),
		ActivityStepJobName(GroupCutover, "gitea-mirror")); err != nil {
		t.Fatalf("setup: correctly-ordered store should already be a rooted tree: %v", err)
	}

	// The ConfigMaps are reaped; a later read can only offer the durable
	// record's alphabetical list.
	alpha := alphabeticalCutoverOrder6131()
	seedCutoverSteps6131(t, st, depID, alpha)

	edges := cutoverEdges6131(t, st, depID)
	// The order is wrong — that is known and accepted — but the graph is
	// a tree, rooted at whatever the only available source put first.
	if err := checkAcyclicUniqueRoot(edges, ActivityStepJobName(GroupCutover, alpha[0])); err != nil {
		t.Fatalf("re-seed with no order-bearing source must still leave a rooted, acyclic tree: %v", err)
	}
}

func cloneEdges6131(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// TestMergeJob_UpdateWithNoEdgeInformationStillInherits_6131 is the
// CONTROL that shares the suspect property: an update whose DependsOn is
// empty because the writer simply does not carry edge information must
// still inherit the stored edges. This is the #1467 / prov #73 durability
// case — the helmwatch bridge's per-event transition path writes
// DependsOn: []string{} on every state change, and without inheritance
// all 135 install Jobs flattened to dependsOn=[] and the canvas lost its
// edges. Whatever #6131 ships must NOT re-open that.
func TestMergeJob_UpdateWithNoEdgeInformationStillInherits_6131(t *testing.T) {
	st := newTestStore(t)
	const depID = "dep-6131-durability"

	seed := Job{
		DeploymentID: depID,
		JobName:      "install-cert-manager",
		AppID:        "cert-manager",
		Type:         JobTypeInstall,
		ParentID:     JobID(depID, GroupBootstrapKit),
		DependsOn:    []string{"install-cilium"},
		Status:       StatusPending,
	}
	if err := st.UpsertJob(seed); err != nil {
		t.Fatalf("seed UpsertJob: %v", err)
	}

	// The per-event transition shape: same Job, new status, no edge
	// information at all.
	transition := Job{
		DeploymentID: depID,
		JobName:      "install-cert-manager",
		AppID:        "cert-manager",
		Type:         JobTypeInstall,
		ParentID:     JobID(depID, GroupBootstrapKit),
		DependsOn:    []string{},
		Status:       StatusRunning,
	}
	if err := st.UpsertJob(transition); err != nil {
		t.Fatalf("transition UpsertJob: %v", err)
	}

	got, _, err := st.GetJob(depID, JobID(depID, "install-cert-manager"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if len(got.DependsOn) != 1 || got.DependsOn[0] != "install-cilium" {
		t.Fatalf("a transition carrying no edge information erased the seeded edge: dependsOn = %v, want [install-cilium] (#1467 prov #73 regression)",
			got.DependsOn)
	}
	if got.Status != StatusRunning {
		t.Fatalf("status = %q, want %q — the transition itself must still land", got.Status, StatusRunning)
	}
}

// TestActivityBridge_LazyStepTransitionDoesNotEraseSeededEdges_6131 is
// the same control one layer up, on the path #6131 must leave alone: a
// transition arriving for a step the bridge never declared registers it
// lazily against a PARTIAL in-memory step set, so its computed dependsOn
// is meaningless. It must inherit, not overwrite.
func TestActivityBridge_LazyStepTransitionDoesNotEraseSeededEdges_6131(t *testing.T) {
	st := newTestStore(t)
	const depID = "dep-6131-lazy"

	seedCutoverSteps6131(t, st, depID, cutoverStepOrder6131)

	// A brand-new bridge (fresh Pod) that has NOT been seeded receives a
	// transition for a mid-chain step. Its in-memory set holds one entry,
	// so dependsOnForStepLocked computes "no predecessor" — which is
	// wrong, and must not be written.
	fresh := NewActivityBridge(st, depID, GroupCutover, "Cutover")
	if err := fresh.StartStep(ActivityStep{Slug: "egress-block-test", DisplayName: "Egress Block Test", Order: 8},
		"resumed without a seed", time.Now().UTC()); err != nil {
		t.Fatalf("StartStep: %v", err)
	}

	edges := cutoverEdges6131(t, st, depID)
	name := ActivityStepJobName(GroupCutover, "egress-block-test")
	want := ActivityStepJobName(GroupCutover, "catalyst-api-env-patch")
	if got := edges[name]; len(got) != 1 || got[0] != want {
		t.Fatalf("an unseeded transition erased the seeded edge on %s: dependsOn = %v, want [%s]", name, got, want)
	}
	// And the tree still has its root.
	if err := checkAcyclicUniqueRoot(edges, ActivityStepJobName(GroupCutover, "gitea-mirror")); err != nil {
		t.Fatalf("rendered execution tree after a lazy transition: %v", err)
	}
}
