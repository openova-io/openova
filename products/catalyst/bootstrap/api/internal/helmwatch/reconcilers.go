// reconcilers.go — the GENERIC one-shot reader that observes EVERY
// non-HelmRelease reconciler object the jobs canvas must surface (issue
// #3646 §5a). It is the ingestion-breadth half of "one honest activity
// canvas": the helmwatch informer already projects HelmRelease installs,
// but Flux Kustomizations, recurring CronJobs, standalone batch Jobs, and
// long-running reconciler Deployments were INVISIBLE — so a green
// "install-openbao" row masked a Failed openbao-snapshot-save CronJob.
//
// This reader is deliberately GENERIC: it lists a fixed set of reconciler
// GVRs via the SAME dynamic.Interface the HelmRelease snapshot uses and
// emits one typed ReconcilerObservation per object. No per-app, per-cloud,
// or per-blueprint branching — a new CronJob in any blueprint surfaces
// automatically because the list is over the kind, not the name. The jobs
// bridge (jobs.Bridge.OnReconcilerObservation) maps each observation onto
// the correct leaf Kind; this package only OBSERVES.
package helmwatch

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// Reconciler-object GVRs the §5a ingestion observes. Kept here next to
// HelmReleaseGVR so the canvas's full ingestion surface is one list.
var (
	// KustomizationGVR — Flux Kustomization (kustomize-controller). Its
	// Ready condition drives a `reconcile-<name>` leaf.
	KustomizationGVR = schema.GroupVersionResource{
		Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations",
	}
	// CronJobGVR — recurring batch CronJob. Each spawned Job is an
	// Execution under a `cron-<name>` leaf.
	CronJobGVR = schema.GroupVersionResource{
		Group: "batch", Version: "v1", Resource: "cronjobs",
	}
	// JobGVR — batch Job. A Job that is an HR install-hook attaches as an
	// Execution under its owning install leaf; an unowned/standalone Job
	// becomes a `task-<name>` leaf.
	JobGVR = schema.GroupVersionResource{
		Group: "batch", Version: "v1", Resource: "jobs",
	}
	// DeploymentGVR — apps Deployment. Only those carrying the
	// ReconcilerMarkerLabel become a `reconciler-<name>` leaf with a
	// HEALTH status; the rest are ignored (a Deployment is not, by
	// itself, a reconciler the operator drives).
	DeploymentGVR = schema.GroupVersionResource{
		Group: "apps", Version: "v1", Resource: "deployments",
	}
)

// ReconcilerMarkerLabel — a Deployment opts INTO the reconciler kind by
// carrying this label (any value). Without it a Deployment is not
// surfaced: the canvas tracks reconcilers, not arbitrary workloads
// (issue #3646 §11 — "only Flux-reconciled objects + the Jobs they spawn
// are in scope"). The reconciler chart stamps it via a render marker.
const ReconcilerMarkerLabel = "catalyst.openova.io/reconciler"

// Reconciler-observation Kind tags. These mirror the jobs.Kind* leaf
// kinds 1:1 (kept as plain strings here so this package does not import
// internal/jobs and create a cycle). The bridge maps them through.
const (
	ReconcilerKindReconcile  = "reconcile"  // Flux Kustomization
	ReconcilerKindCron       = "cron"       // recurring CronJob
	ReconcilerKindTask       = "task"       // standalone batch Job
	ReconcilerKindReconciler = "reconciler" // marked Deployment (health)
)

// Reconciler-observation status strings — the one-shot lifecycle axis
// (pending/running/succeeded/failed) reuses the helmwatch State* vocab so
// the bridge's existing jobStatusFromHelmState translation drops in; the
// HEALTH axis (healthy/degraded/failing) is distinct and the bridge maps
// it onto the jobs HEALTH-axis statuses for reconciler leaves.
const (
	ObsStatusPending   = StatePending
	ObsStatusRunning   = StateInstalling
	ObsStatusSucceeded = StateInstalled
	ObsStatusFailed    = StateFailed

	ObsHealthHealthy  = "healthy"
	ObsHealthDegraded = "degraded"
	ObsHealthFailing  = "failing"
)

