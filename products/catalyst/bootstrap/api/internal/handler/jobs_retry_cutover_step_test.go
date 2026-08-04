// jobs_retry_cutover_step_test.go — coverage for the KindStep leg of the
// generic remediation endpoint: the per-row **Re-run** control on a FAILED
// `cutover-step-*` row of the operator console's /jobs board (UAT row 165,
// issue #3379).
//
// Gates:
//
//  1. A FAILED cutover-step leaf re-drives the engine with
//     operatorRetry=true — the prior failed Job is deleted + re-run, and
//     the durable status ConfigMap reflects the fresh run.
//  2. A SUCCEEDED / RUNNING / PENDING cutover-step leaf is rejected 409
//     (nothing to re-run) — the "gated to Failed" half of row 165.
//  3. A viewer-tier session gets 403 (the control is operator-gated).
//  4. A second Re-run while a run is already in flight gets 409 rather
//     than spawning a colliding engine goroutine.
//  5. A step leaf whose slug is not among the discovered step ConfigMaps
//     gets a graceful 422, never a raw 502.
package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// cutoverStepRetryHarness wires a Handler that has BOTH a jobs Store (so a
// `cutover-step-*` leaf can be seeded + retried) and a cutover deps factory
// bound to a fake clientset pre-seeded with the step ConfigMaps — the two
// halves the KindStep retry leg needs.
func cutoverStepRetryHarness(
	t *testing.T, ownerEmail, jobName, status string, objs ...k8sruntime.Object,
) (*chi.Mux, *Handler, *fakek8s.Clientset, string) {
	t.Helper()

	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(silentLogger(), js)

	// RetryJob builds a dynamic client before dispatching; the KindStep leg
	// never uses it, but the handler must not 502 on the way in.
	factory, _ := fakeReconcilerDynamicFactory()
	h.dynamicFactory = factory

	client := fakek8s.NewSimpleClientset(objs...)
	h.SetCutoverDepsFactory(func() (*cutoverDeps, error) {
		return &cutoverDeps{core: client, ns: cutoverTestNS}, nil
	})

	depID := "dep-cutover-retry"
	kubePath := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(kubePath, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	h.deployments.Store(depID, &Deployment{
		ID:         depID,
		OwnerEmail: ownerEmail,
		Request:    provisioner.Request{SovereignFQDN: "t99.omani.works"},
		Result:     &provisioner.Result{KubeconfigPath: kubePath},
	})

	if err := js.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      jobName,
		Type:         jobs.JobTypeInstall,
		Kind:         jobs.KindStep,
		Status:       status,
	}); err != nil {
		t.Fatalf("seed cutover step leaf: %v", err)
	}

	r := chi.NewRouter()
	r.Post("/api/v1/deployments/{depId}/jobs/{jobId}/retry", h.RetryJob)
	return r, h, client, depID
}

// staleFailedCutoverJob builds a prior GENUINELY-failed
// (BackoffLimitExceeded, i.e. non-transient) Job for a step — exactly the
// object `jobFailedTransiently` refuses to auto-retry and that only the
// operator CTA is allowed to delete + re-run.
//
// It is pre-seeded into the fake clientset's tracker (passed via objs)
// rather than Created through the client: installJobReactor auto-completes
// every CREATED Job, which would overwrite the Failed condition.
func staleFailedCutoverJob(name, stepName string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cutoverTestNS,
			Labels:    map[string]string{cutoverStepLabelKey: stepName},
		},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{
			Type:   batchv1.JobFailed,
			Status: corev1.ConditionTrue,
			Reason: "BackoffLimitExceeded",
		}}},
	}
}

