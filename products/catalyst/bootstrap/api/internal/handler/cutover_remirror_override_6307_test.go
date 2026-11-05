// cutover_remirror_override_6307_test.go — #6307 guards.
//
// The deadlock (live hw296, dep e689e3b34a75fdec, 2026-08-14): step-05 had
// pointed Flux at the local Gitea, step-06 terminal-failed, and the fix for
// step-06 was published upstream as chart 0.1.187. The Sovereign could not
// see it — its only delivery path is the local Gitea, the only thing that
// re-syncs that Gitea is step-01, and the #5437 suppression refused to re-run
// step-01 because the chain was past the pivot boundary. An operator /start
// re-ran step-06 with the old script, forever.
//
// The override: an operator may re-fire `POST /start?remirror=true`. It is
// honoured only while no step PAST the boundary has produced durable work (a
// recorded success, a Complete Job, or a Job still in flight) — because that
// is the only state in which the force-push would revert something. The
// bounded cascade still keeps the deny-egress hold from repeating, and
// harbor-prewarm re-running is what keeps the #5437 pin-drift guarantee.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

const (
	stepHelmRepoPatches  = "helmrepository-patches"
	stepCatalystAPIPatch = "catalyst-api-env-patch"
)

// hw296Chain is the shipped chain trimmed to the steps the deadlock involves.
func hw296Chain(t *testing.T) []cutoverStep {
	t.Helper()
	return []cutoverStep{
		{stepName: cutoverStepGiteaMirror, order: 1, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: "harbor-projects", order: 2, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: cutoverStepHarborPrewarm, order: 3, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: cutoverStepFluxGitRepoPatch, order: 5, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: stepHelmRepoPatches, order: 6, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
		{stepName: stepCatalystAPIPatch, order: 7, mode: cutoverModeJob, podSpec: minimalPodSpec(t)},
	}
}

// hw296DeadlockStatus is the durable record at the moment of the deadlock:
// steps 1-5 succeeded on a prior attempt, step 6 failed, step 7 never ran.
func hw296DeadlockStatus(at time.Time) map[string]string {
	ts := at.Format(time.RFC3339)
	return map[string]string{
		"cutoverStartedAt":                                ts,
		"cutoverLastAttemptStartedAt":                     ts,
		"failedStep":                                      stepHelmRepoPatches,
		"step." + cutoverStepGiteaMirror + ".result":      "success",
		"step." + cutoverStepGiteaMirror + ".finishedAt":  at.Add(2 * time.Minute).Format(time.RFC3339),
		"step.harbor-projects.result":                     "success",
		"step." + cutoverStepHarborPrewarm + ".result":    "success",
		"step." + cutoverStepFluxGitRepoPatch + ".result": "success",
		"step." + stepHelmRepoPatches + ".result":         "failed",
	}
}

func TestRemirrorBlockedBy(t *testing.T) {
	chain := hw296Chain(t)
	none := func(string) bool { return false }

	t.Run("nothing past the boundary succeeded: not blocked", func(t *testing.T) {
		if got := remirrorBlockedBy(chain, hw296DeadlockStatus(time.Now()), 1, none); got != "" {
			t.Errorf("blocked by %q; want unblocked — step-06 failed and step-07 never ran", got)
		}
	})
	t.Run("the boundary step's own success is never a blocker", func(t *testing.T) {
		st := hw296DeadlockStatus(time.Now())
		if got := remirrorBlockedBy(chain, st, 1, none); got != "" {
			t.Errorf("blocked by %q; flux-gitrepository-patch writes no commits", got)
		}
	})
	t.Run("a commit-writing step's recorded success blocks", func(t *testing.T) {
		st := hw296DeadlockStatus(time.Now())
		st["step."+stepHelmRepoPatches+".result"] = "success"
		st["step."+stepCatalystAPIPatch+".result"] = "failed"
		if got := remirrorBlockedBy(chain, st, 1, none); got != stepHelmRepoPatches {
			t.Errorf("blocked by %q; want %s", got, stepHelmRepoPatches)
		}
	})
	t.Run("a Complete or still-running Job past the boundary blocks even with a blank row", func(t *testing.T) {
		live := func(name string) bool { return name == stepCatalystAPIPatch }
		if got := remirrorBlockedBy(chain, hw296DeadlockStatus(time.Now()), 1, live); got != stepCatalystAPIPatch {
			t.Errorf("blocked by %q; want %s", got, stepCatalystAPIPatch)
		}
	})
	t.Run("an unanswerable probe fails closed", func(t *testing.T) {
		if got := remirrorBlockedBy(chain, hw296DeadlockStatus(time.Now()), 1, func(string) bool { return true }); got == "" {
			t.Error("want blocked when the Job probe cannot rule out durable work")
		}
	})
}

func TestDecideSnapshotRerun_6307_OperatorOverride(t *testing.T) {
	chain := hw296Chain(t)
	mirror := chain[0]
	now := time.Now().UTC()
	st := hw296DeadlockStatus(now.Add(-3 * time.Hour))
	none := func(string) bool { return false }

	t.Run("without the override the suppression holds and names the escape", func(t *testing.T) {
		d := decideSnapshotRerun(mirror, chain, st, now, none)
		if d.rerun || !d.suppressed {
			t.Fatalf("want suppressed; got %+v", d)
		}
		if !strings.Contains(d.reason, "remirror=true") {
			t.Errorf("the suppression notice must tell the operator about the override; got %q", d.reason)
		}
	})
	t.Run("with the override and no durable work past the boundary the snapshot is re-taken", func(t *testing.T) {
		d := decideSnapshotRerunWithOverride(mirror, chain, st, now, none, &remirrorOverride{stepHasLiveOrCompleteJob: none})
		if !d.rerun {
			t.Fatalf("want rerun; got %+v", d)
		}
		if !strings.Contains(d.reason, "#6307") {
			t.Errorf("reason must cite the override; got %q", d.reason)
		}
	})
	t.Run("with the override but a succeeded commit-writer the request is refused", func(t *testing.T) {
		blocked := hw296DeadlockStatus(now.Add(-3 * time.Hour))
		blocked["step."+stepHelmRepoPatches+".result"] = "success"
		blocked["failedStep"] = stepCatalystAPIPatch
		d := decideSnapshotRerunWithOverride(mirror, chain, blocked, now, none, &remirrorOverride{stepHasLiveOrCompleteJob: none})
		if d.rerun || !d.suppressed {
			t.Fatalf("want refused; got %+v", d)
		}
		if !strings.Contains(d.reason, "refused") || !strings.Contains(d.reason, stepHelmRepoPatches) {
			t.Errorf("refusal must name the step whose commits it protects; got %q", d.reason)
		}
	})
}

// TestRunCutover_6307_RemirrorReTakesSnapshotAndKeepsThePivot drives the
// engine through the override on the exact hw296 state: the mirror and the
// steps below the boundary re-run, flux-gitrepository-patch keeps its
// success (its pivot is idempotent and already live), the failed step-06 is
// deleted and re-run, and the chain completes.
func TestRunCutover_6307_RemirrorReTakesSnapshotAndKeepsThePivot(t *testing.T) {
	failed := makeFailedJobForStep(stepHelmRepoPatches)
	objs := []k8sruntime.Object{
		statusCMWith(hw296DeadlockStatus(time.Now().Add(-3 * time.Hour))),
		completedJobForStep(cutoverStepGiteaMirror),
		completedJobForStep("harbor-projects"),
		completedJobForStep(cutoverStepHarborPrewarm),
		completedJobForStep(cutoverStepFluxGitRepoPatch),
		failed,
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.runCutoverWith(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw296Chain(t),
		cutoverRunOpts{operatorRetry: true, remirror: true})

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	for _, step := range []string{cutoverStepGiteaMirror, "harbor-projects", cutoverStepHarborPrewarm} {
		if got := created(step); got != 1 {
			t.Errorf("%s Jobs minted = %d, want 1 (re-taken snapshot + bounded cascade)", step, got)
		}
	}
	if got := created(cutoverStepFluxGitRepoPatch); got != 0 {
		t.Errorf("flux-gitrepository-patch Jobs minted = %d, want 0 — the boundary step keeps its success", got)
	}
	if got := created(stepHelmRepoPatches); got != 1 {
		t.Errorf("helmrepository-patches Jobs minted = %d, want 1 — the failed step is deleted and re-run (operator retry)", got)
	}
	if _, err := client.BatchV1().Jobs(cutoverTestNS).Get(context.Background(), failed.Name, metav1.GetOptions{}); err == nil {
		t.Errorf("the prior failed Job %s still exists; want it deleted before the re-run", failed.Name)
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true; data=%v", cm.Data["cutoverComplete"], cm.Data)
	}
}

// TestRunCutover_6307_PlainRetryStillKeepsTheSnapshot pins that the override
// is opt-in: a plain operator re-fire on the same state does NOT re-mirror
// (and so does not spend an hour on prewarm) — it re-runs the failed step.
func TestRunCutover_6307_PlainRetryStillKeepsTheSnapshot(t *testing.T) {
	objs := []k8sruntime.Object{
		statusCMWith(hw296DeadlockStatus(time.Now().Add(-3 * time.Hour))),
		completedJobForStep(cutoverStepGiteaMirror),
		completedJobForStep(cutoverStepFluxGitRepoPatch),
		makeFailedJobForStep(stepHelmRepoPatches),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.runCutoverWith(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, hw296Chain(t),
		cutoverRunOpts{operatorRetry: true})

	if got := created(cutoverStepGiteaMirror); got != 0 {
		t.Errorf("gitea-mirror Jobs minted = %d, want 0 on a plain retry past the boundary (#5437 suppression)", got)
	}
	if got := created(stepHelmRepoPatches); got != 1 {
		t.Errorf("helmrepository-patches Jobs minted = %d, want 1", got)
	}
}

// TestHandleCutoverStart_6307_RemirrorRefusedWhenCommitWriterSucceeded: the
// HTTP edge answers 409 with the reason instead of starting a run that would
// silently keep the stale snapshot.
func TestHandleCutoverStart_6307_RemirrorRefusedWhenCommitWriterSucceeded(t *testing.T) {
	st := hw296DeadlockStatus(time.Now().Add(-3 * time.Hour))
	st["step."+stepHelmRepoPatches+".result"] = "success"
	st["step."+stepCatalystAPIPatch+".result"] = "failed"
	st["failedStep"] = stepCatalystAPIPatch
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", cutoverStepGiteaMirror, 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-05-flux-gitrepository-patch", cutoverStepFluxGitRepoPatch, 5, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-06-helmrepository-patches", stepHelmRepoPatches, 6, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-07-catalyst-api-env-patch", stepCatalystAPIPatch, 7, cutoverModeJob, minimalPodSpecYAML, ""),
		statusCMWith(st),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	created := cutoverJobCreateCounter(t, client)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start?remirror=true", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v: %s", err, rec.Body.String())
	}
	if body["error"] != "remirror-refused" || !strings.Contains(fmt.Sprint(body["detail"]), stepHelmRepoPatches) {
		t.Errorf("body = %v; want error=remirror-refused naming %s", body, stepHelmRepoPatches)
	}
	bus := h.cutoverBusFor()
	bus.mu.Lock()
	running := bus.running
	bus.mu.Unlock()
	if running {
		t.Error("a refused remirror must not start an engine run")
	}
	if got := created(cutoverStepGiteaMirror) + created(stepCatalystAPIPatch); got != 0 {
		t.Errorf("Jobs minted = %d, want 0", got)
	}
}

