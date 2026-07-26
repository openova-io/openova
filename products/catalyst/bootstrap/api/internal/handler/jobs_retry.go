// jobs_retry.go — the ONE generic, Flux-native remediation write surface
// for the jobs canvas (issue #3646 §5c).
//
// Before this, the canvas was observe-only: 8+ Failed/stuck activities the
// operator could SEE but not ACT on, with the only recourse being to leave
// the console for a terminal (`flux reconcile hr ...`, `kubectl create job
// --from=cronjob/...`). This endpoint closes that gap with ONE handler that
// dispatches on the leaf's typed Kind (§4b) and re-drives the reconcile via
// the IN-CLUSTER DYNAMIC CLIENT — never an exec.Command / shell-out
// (PRINCIPLES.md #61, ARCHITECTURE.md §"What's user-facing"):
//
//	install-<chart>    (HelmRelease)    → annotate reconcile.fluxcd.io/requestedAt
//	reconcile-<name>   (Kustomization)  → annotate reconcile.fluxcd.io/requestedAt
//	reconciler-<name>  (Deployment)     → annotate the Deployment pod-template
//	                                       to trigger a fresh rollout
//	cron-<name>        (CronJob)        → create a one-off Job from jobTemplate
//	task-<name>        (batch Job)      → annotate the owner / re-drive
//	mutation-<verb>-<kind> (XRC)        → re-submit (bump reconcile annotation)
//
// Each remediation: (1) is owner-checked by the SAME checkOwnership gate the
// GET endpoints use (404 on cross-tenant); (2) is RBAC-gated to operator tier
// or higher (403 otherwise); (3) writes a NEW Execution on the same row with
// the operator's identity in the first LogLine ("[retry] requested by <sub>
// at <ts>") so attempt N+1 is an auditable record; (4) NEVER shells out.
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// reconcileRequestedAtAnnotation — the Flux-native "reconcile now" intent.
// Bumping it to a fresh timestamp makes source/helm/kustomize-controller
// re-reconcile the object on its next pass. This is THE documented operator
// recovery primitive (the same annotation `flux reconcile` writes).
const reconcileRequestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"