// ReconcilerObservation is one observed reconciler object. The bridge
// upserts exactly one leaf Job per observation, keyed by Kind+Name.
type ReconcilerObservation struct {
	// Kind — one of the ReconcilerKind* tags.
	Kind string
	// Name — the object's metadata.name (used as the leaf JobName suffix).
	Name string
	// Namespace — the object's namespace (carried for remediation: the
	// retry primitive needs the namespace to address the object).
	Namespace string
	// Status — the leaf's lifecycle status (ObsStatus*) for one-shot
	// kinds, or HEALTH status (ObsHealth*) for the reconciler kind.
	Status string
	// Health — true when Status is a HEALTH-axis value (reconciler kind);
	// the bridge then writes a HEALTH-axis leaf rather than a one-shot one.
	Health bool
	// Message — a short human line (Ready condition message, or the
	// last-run summary for a CronJob). Becomes the seed LogLine.
	Message string
	// ObservedAt — the most relevant transition timestamp (Ready
	// lastTransitionTime, or last-run completion); zero ⇒ bridge uses now.
	ObservedAt time.Time
	// OwnerInstallChart — for a batch Job that is an HR install-hook, the
	// owning chart's component id ("openbao" for "bp-openbao") so the
	// bridge attaches it as an Execution under "install-openbao" instead
	// of minting a duplicate task-* row. Empty for standalone Jobs.
	OwnerInstallChart string
	// Executions — for a CronJob, the recent spawned-Job runs (newest
	// first), each becoming an Execution on the cron-<name> leaf. Empty
	// for the other kinds.
	Executions []ReconcilerExecution
}

// ReconcilerExecution is one spawned run of a CronJob — a single
// Execution on the cron leaf with its own terminal status.
type ReconcilerExecution struct {
	// Name — the spawned Job's metadata.name (stable per-run id).
	Name string
	// Status — ObsStatusSucceeded / ObsStatusFailed / ObsStatusRunning.
	Status string
	// StartedAt / FinishedAt — run timing (zero when unknown).
	StartedAt  time.Time
	FinishedAt time.Time
	// Message — a short run summary line.
	Message string
}

