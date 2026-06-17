// Tests for the §5a generic reconciler reader (issue #3646).
//
// The load-bearing contract these pin: a recurring CronJob whose latest
// spawned run FAILED surfaces as a cron-kind ReconcilerObservation whose
// Status is the failed run's status (never the optimistic "pending"
// default), so a failing openbao-snapshot-save CronJob can never hide
// behind a green install-openbao row ("no silent green / no invisible
// failing class").
//
// The fixture deliberately seeds several UNOWNED standalone Jobs alongside
// the CronJob + its owned run so the pass-3 `out = append(...)` grows the
// observation slice past its initial capacity — the regression this guards
// is a slice-aliasing defect where the cron observation pointer cached in
// pass 2 dangled into a stale backing array after pass 3 reallocated `out`,
// silently dropping the failed-run status refinement.
package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// reconcilerListScheme registers the *List GVKs the dynamic fake client
// needs so List(...) resolves for every GVR ListReconcilerObservations
// enumerates. Without a registered List kind the fake returns "no kind
// registered" and the pass is silently skipped — which would mask, not
// reproduce, the bug under test.
func reconcilerListScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "KustomizationList"},
		&unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJobList"},
		&unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "JobList"},
		&unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "DeploymentList"},
		&unstructured.UnstructuredList{})
	return scheme
}

// reconcilerFakeClient builds a dynamic fake client with every reconciler
// GVR's List kind registered, seeded with the supplied objects.
func reconcilerFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		reconcilerListScheme(),
		map[schema.GroupVersionResource]string{
			KustomizationGVR: "KustomizationList",
			CronJobGVR:       "CronJobList",
			JobGVR:           "JobList",
			DeploymentGVR:    "DeploymentList",
		},
		objs...,
	)
}

// makeCronJob constructs a minimal batch/v1 CronJob unstructured object.
func makeCronJob(namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "CronJob",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"schedule": "0 * * * *",
			},
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "CronJob"})
	return u
}

// makeBatchJob constructs a batch/v1 Job. When ownerCronJob is non-empty the
// Job carries a CronJob ownerReference (the kube CronJob controller stamps
// this on every spawned run). condType/condStatus drive jobRunStatus (e.g.
// "Failed"/"True" for a failed run, "Complete"/"True" for a successful one);
// pass condType="" for a still-running Job with no terminal condition.
func makeBatchJob(namespace, name, ownerCronJob, condType string, condStatus metav1.ConditionStatus, finishedAt time.Time) *unstructured.Unstructured {
	meta := map[string]any{
		"name":      name,
		"namespace": namespace,
	}
	if ownerCronJob != "" {
		meta["ownerReferences"] = []any{
			map[string]any{
				"apiVersion": "batch/v1",
				"kind":       "CronJob",
				"name":       ownerCronJob,
				"uid":        "uid-" + ownerCronJob,
			},
		}
	}
	status := map[string]any{
		"startTime": finishedAt.Add(-time.Minute).UTC().Format(time.RFC3339),
	}
	if condType != "" {
		status["conditions"] = []any{
			map[string]any{
				"type":               condType,
				"status":             string(condStatus),
				"lastTransitionTime": finishedAt.UTC().Format(time.RFC3339),
			},
		}
	}
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata":   meta,
			"status":     status,
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "batch", Version: "v1", Kind: "Job"})
	return u
}

// findCron returns the single cron-kind observation for the given object
// name, or nil when absent.
func findCron(obs []ReconcilerObservation, name string) *ReconcilerObservation {
	for i := range obs {
		if obs[i].Kind == ReconcilerKindCron && obs[i].Name == name {
			return &obs[i]
		}
	}
	return nil
}

