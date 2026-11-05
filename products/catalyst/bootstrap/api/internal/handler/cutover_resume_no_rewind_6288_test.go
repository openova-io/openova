// cutover_resume_no_rewind_6288_test.go — #6288 guards.
//
// The defect (live hw296, dep e689e3b34a75fdec, 2026-08-14): a routine
// bp-catalyst-platform bump rolled catalyst-api ~3 minutes into step-03
// harbor-prewarm. The fresh Pod's ResumeInterruptedCutover entered the engine
// exactly like an operator re-fire, so the #5437 snapshot judgement compared
// step-01's recorded finishedAt (00:38) with the NEW attempt start (00:41),
// ruled the mirror stale, deleted + re-ran steps 1-2 and only then re-attached
// to the still-Running prewarm Job — stamping its startedAt with the adoption
// time. Had that Job finished inside the restart window, the cascade's
// forceRerun would have deleted it and repeated the ~50-minute step.
//
// Both tests below FAIL against the pre-fix engine: the first mints a second
// gitea-mirror Job and re-stamps cutoverLastAttemptStartedAt; the second
// records the adoption time instead of the Job's own start.
package handler

import (
	"context"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
)

// completeJobAt fabricates a Complete Job for a step with explicit timing.
func completeJobAt(stepName string, created, finished time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cutover-" + stepName + "-1786667826",
			Namespace:         cutoverTestNS,
			Labels:            map[string]string{cutoverStepLabelKey: stepName},
			CreationTimestamp: metav1.NewTime(created),
		},
		Status: batchv1.JobStatus{
			StartTime:      &metav1.Time{Time: created},
			CompletionTime: &metav1.Time{Time: finished},
			Conditions: []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(finished),
			}},
		},
	}
}

// runningJobAt fabricates the in-flight Job the previous process lifetime
// created and was watching when it died: no terminal condition, Active=1.
func runningJobAt(stepName string, started time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "cutover-" + stepName + "-1786667826",
			Namespace:         cutoverTestNS,
			Labels:            map[string]string{cutoverStepLabelKey: stepName},
			CreationTimestamp: metav1.NewTime(started),
		},
		Status: batchv1.JobStatus{
			StartTime: &metav1.Time{Time: started},
			Active:    1,
		},
	}
}

// completeRunningJobLater flips the seeded in-flight Job to Complete shortly
// after the engine has re-attached to it, the way the real Job finishes on
// its own regardless of which catalyst-api Pod is watching.
func completeRunningJobLater(t *testing.T, client *fakek8s.Clientset, job *batchv1.Job, after time.Duration) {
	t.Helper()
	go func() {
		time.Sleep(after)
		cur, err := client.BatchV1().Jobs(job.Namespace).Get(context.Background(), job.Name, metav1.GetOptions{})
		if err != nil {
			return
		}
		cur.Status.Active = 0
		now := metav1.Now()
		cur.Status.CompletionTime = &now
		cur.Status.Conditions = []batchv1.JobCondition{{
			Type:               batchv1.JobComplete,
			Status:             corev1.ConditionTrue,
			LastTransitionTime: now,
		}}
		_, _ = client.BatchV1().Jobs(job.Namespace).UpdateStatus(context.Background(), cur, metav1.UpdateOptions{})
	}()
}

func waitForCutoverEngineIdle(t *testing.T, h *Handler, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("cutover engine still running after %s", budget)
}

// hw296StepCMs is the shipped chain trimmed to the steps the defect
// involves, as the step ConfigMaps ResumeInterruptedCutover discovers: the
// snapshot (01), an idempotent action (02), the long artifact push that was
// in flight (03) and the pivot boundary (05) — present so the #5437
// suppression does NOT apply (nothing at/after it has progressed) and a
// rewind would really happen on the pre-fix engine.
func hw296StepCMs() []k8sruntime.Object {
	return []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", cutoverStepGiteaMirror, 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-03-harbor-prewarm", cutoverStepHarborPrewarm, 3, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-05-flux-gitrepository-patch", cutoverStepFluxGitRepoPatch, 5, cutoverModeJob, minimalPodSpecYAML, ""),
	}
}

