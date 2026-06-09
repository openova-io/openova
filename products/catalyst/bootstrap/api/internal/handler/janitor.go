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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	huaweiprovider "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers/huawei"
)

const (
	// Wave 5.150 (Refs #2512, #2513 — Principle 16 L2 INCIDENT-MGMT):
	// 1h was way too slow for high-iteration debug cycles. A failed
	// prov can leak 3+ EIPs / 1 VPC / 1 SG in 90 seconds and the next
	// prov 2 minutes later hits the quota cap. 5 minutes is short
	// enough to keep up with rapid retries while still leaving an
	// operator a meaningful window to inspect a failed dep before the
	// janitor reaps it (failedMaxAge stays 24h — reap-by-age is
	// separately gated).
	defaultJanitorInterval     = 5 * time.Minute
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

	// Step 3: Wave 5.138 — per-provider orphan-EIP sweep. EIPs are not
	// tagged on HCS (Wave 5.4 disabled tags) so per-deployment Wipe
	// cannot find EIPs from PRIOR failed provs. They accumulate up to
	// the project quota (publicIp default cap = 10) and the next
	// fresh prov fails fast with "error allocating EIP: conflict". This
	// sweep identifies catalyst-owned EIPs through their bandwidth name
	// prefix and deletes any status=DOWN + unbound ones across all
	// known HCS tfvars on the PVC. Idempotent + safe during active provs
	// (bound + ACTIVE EIPs are never touched).
	stats.OrphanEIPs = h.cleanOrphanEIPsHuawei(tofuWorkDir, activeIDs)

	// Step 4: G73 (Refs #2620) — per-provider orphan-keypair sweep.
	// Keypairs are not tagged on HCS so per-deployment Wipe.sweepKeypairs
	// finds them only via the parent Sovereign FQDN in tfvars; every
	// keypair belonging to a wiped record falls out of reach and
	// accumulates against the project quota (default cap = 100). The
	// 2026-05-27 Kom4DC RCA observed 68 orphans piled up across failed
	// provs (memory feedback_hcs_kom4dc_wipe_cascade_quirks.md). Same
	// shape as Step 3: catalyst- prefix match, in-flight protection by
	// 8-char deployment-ID prefix, bastion-* hard-protected.
	stats.OrphanKeypairs = h.cleanOrphanKeypairsHuawei(tofuWorkDir, activeIDs)

	h.log.Info("[JANITOR] pass complete",
		"durationMs", int(time.Since(startedAt).Milliseconds()),
		"reaped", stats.Reaped,
		"reapErrors", stats.ReapErrors,
		"orphanKubeconfigsDeleted", stats.OrphanKubeconfigs,
		"orphanTofuWorkdirsDeleted", stats.OrphanTofuWorkdirs,
		"orphanEIPsDeleted", stats.OrphanEIPs,
		"orphanKeypairsDeleted", stats.OrphanKeypairs,
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
	OrphanEIPs         int
	OrphanKeypairs     int
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

// cleanOrphanEIPsHuawei walks the per-deployment tfvars files on the
// PVC to find a working set of huawei creds (any active deployment's
// AK/SK/project_id/region), then calls huawei.Provider.SweepOrphanEIPs
// which lists all publicIPs in the project and deletes catalyst-owned
// status=DOWN + unbound ones.
//
// Wave 5.138 (2026-05-27): EIPs are NOT tagged on HCS so per-deployment
// Wipe.listEIPsByTag cannot find EIPs from PRIOR failed provs. They
// accumulate to project quota (publicIp default cap = 10) and the next
// fresh prov fails fast with "error allocating EIP: conflict". This
// sweep solves the gap; see huawei.Provider.SweepOrphanEIPs for the
// full rationale.
//
// Returns the number of EIPs deleted. Returns 0 + logs on any error.
// Safe to call even when no huawei deployment exists (no-op).
func (h *Handler) cleanOrphanEIPsHuawei(tofuWorkDir string, activeIDs map[string]struct{}) int {
	if tofuWorkDir == "" {
		return 0
	}
	entries, err := os.ReadDir(tofuWorkDir)
	if err != nil {
		return 0
	}
	// Find the first deployment with valid huawei creds in its tfvars.
	// All catalyst-huawei deployments in a single mothership share the
	// same HCS project so any one set of creds works for the project-
	// wide sweep.
	var ak, sk, projectID, region string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tfvarsPath := filepath.Join(tofuWorkDir, e.Name(), "tofu.auto.tfvars.json")
		data, err := os.ReadFile(tfvarsPath)
		if err != nil {
			continue
		}
		var tv map[string]any
		if err := json.Unmarshal(data, &tv); err != nil {
			continue
		}
		akV, _ := tv["huawei_access_key"].(string)
		skV, _ := tv["huawei_secret_key"].(string)
		pidV, _ := tv["huawei_project_id"].(string)
		regV, _ := tv["huawei_region"].(string)
		if akV != "" && skV != "" && pidV != "" {
			ak = akV
			sk = skV
			projectID = pidV
			region = regV
			if region == "" {
				region = "me-east-215"
			}
			break
		}
	}
	if ak == "" {
		// No huawei deployments on this mothership — nothing to sweep.
		return 0
	}
	cp, perr := providers.Get("huawei")
	if perr != nil || cp == nil {
		h.log.Warn("[JANITOR] huawei provider not wired — skipping orphan-EIP sweep", "err", perr)
		return 0
	}
	hp, ok := cp.(*huaweiprovider.Provider)
	if !ok || hp == nil {
		h.log.Warn("[JANITOR] huawei provider type assertion failed — skipping orphan-EIP sweep")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// G16 (Refs #2555): protect ONLY in-flight deployments. G15's first
	// cut used activeIDs (every record on disk), which meant a FAILED
	// deployment's EIPs were "skipped" forever because its record was
	// still in the set. Result: hw43+hw44 failed deployments' EIPs
	// piled up to project quota, starving the next prov.
	// In-flight statuses (must protect): pending, provisioning,
	// tofu-applying, flux-bootstrapping, phase1-watching, wiping.
	// Terminal statuses (let janitor reclaim): ready (bound EIPs won't
	// match orphan criteria anyway), failed, wiped, adopted.
	activePrefixes := map[string]struct{}{}
	h.deployments.Range(func(_, val any) bool {
		dep, ok := val.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		st := dep.Status
		id := dep.ID
		dep.mu.Unlock()
		switch st {
		case "pending", "provisioning", "tofu-applying",
			"flux-bootstrapping", "phase1-watching", "wiping":
			if len(id) >= 8 {
				activePrefixes[id[:8]] = struct{}{}
			}
		}
		return true
	})
	deleted, err := hp.SweepOrphanEIPs(ctx, ak, sk, projectID, region, activePrefixes, func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-EIP sweep error", "err", err.Error())
		return deleted
	}
	return deleted
}

// cleanOrphanKeypairsHuawei mirrors cleanOrphanEIPsHuawei but for SSH
// keypairs. Keypairs are not tagged on HCS, so per-deployment Wipe
// finds them only via the parent Sovereign FQDN in tfvars — every
// keypair on a record wiped via the canonical wipe endpoint falls out
// of reach and piles up against the project quota (default cap = 100).
//
// G73 (Refs #2620): walks `tofuWorkDir` for any per-deployment
// `tofu.auto.tfvars.json` carrying huawei creds, then calls
// `huawei.Provider.SweepOrphanKeypairs` for the project-wide sweep.
//
// Same in-flight protection scheme as cleanOrphanEIPsHuawei: only
// statuses `pending` / `provisioning` / `tofu-applying` /
// `flux-bootstrapping` / `phase1-watching` / `wiping` mark a
// deployment's 8-char ID prefix as protected. Terminal statuses
// (ready / failed / wiped / adopted) leave their keypairs reclaimable.
func (h *Handler) cleanOrphanKeypairsHuawei(tofuWorkDir string, activeIDs map[string]struct{}) int {
	if tofuWorkDir == "" {
		return 0
	}
	entries, err := os.ReadDir(tofuWorkDir)
	if err != nil {
		return 0
	}
	var ak, sk, projectID, region string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		tfvarsPath := filepath.Join(tofuWorkDir, e.Name(), "tofu.auto.tfvars.json")
		data, err := os.ReadFile(tfvarsPath)
		if err != nil {
			continue
		}
		var tv map[string]any
		if err := json.Unmarshal(data, &tv); err != nil {
			continue
		}
		akV, _ := tv["huawei_access_key"].(string)
		skV, _ := tv["huawei_secret_key"].(string)
		pidV, _ := tv["huawei_project_id"].(string)
		regV, _ := tv["huawei_region"].(string)
		if akV != "" && skV != "" && pidV != "" {
			ak = akV
			sk = skV
			projectID = pidV
			region = regV
			if region == "" {
				region = "me-east-215"
			}
			break
		}
	}
	if ak == "" {
		return 0
	}
	cp, perr := providers.Get("huawei")
	if perr != nil || cp == nil {
		h.log.Warn("[JANITOR] huawei provider not wired — skipping orphan-keypair sweep", "err", perr)
		return 0
	}
	hp, ok := cp.(*huaweiprovider.Provider)
	if !ok || hp == nil {
		h.log.Warn("[JANITOR] huawei provider type assertion failed — skipping orphan-keypair sweep")
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	activePrefixes := map[string]struct{}{}
	h.deployments.Range(func(_, val any) bool {
		dep, ok := val.(*Deployment)
		if !ok || dep == nil {
			return true
		}
		dep.mu.Lock()
		st := dep.Status
		id := dep.ID
		dep.mu.Unlock()
		switch st {
		case "pending", "provisioning", "tofu-applying",
			"flux-bootstrapping", "phase1-watching", "wiping":
			if len(id) >= 8 {
				activePrefixes[id[:8]] = struct{}{}
			}
		}
		return true
	})
	deleted, err := hp.SweepOrphanKeypairs(ctx, ak, sk, projectID, region, activePrefixes, func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-keypair sweep error", "err", err.Error())
		return deleted
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
		// #3147/#3151: `_shared` is the cloud-agnostic cloud-init staging dir
		// (a sibling of every per-deployment workdir, staged by stageModule so
		// the `${path.module}/../_shared/` templatefile reference resolves at
		// apply time), NOT an orphaned deployment. Deployment IDs are hex;
		// reserved dirs are underscore-prefixed. Reaping `_shared` races the
		// next prov's tofu plan into a "no file at ./../_shared/..." failure.
		if strings.HasPrefix(id, "_") {
			continue
		}
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

