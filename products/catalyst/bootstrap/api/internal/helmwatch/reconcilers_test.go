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

// makeSyftJob constructs a syft-grype SBOM-scan Job (issue #3919). variant
// drives the syft detection path exercised: "owner" (a CronJob ownerReference
// to the syft-grype CronJob — authoritative), "label" (the bp-syft-grype
// blueprint label, namespace-scoped), or "namespace-only" (a bare Job that
// only resides in the syft-grype namespace — the belt-and-braces fallback).
func makeSyftJob(name, variant string, fin time.Time) *unstructured.Unstructured {
	owner := ""
	if variant == "owner" {
		owner = syftGrypeCronName
	}
	u := makeBatchJob(syftGrypeNamespace, name, owner, "Complete", metav1.ConditionTrue, fin)
	meta := u.Object["metadata"].(map[string]any)
	switch variant {
	case "label":
		meta["labels"] = map[string]any{
			"catalyst.openova.io/blueprint": "bp-syft-grype",
			"app.kubernetes.io/instance":    "syft-grype",
		}
	case "namespace-only":
		// no labels, no owner — relies solely on the syft-grype namespace
	}
	return u
}

// setJobFailed flips a batch Job's terminal condition from "Complete" to
// "Failed" in place (status.conditions[0].type), so jobRunStatus reports it
// as a failed run. Used to model a scan run that errored.
func setJobFailed(u *unstructured.Unstructured) {
	status, _ := u.Object["status"].(map[string]any)
	conds, _ := status["conditions"].([]any)
	if len(conds) > 0 {
		if c, ok := conds[0].(map[string]any); ok {
			c["type"] = "Failed"
		}
	}
}

// TestListReconcilerObservations_Day2ScannersCollapseToOneIdentityRow pins
// the #3925 model (jobs-convergence-monitor-model.md §4 Surface B): Day-2
// security-scanner Jobs (trivy-operator AND syft-grype) are NOT excluded and
// are NOT one-row-per-run — each scanner COLLAPSES to exactly ONE
// identity-keyed task row carrying its run-history (all runs as Executions),
// so a 600-run flood is one row + 600 runs. This supersedes the earlier #3919
// "exclude entirely" disposition: a recurring scan is a finite job that
// recurs, so it belongs in the Jobs view as one row, never dropped.
func TestListReconcilerObservations_Day2ScannersCollapseToOneIdentityRow(t *testing.T) {
	t0 := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)
	objs := []runtime.Object{}

	// Simulate the live hw171 flood: a large fleet of trivy scan Jobs across
	// all three trivy detection paths. 600 stands in for the ~614 reports
	// observed live — this is the exact "600 runs behind it" case the model
	// names.
	trivyFleet := 600
	for i := 0; i < trivyFleet; i++ {
		var label string
		switch i % 3 {
		case 0:
			label = "managed-by"
		case 1:
			label = "resource-label"
		default:
			label = "name-only"
		}
		// Each run finishes at a distinct minute so recency is well-defined;
		// run i finishes at t0+i minutes, so the LAST one is the most recent.
		objs = append(objs, makeScanJob("scan-vulnerabilityreport-"+itoaTest(i), label, t0.Add(time.Duration(i)*time.Minute)))
	}

	// A fleet of syft-grype SBOM Jobs across all three syft detection paths,
	// PLUS the syft-grype CronJob itself (which seeds the identity row but
	// must NOT mint its own cron-syft-grype leaf).
	syftFleet := 30
	for i := 0; i < syftFleet; i++ {
		var variant string
		switch i % 3 {
		case 0:
			variant = "owner"
		case 1:
			variant = "label"
		default:
			variant = "namespace-only"
		}
		objs = append(objs, makeSyftJob("syft-grype-2900"+itoaTest(i), variant, t0.Add(time.Duration(i)*time.Minute)))
	}
	objs = append(objs, makeCronJob(syftGrypeNamespace, syftGrypeCronName))

	// A handful of REAL reconciler Jobs that must each still surface — incl. a
	// fixed-name Job whose name starts "scan-" but lives in a TENANT namespace
	// (NOT trivy-system) so it is a genuine task, not a scanner.
	objs = append(objs,
		makeBatchJob("cnpg", "cnpg-pair-primary-join", "", "Complete", metav1.ConditionTrue, t0),
		makeBatchJob("openbao", "openbao-init-29000111", "", "Complete", metav1.ConditionTrue, t0),
		makeBatchJob("gitea", "gitea-sso-configure", "", "Failed", metav1.ConditionTrue, t0),
		makeBatchJob("acme-prod", "scan-invoices-nightly", "", "Complete", metav1.ConditionTrue, t0),
	)

	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(objs...))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}

	// Count task + cron leaves. After collapse the task leaves are: the 4 real
	// reconcilers + exactly the 2 scanner identity rows. ZERO cron leaf for
	// syft (its CronJob folds onto the syft identity task row instead).
	taskLeaves := []string{}
	cronLeaves := []string{}
	for i := range obs {
		switch obs[i].Kind {
		case ReconcilerKindTask:
			taskLeaves = append(taskLeaves, obs[i].Name)
		case ReconcilerKindCron:
			cronLeaves = append(cronLeaves, obs[i].Name)
		}
	}

	// Exactly the 4 real reconciler Jobs + 2 collapsed scanner rows survive.
	wantTasks := map[string]bool{
		"cnpg-pair-primary-join": true,
		"openbao-init":           true, // run-suffix stripped from -29000111
		"gitea-sso-configure":    true,
		"scan-invoices-nightly":  true, // tenant "scan-*" Job is a REAL task
		trivyScanIdentity:        true, // collapsed trivy row
		syftScanIdentity:         true, // collapsed syft row
	}
	if len(taskLeaves) != len(wantTasks) {
		t.Fatalf("scanners NOT collapsed to one identity row: got %d task leaves %v, want exactly %d (4 real + 2 scanner). (pre-collapse ≈ %d trivy per-run rows)",
			len(taskLeaves), taskLeaves, len(wantTasks), trivyFleet)
	}
	for _, name := range taskLeaves {
		if !wantTasks[name] {
			t.Errorf("unexpected task leaf %q — only the 4 real reconcilers + 2 collapsed scanner rows should exist", name)
		}
	}

	// ZERO per-run scanner leaf leaked — no scan-vulnerabilityreport-* row, no
	// syft-grype-<n> row, no legacy security-scans summary row, no cron leaf.
	for _, name := range cronLeaves {
		if name == syftGrypeCronName {
			t.Errorf("the syft-grype scanner CronJob leaked a cron-%s leaf — it must collapse onto the %s row", name, syftScanIdentity)
		}
	}
	for i := range obs {
		n := obs[i].Name
		if strings.HasPrefix(n, "scan-vulnerabilityreport-") ||
			strings.HasPrefix(n, "syft-grype-") ||
			n == "security-scans" {
			t.Errorf("a per-run scanner leaf leaked into the flow: kind=%q name=%q — scanner runs must fold onto the identity row as Executions", obs[i].Kind, n)
		}
	}

	// The collapsed trivy row carries ALL 600 runs as run-history (run count =
	// number of Executions), exactly ONE row, Succeeded headline.
	trivy := findTask(obs, trivyScanIdentity)
	if trivy == nil {
		t.Fatalf("expected one collapsed %q task row; got none", trivyScanIdentity)
	}
	if len(trivy.Executions) != trivyFleet {
		t.Errorf("collapsed trivy row should carry %d runs (run-history); got %d Executions", trivyFleet, len(trivy.Executions))
	}
	if trivy.Status != ObsStatusSucceeded {
		t.Errorf("collapsed trivy row headline should be Succeeded (all runs complete); got %q", trivy.Status)
	}

	// The collapsed syft row carries all 30 spawned runs as run-history.
	syft := findTask(obs, syftScanIdentity)
	if syft == nil {
		t.Fatalf("expected one collapsed %q task row; got none", syftScanIdentity)
	}
	if len(syft.Executions) != syftFleet {
		t.Errorf("collapsed syft row should carry %d runs (run-history); got %d Executions", syftFleet, len(syft.Executions))
	}

	// The genuine reconciler Jobs each survived with the right status (no
	// flapping): gitea-sso-configure is Failed; the rest Succeeded.
	if g := findTask(obs, "gitea-sso-configure"); g == nil || g.Status != ObsStatusFailed {
		t.Errorf("gitea-sso-configure should survive as a Failed task, got %+v", g)
	}
	if c := findTask(obs, "cnpg-pair-primary-join"); c == nil || c.Status != ObsStatusSucceeded {
		t.Errorf("cnpg-pair-primary-join should survive as a Succeeded task, got %+v", c)
	}
}

