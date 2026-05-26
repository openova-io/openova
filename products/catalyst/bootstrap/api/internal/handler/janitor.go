// Package handler — periodic janitor that auto-purges abandoned wreckage
// from the catalyst-api-deployments PVC.
//
// Why this exists (issue #2470)
// ==============================
// Failed deployments leave wreckage on the PVC that breaks subsequent
// Sovereign provs:
//
//   - deployments/{id}.json (record)
//   - kubeconfigs/{id}.yaml (client cert + bearer token)
//   - tofu/{id}/ (workdir + state + locks)
//   - Cloud-provider resources (ECS, ELB, EIP, NAT, VPC, SG) when tofu
//     destroy partially failed
//
// The CRITICAL failure mode is the kubeconfig — see hw29 fix-forward
// session 2026-05-27 for the full RCA. On Huawei the public-EIP pool
// is small, so a wiped Sovereign's old CP EIP gets reassigned to a new
// Sovereign's CP within hours. The mothership's helmwatch.Bridge keeps
// trying every stale kubeconfig on every reconcile, presenting OLD CA-
// signed client certs to the NEW Sovereign's apiserver. That floods the
// new Sovereign's k3s with x509 cert-auth errors (2619 in 10 min was
// observed), starves apiserver request budget, kine watch event
// delivery lags, controller-runtime cached clients on the Sovereign
// see stale Get results, CNPG operator (and cert-manager, harbor,
// etc.) hit "AlreadyExists" on Create-after-Get, the cluster wedges
// forever. Same shape across every PG-backed bootstrap component.
//
// Hetzner doesn't show this — IPs aren't aggressively recycled, so
// stale kubeconfigs hit dead IPs (connection refused, no auth-flood).
//
// What this janitor does
// =======================
// Every JanitorInterval (default 1h, env-overridable per #4) we:
//
//   1. List in-memory + on-disk deployments
//   2. For each with Status=failed AND age > JanitorFailedMaxAge
//      (default 24h): delete kubeconfig + tofu workdir + record
//   3. For each with Status=wiped AND wipedAt > JanitorWipedMaxAge
//      (default 1h): cascading delete kubeconfig + tofu workdir +
//      record (HCS resources already destroyed during the wipe)
//   4. Walk kubeconfigsDir + tofuDir for orphan files/dirs whose ID
//      has no matching record in deployments/ — delete the orphan.
//
// We DO NOT call cloud-provider destroy here — that path requires the
// operator's cloud token which we GC from memory after Phase 0. For
// failed deployments older than 24h we trust that either:
//   (a) the operator already wiped via the wizard (Cancel & Wipe), in
//       which case Status=wiped and we just clean orphans, OR
//   (b) the operator forgot, in which case the cloud-provider resources
//       are stranded but the per-cloud orphan-sweep CronJob in the
//       Sovereign chart picks them up.
//
// What this janitor does NOT do
// ==============================
// It does NOT touch deployments with Status in {ready, phase1-watching,
// provisioning, tofu-applying, flux-bootstrapping, pending, wiping}.
// It does NOT touch the live in-memory Deployment struct's WipeMutex
// section — wipe is single-flight via the existing dep.Status="wiping"
// guard.
//
// Configuration knobs (per docs/INVIOLABLE-PRINCIPLES.md #4)
// ==========================================================
//   - CATALYST_JANITOR_INTERVAL: time.Duration, default 1h.
//   - CATALYST_JANITOR_FAILED_MAX_AGE: time.Duration, default 24h.
//   - CATALYST_JANITOR_WIPED_MAX_AGE: time.Duration, default 1h.
//   - CATALYST_JANITOR_ENABLED: "false" disables the janitor (e.g.
//     during a controlled debug session where the operator is mid-RCA
//     on a failed deployment and doesn't want the record wiped out
//     from under them).

package handler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultJanitorInterval     = 1 * time.Hour
	defaultJanitorFailedMaxAge = 24 * time.Hour
	defaultJanitorWipedMaxAge  = 1 * time.Hour
)

