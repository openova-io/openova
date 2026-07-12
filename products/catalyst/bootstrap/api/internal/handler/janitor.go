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
//   - CATALYST_JANITOR_DESTRUCTIVE: "true" arms the cloud-resource sweeps
//     (EIPs/keypairs/VPCs/EVS) to ACTUALLY delete. Default FALSE → the
//     sweeps run LOG-ONLY: each pass logs exactly what it WOULD reap
//     (prefix, resource, reason) but deletes nothing. #4466 — makes
//     "deletes prod infra" opt-in, not default. The b9f9590b node
//     self-reap would have surfaced in this log-only window first.
//   - CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS: comma/space-separated
//     deployment id(s) (or 8-char prefixes) hard-excluded from EVERY
//     delete path, independent of status inference. #4466 belt-and-
//     suspenders over the buildActivePrefixes denylist — set this to the
//     live Sovereign's dep id so cleanup can never reach it even if status
//     inference regresses.

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

// janitorDestructive reports whether the cloud-resource sweeps (EIPs /
// keypairs / VPCs / EVS) are permitted to ACTUALLY delete. #4466 makes
// "deletes prod infra" opt-in: until CATALYST_JANITOR_DESTRUCTIVE=true is
// explicitly set the sweeps run in LOG-ONLY mode — every pass logs exactly
// the resource it WOULD reap (prefix, resource, reason) but deletes
// nothing. The b9f9590b self-reap (all 12 ECS nodes deleted ~2.5 min after
// convergence) would have surfaced in this log-only window before any node
// died. Default FALSE — fail safe.
func janitorDestructive() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("CATALYST_JANITOR_DESTRUCTIVE")), "true")
}