// ListReconcilerObservations performs a ONE-SHOT list of every observed
// reconciler GVR via the supplied dynamic client and returns the typed
// observations. It never spins up an informer — it is the §5a counterpart
// of ListAndSnapshotHelmReleases, invoked from the same chroot seed path.
//
// Best-effort per GVR: a List error on one kind (e.g. the Kustomization
// CRD absent on a cluster that uses only HelmReleases) is swallowed so the
// other kinds still surface. Returns an empty slice (never nil) when
// nothing is found. The returned slice is in a stable kind order
// (kustomizations, cronjobs, jobs, deployments) so callers/tests see a
// deterministic sequence.
func ListReconcilerObservations(ctx context.Context, dyn dynamic.Interface) ([]ReconcilerObservation, error) {
	if dyn == nil {
		return []ReconcilerObservation{}, nil
	}
	out := make([]ReconcilerObservation, 0, 16)

	// (1) Flux Kustomizations → reconcile-<name>, status from Ready.
	if list, err := dyn.Resource(KustomizationGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			u := &list.Items[i]
			conds, _ := extractConditions(u)
			out = append(out, ReconcilerObservation{
				Kind:       ReconcilerKindReconcile,
				Name:       u.GetName(),
				Namespace:  u.GetNamespace(),
				Status:     statusFromReadyCondition(conds),
				Message:    messageFromReadyCondition(conds),
				ObservedAt: readyTransitionTime(conds),
			})
		}
	}

	// (2) CronJobs → cron-<name>; status + per-run Executions from the
	// owned spawned Jobs (collected in pass 3's index below).
	//
	// The cron observations are appended to `out` HERE (so the documented
	// kind order kustomizations→cronjobs→jobs→deployments holds), and the
	// map indexes them by their POSITION in `out`, never by a cached
	// pointer: pass 3 appends task/install-hook rows to `out`, which can
	// reallocate the backing array — a cached &out[i] pointer would then
	// dangle into the stale array and the failed-run status refinement
	// below would be silently dropped (a Failed openbao-snapshot-save would
	// read as the optimistic "pending" default → the exact "invisible
	// failing class" #3646 forbids). An index is stable across reallocation
	// because pass 3 only ever appends AFTER the cron rows, so out[idx]
	// always addresses the same logical element in the current array.
	cronIdx := map[string]int{} // ns/name → index into out
	if list, err := dyn.Resource(CronJobGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			u := &list.Items[i]
			obs := ReconcilerObservation{
				Kind:      ReconcilerKindCron,
				Name:      u.GetName(),
				Namespace: u.GetNamespace(),
				// Default until the spawned-Job pass refines it. A CronJob
				// with no completed run yet is honestly "pending".
				Status:  ObsStatusPending,
				Message: "no run observed yet",
			}
			key := u.GetNamespace() + "/" + u.GetName()
			out = append(out, obs)
			cronIdx[key] = len(out) - 1
		}
	}

	// (3) Jobs → either Executions on a cron leaf (CronJob-owned), or an
	// Execution on an install leaf (HR install-hook), or a standalone
	// task-<name> leaf. The ownership de-dup (issue #3646 §5a) keys off
	// metadata.ownerReferences + the helm hook label.
	if list, err := dyn.Resource(JobGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			u := &list.Items[i]
			status, started, finished := jobRunStatus(u)
			if cronName, ok := cronJobOwnerName(u); ok {
				// Attach as an Execution under the owning cron leaf. Mutate
				// through the index (out[idx]) so the write always lands on
				// the live backing array even after earlier appends in this
				// pass reallocated `out`.
				key := u.GetNamespace() + "/" + cronName
				if idx, ok := cronIdx[key]; ok {
					c := &out[idx]
					c.Executions = append(c.Executions, ReconcilerExecution{
						Name:       u.GetName(),
						Status:     status,
						StartedAt:  started,
						FinishedAt: finished,
						Message:    "run " + u.GetName() + ": " + status,
					})
					// The cron leaf's headline status reflects its MOST RECENT
					// run, not list order (the apiserver returns owned Jobs in
					// no guaranteed chronological order, so a stale Succeeded
					// run arriving after a fresh Failed one must not overwrite
					// the failure — that would re-introduce the silent-green
					// the §5a ingestion exists to kill). Recency = the run's
					// finish time, falling back to its start for an in-flight
					// run. The first owned run always wins (default ObservedAt
					// is zero); later runs override only when strictly newer.
					runAt := firstNonZero(finished, started)
					if c.ObservedAt.IsZero() || runAt.IsZero() || !runAt.Before(c.ObservedAt) {
						c.Status = status
						c.Message = "last run " + u.GetName() + ": " + status
						if !runAt.IsZero() {
							c.ObservedAt = runAt
						}
					}
				}
				continue
			}
			if chart, ok := installHookOwnerChart(u); ok {
				// HR install-hook Job — attaches under install-<chart>.
				out = append(out, ReconcilerObservation{
					Kind:              ReconcilerKindTask,
					Name:              u.GetName(),
					Namespace:         u.GetNamespace(),
					Status:            status,
					Message:           "install-hook run: " + status,
					ObservedAt:        firstNonZero(finished, started),
					OwnerInstallChart: chart,
				})
				continue
			}
			// Standalone / cluster-owned batch Job → task-<name> leaf.
			out = append(out, ReconcilerObservation{
				Kind:       ReconcilerKindTask,
				Name:       u.GetName(),
				Namespace:  u.GetNamespace(),
				Status:     status,
				Message:    "batch job: " + status,
				ObservedAt: firstNonZero(finished, started),
			})
		}
	}

	// (4) reconciler-marked Deployments → reconciler-<name> HEALTH leaf.
	if list, err := dyn.Resource(DeploymentGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			u := &list.Items[i]
			labels := u.GetLabels()
			if _, ok := labels[ReconcilerMarkerLabel]; !ok {
				continue
			}
			health, msg := deploymentHealth(u)
			out = append(out, ReconcilerObservation{
				Kind:       ReconcilerKindReconciler,
				Name:       u.GetName(),
				Namespace:  u.GetNamespace(),
				Status:     health,
				Health:     true,
				Message:    msg,
				ObservedAt: time.Now().UTC(),
			})
		}
	}

	return out, nil
}

// statusFromReadyCondition maps a Ready condition onto the one-shot
// lifecycle status. Ready=True ⇒ succeeded; Ready=False with a terminal
// reason ⇒ failed; otherwise running (reconcile in progress); no Ready
// condition ⇒ pending.
func statusFromReadyCondition(conds []metav1.Condition) string {
	ready := findCondition(conds, "Ready")
	if ready == nil {
		return ObsStatusPending
	}
	switch ready.Status {
	case metav1.ConditionTrue:
		return ObsStatusSucceeded
	case metav1.ConditionFalse:
		// A Kustomization that is progressing reports Ready=False with
		// reason=Progressing/ReconciliationFailed. Treat an explicit
		// failure reason as failed; anything else as still running.
		r := strings.ToLower(ready.Reason)
		if strings.Contains(r, "fail") || strings.Contains(r, "error") || strings.Contains(r, "stalled") {
			return ObsStatusFailed
		}
		return ObsStatusRunning
	default:
		return ObsStatusRunning
	}
}

// messageFromReadyCondition returns the Ready condition message, or a
// placeholder when absent.
func messageFromReadyCondition(conds []metav1.Condition) string {
	if ready := findCondition(conds, "Ready"); ready != nil && strings.TrimSpace(ready.Message) != "" {
		return ready.Message
	}
	return "no Ready condition reported"
}