func TestCutoverRemirrorRequested(t *testing.T) {
	for q, want := range map[string]bool{"": false, "?remirror=false": false, "?remirror=true": true, "?remirror=1": true, "?remirror=TRUE": true} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start"+q, nil)
		if got := cutoverRemirrorRequested(req); got != want {
			t.Errorf("%q: got %v want %v", q, got, want)
		}
	}
}

// TestRunCutover_6307_StepRunsAsTheRolledChartDefinesIt: once the re-mirror
// has let Flux roll the chart that carries the fix, the engine must execute
// the step with the NEW PodSpec in the same attempt — not the one captured
// at /start.
func TestRunCutover_6307_StepRunsAsTheRolledChartDefinesIt(t *testing.T) {
	const rolledImage = "busybox:1.37-fixed"
	rolledPodSpec := strings.Replace(minimalPodSpecYAML, "busybox:1.36", rolledImage, 1)
	objs := []k8sruntime.Object{
		statusCMWith(map[string]string{}),
		makeCutoverStepCM("cutover-step-06-helmrepository-patches", stepHelmRepoPatches, 6, cutoverModeJob, rolledPodSpec, ""),
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)

	// The chain as captured at /start still carries the OLD PodSpec.
	stale := []cutoverStep{{stepName: stepHelmRepoPatches, order: 6, mode: cutoverModeJob, podSpec: minimalPodSpec(t)}}
	h.runCutoverWith(context.Background(), &cutoverDeps{core: client, ns: cutoverTestNS}, stale, cutoverRunOpts{operatorRetry: true})

	jobs, err := client.BatchV1().Jobs(cutoverTestNS).List(context.Background(), metav1.ListOptions{})
	if err != nil || len(jobs.Items) != 1 {
		t.Fatalf("jobs = %d (%v), want 1", len(jobs.Items), err)
	}
	if got := jobs.Items[0].Spec.Template.Spec.Containers[0].Image; got != rolledImage {
		t.Errorf("step ran with image %q, want the rolled chart's %q", got, rolledImage)
	}
}

var _ = corev1.ConditionTrue