// janitorExtraProtectedPrefixes returns the explicit, status-independent
// do-not-touch deployment-ID prefixes from CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS
// (comma/space-separated full IDs or 8-char prefixes). #4466 belt-and-
// suspenders: even if status inference (buildActivePrefixes) ever regresses
// again, the CURRENTLY-active deployment id(s) named here are hard-excluded
// from every delete path. Operators set this to the live Sovereign's dep id
// so cleanup automation can never reach it regardless of any status bug.
func janitorExtraProtectedPrefixes() map[string]struct{} {
	out := map[string]struct{}{}
	raw := os.Getenv("CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS")
	if raw == "" {
		return out
	}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		f = strings.TrimSpace(f)
		if len(f) >= 8 {
			out[f[:8]] = struct{}{}
		}
	}
	return out
}

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
		// #4466: cloud-resource sweeps are LOG-ONLY until this is true.
		"cloudSweepDestructive", janitorDestructive(),
		"explicitProtectedPrefixes", len(janitorExtraProtectedPrefixes()),
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

	// Step 1b (#4677) — ghost-record GC. A record stuck in a live-claimed
	// status ("ready") whose apiserver is persistently unreachable is a ghost
	// left by a decommission that bypassed the full wipe (the omantel.biz ghost
	// the console kept rendering). Log-only by default; reaps only under the
	// destructive gate. Never touches an in-flight / terminal / reachable dep.
	stats.GhostRecords = h.cleanGhostRecords(tofuWorkDir)

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

	// Step 5: #4431 — per-provider orphan-VPC sweep. VPCs are not tagged
	// on HCS so per-deployment Wipe.cascadeDeleteVPC finds them only via
	// the parent Sovereign FQDN in tfvars; a wipe whose VPC teardown
	// lagged (peering pin / residual ports) leaks a catalyst-* VPC that
	// occupies the project quota (Kom4DC me-east-215 cap = 5, NOT
	// tenant-raisable) until a manual bastion cleanup — false-failing the
	// next fresh multi-region prov at the G68 VPC pre-flight. EIPs (Step
	// 3) and keypairs (Step 4) already had a reclaim sweep; VPCs did not.
	// Same catalyst- prefix match + 8-char in-flight protection, with
	// bastion / non-catalyst VPCs hard-protected.
	stats.OrphanVPCs = h.cleanOrphanVPCsHuawei(tofuWorkDir, activeIDs)

	// Step 6: #4466 — standalone detached-EVS-orphan sweep. A node
	// self-reap BEFORE a wipe strands CSI `pvc-*` volumes (283 on
	// b9f9590b) as pure quota debt with no controller left to detach +
	// delete them. EIPs/keypairs/VPCs each had a reclaim sweep; detached
	// volumes did not. Only status=available, zero-attachment pvc-* volumes
	// owned by no live dep are eligible (same protect-by-default guard).
	stats.OrphanEVS = h.cleanOrphanEVSHuawei(tofuWorkDir, activeIDs)

	// Step 7: #4872 — per-account orphan-OBS-bucket sweep. OBS buckets are
	// not tagged on HCS (Wave 5.4 disabled tags) so the per-deployment Wipe
	// (purgeOBSBuckets) reaches only buckets bearing the CURRENT Sovereign's
	// prefix; a bucket from a PRIOR failed/wiped prov — or one whose wipe
	// never ran — falls out of reach and piles up against the OBS 100-bucket
	// account quota, false-failing the next fresh prov's Phase-0 tofu apply
	// with `Code=TooManyBuckets` (58 May+June orphans back to hw01 observed).
	// EIPs/keypairs/VPCs/EVS each already had a project-wide reclaim sweep;
	// object storage did not. Same catalyst- prefix match + 8-char in-flight
	// protection + log-only gate as its siblings; bastion/non-catalyst
	// buckets are hard-protected in isReclaimableOrphanOBSBucket itself.
	stats.OrphanOBSBuckets = h.cleanOrphanOBSBucketsHuawei(tofuWorkDir, activeIDs)

	h.log.Info("[JANITOR] pass complete",
		"durationMs", int(time.Since(startedAt).Milliseconds()),
		"destructive", janitorDestructive(),
		"reaped", stats.Reaped,
		"reapErrors", stats.ReapErrors,
		"orphanKubeconfigsDeleted", stats.OrphanKubeconfigs,
		"orphanTofuWorkdirsDeleted", stats.OrphanTofuWorkdirs,
		"orphanEIPsDeleted", stats.OrphanEIPs,
		"orphanKeypairsDeleted", stats.OrphanKeypairs,
		"orphanVPCsDeleted", stats.OrphanVPCs,
		"orphanEVSDeleted", stats.OrphanEVS,
		"orphanOBSBucketsDeleted", stats.OrphanOBSBuckets,
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
	GhostRecords       int
	OrphanTofuWorkdirs int
	OrphanEIPs         int
	OrphanKeypairs     int
	OrphanVPCs         int
	OrphanEVS          int
	OrphanOBSBuckets   int
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

// buildActivePrefixes returns the set of 8-char deployment-ID prefixes
// whose cloud infra (EIP / keypair / VPC) the orphan sweeps MUST NOT
// reclaim. Shared by all three sweeps (EIPs / keypairs / VPCs).
//
// #4454 — FAIL-SAFE INVERSION. The earlier design was an ALLOWLIST that
// protected only in-flight statuses (pending … phase1-watching, wiping)
// and deliberately left ready/failed/wiped/adopted reclaimable. That
// fails OPEN: the instant a fresh Sovereign flips to `ready` its prefix
// dropped out of the protected set and its catalyst-<prefix>-* EIP /
// keypair / VPC-peering / VPC were reaped as "orphans," cascading the
// node deletion. On dep b9f9590b (omantel.biz) all 12 ECS nodes went
// DELETED in a 1-second window ~2.5 min after convergence.
//
// We now PROTECT BY DEFAULT and exclude ONLY genuinely-wiped records.
// A `wiped` record SHOULD have no cloud infra, so anything still bearing
// its prefix is a true leak and stays sweep-eligible. EVERY other status
// — pending … phase1-watching, ready, adopted, cutover-running,
// cutover-complete, failed, AND any status added in the future —
// protects its prefix. A future-added status therefore fails SAFE
// (protected), not OPEN (reaped). `failed` is protected too, honouring
// DEBUG-BEFORE-WIPE: never reap infra we may still need to forensically
// read.
func (h *Handler) buildActivePrefixes() map[string]struct{} {
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
		case "wiped":
			// A wiped record SHOULD have no cloud infra; anything left
			// is a genuine leak → eligible for the sweep. Do NOT protect.
		default:
			// ANY non-wiped record protects its prefix (fail-safe).
			if len(id) >= 8 {
				activePrefixes[id[:8]] = struct{}{}
			}
		}
		return true
	})
	// #4466 belt-and-suspenders: merge the explicit, status-INDEPENDENT
	// active-deployment do-not-touch allowlist. Even if the status-inference
	// loop above ever regresses (the exact b9f9590b class of fault), the
	// operator-named live deployment id(s) stay hard-protected.
	for p := range janitorExtraProtectedPrefixes() {
		activePrefixes[p] = struct{}{}
	}
	return activePrefixes
}

