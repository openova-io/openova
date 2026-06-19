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

// SecurityScanSummaryName — the stable Name of the SINGLE collapsed
// security-scan task leaf (issue #3919). Every trivy-operator scan Job is
// counted into this one leaf instead of minting its own task-* row, so the
// /jobs canvas shows "scanning is happening" without the hundreds of
// ever-growing per-workload rows. The jobs bridge keys leaves by Kind+Name,
// so a fixed Name guarantees the count refreshes the same row across
// re-reads rather than accumulating new ones.
const SecurityScanSummaryName = "security-scans"

// trivyScanNamespace — the namespace the trivy-operator runs its scan Jobs
// in. Scan Jobs anywhere else are still caught by the label/name checks in
// isSecurityScanJob; this is the fast common-case match.
const trivyScanNamespace = "trivy-system"

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
	// taskIdx indexes standalone task leaves by ns/base-name (issue #3916)
	// so multiple runs of the same base Job collapse into ONE stable leaf
	// with per-run Executions, never a fresh leaf per run. Indexed by
	// position in `out` (never a cached pointer) for the same
	// reallocation-safety reason cronIdx is.
	taskIdx := map[string]int{} // ns/base → index into out
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
	//
	// scanJobCount accumulates trivy/aquasecurity security-scan Jobs
	// (issue #3919) so they NEVER mint per-Job task-* leaves — they are
	// collapsed into ONE "security-scans" summary leaf after the loop. The
	// trivy-operator spawns one scan Job per workload (+ per container) and
	// continuously re-scans, so without this they flood the /jobs canvas with
	// hundreds of ever-growing rows that dwarf the real ~100 platform
	// reconcilers. This mirrors the dashboard treemap's `dropEphemeralRows`
	// scan exclusion (#3869) — same class of filter, applied to the jobs
	// ingestion. Collapse (not drop) so the operator still sees scanning is
	// running without the per-report flood.
	scanJobCount := 0
	if list, err := dyn.Resource(JobGVR).Namespace("").List(ctx, metav1.ListOptions{}); err == nil {
		for i := range list.Items {
			u := &list.Items[i]
			// Security-scan Job (trivy-operator) → counted into the single
			// summary leaf, never a per-Job task-* row (#3919).
			if isSecurityScanJob(u) {
				scanJobCount++
				continue
			}
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
				// Keyed by the OWNING chart (the install leaf), so per-run
				// instance names never matter here — the install bridge
				// dedups the Execution.
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
			// Standalone / cluster-owned batch Job → a STABLE task-<base>
			// leaf, NOT task-<per-run-name>.
			//
			// Accumulation bug (issue #3916): a Job created from a
			// generateName (or re-created by Flux on each reconcile/retry)
			// carries a fresh `-<hash>` run suffix every time. Keying the leaf
			// on u.GetName() minted a brand-new task-* row per run → the /jobs
			// model grew unbounded (region A 141→145 while the actual k8s Jobs
			// stayed ~18-24/region). The fix keys the leaf on the STABLE base
			// identity (taskBaseName) and records the per-run instance as an
			// Execution under that one leaf — so N reruns = 1 row + N runs,
			// never N rows. Identical structure across regions because the key
			// no longer carries the run-instance entropy.
			base := taskBaseName(u)
			key := u.GetNamespace() + "/" + base
			if idx, ok := taskIdx[key]; ok {
				c := &out[idx]
				c.Executions = append(c.Executions, ReconcilerExecution{
					Name:       u.GetName(),
					Status:     status,
					StartedAt:  started,
					FinishedAt: finished,
					Message:    "run " + u.GetName() + ": " + status,
				})
				// Headline status reflects the MOST RECENT run (same recency
				// rule as the cron leaf): a stale Succeeded run arriving after
				// a fresh Failed one must not overwrite the failure. Recency =
				// finish time, falling back to start for an in-flight run.
				runAt := firstNonZero(finished, started)
				if c.ObservedAt.IsZero() || runAt.IsZero() || !runAt.Before(c.ObservedAt) {
					c.Status = status
					c.Message = "last run " + u.GetName() + ": " + status
					if !runAt.IsZero() {
						c.ObservedAt = runAt
					}
				}
				continue
			}
			out = append(out, ReconcilerObservation{
				Kind:      ReconcilerKindTask,
				Name:      base,
				Namespace: u.GetNamespace(),
				Status:    status,
				Message:   "batch job: " + status,
				Executions: []ReconcilerExecution{{
					Name:       u.GetName(),
					Status:     status,
					StartedAt:  started,
					FinishedAt: finished,
					Message:    "run " + u.GetName() + ": " + status,
				}},
				ObservedAt: firstNonZero(finished, started),
			})
			taskIdx[key] = len(out) - 1
		}
	}

	// (3b) Collapsed security-scan summary (issue #3919). When trivy-operator
	// scan Jobs were observed, emit exactly ONE task leaf carrying the count
	// so the operator sees "scanning is happening" without the per-workload
	// flood. A stable Name keeps it as a single leaf across re-reads (the
	// jobs bridge upserts by Kind+Name); the count lives in the Message so a
	// growing scan fleet refreshes the same row instead of adding new ones.
	// Succeeded because the scans themselves are not a provisioning gate —
	// their job is to run continuously, never to "complete".
	if scanJobCount > 0 {
		out = append(out, ReconcilerObservation{
			Kind:       ReconcilerKindTask,
			Name:       SecurityScanSummaryName,
			Namespace:  "trivy-system",
			Status:     ObsStatusSucceeded,
			Message:    "security scans: " + itoa(int64(scanJobCount)) + " scan jobs (collapsed)",
			ObservedAt: time.Now().UTC(),
		})
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

// statusFromReadyCondition maps a Flux Kustomization's Ready condition
// onto the lifecycle status WITHOUT flapping a still-reconciling object
// into a terminal Failed (issue #3916).
//
// Flux reconciles continuously: a healthy-but-converging Kustomization
// oscillates Ready between False(reason=Progressing / BuildFailed /
// HealthCheckFailed / DependencyNotReady, while it retries) and
// True(reason=ReconciliationSucceeded). Because the chroot seed re-reads
// live state on every /jobs poll and the later status wins, mapping each
// transient False to terminal `failed` made the leaf flap Failed↔Succeeded
// on the page even though the prov converged green.
//
// The mapping therefore only emits a TERMINAL status for a STABLE state:
//   - Ready=True                       ⇒ succeeded (stably reconciled).
//   - Ready=False, reason=Stalled      ⇒ failed (Flux's own permanent /
//     terminal signal — it has GIVEN UP retrying; this is the only honest
//     terminal failure for a continuously-reconciling object).
//   - Ready=False, any other reason    ⇒ running (still reconciling /
//     retrying — a transient build/health/dependency error Flux will
//     re-attempt; NOT a terminal failure).
//   - Ready=Unknown / no Ready cond    ⇒ running / pending.
func statusFromReadyCondition(conds []metav1.Condition) string {
	ready := findCondition(conds, "Ready")
	if ready == nil {
		return ObsStatusPending
	}
	switch ready.Status {
	case metav1.ConditionTrue:
		return ObsStatusSucceeded
	case metav1.ConditionFalse:
		// Only Flux's terminal "Stalled" reason (retries exhausted, no
		// further automatic reconcile) is a genuine terminal failure. Every
		// other Ready=False is an in-flight reconcile/retry — running, never
		// a flapping terminal Failed.
		if strings.EqualFold(strings.TrimSpace(ready.Reason), "Stalled") {
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

// taskBaseName returns the STABLE base identity for a standalone batch
// Job (issue #3916) — the key the task-<base> leaf is minted under, so
// repeated runs of the same logical task collapse into one leaf with
// per-run Executions instead of accumulating a fresh leaf per run.
//
// Resolution order:
//  1. metadata.generateName — authoritative when present. The Job
//     controller (and Flux's prune-and-recreate) sets generateName to the
//     base and lets the apiserver append a "-<5-char-rand>" suffix, so the
//     generateName (minus its trailing dash) IS the stable identity.
//  2. otherwise strip a single trailing controller-appended run suffix
//     from metadata.name: either a numeric CronJob-style "-<digits>"
//     stamp or a lowercase-alphanumeric "-<hash>" (>=5 chars) the Job
//     controller / generateName path leaves. A name with no such suffix
//     (a human-authored fixed-name Job) is returned unchanged.
//
// The stripping is deliberately conservative — it removes AT MOST ONE
// trailing segment and only when that segment looks machine-generated, so
// a meaningful final segment of a fixed name (e.g. "cnpg-pair-primary-3-
// join") is preserved.
func taskBaseName(u *unstructured.Unstructured) string {
	if gn := strings.TrimSpace(u.GetGenerateName()); gn != "" {
		return strings.TrimRight(gn, "-")
	}
	name := u.GetName()
	i := strings.LastIndexByte(name, '-')
	if i <= 0 || i == len(name)-1 {
		return name
	}
	suffix := name[i+1:]
	if isRunSuffix(suffix) {
		return name[:i]
	}
	return name
}

// isRunSuffix reports whether a trailing name segment looks like a
// controller-appended run instance id: an all-digit stamp (CronJob unix
// minute, e.g. "28912345") OR a lowercase base36-ish hash of length >=5
// (the apiserver's generateName suffix is 5 lowercase alphanumerics, e.g.
// "x7f2k"). Pure-alpha words shorter than the hash length, or any segment
// containing an uppercase letter, are treated as MEANINGFUL (not a run id)
// so a fixed-name Job like "...-join" or "...-migrate" is never truncated.
func isRunSuffix(s string) bool {
	if s == "" {
		return false
	}
	allDigits := true
	for _, r := range s {
		if r < '0' || r > '9' {
			allDigits = false
			break
		}
	}
	if allDigits {
		return true
	}
	// generateName random suffix: exactly-5 lowercase [a-z0-9], and NOT
	// all-letters (an all-letters 5-char tail like "build" or "enrol" is a
	// real word, not a hash). Require at least one digit to distinguish a
	// random suffix from a meaningful trailing word.
	if len(s) != 5 {
		return false
	}
	hasDigit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return hasDigit
}

// isSecurityScanJob reports whether a batch Job is a trivy/aquasecurity
// security-scan Job (issue #3919) that must be COLLAPSED into the single
// security-scans summary leaf rather than minting its own task-* row.
//
// The trivy-operator spawns one short-lived scan Job per workload (and per
// container) and re-scans continuously, producing the VulnerabilityReport /
// ConfigAuditReport / ExposedSecretReport / RbacAssessmentReport CRs — on a
// converged Sovereign that is hundreds of ever-growing Jobs that flood and
// dwarf the ~100 real platform reconcilers on the /jobs canvas. They are
// security activity, not provisioning/convergence, so they do not belong as
// individual rows. This is the jobs-ingestion analogue of the dashboard
// treemap's scan exclusion (#3869).
//
// Detection is layered (any match ⇒ scan Job), most-authoritative first:
//  1. the trivy-operator managed-by label
//     (app.kubernetes.io/managed-by=trivy-operator) — the operator stamps
//     it on every scan Job it creates;
//  2. any trivy-operator.* label key (e.g. trivy-operator.resource.kind),
//     present on scan Jobs regardless of the managed-by value;
//  3. the well-known scan-* name prefix in the trivy-system namespace, a
//     belt-and-braces fallback if labels are stripped by a future operator
//     release. Name-prefix alone OUTSIDE trivy-system is intentionally NOT
//     treated as a scan Job so an unrelated user Job named "scan-*" in a
//     tenant namespace still surfaces as a real task.
func isSecurityScanJob(u *unstructured.Unstructured) bool {
	labels := u.GetLabels()
	if strings.EqualFold(strings.TrimSpace(labels["app.kubernetes.io/managed-by"]), "trivy-operator") {
		return true
	}
	for k := range labels {
		if strings.HasPrefix(k, "trivy-operator.") {
			return true
		}
	}
	if u.GetNamespace() == trivyScanNamespace && strings.HasPrefix(u.GetName(), "scan-") {
		return true
	}
	return false
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
