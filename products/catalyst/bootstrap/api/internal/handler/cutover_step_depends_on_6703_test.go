package handler

// Epic #6703 P4 — the cutover step contract gains a declared dependsOn DAG.
//
// The chart may annotate each cutover-step-NN-<slug> ConfigMap with
// `bp.openova.io/cutover-depends-on: "<stepName>,<stepName>"`. These tests pin
// the three halves of the contract on the catalyst-api side:
//
//   1. parse — absent annotation is "undeclared" (nil), present-but-empty is
//      a declared root ([]), a list is trimmed/de-duplicated;
//   2. validate — listCutoverSteps REFUSES a declaration the sequential
//      engine cannot honour (a dependency that runs later, an unknown step,
//      a self reference), the same way it refuses a malformed podSpec;
//   3. project — the declared edges reach the jobs store BY VALUE through
//      the same read-path seed chrootSeedCutoverActivity uses, with the
//      durable-only tail still chained after the order-bearing steps.

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// withDependsOn stamps the dependsOn annotation on a step ConfigMap fixture.
func withDependsOn(cm *corev1.ConfigMap, value string) *corev1.ConfigMap {
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}
	cm.Annotations[cutoverStepDependsOnAnnotation] = value
	return cm
}

func TestParseCutoverStep_DependsOnAnnotation_6703(t *testing.T) {
	// Absent → undeclared (nil), so the bridge chains on order.
	step, err := parseCutoverStep(*makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if step.dependsOn != nil {
		t.Fatalf("absent annotation must parse as nil (undeclared), got %#v", step.dependsOn)
	}

	// Present but empty → a declared ROOT: non-nil, zero entries.
	step, err = parseCutoverStep(*withDependsOn(makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""), ""))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if step.dependsOn == nil || len(step.dependsOn) != 0 {
		t.Fatalf("empty annotation must parse as a declared root ([]), got %#v", step.dependsOn)
	}

	// A list → trimmed, blanks dropped, duplicates collapsed, order kept.
	step, err = parseCutoverStep(*withDependsOn(makeCutoverStepCM("cutover-step-05-flux", "flux-gitrepository-patch", 5, cutoverModeJob, minimalPodSpecYAML, ""),
		" gitea-mirror , registry-pivot,, gitea-mirror "))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := []string{"gitea-mirror", "registry-pivot"}; !reflect.DeepEqual(step.dependsOn, want) {
		t.Fatalf("dependsOn = %#v, want %#v", step.dependsOn, want)
	}
}

func TestListCutoverSteps_AcceptsADeclaredDAGConsistentWithOrder_6703(t *testing.T) {
	objs := []k8sruntime.Object{
		withDependsOn(makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""), ""),
		withDependsOn(makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""), ""),
		withDependsOn(makeCutoverStepCM("cutover-step-03-harbor-prewarm", "harbor-prewarm", 3, cutoverModeJob, minimalPodSpecYAML, ""), "harbor-projects"),
		withDependsOn(makeCutoverStepCM("cutover-step-05-flux", "flux-gitrepository-patch", 5, cutoverModeJob, minimalPodSpecYAML, ""), "gitea-mirror,harbor-prewarm"),
		// Undeclared steps may coexist with declared ones.
		makeCutoverStepCM("cutover-step-04-registry-pivot", "registry-pivot", 4, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	_, client := fakeHandlerWithCutover(t, objs...)
	steps, err := listCutoverSteps(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS})
	if err != nil {
		t.Fatalf("listCutoverSteps: %v", err)
	}
	got := map[string][]string{}
	for _, s := range steps {
		got[s.stepName] = s.dependsOn
	}
	want := map[string][]string{
		"gitea-mirror":             {},
		"harbor-projects":          {},
		"harbor-prewarm":           {"harbor-projects"},
		"registry-pivot":           nil,
		"flux-gitrepository-patch": {"gitea-mirror", "harbor-prewarm"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependsOn per step\n got: %#v\nwant: %#v", got, want)
	}
}