// StartJanitor launches the periodic janitor in a background goroutine.
// Returns immediately. Run from main.go after the handler + store are
// wired. ctx cancellation stops the loop.
func (h *Handler) StartJanitor(ctx context.Context, tofuWorkDir string) {
	if os.Getenv("CATALYST_JANITOR_ENABLED") == "false" {
		h.log.Info("[JANITOR] disabled via CATALYST_JANITOR_ENABLED=false")
		return
	}

	interval := durationEnv("CATALYST_JANITOR_INTERVAL", defaultJanitorInterval)
	failedMaxAge := durationEnv("CATALYST_JANITOR_FAILED_MAX_AGE", defaultJanitorFailedMaxAge)
	wipedMaxAge := durationEnv("CATALYST_JANITOR_WIPED_MAX_AGE", defaultJanitorWipedMaxAge)

	h.log.Info("[JANITOR] starting",
		"intervalSec", int(interval.Seconds()),
		"failedMaxAgeSec", int(failedMaxAge.Seconds()),
		"wipedMaxAgeSec", int(wipedMaxAge.Seconds()),
		"kubeconfigsDir", h.kubeconfigsDir,
		"tofuWorkDir", tofuWorkDir,
	)

	go func() {
		// Fire once after a brief warmup so a fresh Pod restart doesn't
		// immediately reap a failed deployment the operator was about
		// to investigate.
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		h.runJanitorPass(tofuWorkDir, failedMaxAge, wipedMaxAge)
		for {
			select {
			case <-ctx.Done():
				h.log.Info("[JANITOR] stopped")
				return
			case <-t.C:
				h.runJanitorPass(tofuWorkDir, failedMaxAge, wipedMaxAge)
			}
		}
	}()
}

// runJanitorPass performs one pass of the janitor. Exposed for tests.
func (h *Handler) runJanitorPass(tofuWorkDir string, failedMaxAge, wipedMaxAge time.Duration) {
	startedAt := time.Now()
	stats := janitorStats{}

	// Step 1: walk in-memory deployments and reap eligible ones.
	now := time.Now()
	var toReap []reapTarget
	h.deployments.Range(func(key, value any) bool {
		id, ok := key.(string)
		if !ok {
			return true
		}
		dep, ok := value.(*Deployment)
		if !ok {
			return true
		}
		dep.mu.Lock()
		status := dep.Status
		startedAt := dep.StartedAt
		finishedAt := dep.FinishedAt
		dep.mu.Unlock()

		var age time.Duration
		if !finishedAt.IsZero() {
			age = now.Sub(finishedAt)
		} else {
			age = now.Sub(startedAt)
		}

		switch status {
		case "failed":
			if age >= failedMaxAge {
				toReap = append(toReap, reapTarget{ID: id, Reason: "failed-too-long", AgeSec: int(age.Seconds())})
			}
		case "wiped":
			if age >= wipedMaxAge {
				toReap = append(toReap, reapTarget{ID: id, Reason: "wiped-bookkeeping", AgeSec: int(age.Seconds())})
			}
		}
		return true
	})

	for _, t := range toReap {
		if err := h.reapDeployment(t.ID, t.Reason, tofuWorkDir); err != nil {
			h.log.Warn("[JANITOR] reap failed",
				"id", t.ID,
				"reason", t.Reason,
				"err", err.Error(),
			)
			stats.ReapErrors++
		} else {
			h.log.Info("[JANITOR] reaped",
				"id", t.ID,
				"reason", t.Reason,
				"ageSec", t.AgeSec,
			)
			stats.Reaped++
		}
	}

	// Step 2: walk on-disk dirs for orphans (files whose ID has no
	// matching in-memory deployment AND no matching on-disk record).
	activeIDs := h.activeIDSet()
	stats.OrphanKubeconfigs = h.cleanOrphanKubeconfigs(activeIDs)
	stats.OrphanTofuWorkdirs = h.cleanOrphanTofuWorkdirs(tofuWorkDir, activeIDs)

	h.log.Info("[JANITOR] pass complete",
		"durationMs", int(time.Since(startedAt).Milliseconds()),
		"reaped", stats.Reaped,
		"reapErrors", stats.ReapErrors,
		"orphanKubeconfigsDeleted", stats.OrphanKubeconfigs,
		"orphanTofuWorkdirsDeleted", stats.OrphanTofuWorkdirs,
	)
}

type reapTarget struct {
	ID     string
	Reason string
	AgeSec int
}

type janitorStats struct {
	Reaped             int
	ReapErrors         int
	OrphanKubeconfigs  int
	OrphanTofuWorkdirs int
}