// Row 165, positive direction: the operator's Re-run on a FAILED
// cutover-step row re-drives the engine with operatorRetry=true — the
// step's stale non-transient failed Job is DELETED + re-run and the chain
// resumes through the remaining steps.
func TestRetryJob_CutoverStep_Failed_RedrivesEngine(t *testing.T) {
	const staleJobName = "cutover-gitea-mirror-1"
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
		makeCutoverStepCM("cutover-step-02-harbor-projects", "harbor-projects", 2, cutoverModeJob, minimalPodSpecYAML, ""),
		staleFailedCutoverJob(staleJobName, "gitea-mirror"),
	}
	jobName := jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror")
	r, h, client, depID := cutoverStepRetryHarness(t, "owner@t99.omani.works", jobName, jobs.StatusFailed, objs...)
	installJobReactor(t, client, batchv1.JobComplete)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, jobName,
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 for a failed cutover-step re-run, got %d; body=%s", rec.Code, rec.Body.String())
	}

	waitCutoverIdle(t, h)

	// The stale non-transient failed Job was DELETED — the operatorRetry=true
	// semantic the control exists to reach. Without it the engine re-surfaces
	// that Job as a terminal wedge and the step never re-executes.
	if _, err := client.BatchV1().Jobs(cutoverTestNS).Get(
		context.Background(), staleJobName, metav1.GetOptions{},
	); err == nil {
		t.Error("stale failed Job still present — the re-run did not delete + re-run the step")
	}

	// …and a FRESH Job for the step was created in its place.
	jl, err := client.BatchV1().Jobs(cutoverTestNS).List(context.Background(), metav1.ListOptions{
		LabelSelector: cutoverStepLabelKey + "=gitea-mirror",
	})
	if err != nil {
		t.Fatalf("list step Jobs: %v", err)
	}
	if len(jl.Items) == 0 {
		t.Error("no fresh Job created for the re-run step")
	}

	deps, _ := h.cutoverDepsFor()
	status, err := readCutoverStatus(context.Background(), deps)
	if err != nil {
		t.Fatalf("readCutoverStatus: %v", err)
	}
	if got := status["step.gitea-mirror.result"]; got != "success" {
		t.Errorf("step.gitea-mirror.result = %q, want success (the re-run drove it green); status=%v",
			got, status)
	}
	// The chain resumed past the re-run step rather than stopping at it.
	if got := status["step.harbor-projects.result"]; got != "success" {
		t.Errorf("step.harbor-projects.result = %q, want success (chain resumed); status=%v", got, status)
	}

	// The retry is auditable — a new Execution with the operator identity.
	_, execs, err := h.jobs.GetJob(depID, jobs.JobID(depID, jobName))
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if len(execs) == 0 {
		t.Fatal("no Execution recorded for the cutover-step re-run")
	}
}

// Row 165, negative direction: the control is gated to Failed. A
// succeeded / running / pending step is rejected 409 by the backend so a
// UI that ever rendered the control on those rows cannot silently mutate
// a healthy cutover.
func TestRetryJob_CutoverStep_NonFailedStatuses_409(t *testing.T) {
	for _, status := range []string{jobs.StatusSucceeded, jobs.StatusRunning, jobs.StatusPending} {
		t.Run(status, func(t *testing.T) {
			objs := []k8sruntime.Object{
				makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
			}
			jobName := jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror")
			r, _, _, depID := cutoverStepRetryHarness(t, "owner@t99.omani.works", jobName, status, objs...)

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, retryReq(depID, jobName,
				&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status %q: want 409 (not retryable), got %d; body=%s",
					status, rec.Code, rec.Body.String())
			}
		})
	}
}

// The control is operator-gated — a viewer session cannot re-drive a
// cutover step on a customer cluster.
func TestRetryJob_CutoverStep_Viewer_403(t *testing.T) {
	t.Setenv("OPERATOR_EMAIL", "")
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	jobName := jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror")
	r, _, _, depID := cutoverStepRetryHarness(t, "" /* legacy: no owner */, jobName, jobs.StatusFailed, objs...)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, jobName,
		&auth.Claims{Email: "viewer@t99.omani.works", Tier: "viewer"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 for viewer, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// A Re-run fired while the engine is already mid-run is rejected 409
// rather than spawning a second colliding engine goroutine.
func TestRetryJob_CutoverStep_RunInFlight_409(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	jobName := jobs.ActivityStepJobName(jobs.GroupCutover, "gitea-mirror")
	r, h, _, depID := cutoverStepRetryHarness(t, "owner@t99.omani.works", jobName, jobs.StatusFailed, objs...)

	// Claim the run flag as an in-flight cutover would.
	if !h.cutoverBusFor().tryStartRun() {
		t.Fatal("tryStartRun on a fresh bus must succeed")
	}
	t.Cleanup(h.cutoverBusFor().endRun)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, jobName,
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 while a run is in flight, got %d; body=%s", rec.Code, rec.Body.String())
	}
}

// A step leaf whose slug is not among the discovered step ConfigMaps is a
// client-side condition (nothing to re-run), so it must surface as a
// graceful 422 with an actionable detail — never a raw 502.
func TestRetryJob_CutoverStep_UnknownSlug_422(t *testing.T) {
	objs := []k8sruntime.Object{
		makeCutoverStepCM("cutover-step-01-gitea-mirror", "gitea-mirror", 1, cutoverModeJob, minimalPodSpecYAML, ""),
	}
	jobName := jobs.ActivityStepJobName(jobs.GroupCutover, "no-such-step")
	r, _, _, depID := cutoverStepRetryHarness(t, "owner@t99.omani.works", jobName, jobs.StatusFailed, objs...)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, retryReq(depID, jobName,
		&auth.Claims{Email: "owner@t99.omani.works", Tier: "operator"}))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 for an unknown step slug, got %d; body=%s", rec.Code, rec.Body.String())
	}
	// …and for the RIGHT reason: the detail must name the unknown step, not
	// the generic "kind %q does not support retry" the leg replaces.
	if body := rec.Body.String(); !strings.Contains(body, "no-such-step") {
		t.Errorf("422 detail must name the unknown step slug; got %s", body)
	}
}