// readyTransitionTime returns the Ready condition lastTransitionTime, or
// zero when absent.
func readyTransitionTime(conds []metav1.Condition) time.Time {
	if ready := findCondition(conds, "Ready"); ready != nil {
		return ready.LastTransitionTime.Time.UTC()
	}
	return time.Time{}
}

// jobRunStatus derives a batch Job's run status + timing from its
// .status.conditions (Complete / Failed) + .status fields. Running when
// neither terminal condition is present.
func jobRunStatus(u *unstructured.Unstructured) (status string, started, finished time.Time) {
	conds, _ := extractConditions(u)
	for _, c := range conds {
		if c.Status != metav1.ConditionTrue {
			continue
		}
		switch c.Type {
		case "Complete", "SuccessCriteriaMet":
			return ObsStatusSucceeded, jobStartTime(u), c.LastTransitionTime.Time.UTC()
		case "Failed":
			return ObsStatusFailed, jobStartTime(u), c.LastTransitionTime.Time.UTC()
		}
	}
	return ObsStatusRunning, jobStartTime(u), time.Time{}
}

// jobStartTime reads .status.startTime as a parsed time, or zero.
func jobStartTime(u *unstructured.Unstructured) time.Time {
	if s, ok, _ := unstructured.NestedString(u.Object, "status", "startTime"); ok && s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// cronJobOwnerName returns the owning CronJob name when the Job carries a
// CronJob ownerReference (the kube CronJob controller stamps it).
func cronJobOwnerName(u *unstructured.Unstructured) (string, bool) {
	for _, ref := range u.GetOwnerReferences() {
		if ref.Kind == "CronJob" {
			return ref.Name, true
		}
	}
	return "", false
}

// installHookOwnerChart returns the owning install chart's component id
// when the Job is a Helm install-hook (carries a helm.sh/hook annotation
// AND a managed-by:Helm-style marker pointing at a bp-* release). The
// component id has the "bp-" prefix stripped to match the install-<chart>
// leaf JobName. Returns ("", false) for non-hook Jobs.
//
// Detection is name-prefix based against the canonical Helm hook naming
// the bootstrap charts use ("<chart>-bp-<chart>-..."): a Job whose name
// starts with "<release>-" where the release is a bp-* HelmRelease is an
// install-hook. We read the helm release-name label/annotation when
// present (authoritative) and fall back to the helm.sh/hook annotation +
// the bp- name prefix.
func installHookOwnerChart(u *unstructured.Unstructured) (string, bool) {
	annos := u.GetAnnotations()
	labels := u.GetLabels()
	// Authoritative: the Helm release name the hook belongs to.
	if rel := firstNonEmpty(
		annos["meta.helm.sh/release-name"],
		labels["app.kubernetes.io/instance"],
	); rel != "" && strings.HasPrefix(rel, "bp-") {
		// Only treat it as a hook (vs the chart's own steady-state Job)
		// when a hook annotation is present; otherwise a plain Job that
		// happens to be labelled by a release is a task, not a hook
		// Execution. Either helm.sh/hook (classic) marks it.
		if _, isHook := annos["helm.sh/hook"]; isHook {
			return strings.TrimPrefix(rel, "bp-"), true
		}
	}
	return "", false
}

// deploymentHealth derives a marked Deployment's HEALTH status: healthy
// when Available=True and readyReplicas == spec.replicas (>0); failing
// when zero ready replicas are desired-but-absent; degraded in between.
func deploymentHealth(u *unstructured.Unstructured) (string, string) {
	desired, _, _ := unstructured.NestedInt64(u.Object, "spec", "replicas")
	ready, _, _ := unstructured.NestedInt64(u.Object, "status", "readyReplicas")
	available := false
	conds, _ := extractConditions(u)
	if c := findCondition(conds, "Available"); c != nil && c.Status == metav1.ConditionTrue {
		available = true
	}
	switch {
	case desired == 0:
		// Scaled to zero on purpose — a marked reconciler at 0 replicas is
		// degraded (it isn't reconciling), which is the honest state the
		// §3a DoD scale-to-zero walk expects.
		return ObsHealthDegraded, "scaled to 0 replicas — not reconciling"
	case available && ready >= desired:
		return ObsHealthHealthy, "available; ready replicas " + itoa(ready) + "/" + itoa(desired)
	case ready == 0:
		return ObsHealthFailing, "0 ready replicas of " + itoa(desired) + " desired"
	default:
		return ObsHealthDegraded, "ready replicas " + itoa(ready) + "/" + itoa(desired)
	}
}

// --- tiny local helpers (no fmt import to keep this leaf cheap) ---

func firstNonZero(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