// RetryJob handles POST /api/v1/deployments/{depId}/jobs/{jobId}/retry.
//
// Response:
//
//	200 — retry dispatched; body carries the new executionId + the kind +
//	      the action taken.
//	400 — missing path params / unsupported kind for retry.
//	403 — caller lacks operator RBAC.
//	404 — unknown deployment / cross-tenant / unknown job.
//	409 — the activity is not in a Failed/degraded state (nothing to retry).
//	502 — the in-cluster client/annotation write failed.
//	503 — jobs store unavailable.
func (h *Handler) RetryJob(w http.ResponseWriter, r *http.Request) {
	st := h.jobsStore()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "jobs-store-unavailable",
		})
		return
	}
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	jobID := strings.TrimSpace(chi.URLParam(r, "jobId"))
	if depID == "" || jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-path-params",
		})
		return
	}

	// Resolve the deployment + ownership gate (404 on cross-tenant), the
	// SAME gate the GET endpoints use. chrootEnsureDeployment lets the
	// Sovereign-side catalyst-api serve its imported deployment by id.
	dep := h.chrootEnsureDeployment(depID)
	if dep == nil {
		if val, ok := h.deployments.Load(depID); ok {
			dep = val.(*Deployment)
		}
	}
	if dep == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "deployment not found"})
		return
	}
	if !h.checkOwnership(w, r, dep) {
		return // 404 already written
	}

	// RBAC: remediation is an operator action. A viewer/developer session
	// gets 403 — the canonical operator-or-higher gate. nil claims (CI /
	// tests building Handler{} directly) pass through.
	claims := auth.ClaimsFromContext(r.Context())
	if !jobRetryCallerAuthorized(claims, dep) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "forbidden",
			"detail": "retrying a reconcile requires operator tier or higher",
		})
		return
	}

	job, _, err := st.GetJob(depID, jobID)
	if errors.Is(err, jobs.ErrNotFound) {
		// Cold store (e.g. first hit after a Pod restart) — lazily seed the
		// per-deployment store from the live cluster, then resolve again.
		// Seeding ONLY when the leaf is missing is deliberate (#4731): the
		// re-seed refreshes every leaf's status from the live cluster, and
		// the retry path must NOT let that flip the status of the very leaf
		// the operator is retrying (e.g. a Failed leaf whose stale completed
		// Job object still lingers in-cluster). The /jobs read paths own the
		// live-status refresh; here we only need the leaf to exist.
		h.chrootSeedJobsStoreIfEmpty(r.Context(), dep)
		job, _, err = st.GetJob(depID, jobID)
	}
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job-not-found"})
			return
		}
		h.log.Error("RetryJob: load failed", "depId", depID, "jobId", jobID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store-read-failed"})
		return
	}

	// Only Failed (one-shot) or degraded/failing (health) rows are
	// retryable — re-driving a healthy/running reconcile is a no-op the UI
	// shouldn't offer, so reject it honestly rather than silently bumping.
	if !jobRetryableStatus(job.Status) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "not-retryable",
			"detail": fmt.Sprintf("job status %q is not a failed/degraded state", job.Status),
		})
		return
	}

	// Build the dynamic client against the target cluster (in-cluster on a
	// Sovereign chroot; the posted-back kubeconfig on the mother).
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil {
		h.log.Warn("RetryJob: dynamic client unavailable", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "cluster-client-unavailable",
			"detail": err.Error(),
		})
		return
	}

	operator := retryOperatorIdentity(claims, r)
	now := time.Now().UTC()
	action, rerr := h.dispatchRetry(r.Context(), dyn, job, now)
	if rerr != nil {
		// #3379: a cutover-step Re-run that arrives while the engine already
		// holds its single-run flag is a CONFLICT, not a fault — the same
		// answer HandleCutoverStart gives a concurrent /start. Spawning a
		// second engine goroutine would race the first over the step Jobs.
		if errors.Is(rerr, errCutoverRunInFlight) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "cutover-in-progress",
				"detail": rerr.Error(),
			})
			return
		}
		// #4845: a leaf with no directly-retryable backing (aggregate
		// trivy-operator scan, mutation-bridge, unsupported kind) is a
		// client-side condition, not a server fault — return a graceful 422
		// with an actionable message instead of a raw 502 so the operator
		// sees "not directly retryable" rather than a gateway error.
		if errors.Is(rerr, errNotDirectlyRetryable) {
			h.log.Info("RetryJob: leaf not directly retryable", "depId", depID, "jobId", jobID, "kind", job.Kind, "detail", rerr.Error())
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
				"error":  "not-directly-retryable",
				"detail": rerr.Error(),
			})
			return
		}
		h.log.Warn("RetryJob: remediation failed", "depId", depID, "jobId", jobID, "kind", job.Kind, "err", rerr)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "remediation-failed",
			"detail": rerr.Error(),
		})
		return
	}

	// Record attempt N+1 as a NEW Execution on the same row with the
	// operator's identity in the first LogLine — the audit trail the
	// Execution-per-attempt model (types.go:259) was built for.
	execID := ""
	if exec, e := st.StartExecution(depID, job.JobName, now); e != nil {
		// The remediation already fired; failing to record the Execution
		// must not 500 the operator — log + return the action.
		h.log.Warn("RetryJob: record execution failed", "depId", depID, "jobId", jobID, "err", e)
	} else {
		execID = exec.ID
		_ = st.AppendLogLines(depID, exec.ID, []jobs.LogLine{{
			Timestamp: now,
			Level:     jobs.LevelInfo,
			Message:   fmt.Sprintf("[retry] requested by %s at %s — %s", operator, now.Format(time.RFC3339), action),
		}})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":          job.ID,
		"kind":        job.Kind,
		"action":      action,
		"executionId": execID,
		"requestedAt": now.Format(time.RFC3339),
		"requestedBy": operator,
	})
}

