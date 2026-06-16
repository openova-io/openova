// cutover_activity_bridge.go — projects the 11-step self-sovereign
// CUTOVER execution into the jobs/activity store as a `Cutover` group of
// step Job rows with dependency edges (issue #3646).
//
// # Problem (issue #3646, founder-caught on hw149)
//
// The console /jobs page showed `install-self-sovereign-cutover` as
// "Succeeded" — but that "Succeeded" is the chart INSTALL (the
// bp-self-sovereign-cutover Blueprint deploying DORMANT at bootstrap; it
// ships 11 step ConfigMaps + RBAC and fires NO steps at install). The
// real 11-step cutover EXECUTION (fired post-handover) was invisible on
// /jobs — its true state lived ONLY in the self-sovereign-cutover-status
// ConfigMap. An operator reasonably read "Succeeded" as "cutover done"
// when the execution was actually mid-flight at step 3/11.
//
// # Fix — one unified model
//
// "Flux first": every platform activity should appear on the jobs canvas
// as a projection of a reconciling object. A HelmRelease already
// projects to a leaf Job via the helmwatch bridge; this file does the
// same for the cutover EXECUTION using the GENERIC jobs.ActivityBridge —
// each of the 11 batch/v1 Job steps becomes a leaf Job under a distinct
// `Cutover` group, with dependsOn edges along the step order. The
// projection is driven by the SAME state transitions the engine already
// emits (runCutover/runCutoverStep), reading the REAL Job/step results —
// never a fabricated row. The dormant chart-install row stays a separate
// bootstrap-kit leaf; the execution now has its own honest group whose
// rolled-up status is "succeeded" only when every step actually
// succeeded (and `cutoverComplete=true`).
//
// This bridge is the cutover-specific WIRING of the generic pattern; the
// reusable machinery (group + step Jobs, executions, log lines, edges,
// idempotent resume) lives entirely in jobs.ActivityBridge so a future
// activity source (DR switchover, backup/restore) wires the same way.
package handler