// TestListReconcilerObservations_FailingCronJobSurfacesFailed is the core
// #3646 guard: a CronJob whose latest spawned run FAILED must surface as a
// cron-kind observation with Status=failed and a per-run Execution, even
// when a crowd of unowned standalone Jobs forces the observation slice to
// reallocate between the cron pass and the Job pass.
func TestListReconcilerObservations_FailingCronJobSurfacesFailed(t *testing.T) {
	finished := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)

	objs := []runtime.Object{
		// The failing CronJob the walker found on hw158.
		makeCronJob("openbao", "openbao-snapshot-save"),
		// Its latest spawned run — Failed.
		makeBatchJob("openbao", "openbao-snapshot-save-28000000", "openbao-snapshot-save", "Failed", metav1.ConditionTrue, finished),
	}
	// Several unowned standalone Jobs so pass 3 appends well past the
	// observation slice's initial capacity (16) and reallocates the backing
	// array — the exact condition that used to drop the cron refinement.
	for i := 0; i < 24; i++ {
		objs = append(objs, makeBatchJob("default",
			"standalone-job-"+itoa(int64(i)), "", "Complete", metav1.ConditionTrue, finished))
	}

	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	cron := findCron(obs, "openbao-snapshot-save")
	if cron == nil {
		t.Fatalf("expected a cron-kind observation for openbao-snapshot-save; got none in %d observations", len(obs))
	}
	if cron.Status != ObsStatusFailed {
		t.Errorf("cron status = %q, want %q (a failed last run must never read as the optimistic default)", cron.Status, ObsStatusFailed)
	}
	if len(cron.Executions) != 1 {
		t.Fatalf("expected exactly 1 spawned-run Execution on the cron leaf, got %d", len(cron.Executions))
	}
	if cron.Executions[0].Status != ObsStatusFailed {
		t.Errorf("spawned-run Execution status = %q, want %q", cron.Executions[0].Status, ObsStatusFailed)
	}
	if cron.Executions[0].Name != "openbao-snapshot-save-28000000" {
		t.Errorf("spawned-run Execution name = %q, want the owned Job name", cron.Executions[0].Name)
	}
}

// TestListReconcilerObservations_StaleSuccessDoesNotMaskFreshFailure pins the
// recency rule: when the apiserver returns a CronJob's owned Jobs out of
// chronological order — an OLD Succeeded run after a FRESH Failed run — the
// cron's headline status must reflect the most-recent (Failed) run, never the
// stale success. Otherwise a CronJob that has started failing reads green.
func TestListReconcilerObservations_StaleSuccessDoesNotMaskFreshFailure(t *testing.T) {
	oldRun := time.Date(2026, 6, 17, 1, 0, 0, 0, time.UTC)
	freshRun := time.Date(2026, 6, 17, 3, 0, 0, 0, time.UTC)

	// Order matters: the stale SUCCEEDED run is listed LAST so a naive
	// last-write-wins would clobber the fresh FAILED status.
	objs := []runtime.Object{
		makeCronJob("openbao", "openbao-snapshot-save"),
		makeBatchJob("openbao", "run-fresh-failed", "openbao-snapshot-save", "Failed", metav1.ConditionTrue, freshRun),
		makeBatchJob("openbao", "run-old-ok", "openbao-snapshot-save", "Complete", metav1.ConditionTrue, oldRun),
	}

	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	cron := findCron(obs, "openbao-snapshot-save")
	if cron == nil {
		t.Fatalf("expected a cron-kind observation; got none")
	}
	if cron.Status != ObsStatusFailed {
		t.Errorf("cron status = %q, want %q — a stale success must not mask the fresh failure", cron.Status, ObsStatusFailed)
	}
	if len(cron.Executions) != 2 {
		t.Errorf("expected both runs recorded as Executions, got %d", len(cron.Executions))
	}
}

// TestListReconcilerObservations_CronJobWithNoRunIsPending pins the honest
// "scheduled, never run yet" state: a CronJob with no spawned Job surfaces
// as a cron observation with the pending default (NOT a fabricated success).
func TestListReconcilerObservations_CronJobWithNoRunIsPending(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(),
		reconcilerFakeClient(makeCronJob("trivy", "trivy-scan")))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	cron := findCron(obs, "trivy-scan")
	if cron == nil {
		t.Fatalf("expected a cron-kind observation for trivy-scan; got none")
	}
	if cron.Status != ObsStatusPending {
		t.Errorf("never-run cron status = %q, want %q", cron.Status, ObsStatusPending)
	}
	if len(cron.Executions) != 0 {
		t.Errorf("never-run cron should carry no Executions, got %d", len(cron.Executions))
	}
}