// dispatchRetry performs the kind-specific Flux-native remediation and
// returns a short human description of the action taken. Pure dispatch on
// job.Kind — ONE switch, ZERO per-app branching (issue #3646 §6/§15).
func (h *Handler) dispatchRetry(ctx context.Context, dyn dynamic.Interface, job jobs.Job, now time.Time) (string, error) {
	name := retryTargetName(job)
	switch job.Kind {
	case jobs.KindInstall:
		// HelmRelease in flux-system.
		if err := triggerReconcileAnnotation(ctx, dyn, helmwatch.HelmReleaseGVR, helmwatch.FluxNamespace, "bp-"+name, now); err != nil {
			return "", err
		}
		return "annotated HelmRelease bp-" + name + " for reconcile", nil

	case jobs.KindReconcile:
		// Flux Kustomization — discover its namespace, then annotate.
		ns, err := resolveObjectNamespace(ctx, dyn, helmwatch.KustomizationGVR, name)
		if err != nil {
			return "", err
		}
		if err := triggerReconcileAnnotation(ctx, dyn, helmwatch.KustomizationGVR, ns, name, now); err != nil {
			return "", err
		}
		return "annotated Kustomization " + name + " for reconcile", nil

	case jobs.KindReconciler:
		// Reconciler Deployment — bump the pod-template annotation to roll
		// it (the Flux-native equivalent for a non-HR reconciler workload).
		ns, err := resolveObjectNamespace(ctx, dyn, helmwatch.DeploymentGVR, name)
		if err != nil {
			return "", err
		}
		if err := triggerDeploymentRollout(ctx, dyn, ns, name, now); err != nil {
			return "", err
		}
		return "triggered rollout of Deployment " + name, nil

	case jobs.KindCron:
		// CronJob — create a one-off Job from its jobTemplate ("Run now").
		ns, err := resolveObjectNamespace(ctx, dyn, helmwatch.CronJobGVR, name)
		if err != nil {
			return "", err
		}
		runName, err := createJobFromCronJob(ctx, dyn, ns, name, now)
		if err != nil {
			return "", err
		}
		return "created one-off Job " + runName + " from CronJob " + name, nil

	case jobs.KindStep:
		// One step of a projected activity ("cutover-step-<slug>"). Re-drives
		// the cutover engine in operator-retry mode so the failed step's
		// stale Job is deleted + re-run and the chain resumes — see
		// jobs_retry_cutover_step.go (issue #3379, UAT row 165).
		return h.retryActivityStep(ctx, job, now)

	case jobs.KindMutation:
		// Crossplane XRC re-submit — bump the catalyst reconcile annotation
		// on the stored claim. Best-effort: the claim GVR is not always
		// resolvable from the leaf alone, so we re-drive via the HR fallback
		// when the mutation maps to a bp-* release.
		return "", fmt.Errorf("mutation re-submit is driven by the mutation bridge; retry via the originating Day-2 action: %w", errNotDirectlyRetryable)

	case jobs.KindTask:
		// Standalone batch Job. A finished Job is TERMINAL — its
		// spec.template is immutable, so patching/re-applying it in place
		// fails with `Job spec.template field is immutable` (the live-walk
		// 502). The honest re-run is to DELETE the old Job and CREATE a
		// fresh one from its spec.template with the server-managed +
		// immutable fields stripped. A Job OWNED by a CronJob is re-driven
		// through its CronJob ("Run now") instead — recreating a
		// CronJob-managed Job standalone would orphan it from its owner.
		ns, err := resolveObjectNamespace(ctx, dyn, helmwatch.JobGVR, name)
		if err != nil {
			return "", err
		}
		old, err := dyn.Resource(helmwatch.JobGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", fmt.Errorf("get Job %s/%s: %w", ns, name, err)
		}
		if cron := cronJobOwnerName(old); cron != "" {
			runName, cerr := createJobFromCronJob(ctx, dyn, ns, cron, now)
			if cerr != nil {
				return "", cerr
			}
			return "created one-off Job " + runName + " from owning CronJob " + cron, nil
		}
		if err := recreateStandaloneJob(ctx, dyn, ns, old, now); err != nil {
			return "", err
		}
		return "deleted + recreated Job " + name + " for a fresh run", nil

	default:
		return "", fmt.Errorf("kind %q does not support retry: %w", job.Kind, errNotDirectlyRetryable)
	}
}

// errNotDirectlyRetryable marks a retry request against a leaf that has no
// directly-retryable backing resource — an aggregate/operator-managed
// reconciler (trivy-operator scan aggregate, a mutation bridged via a Day-2
// action, or a kind with no retry mechanism). It is a client-side condition
// (nothing to re-run here), NOT a server fault, so RetryJob maps it to a
// graceful 422 with an actionable message instead of a raw 502. #4845.
var errNotDirectlyRetryable = errors.New("reconciler is aggregate or operator-managed; not directly retryable")

