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
	"strings"
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

// makeKustomization constructs a Flux Kustomization unstructured object with
// a single Ready condition (status + reason) so statusFromReadyCondition can
// be driven through ListReconcilerObservations.
func makeKustomization(namespace, name string, readyStatus metav1.ConditionStatus, reason string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
			"kind":       "Kustomization",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
			"status": map[string]any{
				"conditions": []any{
					map[string]any{
						"type":               "Ready",
						"status":             string(readyStatus),
						"reason":             reason,
						"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
					},
				},
			},
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Kind: "Kustomization"})
	return u
}

func findReconcile(obs []ReconcilerObservation, name string) *ReconcilerObservation {
	for i := range obs {
		if obs[i].Kind == ReconcilerKindReconcile && obs[i].Name == name {
			return &obs[i]
		}
	}
	return nil
}

func findTask(obs []ReconcilerObservation, name string) *ReconcilerObservation {
	for i := range obs {
		if obs[i].Kind == ReconcilerKindTask && obs[i].Name == name {
			return &obs[i]
		}
	}
	return nil
}

// TestStatusFromReadyCondition_TransientFalseIsRunningNotFailed pins the
// anti-flap contract (issue #3916): a Kustomization that is still
// reconciling — Ready=False with a transient build/health/dependency reason
// Flux will RETRY — must read as running (in-progress), NEVER a terminal
// Failed that flaps back to Succeeded on the next poll. Only Flux's terminal
// "Stalled" reason is a genuine terminal failure.
func TestStatusFromReadyCondition_TransientFalseIsRunningNotFailed(t *testing.T) {
	transient := []string{
		"BuildFailed", "HealthCheckFailed", "ReconciliationFailed",
		"Progressing", "DependencyNotReady", "ArtifactFailed",
	}
	for _, reason := range transient {
		obs, err := ListReconcilerObservations(context.Background(),
			reconcilerFakeClient(makeKustomization("flux-system", "apps-"+reason, metav1.ConditionFalse, reason)))
		if err != nil {
			t.Fatalf("ListReconcilerObservations(%s): %v", reason, err)
		}
		rec := findReconcile(obs, "apps-"+reason)
		if rec == nil {
			t.Fatalf("reason=%s: no reconcile observation", reason)
		}
		if rec.Status == ObsStatusFailed {
			t.Errorf("reason=%s: a still-reconciling Kustomization read TERMINAL failed — this is the flap (#3916); want running", reason)
		}
		if rec.Status != ObsStatusRunning {
			t.Errorf("reason=%s: status = %q, want %q (in-progress)", reason, rec.Status, ObsStatusRunning)
		}
	}
}

// TestStatusFromReadyCondition_StalledIsTerminalFailed pins the converse: a
// Kustomization Flux has GIVEN UP on (Ready=False, reason=Stalled) is a
// genuine terminal failure and must surface as Failed.
func TestStatusFromReadyCondition_StalledIsTerminalFailed(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(),
		reconcilerFakeClient(makeKustomization("flux-system", "apps-dead", metav1.ConditionFalse, "Stalled")))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	rec := findReconcile(obs, "apps-dead")
	if rec == nil {
		t.Fatal("no reconcile observation for the stalled Kustomization")
	}
	if rec.Status != ObsStatusFailed {
		t.Errorf("stalled Kustomization status = %q, want %q (terminal)", rec.Status, ObsStatusFailed)
	}
}

// TestStatusFromReadyCondition_ReadyTrueIsSucceeded keeps the happy path
// honest: Ready=True is a stable terminal success.
func TestStatusFromReadyCondition_ReadyTrueIsSucceeded(t *testing.T) {
	obs, err := ListReconcilerObservations(context.Background(),
		reconcilerFakeClient(makeKustomization("flux-system", "apps-ok", metav1.ConditionTrue, "ReconciliationSucceeded")))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	rec := findReconcile(obs, "apps-ok")
	if rec == nil || rec.Status != ObsStatusSucceeded {
		t.Fatalf("Ready=True Kustomization want succeeded, got %+v", rec)
	}
}