func TestListCutoverSteps_RefusesDeclarationsTheEngineCannotHonour_6703(t *testing.T) {
	cases := []struct {
		name    string
		objs    []k8sruntime.Object
		wantErr string
	}{
		{
			name: "dependency that runs later",
			objs: []k8sruntime.Object{
				withDependsOn(makeCutoverStepCM("cutover-step-01-a", "a", 1, cutoverModeJob, minimalPodSpecYAML, ""), "b"),
				makeCutoverStepCM("cutover-step-02-b", "b", 2, cutoverModeJob, minimalPodSpecYAML, ""),
			},
			wantErr: "does not run before it",
		},
		{
			name: "unknown step",
			objs: []k8sruntime.Object{
				makeCutoverStepCM("cutover-step-01-a", "a", 1, cutoverModeJob, minimalPodSpecYAML, ""),
				withDependsOn(makeCutoverStepCM("cutover-step-02-b", "b", 2, cutoverModeJob, minimalPodSpecYAML, ""), "a,ghost"),
			},
			wantErr: "unknown step \"ghost\"",
		},
		{
			name: "self reference",
			objs: []k8sruntime.Object{
				withDependsOn(makeCutoverStepCM("cutover-step-01-a", "a", 1, cutoverModeJob, minimalPodSpecYAML, ""), "a"),
			},
			wantErr: "dependency on itself",
		},
		{
			name: "same order is not before",
			objs: []k8sruntime.Object{
				makeCutoverStepCM("cutover-step-01-a", "a", 1, cutoverModeJob, minimalPodSpecYAML, ""),
				withDependsOn(makeCutoverStepCM("cutover-step-01-b", "b", 1, cutoverModeJob, minimalPodSpecYAML, ""), "a"),
			},
			wantErr: "does not run before it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, client := fakeHandlerWithCutover(t, tc.objs...)
			_, err := listCutoverSteps(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS})
			if err == nil {
				t.Fatalf("listCutoverSteps must refuse: %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The end-to-end: the read-path seed projects the declared DAG into the
// jobs store, by value, and a step known only from the durable record
// still projects — chained after the last order-bearing step.
func TestProjectCutoverResumeSeedSteps_DeclaredEdgesReachTheStore_6703(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h.jobs = js
	const depID = "dep-6703-dag"
	h.cutoverActivityDepID = depID

	ordered := []cutoverStep{
		{order: 1, stepName: "gitea-mirror", dependsOn: []string{}},
		{order: 2, stepName: "harbor-projects", dependsOn: []string{}},
		{order: 3, stepName: "harbor-prewarm", dependsOn: []string{"harbor-projects"}},
		{order: 4, stepName: "registry-pivot", dependsOn: []string{"harbor-prewarm"}},
		{order: 5, stepName: "flux-gitrepository-patch", dependsOn: []string{"gitea-mirror", "registry-pivot"}},
	}
	// The durable record also remembers a step whose ConfigMap is gone.
	durable := mergeCutoverStepOrder(stepNamesFromSteps(ordered), []string{"egress-block-test", "gitea-mirror"})
	steps := activityStepsFromCutover(ordered, durable)

	if got := len(steps); got != 6 {
		t.Fatalf("activityStepsFromCutover: %d steps, want 6 (5 ordered + 1 durable-only)", got)
	}
	if steps[5].Slug != "egress-block-test" || steps[5].Order != 6 || steps[5].DependsOn != nil {
		t.Fatalf("durable-only step must trail with Order 6 and nil DependsOn, got %+v", steps[5])
	}

	h.projectCutoverResumeSeedSteps(steps, map[string]string{})

	all, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	got := map[string][]string{}
	for _, j := range all {
		if j.Kind != jobs.KindStep {
			continue
		}
		deps := append([]string{}, j.DependsOn...) // never nil: a root compares as {}
		sort.Strings(deps)
		got[j.JobName] = deps
	}
	n := func(slug string) string { return jobs.ActivityStepJobName(jobs.GroupCutover, slug) }
	want := map[string][]string{
		n("gitea-mirror"):             {},
		n("harbor-projects"):          {}, // a declared root — the linear chain would have drawn gitea-mirror here
		n("harbor-prewarm"):           {n("harbor-projects")},
		n("registry-pivot"):           {n("harbor-prewarm")},
		n("flux-gitrepository-patch"): {n("gitea-mirror"), n("registry-pivot")},
		n("egress-block-test"):        {n("flux-gitrepository-patch")}, // durable-only: chained after the last ordered step
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projected edges\n got: %v\nwant: %v", got, want)
	}
}

// Control: with NO annotation on any ConfigMap the projection is exactly the
// pre-P4 linear chain — the contract change is invisible to a chart that has
// not opted in (the live hw307 chart 0.1.202 today).
func TestProjectCutoverResumeSeedSteps_UndeclaredChartKeepsTheChain_6703(t *testing.T) {
	h, _ := fakeHandlerWithCutover(t)
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h.jobs = js
	const depID = "dep-6703-chain"
	h.cutoverActivityDepID = depID

	ordered := make([]cutoverStep, 0, len(theElevenStepsInChartOrder))
	for i, name := range theElevenStepsInChartOrder {
		ordered = append(ordered, cutoverStep{order: i + 1, stepName: name})
	}
	h.projectCutoverResumeSeedSteps(activityStepsFromCutover(ordered, nil), map[string]string{})

	all, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	byName := map[string][]string{}
	for _, j := range all {
		if j.Kind == jobs.KindStep {
			byName[j.JobName] = j.DependsOn
		}
	}
	n := func(slug string) string { return jobs.ActivityStepJobName(jobs.GroupCutover, slug) }
	for i, name := range theElevenStepsInChartOrder {
		got := byName[n(name)]
		if i == 0 {
			if len(got) != 0 {
				t.Fatalf("%s must be the root, got %v", name, got)
			}
			continue
		}
		if want := n(theElevenStepsInChartOrder[i-1]); len(got) != 1 || got[0] != want {
			t.Fatalf("%s dependsOn = %v, want [%s] (linear chain)", name, got, want)
		}
	}
}