// TestListReconcilerObservations_ScannerStickyHeadlineNoFlap pins the
// sticky-terminal property of the collapsed scanner row (#3925/#3918): the
// headline reflects the MOST RECENT run and never flaps to a stale run's
// status. A fresh Failed run arriving after an older Succeeded one must leave
// the row Failed; a later Succeeded run then recovers it — recency, not list
// order, decides.
func TestListReconcilerObservations_ScannerStickyHeadlineNoFlap(t *testing.T) {
	t0 := time.Date(2026, 6, 20, 1, 0, 0, 0, time.UTC)

	// List order is deliberately NON-chronological: the older Succeeded run is
	// listed AFTER the newer Failed run, so a naive last-wins would wrongly
	// report Succeeded. Recency resolution must pick the newer Failed.
	older := makeScanJob("scan-vulnerabilityreport-old", "managed-by", t0)                       // succeeded, older
	newer := makeScanJob("scan-vulnerabilityreport-new", "managed-by", t0.Add(10*time.Minute))   // failed, newer
	// flip the newer run to a failed condition.
	setJobFailed(newer)

	obs, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(newer, older))
	if err != nil {
		t.Fatalf("ListReconcilerObservations: %v", err)
	}
	trivy := findTask(obs, trivyScanIdentity)
	if trivy == nil {
		t.Fatalf("expected one collapsed %q row", trivyScanIdentity)
	}
	if len(trivy.Executions) != 2 {
		t.Errorf("expected 2 runs in run-history; got %d", len(trivy.Executions))
	}
	if trivy.Status != ObsStatusFailed {
		t.Errorf("sticky headline should reflect the most-recent (Failed) run regardless of list order; got %q", trivy.Status)
	}

	// A still-later Succeeded run recovers the headline (real run status, not a
	// frozen terminal — finite jobs ARE finite, the latest run wins).
	recovered := makeScanJob("scan-vulnerabilityreport-recover", "managed-by", t0.Add(20*time.Minute))
	obs2, err := ListReconcilerObservations(context.Background(), reconcilerFakeClient(newer, older, recovered))
	if err != nil {
		t.Fatalf("ListReconcilerObservations(recover): %v", err)
	}
	if r := findTask(obs2, trivyScanIdentity); r == nil || r.Status != ObsStatusSucceeded {
		t.Errorf("a newer Succeeded run should recover the headline; got %+v", r)
	}
}

// itoaTest — tiny local int→string for the test (avoids a strconv import for
// one call site and matches the package's no-fmt helper style).
func itoaTest(n int) string { return itoa(int64(n)) }
