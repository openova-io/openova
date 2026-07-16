package handler

// wipe_evs_backstop.go — #5028 post-destroy EVS backstop.
//
// THE LEAK this closes
// ────────────────────
// The canonical wipe runs `tofu destroy`, which removes only the
// Terraform-declared infra (nodes, IaC disks, VPC/EIP/NAT/…). The CSI driver
// (huawei-evs-csi) provisions ONE cloud EVS volume per PVC at RUNTIME, named
// `pvc-<uuid>` with NO deployment identity — outside tofu state. `tofu
// destroy` tears down the nodes before the in-cluster CSI controller can
// delete those volumes, so every wipe strands them as status=available.
//
// #4677 (wipe_drain.go) drains the PVCs BEFORE tofu destroy so CSI releases
// the volumes — but ONLY while the cluster's apiserver is still reachable. On
// an already-dead env (or when CSI can't finish before the drain deadline)
// the drain no-ops and its own comment promises "the post-destroy cloud-GC
// (swept by detached-status, not name) is the backstop for that case." That
// backstop was never wired into the wipe path: the ONLY caller of
// SweepOrphanEVS was the periodic janitor, which is log-only unless
// CATALYST_JANITOR_DESTRUCTIVE=true. So the stranded `pvc-*` volumes piled up
// as available — 332 of the 400-volume HCS kom4dc quota after ~3 wipes,
// hard-blocking the next fresh prov's PVCs (413 VolumeLimitExceeded) → no DB
// → keycloak CrashLoop → convergence wedged (live on hw244, 2026-07-12).
//
// THE FIX
// ───────
// After tofu destroy, reap this deployment's now-detached `pvc-*` volumes
// (SweepOrphanEVS, throttled + 429-backoff per #5028) while hard-protecting
// EVERY OTHER live deployment: run buildActivePrefixes() (protect-by-default,
// the #4454 fail-safe denylist) and remove ONLY the id being wiped so its
// volumes become reclaimable. A volume of any OTHER live env — even one
// momentarily detached mid-roll — stays protected. Because buildActivePrefixes
// protects only NON-wiped records, this same pass also reaps any available
// `pvc-*` left behind by a PRIOR partially-failed wipe, so quota cannot fill
// across provs. `available` + zero-attachment + `pvc-` prefix is enforced in
// isReclaimableOrphanEVS, so an in-use volume or the bastion is never touched.

import (
	"context"
	"strings"
)

// evsReaper is the subset of the huawei provider the post-wipe EVS backstop
// needs. *huawei.Provider satisfies it in production; tests supply a recording
// fake so the wiring (right active set + destructive=true) is asserted without
// an HCS endpoint.
type evsReaper interface {
	SweepOrphanEVS(ctx context.Context, ak, sk, projectID, region string,
		activeDepIDPrefixes map[string]struct{}, destructive bool, progress func(msg string)) (int, error)
}

// activePrefixesForWipeBackstop returns the protect-by-default active-prefix
// set with ONLY the deployment being wiped removed. Every other non-`wiped`
// record stays protected (fail-safe), so the backstop reaps this dep's
// detached volumes (and any orphaned-available `pvc-*` from previously-wiped
// deps) but never a live neighbour's.
func (h *Handler) activePrefixesForWipeBackstop(id string) map[string]struct{} {
	active := h.buildActivePrefixes()
	if len(id) >= 8 {
		delete(active, id[:8])
	}
	return active
}

// reapDeploymentEVSBackstop runs the post-destroy EVS reap for the wiped
// deployment. destructive is ALWAYS true here — the operator explicitly
// invoked a wipe, so (unlike the background janitor's log-only default) the
// backstop acts, exactly as VPCReclaimPreflight does for the in-band VPC
// reclaim. Best-effort: an error is logged, never surfaced as a wipe failure.
// Returns the number of volumes reaped.
func (h *Handler) reapDeploymentEVSBackstop(ctx context.Context, reaper evsReaper, id string, creds huaweiSweepCreds, progress func(msg string)) int {
	if reaper == nil || strings.TrimSpace(creds.AK) == "" || strings.TrimSpace(creds.SK) == "" || strings.TrimSpace(creds.ProjectID) == "" {
		return 0
	}
	if progress == nil {
		progress = func(string) {}
	}
	active := h.activePrefixesForWipeBackstop(id)
	reaped, err := reaper.SweepOrphanEVS(ctx, creds.AK, creds.SK, creds.ProjectID, creds.Region, active, true /*destructive — operator asked to wipe*/, progress)
	if err != nil {
		h.log.Warn("[WIPE] post-destroy EVS backstop error (#5028)", "err", err.Error(), "id", id)
	}
	return reaped
}

// applyWorkdirEVSCredsFallback fills the empty Huawei AK/SK/project_id/region
// keys in the resolved wipe-creds map from the durable per-deployment
// tofu.auto.tfvars.json — the #5135 fix. On the canonical body-less wipe after
// a catalyst-api Pod roll, buildWipeCredsRaw yields empty access_key/secret_key
// (dep.Request secrets don't survive a restart), which would make
// huaweiSweepCredsFromRaw return ok=false and silently skip the #5028
// post-destroy EVS backstop. This recovers the creds from disk (which tofu
// destroy has not yet removed at call time) so the backstop still reaps this
// dep's stranded CSI `pvc-*` volumes.
//
// Contract: no-op (returns false) for non-huawei providers, when AK+SK are
// already present (body/request creds win — only empty keys are filled), or
// when the workdir tfvars is missing/unparseable (honest skip, never a panic).
// Returns true when at least one previously-empty key was filled, so the caller
// can emit an observable progress line.
func applyWorkdirEVSCredsFallback(providerName string, credsRaw map[string]string, workdir string) bool {
	if credsRaw == nil || !strings.EqualFold(strings.TrimSpace(providerName), "huawei") {
		return false
	}
	// Body/request already supplied the authenticating pair — nothing to fill.
	if strings.TrimSpace(credsRaw["access_key"]) != "" && strings.TrimSpace(credsRaw["secret_key"]) != "" {
		return false
	}
	hw, ok := loadHuaweiCredsFromTfvars(workdir)
	if !ok {
		return false
	}
	filled := false
	fill := func(key, val string) {
		if strings.TrimSpace(credsRaw[key]) == "" && strings.TrimSpace(val) != "" {
			credsRaw[key] = val
			filled = true
		}
	}
	fill("access_key", hw.AccessKey)
	fill("secret_key", hw.SecretKey)
	fill("project_id", hw.ProjectID)
	fill("region", hw.Region)
	return filled
}

// huaweiSweepCredsFromRaw builds the sweep creds from the resolved wipe creds
// map (buildWipeCredsRaw output). This is the reliable in-band source — the
// per-deployment tfvars the janitor's discoverHuaweiCreds walks may already be
// gone once tofu destroy has run. Returns ok=false when the triple is
// incomplete (a non-huawei wipe, or missing creds).
func huaweiSweepCredsFromRaw(raw map[string]string) (huaweiSweepCreds, bool) {
	c := huaweiSweepCreds{
		AK:        strings.TrimSpace(raw["access_key"]),
		SK:        strings.TrimSpace(raw["secret_key"]),
		ProjectID: strings.TrimSpace(raw["project_id"]),
		Region:    strings.TrimSpace(raw["region"]),
	}
	if c.AK == "" || c.SK == "" || c.ProjectID == "" {
		return huaweiSweepCreds{}, false
	}
	if c.Region == "" {
		c.Region = "me-east-215"
	}
	return c, true
}