// huaweiSweepCreds is a discovered set of project-wide Huawei creds the
// orphan sweeps run under. All catalyst-huawei deployments in one
// mothership share the same HCS project, so any one set drives the
// project-wide sweep.
type huaweiSweepCreds struct {
	AK        string
	SK        string
	ProjectID string
	Region    string
}

// discoverHuaweiCreds finds a working set of Huawei creds for the
// project-wide orphan sweep. It first walks the per-deployment tfvars on
// the PVC (the historical source). #4466 cred-source fallback: when the
// LAST wipe has deleted its own `tofu.auto.tfvars.json` (the only cred
// source the tfvars-walk knows), the walk short-circuits to "no huawei
// deployments" and the post-wipe orphan sweep never runs — leaving the
// very orphans the wipe failed to delete (283 stranded EVS on b9f9590b).
// So when the tfvars walk comes up empty we fall back to the
// `huawei-operator-creds` Secret, projected into the catalyst-api Pod as
// CATALYST_HUAWEI_ACCESS_KEY / _SECRET_KEY / _PROJECT_ID / _REGION — the
// same env the provisioner reads. Returns ok=false only when BOTH sources
// are empty (genuinely no Huawei on this mothership).
func discoverHuaweiCreds(tofuWorkDir string) (huaweiSweepCreds, bool) {
	var c huaweiSweepCreds
	if tofuWorkDir != "" {
		if entries, err := os.ReadDir(tofuWorkDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				data, err := os.ReadFile(filepath.Join(tofuWorkDir, e.Name(), "tofu.auto.tfvars.json"))
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
					c.AK, c.SK, c.ProjectID, c.Region = akV, skV, pidV, regV
					if c.Region == "" {
						c.Region = "me-east-215"
					}
					return c, true
				}
			}
		}
	}
	// Fallback: the huawei-operator-creds Secret env. This is what keeps a
	// post-wipe sweep alive after the wipe deletes the dep's tfvars.
	c.AK = os.Getenv("CATALYST_HUAWEI_ACCESS_KEY")
	c.SK = os.Getenv("CATALYST_HUAWEI_SECRET_KEY")
	c.ProjectID = os.Getenv("CATALYST_HUAWEI_PROJECT_ID")
	c.Region = os.Getenv("CATALYST_HUAWEI_REGION")
	if c.AK != "" && c.SK != "" && c.ProjectID != "" {
		if c.Region == "" {
			c.Region = "me-east-215"
		}
		return c, true
	}
	return huaweiSweepCreds{}, false
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
	creds, ok := discoverHuaweiCreds(tofuWorkDir)
	if !ok {
		// No huawei deployments on this mothership — nothing to sweep.
		return 0
	}
	hp := h.huaweiProvider("orphan-EIP")
	if hp == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// #4454: protect by DEFAULT (every non-wiped record), reclaim ONLY
	// genuinely-wiped records' leaked infra. See buildActivePrefixes for
	// the fail-safe-inversion rationale (the b9f9590b self-destruct).
	activePrefixes := h.buildActivePrefixes()
	deleted, err := hp.SweepOrphanEIPs(ctx, creds.AK, creds.SK, creds.ProjectID, creds.Region, activePrefixes, janitorDestructive(), func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-EIP sweep error", "err", err.Error())
		return deleted
	}
	return deleted
}