// reapDeployment is the cleanup primitive used by the janitor. It
// mirrors the wipe handler's local-cleanup steps (kubeconfig, tofu
// workdir, record) WITHOUT calling cloud-provider destroy — see file
// header for rationale. Idempotent.
func (h *Handler) reapDeployment(id, reason, tofuWorkDir string) error {
	var firstErr error
	addErr := func(label string, err error) {
		if err != nil && !os.IsNotExist(err) {
			h.log.Warn("[JANITOR] reap step failed",
				"id", id,
				"reason", reason,
				"step", label,
				"err", err.Error(),
			)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", label, err)
			}
		}
	}

	// Kubeconfig (primary + multi-region secondaries).
	if h.kubeconfigsDir != "" {
		kc := filepath.Join(h.kubeconfigsDir, id+".yaml")
		addErr("remove primary kubeconfig", os.Remove(kc))
		if entries, derr := os.ReadDir(h.kubeconfigsDir); derr == nil {
			prefix := id + "-"
			for _, e := range entries {
				n := e.Name()
				if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, ".yaml") {
					addErr("remove secondary kubeconfig "+n, os.Remove(filepath.Join(h.kubeconfigsDir, n)))
				}
			}
		}
	}

	// Tofu workdir.
	addErr("remove tofu workdir", os.RemoveAll(filepath.Join(tofuWorkDir, id)))

	// On-disk record.
	if h.store != nil {
		addErr("store delete", h.store.Delete(id))
	}

	// In-memory cache. Evict only after the on-disk pieces are gone so
	// a crash mid-reap leaves the deployment recoverable via restoreFromStore.
	h.deployments.Delete(id)

	// Eject the chroot client from k8scache.Factory so helmwatch.Bridge
	// stops connecting to a kubeconfig that no longer exists.
	if h.k8sCache != nil {
		h.k8sCache.RemoveCluster(id)
	}

	return firstErr
}

// activeIDSet returns the union of deployment IDs known in-memory or
// on-disk. Used by the orphan walker to identify files that have no
// corresponding record (truly stranded).
func (h *Handler) activeIDSet() map[string]struct{} {
	set := make(map[string]struct{})
	h.deployments.Range(func(key, _ any) bool {
		if id, ok := key.(string); ok {
			set[id] = struct{}{}
		}
		return true
	})
	if h.store != nil {
		// Store has no List() — walk the dir directly. Filter to *.json
		// and strip the suffix to recover the ID.
		if entries, err := os.ReadDir(h.store.Dir()); err == nil {
			for _, e := range entries {
				n := e.Name()
				if !strings.HasSuffix(n, ".json") {
					continue
				}
				set[strings.TrimSuffix(n, ".json")] = struct{}{}
			}
		}
	}
	return set
}

// cleanOrphanKubeconfigs walks kubeconfigsDir and deletes any file
// whose ID (prefix before . or -) has no matching deployment.
func (h *Handler) cleanOrphanKubeconfigs(activeIDs map[string]struct{}) int {
	if h.kubeconfigsDir == "" {
		return 0
	}
	entries, err := os.ReadDir(h.kubeconfigsDir)
	if err != nil {
		return 0
	}
	deleted := 0
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".yaml") {
			continue
		}
		// Extract ID: filename is {id}.yaml or {id}-{region}.yaml
		id := strings.TrimSuffix(n, ".yaml")
		if idx := strings.Index(id, "-"); idx > 0 {
			id = id[:idx]
		}
		if _, ok := activeIDs[id]; ok {
			continue
		}
		path := filepath.Join(h.kubeconfigsDir, n)
		if err := os.Remove(path); err == nil {
			deleted++
			h.log.Info("[JANITOR] orphan kubeconfig deleted",
				"file", n,
				"derivedID", id,
			)
		}
	}
	return deleted
}

// cleanOrphanTofuWorkdirs walks tofuWorkDir and deletes any sub-dir
// whose name (deployment ID) has no matching deployment.
func (h *Handler) cleanOrphanTofuWorkdirs(tofuWorkDir string, activeIDs map[string]struct{}) int {
	if tofuWorkDir == "" {
		return 0
	}
	entries, err := os.ReadDir(tofuWorkDir)
	if err != nil {
		return 0
	}
	deleted := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if _, ok := activeIDs[id]; ok {
			continue
		}
		path := filepath.Join(tofuWorkDir, id)
		if err := os.RemoveAll(path); err == nil {
			deleted++
			h.log.Info("[JANITOR] orphan tofu workdir deleted",
				"dir", id,
			)
		}
	}
	return deleted
}

// durationEnv parses a duration env var with a default fallback.
func durationEnv(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