// retryTargetName derives the underlying object's name from a leaf Job by
// stripping its kind prefix. The install leaf carries the bare chart in
// AppID; the §5a kinds carry the object name in AppID too (set by the
// reconciler bridge). Fall back to JobName-prefix-strip when AppID is empty.
func retryTargetName(job jobs.Job) string {
	if strings.TrimSpace(job.AppID) != "" {
		return job.AppID
	}
	jn := job.JobName
	for _, pfx := range []string{
		jobs.JobNamePrefix, jobs.ReconcileJobPrefix, jobs.CronJobPrefix,
		jobs.TaskJobPrefix, jobs.ReconcilerJobPrefix,
	} {
		if strings.HasPrefix(jn, pfx) {
			return strings.TrimPrefix(jn, pfx)
		}
	}
	return jn
}

// triggerReconcileAnnotation patches the object's
// reconcile.fluxcd.io/requestedAt annotation to `now` via a strategic
// merge over the dynamic client — NO shell-out. Idempotent + safe to repeat.
func triggerReconcileAnnotation(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, ns, name string, now time.Time) error {
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q}}}`,
		reconcileRequestedAtAnnotation, now.Format(time.RFC3339Nano),
	))
	_, err := dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("annotate %s/%s for reconcile: %w", ns, name, err)
	}
	return nil
}

// triggerDeploymentRollout bumps a kubectl-style restart annotation on the
// Deployment's pod template so the workload rolls — the standard
// non-destructive "restart this reconciler" intent, via the dynamic client.
func triggerDeploymentRollout(ctx context.Context, dyn dynamic.Interface, ns, name string, now time.Time) error {
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q,%q:%q}}}}}`,
		now.Format(time.RFC3339Nano), reconcileRequestedAtAnnotation, now.Format(time.RFC3339Nano),
	))
	_, err := dyn.Resource(helmwatch.DeploymentGVR).Namespace(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("roll Deployment %s/%s: %w", ns, name, err)
	}
	return nil
}

// createJobFromCronJob materialises a one-off Job from a CronJob's
// jobTemplate ("Run now"), via the dynamic client. The new Job is named
// "<cron>-manual-<unix>" so successive runs never collide and the operator
// can map it back in `kubectl get jobs`.
func createJobFromCronJob(ctx context.Context, dyn dynamic.Interface, ns, cronName string, now time.Time) (string, error) {
	cj, err := dyn.Resource(helmwatch.CronJobGVR).Namespace(ns).Get(ctx, cronName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get CronJob %s/%s: %w", ns, cronName, err)
	}
	jobSpec, found, err := unstructured.NestedMap(cj.Object, "spec", "jobTemplate", "spec")
	if err != nil || !found {
		return "", fmt.Errorf("CronJob %s/%s has no jobTemplate.spec", ns, cronName)
	}
	runName := fmt.Sprintf("%s-manual-%d", cronName, now.Unix())
	if len(runName) > 63 {
		runName = runName[:63]
	}
	newJob := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      runName,
			"namespace": ns,
			"labels": map[string]any{
				"catalyst.openova.io/manual-run":   "true",
				"catalyst.openova.io/from-cronjob": cronName,
			},
			"annotations": map[string]any{
				reconcileRequestedAtAnnotation: now.Format(time.RFC3339Nano),
			},
		},
		"spec": jobSpec,
	}}
	if _, err := dyn.Resource(helmwatch.JobGVR).Namespace(ns).Create(ctx, newJob, metav1.CreateOptions{}); err != nil {
		return "", fmt.Errorf("create one-off Job %s/%s: %w", ns, runName, err)
	}
	return runName, nil
}

// cronJobOwnerName returns the name of the CronJob that owns this Job, or ""
// when the Job is standalone. A Job materialised by a CronJob carries an
// ownerReference of Kind=CronJob; re-running such a Job must go through the
// CronJob ("Run now"), never a standalone recreate that would orphan it.
func cronJobOwnerName(job *unstructured.Unstructured) string {
	for _, ref := range job.GetOwnerReferences() {
		if ref.Kind == "CronJob" {
			return ref.Name
		}
	}
	return ""
}