// huaweiProvider resolves the wired Huawei provider, logging + returning
// nil when it is unavailable. Shared by every orphan sweep. `what`
// labels the log line (e.g. "orphan-EVS").
func (h *Handler) huaweiProvider(what string) *huaweiprovider.Provider {
	cp, perr := providers.Get("huawei")
	if perr != nil || cp == nil {
		h.log.Warn("[JANITOR] huawei provider not wired — skipping "+what+" sweep", "err", perr)
		return nil
	}
	hp, ok := cp.(*huaweiprovider.Provider)
	if !ok || hp == nil {
		h.log.Warn("[JANITOR] huawei provider type assertion failed — skipping " + what + " sweep")
		return nil
	}
	return hp
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
// Same fail-safe protection scheme as cleanOrphanEIPsHuawei (#4454):
// buildActivePrefixes protects EVERY non-`wiped` record's 8-char ID
// prefix. Only a `wiped` record (which SHOULD have no cloud infra) leaves
// its keypairs reclaimable.
func (h *Handler) cleanOrphanKeypairsHuawei(tofuWorkDir string, activeIDs map[string]struct{}) int {
	creds, ok := discoverHuaweiCreds(tofuWorkDir)
	if !ok {
		return 0
	}
	hp := h.huaweiProvider("orphan-keypair")
	if hp == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// #4454: protect by DEFAULT (every non-wiped record). See
	// buildActivePrefixes for the fail-safe-inversion rationale.
	activePrefixes := h.buildActivePrefixes()
	deleted, err := hp.SweepOrphanKeypairs(ctx, creds.AK, creds.SK, creds.ProjectID, creds.Region, activePrefixes, janitorDestructive(), func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-keypair sweep error", "err", err.Error())
		return deleted
	}
	return deleted
}

// cleanOrphanVPCsHuawei mirrors cleanOrphanEIPsHuawei / cleanOrphanKeypairsHuawei
// but for VPCs (#4431). VPCs are untagged on HCS so the per-deployment
// Wipe finds them only via the parent Sovereign FQDN in tfvars; a wipe
// whose VPC teardown lagged (peering pin / residual ports) leaks an
// orphaned catalyst-* VPC that occupies the HCS project quota (default
// cap = 5 on Kom4DC me-east-215, NOT tenant-raisable) forever — so the
// next fresh 2-region prov false-fails the G68 VPC pre-flight at
// "project at 5/5 VPCs". This sweep is the missing reclaim path: EIPs
// and keypairs already had one, VPCs did not.
//
// Same fail-safe protection scheme as its siblings (#4454):
// buildActivePrefixes protects EVERY non-`wiped` record's 8-char ID
// prefix; only a `wiped` record leaves its VPCs reclaimable. Bastion /
// non-catalyst VPCs are hard-protected in SweepOrphanVPCs itself.
func (h *Handler) cleanOrphanVPCsHuawei(tofuWorkDir string, activeIDs map[string]struct{}) int {
	creds, ok := discoverHuaweiCreds(tofuWorkDir)
	if !ok {
		return 0
	}
	hp := h.huaweiProvider("orphan-VPC")
	if hp == nil {
		return 0
	}
	// VPC cascade teardown (peerings → floating-IPs → subnets → ports →
	// VPC) is slower than the EIP/keypair single-DELETE, so give it a
	// wider budget than the 2-minute sibling sweeps.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// #4454: protect by DEFAULT (every non-wiped record). See
	// buildActivePrefixes for the fail-safe-inversion rationale.
	activePrefixes := h.buildActivePrefixes()
	deleted, err := hp.SweepOrphanVPCs(ctx, creds.AK, creds.SK, creds.ProjectID, creds.Region, activePrefixes, janitorDestructive(), func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-VPC sweep error", "err", err.Error())
		return deleted
	}
	return deleted
}

// cleanOrphanEVSHuawei is the detached-EVS-orphan sweep (#4466). A node
// self-reap BEFORE a wipe strands CSI `pvc-*` volumes — 283 were stranded
// on b9f9590b as pure quota debt because nothing was left to detach +
// delete them. EIPs / keypairs / VPCs each already had a project-wide
// reclaim sweep; detached volumes did not. Same protect-by-default guard
// (buildActivePrefixes) + same log-only gate (janitorDestructive) as its
// siblings. Only `status:available`, zero-attachment `pvc-*` volumes that
// belong to no live dep are eligible.
func (h *Handler) cleanOrphanEVSHuawei(tofuWorkDir string, activeIDs map[string]struct{}) int {
	creds, ok := discoverHuaweiCreds(tofuWorkDir)
	if !ok {
		return 0
	}
	hp := h.huaweiProvider("orphan-EVS")
	if hp == nil {
		return 0
	}
	// #5028 — the destructive reap now throttles deletes ≥~0.9s apart (the
	// HCS EVS API rate-limits HARD → un-throttled bulk deletes 429 wholesale).
	// A one-time backlog near the 400-volume quota therefore needs real
	// wall-clock to drain, so widen the budget from 3m to 10m (≈600 deletes)
	// — enough to clear a full backlog in a single pass. The next pass (5m
	// later) continues anything still outstanding.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	activePrefixes := h.buildActivePrefixes()
	deleted, err := hp.SweepOrphanEVS(ctx, creds.AK, creds.SK, creds.ProjectID, creds.Region, activePrefixes, janitorDestructive(), func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-EVS sweep error", "err", err.Error())
		return deleted
	}
	return deleted
}

// cleanOrphanOBSBucketsHuawei is the object-storage orphan sweep (#4872). OBS
// buckets are not tagged on HCS so the per-deployment Wipe (purgeOBSBuckets)
// reaches only buckets bearing the CURRENT Sovereign's prefix; a bucket from a
// PRIOR failed/wiped prov falls out of reach and accumulates against the OBS
// 100-bucket account quota until the next fresh prov false-fails Phase-0 with
// `Code=TooManyBuckets`. EIPs / keypairs / VPCs / EVS each already had a
// project-wide reclaim sweep; object storage did not. Same protect-by-default
// guard (buildActivePrefixes) + same log-only gate (janitorDestructive) as its
// siblings. Only catalyst-prefixed buckets that carry no in-flight
// deployment-ID prefix are eligible; bastion / non-catalyst buckets are
// hard-protected.
func (h *Handler) cleanOrphanOBSBucketsHuawei(tofuWorkDir string, activeIDs map[string]struct{}) int {
	creds, ok := discoverHuaweiCreds(tofuWorkDir)
	if !ok {
		return 0
	}
	hp := h.huaweiProvider("orphan-OBS-bucket")
	if hp == nil {
		return 0
	}
	// Emptying + deleting many buckets (each may hold a full CNPG/openbao
	// backup set + incomplete multipart parts) is slower than the single-
	// DELETE sibling sweeps, so give it a wider budget.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	activePrefixes := h.buildActivePrefixes()
	deleted, err := hp.SweepOrphanOBSBuckets(ctx, creds.AK, creds.SK, creds.ProjectID, creds.Region, activePrefixes, janitorDestructive(), func(msg string) {
		h.log.Info("[JANITOR] " + msg)
	})
	if err != nil {
		h.log.Warn("[JANITOR] orphan-OBS-bucket sweep error", "err", err.Error())
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
