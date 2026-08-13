// Regression coverage for the carried-over-failure REPORTING path (#6262).
//
// When an unattended resume (operatorRetry=false) finds a Failed Job from a
// PRIOR cutover attempt, runCutoverStep deliberately does not re-run the
// step — it surfaces the prior failure and halts. That halt semantic is
// correct and these tests keep it pinned.
//
// What was NOT correct is the evidence it wrote while halting. Two fields
// actively disguised a stale failure as a fresh one:
//
//   - step.<name>.startedAt was never patched, so the durable row read
//     result=failed + finishedAt=<set> + startedAt="" — a step that
//     finished without ever starting. That empty cell is the clearest
//     available signal that NO Job ran this attempt, and it rendered as
//     missing data instead.
//   - step.<name>.finishedAt was stamped with the moment of the re-read,
//     because jobCompletionTime matched only batchv1.JobComplete and the
//     apiserver sets Status.CompletionTime only on success. A failure from
//     a prior attempt therefore landed on the CURRENT attempt's timeline.
//
// Measured on hw296 (dep e689e3b34a75fdec): Job
// cutover-harbor-prewarm-1786655169 failed at 21:08:08Z; the 22:02:16Z
// auto-resume re-reported it with finishedAt=22:03:34Z, one second after
// step-02 succeeded. Reading that row top-down says "step-03 just failed in
// this attempt". It had not run in this attempt at all.
//
// The assertions below are written against the JOB's OWN timestamps rather
// than against "not now", so they fail on the pre-fix code for the real
// reason rather than on a clock race.
package handler

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
)

// hw296FailedPrewarmJob reproduces the exact live object: a harbor-prewarm
// Job that exhausted its backoff limit 56 minutes before the resume that
// re-reads it. BackoffLimitExceeded (NOT DeadlineExceeded) is what makes
// jobFailedTransiently return false and routes this to the carried-over
// branch.
func hw296FailedPrewarmJob(failedAt, startedAt time.Time) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cutover-harbor-prewarm-1786655169",
			Namespace: cutoverTestNS,
			Labels: map[string]string{
				cutoverStepPartOfLabel:    cutoverStepPartOfValue,
				cutoverStepComponentLabel: "cutover-job",
				cutoverStepLabelKey:       "harbor-prewarm",
			},
			CreationTimestamp: metav1.NewTime(startedAt),
		},
		Status: batchv1.JobStatus{
			Failed:    4,
			StartTime: &metav1.Time{Time: startedAt},
			Conditions: []batchv1.JobCondition{{
				Type:               batchv1.JobFailed,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(failedAt),
				Reason:             "BackoffLimitExceeded",
				Message:            "Job has reached the specified backoff limit",
			}},
		},
	}
}

// TestJobCompletionTime_UsesFailedConditionTime is the unit-level seam.
// A Failed Job carries its terminal time ONLY on the JobFailed condition;
// Status.CompletionTime stays nil. Matching just JobComplete silently
// yielded time.Now() for every failure.
func TestJobCompletionTime_UsesFailedConditionTime(t *testing.T) {
	failedAt := time.Date(2026, 8, 13, 21, 8, 8, 0, time.UTC)
	job := hw296FailedPrewarmJob(failedAt, failedAt.Add(-77*time.Second))

	got := jobCompletionTime(job)
	if !got.Equal(failedAt) {
		t.Errorf("jobCompletionTime(failed job) = %s, want the JobFailed condition time %s\n"+
			"a Failed Job has no Status.CompletionTime, so falling through to time.Now() "+
			"stamps the moment of the RE-READ and erases how stale the failure is (#6262)",
			got.Format(time.RFC3339), failedAt.Format(time.RFC3339))
	}
}

// TestJobCompletionTime_CompleteJobUnchanged is the CONTROL. Broadening the
// condition match must not disturb the success path that already worked:
// Status.CompletionTime still wins, and a Complete condition still resolves
// ahead of anything else.
func TestJobCompletionTime_CompleteJobUnchanged(t *testing.T) {
	completedAt := time.Date(2026, 8, 13, 22, 3, 28, 0, time.UTC)

	withCompletionTime := &batchv1.Job{
		Status: batchv1.JobStatus{
			CompletionTime: &metav1.Time{Time: completedAt},
			Conditions: []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(completedAt.Add(9 * time.Second)),
			}},
		},
	}
	if got := jobCompletionTime(withCompletionTime); !got.Equal(completedAt) {
		t.Errorf("jobCompletionTime(complete job) = %s, want Status.CompletionTime %s",
			got.Format(time.RFC3339), completedAt.Format(time.RFC3339))
	}

	conditionOnly := &batchv1.Job{
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{{
				Type:               batchv1.JobComplete,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(completedAt),
			}},
		},
	}
	if got := jobCompletionTime(conditionOnly); !got.Equal(completedAt) {
		t.Errorf("jobCompletionTime(condition-only complete job) = %s, want %s",
			got.Format(time.RFC3339), completedAt.Format(time.RFC3339))
	}
}