// recreateStandaloneJob DELETES a terminal standalone Job and CREATES a fresh
// one from its spec.template. A finished Job's spec.template is IMMUTABLE, so
// re-applying or patching it in place returns `Job spec.template field is
// immutable` (the live-walk 502). Recreating is the only honest re-run.
//
// The fresh Job is built from the old spec with every server-managed +
// immutable field stripped so the apiserver (and the Job controller)
// regenerate them cleanly:
//   - spec.selector + spec.template.metadata.labels[batch.kubernetes.io/*] /
//     [controller-uid] / [job-name] — the controller-owned label selector the
//     batch controller stamps; reusing it collides with the new Job's
//     generated identity.
//   - The old object's status + metadata.{resourceVersion,uid,
//     creationTimestamp,generation,ownerReferences,managedFields} are simply
//     never copied onto the new object — only spec + a clean metadata
//     (name/namespace/labels/annotations) are carried, so the apiserver
//     rebuilds the rest on create.
//
// The new Job is named "<name>-rerun-<unix>" (not the old name) so the create
// can't race the still-terminating old object, and the old one is deleted
// first with propagationPolicy=Background so its Pods are GC'd. The leaf's
// AppID identity in the jobs canvas is unchanged — a re-run surfaces as a new
// Execution on the same row, not a new leaf.
func recreateStandaloneJob(ctx context.Context, dyn dynamic.Interface, ns string, old *unstructured.Unstructured, now time.Time) error {
	name := old.GetName()

	// Build the fresh Job spec from the old one, stripping immutable +
	// controller-managed fields.
	spec, found, err := unstructured.NestedMap(old.Object, "spec")
	if err != nil || !found {
		return fmt.Errorf("Job %s/%s has no spec to recreate from", ns, name)
	}
	// spec.selector is generated by the Job controller from the
	// controller-uid; it is immutable + must not be carried to the new Job.
	delete(spec, "selector")
	// Drop the controller-stamped pod-template labels that encode the OLD
	// Job's identity. Keeping them makes the new Job's pods adopt the old
	// selector → AlreadyExists / mismatched-selector rejection.
	stripControllerPodTemplateLabels(spec)

	runName := fmt.Sprintf("%s-rerun-%d", name, now.Unix())
	if len(runName) > 63 {
		runName = runName[:63]
	}

	// Carry forward the operator-meaningful labels/annotations from the old
	// Job's own metadata (not the pod template), minus anything server-owned.
	labels := map[string]any{
		"catalyst.openova.io/rerun-of": name,
	}
	for k, v := range old.GetLabels() {
		if isControllerOwnedLabel(k) {
			continue
		}
		if s, ok := any(v).(string); ok {
			labels[k] = s
		} else {
			labels[k] = v
		}
	}

	newJob := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "Job",
		"metadata": map[string]any{
			"name":      runName,
			"namespace": ns,
			"labels":    labels,
			"annotations": map[string]any{
				reconcileRequestedAtAnnotation: now.Format(time.RFC3339Nano),
			},
		},
		"spec": spec,
	}}

	// Delete the old Job first (Background so the apiserver GCs its Pods),
	// then create the fresh one. Recreating under a NEW name avoids a
	// delete/create race against the still-terminating old object.
	propagation := metav1.DeletePropagationBackground
	if err := dyn.Resource(helmwatch.JobGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{
		PropagationPolicy: &propagation,
	}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete completed Job %s/%s: %w", ns, name, err)
	}
	if _, err := dyn.Resource(helmwatch.JobGVR).Namespace(ns).Create(ctx, newJob, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("recreate Job %s/%s as %s: %w", ns, name, runName, err)
	}
	return nil
}

// stripControllerPodTemplateLabels removes the Job-controller-owned labels
// from spec.template.metadata.labels so the recreated Job's pod template no
// longer carries the OLD Job's identity selector.
func stripControllerPodTemplateLabels(spec map[string]any) {
	tmplLabels, found, err := unstructured.NestedMap(spec, "template", "metadata", "labels")
	if err != nil || !found {
		return
	}
	for k := range tmplLabels {
		if isControllerOwnedLabel(k) {
			delete(tmplLabels, k)
		}
	}
	if len(tmplLabels) == 0 {
		// An empty labels map is harmless, but drop it so the template
		// metadata stays minimal.
		_ = unstructured.SetNestedMap(spec, map[string]any{}, "template", "metadata", "labels")
		return
	}
	_ = unstructured.SetNestedMap(spec, tmplLabels, "template", "metadata", "labels")
}