// TestListReconcilerObservations_TaskRunsCollapseToOneStableLeaf pins the
// accumulation fix (issue #3916): N standalone Jobs that share a base
// identity (a generateName, or a controller-appended run suffix) collapse
// into ONE task-<base> leaf carrying N Executions — NOT N separate leaves.
// Before the fix each run minted task-<unique-run-name>, growing the /jobs
// model unbounded as Flux re-created the Jobs on every reconcile.
func TestListReconcilerObservations_TaskRunsCollapseToOneStableLeaf(t *testing.T) {
	t0 := time.Date(2026, 6, 19, 1, 0, 0, 0, time.UTC)

	// Three runs of the same logical task, named via the generateName
	// convention base + "-<5-rand>" the apiserver appends.
	objs := []runtime.Object{}
	mk := func(name string, gen string, fin time.Time) *unstructured.Unstructured {
		u := makeBatchJob("data", name, "", "Complete", metav1.ConditionTrue, fin)
		if gen != "" {
			meta := u.Object["metadata"].(map[string]any)
			meta["generateName"] = gen
		}
		return u
	}
	objs = append(objs,
		mk("db-migrate-a1b2c", "db-migrate-", t0),
		mk("db-migrate-d3e4f", "db-migrate-", t0.Add(time.Hour)),
		mk("db-migrate-g5h6j", "db-migrate-", t0.Add(2*time.Hour)),
	)

	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	// Exactly ONE task leaf, keyed by the stable base "db-migrate".
	taskCount := 0
	for i := range obs {
		if obs[i].Kind == ReconcilerKindTask {
			taskCount++
		}
	}
	if taskCount != 1 {
		t.Fatalf("accumulation bug: want exactly 1 stable task leaf, got %d (one per run = the #3916 unbounded growth)", taskCount)
	}
	task := findTask(obs, "db-migrate")
	if task == nil {
		t.Fatalf("no task leaf keyed by the stable base name 'db-migrate'; got %d task observations", taskCount)
	}
	if len(task.Executions) != 3 {
		t.Errorf("the 3 runs must record as 3 Executions under the one leaf, got %d", len(task.Executions))
	}
}

// TestListReconcilerObservations_TaskRunSuffixStrippedFromName pins the
// run-suffix stripping for Jobs without a generateName (re-created by Flux):
// a numeric CronJob-style stamp and a 5-char random hash both collapse to the
// same base, while a meaningful trailing word is preserved.
func TestListReconcilerObservations_TaskRunSuffixStrippedFromName(t *testing.T) {
	fin := time.Date(2026, 6, 19, 1, 0, 0, 0, time.UTC)
	objs := []runtime.Object{
		makeBatchJob("data", "snapshot-28999111", "", "Complete", metav1.ConditionTrue, fin),
		makeBatchJob("data", "snapshot-29000222", "", "Complete", metav1.ConditionTrue, fin.Add(time.Hour)),
		// A fixed-name Job whose final segment is a real word — must NOT
		// be truncated.
		makeBatchJob("data", "cnpg-pair-primary-join", "", "Complete", metav1.ConditionTrue, fin),
	}
	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	if findTask(obs, "snapshot") == nil {
		t.Error("numeric run-suffix Jobs should collapse to task 'snapshot'")
	}
	if findTask(obs, "cnpg-pair-primary-join") == nil {
		t.Error("a fixed-name Job's meaningful trailing word was wrongly stripped")
	}
	// And the two numeric runs are ONE leaf.
	n := 0
	for i := range obs {
		if obs[i].Kind == ReconcilerKindTask && obs[i].Name == "snapshot" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("numeric-suffix runs want 1 stable leaf, got %d", n)
	}
}