// TestResumeInterruptedCutover_6288_ContinuesAttemptWithoutRewind replays the
// hw296 timeline: attempt started 00:37:06, steps 1-2 succeeded inside it,
// step 3's Job is still Running when the process comes back. The resume must
// keep steps 1-2, keep the attempt's identity, and re-attach to the Running
// Job — never mint a second gitea-mirror Job.
func TestResumeInterruptedCutover_6288_ContinuesAttemptWithoutRewind(t *testing.T) {
	attempt := time.Now().Add(-6 * time.Minute).UTC().Truncate(time.Second)
	mirrorDone := attempt.Add(50 * time.Second)
	projectsDone := attempt.Add(56 * time.Second)
	prewarmStart := attempt.Add(57 * time.Second)

	prewarm := runningJobAt(cutoverStepHarborPrewarm, prewarmStart)
	objs := append(hw296StepCMs(),
		statusCMWith(map[string]string{
			"cutoverStartedAt":                                attempt.Format(time.RFC3339),
			"cutoverLastAttemptStartedAt":                     attempt.Format(time.RFC3339),
			"currentStep":                                     cutoverStepHarborPrewarm,
			"currentStepIndex":                                "2",
			"step." + cutoverStepGiteaMirror + ".result":      "success",
			"step." + cutoverStepGiteaMirror + ".startedAt":   attempt.Add(6 * time.Second).Format(time.RFC3339),
			"step." + cutoverStepGiteaMirror + ".finishedAt":  mirrorDone.Format(time.RFC3339),
			"step." + cutoverStepGiteaMirror + ".jobName":     "cutover-gitea-mirror-1786667826",
			"step.harbor-projects.result":                     "success",
			"step.harbor-projects.startedAt":                  mirrorDone.Format(time.RFC3339),
			"step.harbor-projects.finishedAt":                 projectsDone.Format(time.RFC3339),
			"step.harbor-projects.jobName":                    "cutover-harbor-projects-1786667826",
			"step." + cutoverStepHarborPrewarm + ".result":    "running",
			"step." + cutoverStepHarborPrewarm + ".startedAt": prewarmStart.Format(time.RFC3339),
			"step." + cutoverStepHarborPrewarm + ".jobName":   prewarm.Name,
		}),
		completeJobAt(cutoverStepGiteaMirror, attempt.Add(6*time.Second), mirrorDone),
		completeJobAt("harbor-projects", mirrorDone, projectsDone),
		prewarm,
	)
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)
	completeRunningJobLater(t, client, prewarm, 150*time.Millisecond)

	h.ResumeInterruptedCutover(context.Background())
	waitForCutoverEngineIdle(t, h, 15*time.Second)

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if got := created(cutoverStepGiteaMirror); got != 0 {
		t.Errorf("gitea-mirror Jobs minted on resume = %d, want 0 — the resume rewound the chain to step 1 (#6288)", got)
	}
	if got := created("harbor-projects"); got != 0 {
		t.Errorf("harbor-projects Jobs minted on resume = %d, want 0 (#6288 cascade)", got)
	}
	if got := created(cutoverStepHarborPrewarm); got != 0 {
		t.Errorf("harbor-prewarm Jobs minted = %d, want 0 — the Running Job must be re-attached, not replaced", got)
	}
	if got := cm.Data["cutoverLastAttemptStartedAt"]; got != attempt.Format(time.RFC3339) {
		t.Errorf("cutoverLastAttemptStartedAt = %q, want the interrupted attempt's %s — a resume is not a new attempt", got, attempt.Format(time.RFC3339))
	}
	if got := cm.Data["step."+cutoverStepGiteaMirror+".jobName"]; got != "cutover-gitea-mirror-1786667826" {
		t.Errorf("gitea-mirror jobName = %q, want the attempt's own Job kept", got)
	}
	if got := cm.Data["step."+cutoverStepHarborPrewarm+".jobName"]; got != prewarm.Name {
		t.Errorf("harbor-prewarm jobName = %q, want re-attached %s", got, prewarm.Name)
	}
	if got := cm.Data["step."+cutoverStepHarborPrewarm+".startedAt"]; got != prewarmStart.Format(time.RFC3339) {
		t.Errorf("harbor-prewarm startedAt = %q, want the Job's own start %s, not the adoption time (#6288)", got, prewarmStart.Format(time.RFC3339))
	}
	if cm.Data["cutoverComplete"] != "true" {
		t.Errorf("cutoverComplete = %q, want true after the continued attempt ran to the end; data=%v", cm.Data["cutoverComplete"], cm.Data)
	}
}

