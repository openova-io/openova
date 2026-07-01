package handler

// janitor_ghost_records.go — #4677 automatic ghost-record GC.
//
// THE STRUCTURAL GAP this closes
// ──────────────────────────────
// The janitor's record reap (runJanitorPass Step 1) only fires for status
// "failed" (>failedMaxAge) or "wiped" (>wipedMaxAge). A Sovereign that was
// DECOMMISSIONED via a path that bypassed the full wipe (record-only DELETE, a
// wipe that failed at the record-purge step, or a manual `tofu destroy` in the
// workdir) leaves its record stuck in a LIVE-claimed status ("ready") with NO
// live cloud/cluster behind it. That record is STRUCTURALLY invisible to the
// reap switch → `restoreFromStore()` reloads the ghost on every catalyst-api
// restart → the console renders a stale "ready" row forever (the omantel.biz
// ghost, 2026-07-01).
//
// THE FIX (fail-safe, matching the #4466 discipline)
// ──────────────────────────────────────────────────
// Each pass, for a record in a live-claimed (non-in-flight, non-terminal)
// status older than a threshold, probe its kubeconfig apiserver. If the
// apiserver is UNREACHABLE (a live Sovereign answers; a decommissioned one does
// not) the record is a ghost candidate. LOG it always; only REAP it when the
// janitor is in destructive mode (CATALYST_JANITOR_DESTRUCTIVE=true) — exactly
// the "log-only for a full live cycle before it ever deletes" mandate the
// founder set after the #4454/#4614 self-destructs. Reachable / probe-error /
// in-flight / terminal ⇒ PROTECT (never reap).

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// defaultGhostRecordMaxAge — a record must be at least this old (since its last
// state change) before it can be considered a ghost, so a briefly-unreachable
// fresh Sovereign is never mistaken for one. Runtime-overridable.
const defaultGhostRecordMaxAge = 72 * time.Hour

const envGhostRecordMaxAge = "CATALYST_JANITOR_GHOST_MAX_AGE"

func ghostRecordMaxAge() time.Duration {
	return durationEnv(envGhostRecordMaxAge, defaultGhostRecordMaxAge)
}

// ghostRecordEligibleStatus reports whether a record status is a "live-claimed"
// state that a ghost check may consider. In-flight states are protected (a
// converging Sovereign's apiserver is legitimately not-yet-reachable) and the
// terminal states failed/wiped have their own age-based reap. Everything the
// switch below does NOT list is treated as NOT eligible (fail-safe: an unknown
// future status is never ghost-reaped).
func ghostRecordEligibleStatus(status string) bool {
	switch status {
	case "ready", "cutover-running", "cutover-complete", "handover-complete", "adopted":
		return true
	default:
		// pending / provisioning / tofu-applying / flux-bootstrapping /
		// phase1-watching / wiping (in-flight) and failed / wiped (own reap)
		// and any unknown status → NOT eligible.
		return false
	}
}

// isGhostRecord is the pure decision: a record is a ghost iff its status is
// eligible AND it is older than the threshold AND its apiserver is NOT
// reachable. No network here — reachability is passed in so this is unit
// testable and the network probe is isolated.
func isGhostRecord(status string, ageSinceUpdate, threshold time.Duration, apiserverReachable bool) bool {
	if !ghostRecordEligibleStatus(status) {
		return false
	}
	if ageSinceUpdate < threshold {
		return false
	}
	return !apiserverReachable
}

// apiserverReachable probes the kubeconfig's apiserver /healthz with a short
// timeout. Returns (reachable, probed): probed=false when there is no
// kubeconfig to probe (can't judge → caller must PROTECT). A live Sovereign
// returns reachable=true; a decommissioned one returns reachable=false.
func apiserverReachable(ctx context.Context, kcPath string) (reachable bool, probed bool) {
	raw, err := os.ReadFile(kcPath)
	if err != nil || len(raw) == 0 {
		return false, false
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return false, false
	}
	cfg.Timeout = 8 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return false, false
	}
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if _, err := cs.Discovery().RESTClient().Get().AbsPath("/healthz").DoRaw(pctx); err != nil {
		return false, true // probed, and it did NOT answer → unreachable
	}
	return true, true
}

// cleanGhostRecords scans in-memory deployments for ghosts (live-claimed status
// but apiserver persistently unreachable + old) and, in destructive mode,
// reaps the record + kubeconfig + tofu workdir. Returns the number reaped
// (log-only mode always returns 0). Fail-safe: any ambiguity PROTECTS.
func (h *Handler) cleanGhostRecords(tofuWorkDir string) int {
	if h.kubeconfigsDir == "" {
		return 0
	}
	threshold := ghostRecordMaxAge()
	now := time.Now()
	destructive := janitorDestructive()

	type ghost struct{ id, status string }
	var ghosts []ghost

	h.deployments.Range(func(key, value any) bool {
		id, ok := key.(string)
		if !ok {
			return true
		}
		dep, ok := value.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		status := dep.Status
		startedAt := dep.StartedAt
		finishedAt := dep.FinishedAt
		dep.mu.Unlock()

		if !ghostRecordEligibleStatus(status) {
			return true
		}
		anchor := finishedAt
		if anchor.IsZero() {
			anchor = startedAt
		}
		if anchor.IsZero() || now.Sub(anchor) < threshold {
			return true // too young / no anchor → protect
		}
		reachable, probed := apiserverReachable(context.Background(), filepath.Join(h.kubeconfigsDir, id+".yaml"))
		if !probed {
			return true // couldn't judge → protect
		}
		if isGhostRecord(status, now.Sub(anchor), threshold, reachable) {
			ghosts = append(ghosts, ghost{id: id, status: status})
		}
		return true
	})

	reaped := 0
	for _, g := range ghosts {
		if !destructive {
			h.log.Warn("[JANITOR] ghost record detected (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to reap)",
				"id", g.id, "status", g.status, "detail", "live-claimed status but apiserver unreachable > "+threshold.String(),
			)
			continue
		}
		if err := h.reapDeployment(g.id, "ghost-record-infra-gone", tofuWorkDir); err != nil {
			h.log.Warn("[JANITOR] ghost record reap failed", "id", g.id, "err", err.Error())
			continue
		}
		h.log.Info("[JANITOR] reaped ghost record (live-claimed status, apiserver gone)", "id", g.id, "status", g.status)
		reaped++
	}
	return reaped
}