// TestRunCutoverStep_CarriedOverFailureStampsJobTimes drives the real
// resume path with operatorRetry=false and asserts the durable row it
// writes is legible as CARRIED OVER — while keeping the halt + no-re-run
// semantics that #3379 deliberately chose for unattended resumes.
func TestRunCutoverStep_CarriedOverFailureStampsJobTimes(t *testing.T) {
	jobStartedAt := time.Date(2026, 8, 13, 21, 6, 51, 0, time.UTC)
	jobFailedAt := time.Date(2026, 8, 13, 21, 8, 8, 0, time.UTC)

	preStatus := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cutoverStatusConfigMapName(),
			Namespace: cutoverTestNS,
		},
		Data: map[string]string{
			"cutoverComplete": "false",
			// Exactly as observed live: the prior attempt's rows for this
			// step were reset, so there is no startedAt to recover from the
			// ConfigMap. The Job itself is the only source of truth.
			"step.harbor-prewarm.result":     "",
			"step.harbor-prewarm.startedAt":  "",
			"step.harbor-prewarm.finishedAt": "",
			"step.harbor-prewarm.jobName":    "",
		},
	}
	stepCM := makeCutoverStepCM("cutover-step-03-harbor-prewarm", "harbor-prewarm", 3,
		cutoverModeJob, minimalPodSpecYAML, "")
	priorJob := hw296FailedPrewarmJob(jobFailedAt, jobStartedAt)

	h, client := fakeHandlerWithCutover(t, []k8sruntime.Object{stepCM, preStatus, priorJob}...)

	// Any Job creation here would mean the unattended path silently
	// re-ran a genuinely-failed step — the #3379 fail-closed semantic.
	var muJobs sync.Mutex
	created := []string{}
	client.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, k8sruntime.Object, error) {
		ca, ok := action.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		if job, ok := ca.GetObject().(*batchv1.Job); ok {
			muJobs.Lock()
			created = append(created, job.Name)
			muJobs.Unlock()
		}
		return false, nil, nil
	})

	step, err := parseCutoverStep(*stepCM)
	if err != nil {
		t.Fatalf("parseCutoverStep: %v", err)
	}
	deps := &cutoverDeps{core: client, ns: cutoverTestNS}

	runErr := h.runCutoverStep(context.Background(), deps, step,
		time.Now().Unix(), false /* operatorRetry */, false /* forceRerun */)

	if runErr == nil {
		t.Fatalf("runCutoverStep returned nil; a carried-over Failed Job must still halt the engine")
	}

	muJobs.Lock()
	nCreated := len(created)
	muJobs.Unlock()
	if nCreated != 0 {
		t.Errorf("runCutoverStep created %d Job(s) %v; an unattended resume must NOT re-run a "+
			"genuinely-failed step (#3379 fail-closed)", nCreated, created)
	}

	// The error string is what lands in the status ConfigMap's lastError and
	// is the first thing an operator reads. It must date the failure.
	if !strings.Contains(runErr.Error(), jobFailedAt.Format(time.RFC3339)) {
		t.Errorf("error %q does not name the Job's failure time %s;\n"+
			"without it, lastError cannot be told apart from a failure that "+
			"happened in the current attempt (#6262)",
			runErr.Error(), jobFailedAt.Format(time.RFC3339))
	}

	got, err := readCutoverStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("readCutoverStatus: %v", err)
	}

	if got["step.harbor-prewarm.result"] != "failed" {
		t.Errorf("step.harbor-prewarm.result = %q, want %q",
			got["step.harbor-prewarm.result"], "failed")
	}
	if got["step.harbor-prewarm.jobName"] != priorJob.Name {
		t.Errorf("step.harbor-prewarm.jobName = %q, want %q",
			got["step.harbor-prewarm.jobName"], priorJob.Name)
	}

	// The regression: startedAt was left "" while finishedAt was set,
	// producing a step that finished without starting.
	wantStarted := jobStartedAt.Format(time.RFC3339)
	if got["step.harbor-prewarm.startedAt"] != wantStarted {
		t.Errorf("step.harbor-prewarm.startedAt = %q, want %q (the prior Job's own StartTime);\n"+
			"an empty startedAt beside a populated finishedAt reads as missing data when it is "+
			"in fact the strongest evidence that no Job ran this attempt (#6262)",
			got["step.harbor-prewarm.startedAt"], wantStarted)
	}

	// The regression: finishedAt was time.Now(), which relocated a
	// 56-minute-old failure onto the current attempt.
	wantFinished := jobFailedAt.Format(time.RFC3339)
	if got["step.harbor-prewarm.finishedAt"] != wantFinished {
		t.Errorf("step.harbor-prewarm.finishedAt = %q, want %q (the JobFailed condition time);\n"+
			"stamping the re-read time makes a carried-over failure indistinguishable from a "+
			"fresh one on the status API (#6262)",
			got["step.harbor-prewarm.finishedAt"], wantFinished)
	}
}