// makeScanJob constructs a trivy-operator security-scan Job (issue #3919) —
// the kind that floods the /jobs canvas. label drives which detection path
// is exercised: "managed-by" (app.kubernetes.io/managed-by=trivy-operator),
// "resource-label" (a trivy-operator.* label key), or "name-only" (scan-*
// name in trivy-system with NO trivy labels — the belt-and-braces fallback).
func makeScanJob(name, label string, fin time.Time) *unstructured.Unstructured {
	u := makeBatchJob(trivyScanNamespace, name, "", "Complete", metav1.ConditionTrue, fin)
	meta := u.Object["metadata"].(map[string]any)
	switch label {
	case "managed-by":
		meta["labels"] = map[string]any{"app.kubernetes.io/managed-by": "trivy-operator"}
	case "resource-label":
		meta["labels"] = map[string]any{"trivy-operator.resource.kind": "Pod", "trivy-operator.resource.name": "some-pod"}
	case "name-only":
		// no labels — relies on the scan-* name + trivy-system namespace
	}
	return u
}

// TestListReconcilerObservations_SecurityScansCollapseToOneSummary pins the
// #3919 fix: trivy-operator security-scan Jobs (hundreds, one per workload,
// ever-growing) MUST NOT each mint a task-* row — they collapse into ONE
// "security-scans" summary leaf, so the /jobs canvas shows the real platform
// reconcilers, not a scan flood. A handful of genuine reconciler Jobs in the
// same list are unaffected.
func TestListReconcilerObservations_SecurityScansCollapseToOneSummary(t *testing.T) {
	t0 := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	objs := []runtime.Object{}

	// Simulate the live hw171 flood: a large fleet of scan Jobs across the
	// three trivy detection paths (managed-by label, trivy-operator.* label,
	// and scan-* name in trivy-system). 200 scan Jobs stands in for the ~600
	// reports observed live.
	scanFleet := 200
	for i := 0; i < scanFleet; i++ {
		var label string
		switch i % 3 {
		case 0:
			label = "managed-by"
		case 1:
			label = "resource-label"
		default:
			label = "name-only"
		}
		objs = append(objs, makeScanJob("scan-vulnerabilityreport-"+itoaTest(i), label, t0))
	}

	// A handful of REAL reconciler Jobs that must each still surface.
	objs = append(objs,
		makeBatchJob("cnpg", "cnpg-pair-primary-join", "", "Complete", metav1.ConditionTrue, t0),
		makeBatchJob("openbao", "openbao-init-29000111", "", "Complete", metav1.ConditionTrue, t0),
		makeBatchJob("gitea", "gitea-sso-configure", "", "Failed", metav1.ConditionTrue, t0),
	)

	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	// Count task leaves. With 200 scan Jobs + 3 real Jobs, the PRE-FIX model
	// would yield ~203 task rows; the fix yields 3 real + 1 summary = 4.
	taskCount := 0
	for i := range obs {
		if obs[i].Kind == ReconcilerKindTask {
			taskCount++
		}
	}
	if taskCount > 10 {
		t.Fatalf("scan flood NOT collapsed: got %d task leaves (≈the %d scan Jobs). The #3919 fix must drop these to ~the real-reconciler count.", taskCount, scanFleet)
	}

	// Exactly ONE collapsed summary leaf, carrying the scan count.
	summary := findTask(obs, SecurityScanSummaryName)
	if summary == nil {
		t.Fatalf("no collapsed '%s' summary leaf — scanning must still be visible to the operator as one row", SecurityScanSummaryName)
	}
	if !strings.Contains(summary.Message, itoaTest(scanFleet)) {
		t.Errorf("summary leaf must report the scan count %d, got message %q", scanFleet, summary.Message)
	}

	// The 3 real reconciler Jobs each survived as their own leaf.
	for _, want := range []string{"cnpg-pair-primary-join", "openbao-init", "gitea-sso-configure"} {
		if findTask(obs, want) == nil {
			t.Errorf("real reconciler Job %q was wrongly swept into the scan collapse", want)
		}
	}

	// And NO scan-* per-workload leaf leaked through.
	for i := range obs {
		if obs[i].Kind == ReconcilerKindTask && strings.HasPrefix(obs[i].Name, "scan-vulnerabilityreport-") {
			t.Errorf("a per-workload scan leaf leaked: %q — every scan Job must fold into the summary", obs[i].Name)
		}
	}
}

// itoaTest — tiny local int→string for the test (avoids a strconv import for
// one call site and matches the package's no-fmt helper style).
func itoaTest(n int) string { return itoa(int64(n)) }