// isControllerOwnedLabel reports whether a label key is stamped + owned by the
// Job/batch controller (so it must be stripped before recreate).
func isControllerOwnedLabel(k string) bool {
	switch k {
	case "controller-uid", "job-name":
		return true
	}
	return strings.HasPrefix(k, "batch.kubernetes.io/")
}

// resolveObjectNamespace lists the GVR and returns the namespace of the
// object with the given name. Reconciler/Cron/Task/Kustomization leaves
// don't persist their namespace on the Job, so we re-discover it at retry
// time (the list is cheap + the name is unique within the cluster for these
// reconciler objects). Returns an error when no/many matches.
func resolveObjectNamespace(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string) (string, error) {
	list, err := dyn.Resource(gvr).Namespace("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("list %s to resolve namespace of %q: %w", gvr.Resource, name, err)
	}
	ns := ""
	for i := range list.Items {
		if list.Items[i].GetName() == name {
			if ns != "" && ns != list.Items[i].GetNamespace() {
				return "", fmt.Errorf("%s %q exists in multiple namespaces; cannot disambiguate for retry", gvr.Resource, name)
			}
			ns = list.Items[i].GetNamespace()
		}
	}
	if ns == "" {
		// #4845: a synthetic AGGREGATE reconciler (e.g. the "Trivy Security
		// Scan" row aggregating trivy-operator's scan-vulnerabilityreport-*
		// Jobs) has no standalone Job named after the reconciler, so the
		// resolve fails. That is NOT a server fault — the row simply has no
		// directly-retryable backing. Wrap with errNotDirectlyRetryable so
		// the HTTP handler returns a graceful 422 instead of a raw 502.
		return "", fmt.Errorf("%s %q not found in any namespace: %w", gvr.Resource, name, errNotDirectlyRetryable)
	}
	return ns, nil
}

// jobRetryableStatus reports whether a leaf's status warrants a retry
// affordance: a one-shot Failed, or a health-axis degraded/failing.
func jobRetryableStatus(status string) bool {
	switch status {
	case jobs.StatusFailed, jobs.StatusDegraded, jobs.StatusFailing:
		return true
	}
	return false
}

// jobRetryCallerAuthorized — operator-tier-or-higher gate, mirroring
// execSessionCallerAuthorized's shape but at the operator (not developer)
// floor because remediation mutates cluster state. nil claims pass through
// (CI/tests). The registered Sovereign operator is owner-equivalent.
func jobRetryCallerAuthorized(claims *auth.Claims, dep *Deployment) bool {
	if claims == nil {
		return true
	}
	if applicationInstallCallerAuthorized(claims) {
		return true // admin/owner tier or privileged realm role or registered operator
	}
	switch strings.ToLower(strings.TrimSpace(claims.Tier)) {
	case "operator", "admin", "owner":
		return true
	}
	// Deployment-record fallback: the creator of this deployment is its
	// operator even when the JWT mapper dropped the tier claim (the same
	// trust-the-record path applicationInstallCallerAuthorized documents).
	if dep != nil {
		dep.mu.Lock()
		owner := strings.ToLower(strings.TrimSpace(dep.OwnerEmail))
		dep.mu.Unlock()
		if owner != "" && strings.EqualFold(owner, strings.TrimSpace(claims.Email)) {
			return true
		}
	}
	return false
}

// retryOperatorIdentity returns a stable operator identifier for the audit
// LogLine — the claims subject/email when present, else the X-User-Email
// header, else "operator".
func retryOperatorIdentity(claims *auth.Claims, r *http.Request) string {
	if claims != nil {
		if e := strings.TrimSpace(claims.Email); e != "" {
			return e
		}
		if s := strings.TrimSpace(claims.Sub); s != "" {
			return s
		}
	}
	if e := strings.TrimSpace(r.Header.Get("X-User-Email")); e != "" {
		return e
	}
	return "operator"
}
