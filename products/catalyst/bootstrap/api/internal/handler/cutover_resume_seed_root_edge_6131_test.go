// cutover_resume_seed_root_edge_6131_test.go — the #3646 durability
// replay must still replay, and must leave a ROOTED tree behind (#6131).
//
// This is the end-to-end control for #6131 at the seam that actually
// runs on a Sovereign: chrootSeedCutoverActivity -> projectCutoverResumeSeed
// -> ActivityBridge.SeedSteps. #6131 lets SeedSteps assert an empty
// DependsOn as "this node is the ROOT" instead of having it silently
// inherit the stored edge; if that were to weaken the replay, this test
// is where it would show, because every step here replays its terminal
// state out of the durable status ConfigMap exactly as it does after a
// completed cutover whose step ConfigMaps have been reaped.
package handler

import (
	"fmt"
	"sort"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// cutoverOrder6131 is the true execution sequence, from the
// `bp.openova.io/cutover-order` label on each cutover-step ConfigMap in
// platform/self-sovereign-cutover/chart/templates/.
var cutoverOrder6131 = []string{
	"gitea-mirror",              // 1
	"harbor-projects",           // 2
	"harbor-prewarm",            // 3
	"registry-pivot",            // 4
	"flux-gitrepository-patch",  // 5
	"helmrepository-patches",    // 6
	"catalyst-api-env-patch",    // 7
	"egress-block-test",         // 8
	"gitea-token-mint",          // 9
	"vcluster-registry-pivot",   // 10
	"crossplane-provider-pivot", // 11
}

// TestProjectCutoverResumeSeed_ReplaysAndLeavesARootedTree_6131 replays a
// COMPLETED cutover from the durable status record onto a store that was
// first seeded by the pre-#6099 alphabetical build, and requires both
// halves: every step's durable result still lands, AND the execution
// tree comes out acyclic and rooted at gitea-mirror.
func TestProjectCutoverResumeSeed_ReplaysAndLeavesARootedTree_6131(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h.jobs = js
	const depID = "dep-6131-resume"
	h.cutoverActivityDepID = depID

	// 1. Poison the store the way the pre-#6099 read path did: the
	//    sequence came from listStepNamesFromStatus, which ends in
	//    sort.Strings.
	alpha := append([]string(nil), cutoverOrder6131...)
	sort.Strings(alpha)
	// Control on the INPUT — if a step is ever renamed this stops
	// reproducing the hw293 store and must say so rather than pass.
	if got := predecessorOf6131(alpha, "gitea-mirror"); got != "flux-gitrepository-patch" {
		t.Fatalf("input control: alphabetical predecessor of gitea-mirror is %q, want flux-gitrepository-patch — this test no longer reproduces the hw293 store", got)
	}
	h.projectCutoverResumeSeed(alpha, map[string]string{})

	rootJob := jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror")
	poisoned := stepEdges6131(t, js, depID)
	wantStale := jobs.ActivityStepJobName(jobs.GroupCutover, "flux-gitrepository-patch")
	if got := poisoned[rootJob]; len(got) != 1 || got[0] != wantStale {
		t.Fatalf("setup: poisoned store should carry the stale alphabetical edge on %s, got %v want [%s]", rootJob, got, wantStale)
	}

	// 2. Replay a COMPLETED cutover the way a post-#6099 /jobs read does:
	//    the order comes from the cutover-order labels, the per-step
	//    results come from the durable status ConfigMap.
	status := map[string]string{"cutoverComplete": "true"}
	for _, slug := range cutoverOrder6131 {
		status["step."+slug+".result"] = "success"
		status["step."+slug+".startedAt"] = "2026-08-11T01:00:00Z"
		status["step."+slug+".finishedAt"] = "2026-08-11T01:05:00Z"
	}
	h.projectCutoverResumeSeed(cutoverOrder6131, status)

	// 3. The replay itself must still work — this is the case the
	//    DependsOn preservation exists to protect, so it is the control
	//    on the fix.
	all, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	byName := map[string]jobs.Job{}
	for _, j := range all {
		byName[j.JobName] = j
	}
	for _, slug := range cutoverOrder6131 {
		name := jobs.ActivityStepJobName(jobs.GroupCutover, slug)
		j, ok := byName[name]
		if !ok {
			t.Fatalf("durability replay lost step %s", name)
		}
		if j.Status != jobs.StatusSucceeded {
			t.Errorf("step %s replayed status = %q, want %q", name, j.Status, jobs.StatusSucceeded)
		}
		if j.LatestExecutionID == "" {
			t.Errorf("step %s replayed without an Execution — the Exec Log surface would be empty", name)
		}
	}
	if group, ok := byName[jobs.GroupCutover]; !ok {
		t.Fatal("durability replay lost the Cutover group")
	} else if group.Status != jobs.StatusSucceeded {
		t.Errorf("Cutover group rolled up to %q, want %q after an all-success replay", group.Status, jobs.StatusSucceeded)
	}

	// 4. And the tree it leaves behind is rooted and acyclic.
	edges := stepEdges6131(t, js, depID)
	for i, slug := range cutoverOrder6131 {
		name := jobs.ActivityStepJobName(jobs.GroupCutover, slug)
		got := edges[name]
		if i == 0 {
			if len(got) != 0 {
				t.Errorf("%s is cutover-order=1 and must be the ROOT, got dependsOn %v", name, got)
			}
			continue
		}
		want := jobs.ActivityStepJobName(jobs.GroupCutover, cutoverOrder6131[i-1])
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s dependsOn = %v, want [%s]", name, got, want)
		}
	}
	if err := acyclicUniqueRoot6131(edges, rootJob); err != nil {
		t.Errorf("rendered execution tree after a durable replay: %v", err)
	}
}

func predecessorOf6131(order []string, slug string) string {
	for i, s := range order {
		if s == slug && i > 0 {
			return order[i-1]
		}
	}
	return ""
}

func stepEdges6131(t *testing.T, js *jobs.Store, depID string) map[string][]string {
	t.Helper()
	all, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	edges := map[string][]string{}
	for _, j := range all {
		if j.Type == jobs.JobTypeGroup {
			continue
		}
		edges[j.JobName] = j.DependsOn
	}
	return edges
}

// acyclicUniqueRoot6131 mirrors the assertion in the jobs package: the
// rendered graph must be acyclic with exactly one root, and that root
// must be wantRoot. Cycle first, so a cyclic graph reports the cycle
// rather than the "0 roots" it also causes.
func acyclicUniqueRoot6131(edges map[string][]string, wantRoot string) error {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	names := make([]string, 0, len(edges))
	for n := range edges {
		names = append(names, n)
	}
	sort.Strings(names)
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
				continue
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
	roots := []string{}
	for n, deps := range edges {
		if len(deps) == 0 {
			roots = append(roots, n)
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
