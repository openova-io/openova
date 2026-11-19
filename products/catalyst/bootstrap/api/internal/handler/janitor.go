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
//      (default 24h): PROBE the deployment's cloud inventory FIRST
//      (#5327). Only when the infra is verifiably GONE: delete
//      kubeconfig + tofu workdir + record. While the infra is ALIVE
//      the record + tofu state (the only destroy capability) are
//      preserved — log-only by default; with
//      CATALYST_JANITOR_DESTRUCTIVE=true the janitor runs the
//      canonical cloud destroy (the same providers.CloudProvider.Wipe
//      steps the wipe handler dispatches) and reaps only after a
//      verified-gone re-probe. See gateFailedReap.
//   3. For each with Status=wiped AND wipedAt > JanitorWipedMaxAge
//      (default 1h): cascading delete kubeconfig + tofu workdir +
//      record (HCS resources already destroyed during the wipe)
//   4. Walk kubeconfigsDir + tofuDir for orphan files/dirs whose ID
//      has no matching record in deployments/ — delete the orphan.
//
// By default we DO NOT call cloud-provider destroy here. The former
// rationale for reaping failed records anyway — "either (a) the
// operator already wiped via the wizard (Status=wiped, we just clean
// orphans), or (b) the operator forgot and the per-cloud orphan-sweep
// CronJob in the Sovereign chart picks the leftovers up" — was WRONG
// for phase-1-failed provs (#5327, hw284 dep 383db23ccaf13be5): a prov
// that failed in phase 1 has no functioning Sovereign to run that
// CronJob, and the mothership's own cloud sweeps are log-only by
// default, so reaping the record + tofu state converted a still-alive
// 2-region env into an unowned orphan — unaddressable by the canonical
// wipe endpoint (record gone → 404) and undestroyable by tofu (state
// gone). The Step-2 reap is therefore gated on the infra actually
// being gone (gateFailedReap), honouring the same DEBUG-BEFORE-WIPE
// contract buildActivePrefixes already applies to `failed` infra.
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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/providers"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
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
		// #5327 (hw284) — an ungated "failed-too-long" reap deletes the
		// record, the tofu workdir INCLUDING STATE (the only destroy
		// capability), and the kubeconfigs WITHOUT any cloud destroy, while
		// markPhase1Done deliberately leaves a failed prov's infra alive
		// (DEBUG-BEFORE-WIPE). Gate the reap on the infra actually being
		// GONE; anything ambiguous preserves (fail-closed).
		if t.Reason == "failed-too-long" && !h.gateFailedReap(t.ID, tofuWorkDir) {
			stats.FailedPreserved++
			continue
		}
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

	// Steps 3-7: the project-wide orphan sweep of every provider whose adapter
	// has one (providers.OrphanSweeper: addresses #5.138, key pairs #2620,
	// networks #4431, detached volumes #4466, buckets #4872). Where the cloud
	// does not tag these, the per-deployment Wipe cannot find what a PRIOR
	// failed or wiped prov left; they pile up against the project quotas. Every non-wiped record protects its prefix
	// (buildActivePrefixes, #4454) and the deletes are log-only unless
	// CATALYST_JANITOR_DESTRUCTIVE=true.
	swept := h.cleanCloudOrphans(tofuWorkDir)

	// #5545 — with the destructive gate closed a sweep's count is a
	// WOULD-REAP count and nothing was removed (it counts inside its
	// `if !destructive` branch). Emitting it under a `…Deleted` key made a
	// dry run report deletions that never happened: observed live on the
	// mothership 2026-08-01, `destructive=false` next to
	// `orphanOBSBucketsDeleted=61`, with zero objects touched, and two UAT
	// rows (G4/G5) recorded it. The count is honest; the key names it for
	// what it is in this mode. The filesystem sweeps (kubeconfigs / tofu
	// workdirs) delete when they run and keep their `…Deleted` keys.
	cloudKey := func(base string) string {
		if janitorDestructive() {
			return "orphan" + base + "Deleted"
		}
		return "orphan" + base + "WouldReap"
	}

	args := []any{
		"durationMs", int(time.Since(startedAt).Milliseconds()),
		"destructive", janitorDestructive(),
		"reaped", stats.Reaped,
		"reapErrors", stats.ReapErrors,
		"failedInfraPreserved", stats.FailedPreserved,
		"orphanKubeconfigsDeleted", stats.OrphanKubeconfigs,
		"orphanTofuWorkdirsDeleted", stats.OrphanTofuWorkdirs,
	}
	for _, k := range swept {
		args = append(args, cloudKey(k.kind), k.count)
	}
	h.log.Info("[JANITOR] pass complete", args...)
}