import (
	"strings"
	"sync"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// cutoverActivityProjector lazily owns the jobs.ActivityBridge for the
// cutover group. One per Handler; resolved on first use under the once.
// The resolved deployment id lives inside the bridge itself.
type cutoverActivityProjector struct {
	once   sync.Once
	bridge *jobs.ActivityBridge
}

// cutoverActivityBridge resolves (once) the jobs.ActivityBridge the
// cutover engine projects its steps into, or nil when projection isn't
// possible (no jobs store wired, or no deployment id resolvable yet).
//
// Resolution of the deployment id (the jobs-store key the canvas reads):
//  1. h.cutoverActivityDepID when set (tests / explicit override).
//  2. The chroot's imported deployment — the one in the jobs store that
//     already carries the bootstrap-kit group. On a Sovereign the jobs
//     store holds exactly the deployment imported at handover
//     (HandleJobsImport writes it under the mother's id), so scanning
//     for the bootstrap-kit group uniquely identifies "this Sovereign's
//     deployment" without depending on the in-memory deployment map.
//
// nil is returned (projection silently skipped) when neither resolves —
// the cutover engine's existing ConfigMap-status path is unaffected, so
// projection is strictly additive and never blocks the cutover.
func (h *Handler) cutoverActivityBridge() *jobs.ActivityBridge {
	if h.jobs == nil {
		return nil
	}
	h.cutoverActivityProj.once.Do(func() {
		depID := strings.TrimSpace(h.cutoverActivityDepID)
		if depID == "" {
			depID = h.resolveCutoverActivityDepID()
		}
		if depID == "" {
			// Leave proj.bridge nil; a later call re-evaluates because
			// sync.Once only ran the resolution once — so guard below.
			return
		}
		h.cutoverActivityProj.bridge = jobs.NewActivityBridge(
			h.jobs, depID, jobs.GroupCutover, jobs.GroupCutoverDisplay,
		)
	})
	// If the first resolution attempt failed (no deployment imported
	// yet), retry resolution on subsequent calls — sync.Once won't, so
	// do it inline. This matters because the cutover may start before a
	// /jobs read has lazily seeded the store; by the time steps run the
	// import has landed.
	if h.cutoverActivityProj.bridge == nil {
		depID := strings.TrimSpace(h.cutoverActivityDepID)
		if depID == "" {
			depID = h.resolveCutoverActivityDepID()
		}
		if depID == "" {
			return nil
		}
		h.cutoverActivityProj.bridge = jobs.NewActivityBridge(
			h.jobs, depID, jobs.GroupCutover, jobs.GroupCutoverDisplay,
		)
	}
	return h.cutoverActivityProj.bridge
}

// resolveCutoverActivityDepID scans the jobs store for the deployment
// that carries the bootstrap-kit group — the imported Sovereign
// deployment on a chroot. Returns "" when no such deployment exists yet.
func (h *Handler) resolveCutoverActivityDepID() string {
	if h.jobs == nil {
		return ""
	}
	ids, err := h.jobs.DeploymentIDs()
	if err != nil {
		h.log.Debug("cutover-activity: enumerate deployments failed", "err", err)
		return ""
	}
	// Prefer a deployment that already has the bootstrap-kit group (the
	// imported Sovereign deployment). Fall back to the sole deployment id
	// when exactly one exists (covers the window before the bootstrap-kit
	// seed lands but after the deployment dir is created).
	var fallback string
	count := 0
	for _, id := range ids {
		count++
		fallback = id
		all, err := h.jobs.ListJobs(id)
		if err != nil {
			continue
		}
		for _, j := range all {
			if j.JobName == jobs.GroupBootstrapKit {
				return id
			}
		}
	}
	if count == 1 {
		return fallback
	}
	return ""
}

// stepNamesFromSteps returns the ordered step-name slice from a
// discovered cutoverStep list (the steps are already sorted by order in
// listCutoverSteps). Used to feed projectCutoverResumeSeed without
// re-deriving order.
func stepNamesFromSteps(steps []cutoverStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		if n := strings.TrimSpace(s.stepName); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// cutoverStepDisplay turns a step slug ("harbor-prewarm") into a
// human-readable label ("Harbor Prewarm").
func cutoverStepDisplay(slug string) string {
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// projectCutoverStepStarted flips a step's projected Job to running.
func (h *Handler) projectCutoverStepStarted(step cutoverStep, message string, t time.Time) {
	ab := h.cutoverActivityBridge()
	if ab == nil {
		return
	}
	if err := ab.StartStep(jobs.ActivityStep{
		Slug:        step.stepName,
		DisplayName: cutoverStepDisplay(step.stepName),
		Order:       step.order,
	}, message, t); err != nil {
		h.log.Warn("cutover-activity: start step job failed", "step", step.stepName, "err", err)
	}
}

// projectCutoverStepFinished flips a step's projected Job terminal.
// result is the engine's native result string ("success" | "failed" |
// "skipped").
func (h *Handler) projectCutoverStepFinished(step cutoverStep, result, message string, t time.Time) {
	ab := h.cutoverActivityBridge()
	if ab == nil {
		return
	}
	if err := ab.FinishStep(jobs.ActivityStep{
		Slug:        step.stepName,
		DisplayName: cutoverStepDisplay(step.stepName),
		Order:       step.order,
	}, result, message, t); err != nil {
		h.log.Warn("cutover-activity: finish step job failed", "step", step.stepName, "err", err)
	}
}

// projectCutoverResumeSeed re-projects the durable per-step results from
// the status ConfigMap onto the jobs store on a fresh Pod — so a
// catalyst-api restart mid-cutover (or a /jobs read after a completed
// cutover whose step ConfigMaps were GC'd) still shows the execution's
// real step states. priorStatus is the readCutoverStatus map; stepNames
// is the ordered step-name slice (from listCutoverSteps OR
// listStepNamesFromStatus). Best-effort.
//
// This is the seam that makes the projection survive the exact scenario
// in #3646: the operator looks at /jobs LONG after handover, when the
// cutover goroutine is gone and only the durable status ConfigMap
// remains. Each step's `.result` is replayed through Start/FinishStep so
// the Cutover group rolls up to the true state (succeeded only when every
// step succeeded).
func (h *Handler) projectCutoverResumeSeed(stepNames []string, priorStatus map[string]string) {
	ab := h.cutoverActivityBridge()
	if ab == nil {
		return
	}
	steps := make([]jobs.ActivityStep, 0, len(stepNames))
	for i, name := range stepNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		steps = append(steps, jobs.ActivityStep{
			Slug:        name,
			DisplayName: cutoverStepDisplay(name),
			Order:       i + 1,
		})
	}
	if err := ab.SeedSteps(steps); err != nil {
		h.log.Warn("cutover-activity: resume seed failed", "err", err)
		return
	}
	for _, s := range steps {
		result := priorStatus["step."+s.Slug+".result"]
		startedAt := parseCutoverStatusTime(priorStatus["step."+s.Slug+".startedAt"])
		finishedAt := parseCutoverStatusTime(priorStatus["step."+s.Slug+".finishedAt"])
		switch strings.ToLower(strings.TrimSpace(result)) {
		case jobs.ActivityResultSuccess, jobs.ActivityResultSkipped, jobs.ActivityResultFailed:
			if err := ab.StartStep(s, "[replayed from durable cutover status]", startedAt); err != nil {
				h.log.Warn("cutover-activity: resume start failed", "step", s.Slug, "err", err)
			}
			if err := ab.FinishStep(s, result, "", finishedAt); err != nil {
				h.log.Warn("cutover-activity: resume finish failed", "step", s.Slug, "err", err)
			}
		case jobs.ActivityResultRunning:
			if err := ab.StartStep(s, "[in flight — replayed from durable cutover status]", startedAt); err != nil {
				h.log.Warn("cutover-activity: resume running failed", "step", s.Slug, "err", err)
			}
		default:
			// pending — SeedSteps already wrote the pending row.
		}
	}
}

// parseCutoverStatusTime parses an RFC3339 timestamp from the cutover
// status ConfigMap, falling back to time.Now() so a missing/malformed
// value never drops the projection.
func parseCutoverStatusTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Now().UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}