// TestResumeInterruptedCutover_6288_FailedAttemptIsStillANewAttempt pins the
// boundary of the continuation: an attempt that had terminally FAILED before
// the restart is not continued. The resume opens a new attempt, so the #5437
// judgement still re-takes a mirror that predates it (the hw290 guarantee).
func TestResumeInterruptedCutover_6288_FailedAttemptIsStillANewAttempt(t *testing.T) {
	attempt := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Second)
	mirrorDone := attempt.Add(50 * time.Second)

	objs := append(hw296StepCMs(),
		statusCMWith(map[string]string{
			"cutoverStartedAt":            attempt.Format(time.RFC3339),
			"cutoverLastAttemptStartedAt": attempt.Format(time.RFC3339),
			"failedStep":                  cutoverStepHarborPrewarm,
			"lastError":                   "Job catalyst/cutover-harbor-prewarm-1786667826 reported Failed condition",
			"step." + cutoverStepGiteaMirror + ".result":     "success",
			"step." + cutoverStepGiteaMirror + ".finishedAt": mirrorDone.Format(time.RFC3339),
			"step.harbor-projects.result":                    "success",
			"step.harbor-projects.finishedAt":                attempt.Add(56 * time.Second).Format(time.RFC3339),
			"step." + cutoverStepHarborPrewarm + ".result":   "failed",
		}),
		completeJobAt(cutoverStepGiteaMirror, attempt.Add(6*time.Second), mirrorDone),
		makeTransientlyFailedJobForStep(cutoverStepHarborPrewarm),
	)
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)
	created := cutoverJobCreateCounter(t, client)

	h.ResumeInterruptedCutover(context.Background())
	waitForCutoverEngineIdle(t, h, 15*time.Second)

	cm, err := client.CoreV1().ConfigMaps(cutoverTestNS).Get(context.Background(), cutoverStatusConfigMapName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if got := created(cutoverStepGiteaMirror); got != 1 {
		t.Errorf("gitea-mirror Jobs minted = %d, want 1 — a resume of a FAILED attempt is a new attempt and must re-take the 3-hour-old snapshot (#5437)", got)
	}
	if got := cm.Data["cutoverLastAttemptStartedAt"]; got == attempt.Format(time.RFC3339) {
		t.Errorf("cutoverLastAttemptStartedAt still %q — a new attempt must advance it", got)
	}
}

func TestResumedAttemptStart(t *testing.T) {
	ts := "2026-08-14T00:37:06Z"
	cases := []struct {
		name   string
		status map[string]string
		ok     bool
	}{
		{"in-flight attempt", map[string]string{"cutoverLastAttemptStartedAt": ts}, true},
		{"terminally failed attempt", map[string]string{"cutoverLastAttemptStartedAt": ts, "failedStep": "harbor-prewarm"}, false},
		{"pre-#3681 record without the field", map[string]string{"cutoverStartedAt": ts}, false},
		{"garbage timestamp", map[string]string{"cutoverLastAttemptStartedAt": "yesterday"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := resumedAttemptStart(c.status)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got.Format(time.RFC3339) != ts {
				t.Errorf("start = %s, want %s", got.Format(time.RFC3339), ts)
			}
		})
	}
}