// sweptKind is the count of one kind of a cloud orphan sweep.
type sweptKind struct {
	kind  string
	count int
}

type reapTarget struct {
	ID     string
	Reason string
	AgeSec int
}

type janitorStats struct {
	Reaped             int
	ReapErrors         int
	FailedPreserved    int
	OrphanKubeconfigs  int
	GhostRecords       int
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

// gateFailedReap decides whether a "failed-too-long" reap target may proceed
// to reapDeployment. #5327 (hw284, dep 383db23ccaf13be5): reapDeployment
// deletes the record json, the tofu workdir INCLUDING STATE (the only handle
// that can ever `tofu destroy` the env), and the kubeconfigs — while
// markPhase1Done deliberately leaves a failed prov's cloud infra ALIVE for
// forensics/resume (DEBUG-BEFORE-WIPE). Ungated, the 24h reap stranded
// hw284's live 8-server 2-region env with no owner: the canonical wipe
// endpoint 404'd (record gone), tofu could no longer destroy (state gone),
// and teardown needed a raw Huawei-API break-glass (#5328/PR #5329).
//
// Contract (every ambiguity PROTECTS, mirroring cleanGhostRecords):
//   - infra GONE → true: today's reap is correct local cleanup.
//   - infra ALIVE, log-only (default) → false: ONE loud Warn; the record +
//     tofu state (the destroy capability) are preserved and the target is
//     re-evaluated next pass.
//   - infra ALIVE, CATALYST_JANITOR_DESTRUCTIVE=true → run the canonical
//     cloud destroy (the same providers.CloudProvider.Wipe steps the wipe
//     handler dispatches: tofu destroy against the still-present workdir +
//     provider orphan sweep + object-storage purge), then RE-PROBE; only a
//     verified-gone footprint reaps. Destroy error / still-alive → false.
//   - probe error / creds unresolvable / status no longer "failed" / a wipe
//     already in flight → false (fail-closed).
func (h *Handler) gateFailedReap(id, tofuWorkDir string) bool {
	val, ok := h.deployments.Load(id)
	if !ok {
		return true // record vanished mid-pass — nothing left to protect
	}
	dep, ok := val.(*Deployment)
	if !ok || dep == nil {
		return true
	}
	dep.mu.Lock()
	status := dep.Status
	wipeInFlight := dep.wipeInFlight
	dep.mu.Unlock()
	if status != "failed" || wipeInFlight {
		// Status moved since the Range snapshot (operator retry / wipe took
		// ownership) — this pass must not touch it; next pass re-evaluates.
		return false
	}

	probe := h.janitorFailedInfraProbe
	if probe == nil {
		probe = h.failedDeploymentInfraAlive
	}
	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	alive, perr := probe(probeCtx, dep, tofuWorkDir)
	cancel()
	if perr != nil {
		h.log.Warn("[JANITOR] failed record infra probe errored — preserving record+tofu state (destroy capability); fail-closed, re-evaluating next pass",
			"id", id,
			"err", perr.Error(),
		)
		return false
	}
	if !alive {
		return true
	}
	if !janitorDestructive() {
		h.log.Warn("[JANITOR] failed record " + id + " has LIVE cloud infra — preserving record+tofu state (destroy capability); set CATALYST_JANITOR_DESTRUCTIVE=true to destroy+reap")
		return false
	}

	destroy := h.janitorFailedDestroy
	if destroy == nil {
		destroy = h.destroyFailedDeploymentInfra
	}
	// Same wall-clock budget the wipe handler grants its async destroy
	// (#5193) — a 2-region tofu destroy legitimately takes tens of minutes.
	destroyCtx, dcancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer dcancel()
	if derr := destroy(destroyCtx, dep, tofuWorkDir); derr != nil {
		h.log.Warn("[JANITOR] failed record cloud destroy errored — preserving record+tofu state; re-evaluating next pass",
			"id", id,
			"err", derr.Error(),
		)
		return false
	}
	// VERIFIED destroy only: re-probe and reap solely on a confirmed-gone
	// footprint. A partial destroy keeps the record + state so the next pass
	// (or an operator wipe) can finish the job.
	alive, perr = probe(destroyCtx, dep, tofuWorkDir)
	if perr != nil || alive {
		detail := "infra still alive after destroy"
		if perr != nil {
			detail = "verification probe errored: " + perr.Error()
		}
		h.log.Warn("[JANITOR] failed record cloud destroy NOT verified gone — preserving record+tofu state; re-evaluating next pass",
			"id", id,
			"detail", detail,
		)
		return false
	}
	h.log.Info("[JANITOR] failed record cloud destroy verified gone — proceeding to reap", "id", id)
	return true
}

// failedDeploymentInfraAlive is the production infra probe behind
// gateFailedReap (#5327). It asks the deployment's own cloud provider for the
// server inventory scoped to this deployment (ListServers, the seam of the
// wizard's infrastructure tab and the wipe pre-flight), then, when that is
// empty, the adapter's providers.LivenessProber: where the cloud does not
// tag servers (Wave 5.4) the tag-scoped ListServers returns zero for a live
// deployment, and the prober lists by name prefix instead. Every error path, and a
// record that names no provider, reports alive=true (fail-closed: never
// authorise a reap on a probe that could not actually see the cloud).
func (h *Handler) failedDeploymentInfraAlive(ctx context.Context, dep *Deployment, tofuWorkDir string) (bool, error) {
	providerName := provisioner.ResolveProvider(&dep.Request)
	if providerName == "" {
		return true, errors.New("provider unknown: no provider in the record")
	}
	cp, err := providers.Get(providerName)
	if err != nil {
		return true, fmt.Errorf("providers.Get(%s): %w", providerName, err)
	}
	creds := providers.ProviderCreds{Raw: h.janitorWipeCredsRaw(cp, providerName, dep, tofuWorkDir)}
	servers, err := cp.ListServers(ctx, dep.ID, dep.Request.SovereignFQDN, creds)
	if err != nil {
		return true, fmt.Errorf("%s ListServers: %w", providerName, err)
	}
	if len(servers) > 0 {
		return true, nil
	}
	prober, ok, err := providers.As[providers.LivenessProber](providerName)
	if err != nil || !ok {
		return false, err
	}
	return prober.HasLiveInfrastructure(ctx, dep.ID, dep.Request.SovereignFQDN, creds)
}

// destroyFailedDeploymentInfra is the production destroy behind gateFailedReap
// when CATALYST_JANITOR_DESTRUCTIVE=true. It dispatches the SAME canonical
// purge sequence the wipe handler's runWipePurge dispatches — the
// providers.CloudProvider.Wipe seam (tofu destroy against the still-present
// workdir + provider orphan sweep + object-storage purge) — with the same
// body-less credential resolution. Any error, including a Wipe that reports
// per-resource errors, is surfaced so the caller preserves record + state.
func (h *Handler) destroyFailedDeploymentInfra(ctx context.Context, dep *Deployment, tofuWorkDir string) error {
	providerName := provisioner.ResolveProvider(&dep.Request)
	cp, err := providers.Get(providerName)
	if err != nil {
		return fmt.Errorf("providers.Get(%s): %w", providerName, err)
	}
	res, werr := cp.Wipe(ctx, providers.WipeSpec{
		DeploymentID:  dep.ID,
		SovereignFQDN: dep.Request.SovereignFQDN,
		Creds:         providers.ProviderCreds{Raw: h.janitorWipeCredsRaw(cp, providerName, dep, tofuWorkDir)},
		ProtectedIDs:  h.buildActivePrefixes(),
	}, func(msg string) {
		h.log.Info("[JANITOR] failed-reap destroy: " + providerName + ": " + msg)
	})
	if werr != nil {
		return werr
	}
	if res != nil && len(res.Errors) > 0 {
		return fmt.Errorf("%s wipe reported %d error(s): %s", providerName, len(res.Errors), strings.Join(res.Errors, "; "))
	}
	return nil
}

// janitorWipeCredsRaw is the wipe handler's body-less credential resolution
// (deploymentCredentials) for the janitor's background probe + destroy: the
// record, then the durable per-deployment variables file on the PVC (#5135 —
// the failed dep's workdir still exists precisely BECAUSE gateFailedReap
// preserved it), then the operator's configuration (#5193).
func (h *Handler) janitorWipeCredsRaw(cp providers.CloudProvider, providerName string, dep *Deployment, tofuWorkDir string) map[string]string {
	creds, _ := deploymentCredentials(cp, providerName, nil, nil, dep.Request, filepath.Join(tofuWorkDir, dep.ID))
	return creds
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

// cleanCloudOrphans runs the project-wide orphan sweep of every registered
// provider whose adapter implements providers.OrphanSweeper, under the
// credentials of sweepCredentials, and returns the count of each kind in the
// order the sweepers name them. A provider without a sweeper, or without
// credentials (no account of that provider on this mothership), is skipped.
// The pass may take the sum of the adapter's per-kind budgets; errors are
// logged, never returned.
func (h *Handler) cleanCloudOrphans(tofuWorkDir string) []sweptKind {
	var out []sweptKind
	for _, name := range providers.Names() {
		sweeper, ok, err := providers.As[providers.OrphanSweeper](name)
		if err != nil || !ok {
			continue
		}
		cp, _ := providers.Get(name)
		creds, ok := h.sweepCredentials(cp, name, tofuWorkDir)
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		res, err := sweeper.SweepOrphans(ctx, providers.SweepInput{
			Creds:        providers.ProviderCreds{Raw: creds},
			ProtectedIDs: h.buildActivePrefixes(), // #4454: protect by DEFAULT
			Destructive:  janitorDestructive(),
			Progress:     func(msg string) { h.log.Info("[JANITOR] " + msg) },
		})
		cancel()
		if err != nil {
			h.log.Warn("[JANITOR] orphan sweep error", "provider", name, "err", err.Error())
		}
		for _, kind := range sweeper.SweepKinds() {
			i := slices.IndexFunc(out, func(k sweptKind) bool { return k.kind == kind })
			if i < 0 {
				out = append(out, sweptKind{kind: kind})
				i = len(out) - 1
			}
			out[i].count += res.Deleted[kind]
		}
	}
	return out
}

// sweepCredentials returns the credentials the project-wide sweep of provider
// name runs under: those of the first record of that provider, by id, whose
// credentials (janitorWipeCredsRaw) hold every field its cloud profile marks
// required, else the operator's configuration alone (#4466: the last wipe
// deletes its own variables file). ok is false when neither resolves.
func (h *Handler) sweepCredentials(cp providers.CloudProvider, name, tofuWorkDir string) (map[string]string, bool) {
	var deps []*Deployment
	h.deployments.Range(func(_, v any) bool {
		if dep, ok := v.(*Deployment); ok && dep != nil && provisioner.ResolveProvider(&dep.Request) == name {
			deps = append(deps, dep)
		}
		return true
	})
	slices.SortFunc(deps, func(a, b *Deployment) int { return strings.Compare(a.ID, b.ID) })
	for _, dep := range deps {
		if creds := h.janitorWipeCredsRaw(cp, name, dep, tofuWorkDir); missingCredential(name, dep.Request.Installation, creds, false) == nil {
			return creds, true
		}
	}
	creds, _ := deploymentCredentials(cp, name, nil, nil, provisioner.Request{}, "")
	return creds, missingCredential(name, "", creds, false) == nil
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
