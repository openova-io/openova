// cutover_activity_bridge_test.go — proves the cutover EXECUTION
// projects into the jobs/activity store as the `Cutover` group of step
// Job rows WITH dependency edges, driven by the real engine run (issue
// #3646). This is the wiring-level counterpart to the generic-bridge
// unit tests in internal/jobs/activity_bridge_test.go.
package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// waitCutoverIdle blocks until the in-process cutover running flag clears
// (the engine goroutine finished), or the deadline elapses.
func waitCutoverIdle(t *testing.T, h *Handler) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		bus := h.cutoverBusFor()
		bus.mu.Lock()
		running := bus.running
		bus.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cutover engine did not go idle within deadline")
}

// TestCutoverExecutionProjectsIntoJobsWithEdges runs the real engine
// (HandleCutoverStart) against a fake cluster with 3 step ConfigMaps and
// asserts the cutover EXECUTION lands in the jobs store as a `Cutover`
// group of step rows with the linear dependsOn chain — the unified model
// issue #3646 asks for. Crucially the projected group reflects the real
// execution (succeeded only because every step's Job actually Completed),
// NOT the dormant chart-install row.
func TestCutoverExecutionProjectsIntoJobsWithEdges(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-prewarm", "harbor-prewarm", 2, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-03-registry-pivot", "registry-pivot", 3, cutoverModeDaemonSetWait, "", "registry-pivot"),
		makeReadyDaemonSet("registry-pivot"),
		// #3671: harbor-prewarm flips registriesYamlActive=v2; pre-seed the
		// per-node v2 acks (DesiredNumberScheduled=3) so the registry-pivot
		// daemonset-wait ack-count passes deterministically in the fake.
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: cutoverStatusConfigMapName(), Namespace: cutoverTestNS},
			Data: map[string]string{
				"cutoverComplete":              "false",
				"node.cp-1.registriesYaml":     "v2",
				"node.worker-1.registriesYaml": "v2",
				"node.worker-2.registriesYaml": "v2",
			},
		},
	}
	h, client := fakeHandlerWithCutover(t, objs...)
	installJobReactor(t, client, batchv1.JobComplete)

	// Wire a jobs store + pin the activity projection to a known
	// deployment id so we can assert the projected rows.
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h.jobs = js
	const depID = "dep-cutover-proj"
	h.cutoverActivityDepID = depID

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sovereign/cutover/start", nil)
	h.HandleCutoverStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("HandleCutoverStart: status %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	waitCutoverIdle(t, h)

	// Read back the projected jobs.
	all, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	// The Cutover group must exist + roll up to succeeded (every step's
	// Job Completed).
	group := findCutoverJob(t, all, jobs.GroupCutover)
	if group.Type != jobs.JobTypeGroup {
		t.Fatalf("Cutover group Type = %q, want %q", group.Type, jobs.JobTypeGroup)
	}
	if group.Status != jobs.StatusSucceeded {
		t.Errorf("Cutover group status = %q, want %q (all steps completed)", group.Status, jobs.StatusSucceeded)
	}

	// Each of the 3 steps projects as a leaf under the group.
	mirror := findCutoverJob(t, all, jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror"))
	prewarm := findCutoverJob(t, all, jobs.ActivityStepJobName(jobs.GroupCutover, "harbor-prewarm"))
	pivot := findCutoverJob(t, all, jobs.ActivityStepJobName(jobs.GroupCutover, "registry-pivot"))

	for _, s := range []jobs.Job{mirror, prewarm, pivot} {
		if s.ParentID != jobs.JobID(depID, jobs.GroupCutover) {
			t.Errorf("step %q ParentID = %q, want %q", s.JobName, s.ParentID, jobs.JobID(depID, jobs.GroupCutover))
		}
		if s.Status != jobs.StatusSucceeded {
			t.Errorf("step %q status = %q, want %q", s.JobName, s.Status, jobs.StatusSucceeded)
		}
	}

	// Edges: gitea-mirror (order 1) has no dep; harbor-prewarm depends on
	// gitea-mirror; registry-pivot depends on harbor-prewarm.
	if len(mirror.DependsOn) != 0 {
		t.Errorf("gitea-mirror DependsOn = %v, want []", mirror.DependsOn)
	}
	wantPrewarmDep := jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror")
	if len(prewarm.DependsOn) != 1 || prewarm.DependsOn[0] != wantPrewarmDep {
		t.Errorf("harbor-prewarm DependsOn = %v, want [%q]", prewarm.DependsOn, wantPrewarmDep)
	}
	wantPivotDep := jobs.ActivityStepJobName(jobs.GroupCutover, "harbor-prewarm")
	if len(pivot.DependsOn) != 1 || pivot.DependsOn[0] != wantPivotDep {
		t.Errorf("registry-pivot DependsOn = %v, want [%q]", pivot.DependsOn, wantPivotDep)
	}

	// Sanity: the projected group is DISTINCT from the dormant chart
	// install leaf (install-self-sovereign-cutover) — the whole point of
	// #3646. The install leaf isn't written by this path (no helmwatch
	// seed in this test), so only the execution group should be present.
	if g := jobs.JobID(depID, jobs.GroupCutover); group.ID != g {
		t.Errorf("group ID = %q, want %q", group.ID, g)
	}
}

func findCutoverJob(t *testing.T, in []jobs.Job, name string) jobs.Job {
	t.Helper()
	for _, j := range in {
		if j.JobName == name {
			return j
		}
	}
	t.Fatalf("projected job %q not found in %d rows", name, len(in))
	return jobs.Job{}
}

// TestCutoverResumeSeedReplaysDurableStatus proves the read-path
// projection: with NO live engine run, replaying the durable cutover
// status ConfigMap surfaces the real per-step states under the Cutover
// group — the exact #3646 scenario where an operator opens /jobs after
// handover and the goroutine is gone.
func TestCutoverResumeSeedReplaysDurableStatus(t *testing.T) {
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.jobs = js
	const depID = "dep-resume-proj"
	h.cutoverActivityDepID = depID

	now := time.Now().UTC()
	// Durable status: step1 succeeded, step2 mid-flight, step3 pending.
	status := map[string]string{
		"cutoverComplete":               "false",
		"step.gitea-mirror.result":      "success",
		"step.gitea-mirror.startedAt":   now.Format(time.RFC3339),
		"step.gitea-mirror.finishedAt":  now.Add(time.Minute).Format(time.RFC3339),
		"step.harbor-prewarm.result":    "running",
		"step.harbor-prewarm.startedAt": now.Add(time.Minute).Format(time.RFC3339),
		"step.registry-pivot.result":    "",
	}
	stepNames := []string{"gitea-mirror", "harbor-prewarm", "registry-pivot"}

	h.projectCutoverResumeSeed(stepNames, status)

	all, err := js.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	mirror := findCutoverJob(t, all, jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror"))
	prewarm := findCutoverJob(t, all, jobs.ActivityStepJobName(jobs.GroupCutover, "harbor-prewarm"))
	pivot := findCutoverJob(t, all, jobs.ActivityStepJobName(jobs.GroupCutover, "registry-pivot"))

	if mirror.Status != jobs.StatusSucceeded {
		t.Errorf("gitea-mirror status = %q, want %q", mirror.Status, jobs.StatusSucceeded)
	}
	if prewarm.Status != jobs.StatusRunning {
		t.Errorf("harbor-prewarm status = %q, want %q", prewarm.Status, jobs.StatusRunning)
	}
	if pivot.Status != jobs.StatusPending {
		t.Errorf("registry-pivot status = %q, want %q", pivot.Status, jobs.StatusPending)
	}

	// The group must NOT read succeeded — the cutover is mid-flight. This
	// is the core #3646 guarantee: never show "Succeeded" unless every
	// step truly succeeded.
	group := findCutoverJob(t, all, jobs.GroupCutover)
	if group.Status == jobs.StatusSucceeded {
		t.Errorf("Cutover group status = %q, must not be succeeded while a step is running/pending", group.Status)
	}
	if group.Status != jobs.StatusRunning {
		t.Errorf("Cutover group status = %q, want %q (a step is in flight)", group.Status, jobs.StatusRunning)
	}
}
