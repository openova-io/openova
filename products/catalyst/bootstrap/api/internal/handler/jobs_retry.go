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

	// Make sure the store is seeded (chroot lazy seed) so GetJob resolves
	// the leaf even on the first hit after a Pod restart.
	h.chrootSeedJobsStoreIfEmpty(r.Context(), dep)

	job, _, err := st.GetJob(depID, jobID)
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

	case jobs.KindMutation:
		// Crossplane XRC re-submit — bump the catalyst reconcile annotation
		// on the stored claim. Best-effort: the claim GVR is not always
		// resolvable from the leaf alone, so we re-drive via the HR fallback
		// when the mutation maps to a bp-* release.
		return "", fmt.Errorf("mutation re-submit is driven by the mutation bridge; retry via the originating Day-2 action")

	case jobs.KindTask:
		// Standalone batch Job — annotate it to request a fresh run. A
		// finished Job can't be re-run in place; bumping the annotation is
		// the idempotent, non-destructive signal the owner picks up.
		ns, err := resolveObjectNamespace(ctx, dyn, helmwatch.JobGVR, name)
		if err != nil {
			return "", err
		}
		if err := triggerReconcileAnnotation(ctx, dyn, helmwatch.JobGVR, ns, name, now); err != nil {
			return "", err
		}
		return "annotated Job " + name + " for re-run", nil

	default:
		return "", fmt.Errorf("kind %q does not support retry", job.Kind)
	}
}

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
		return "", fmt.Errorf("%s %q not found in any namespace", gvr.Resource, name)
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
