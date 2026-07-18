# hw262 — openbao region-kill DR-recovery validation (task #960, #5146)

**Goal:** prove `bp-openbao 1.2.61`'s continuous **unseal-reconciler CronJob**
(#5142/#5146) auto-unseals OpenBao after a region-kill restart — the specific
DR-recovery gap that left hw261 openbao-SEALED post-kill (hw261 predated the fix).

## Preconditions (all verified 2026-07-17)
- Mothership catalyst-api rolled to `f42fe5b` (= #5146 merge, includes #5141). ✅
- Train carries `bp-openbao 1.2.61` — 4-surface lockstep PASS + GHCR-published. ✅
- hw261 (dep `fe9eb3995b594b79`) wiped: record 404; **live Huawei ground-truth
  ZERO remnants** (no foreign ECS, only bastion EIP, VPC 1/5, EVS 3/400). ✅
- Preflight one-env gate: only ❌ = transient in-flight CI (waiting to drain). ⏳

## Fire command (match hw261's PROVEN sizing — it fully converged on these)
hw261 tfvars: CP=`m7n.xlarge.8`, worker=`m7n.2xlarge.8` ×3/region, sharedPG=true,
fireCutover=true, active-hotstandby. The `m7n.2xlarge.8` pool capacity was just
freed by the hw261 wipe, so it is available.

```bash
export CATALYST_MOTHERSHIP_KEY=/tmp/hw-priv.pem
CP_SIZE=m7n.xlarge.8 WORKER_SIZE=m7n.2xlarge.8 WORKER_COUNT=3 \
  QATEST=false FIRECUT=true \
  scripts/sovereign-lifecycle.sh fire hw262 omani.works
# fire() runs prov-preflight (fail-closed) → reset-uat → POST /deployments.
```

## Converge (zero-touch — NO hand-patching; that is the whole proof)
- Watch dep status → `ready`; component HRs → installed (~60 components).
- Handover auto-fires → (FIRECUT=true) auto-cutover 11-step chain may run.
- Openbao must reach unsealed/Ready on first convergence (init-Job path).

## Region-kill G12 + the openbao-reconciler proof
Mechanics proven on hw256 (`docs/sessions/2026-07-15/hw256-region-kill/`): batch
HARD os-stop of all region-a ECS via the Huawei API → patch `replica.enabled=false`
on region-b replica clusters → RTO ~5.7s, RPO=0. **Known defects to expect (both
CNPG-orthogonal to the openbao proof):**
- **Defect-1:** region-b helm-controller "correct cluster drift" REVERTS the cnpg
  promote patch mid-kill (re-demotes survivor). Watch for it; re-patch if needed.
- **Defect-2:** rejoin walreceiver crash-loop (`highest timeline N of the primary
  is behind recovery timeline N+1`). Heal = delete the region-b replica Cluster CR
  + re-apply → CNPG re-bootstraps via pg_basebackup (~2m per cluster).

1. Confirm openbao Ready + unsealed pre-kill (`bao status` → Sealed=false).
2. Seed g12_marker (shared-pg) + d31_counter (cnpg-pair) + verify region-b RPO=0.
3. Region-kill: batch os-stop region-a ECS (NOT delete) via HCS API; promote region-b.
4. **THE PROOF (openbao — the NEW validation hw256 did not cover):** openbao pods
   restart post-kill → they come up SEALED (shamir).
   Within ~2 min the `openbao-unseal-reconciler` CronJob fires, reads the
   persisted `openbao-unseal-keys` Secret, runs `bao operator unseal` → openbao
   returns to **Sealed=false WITHOUT hand-intervention**. Capture:
   - `kubectl get cronjob openbao-unseal-reconciler` (schedule */2)
   - reconciler Job logs showing the unseal
   - `bao status` Sealed=false after the reconciler run
   - SSO/funnel/per-Org rows that were openbao-confounded on hw261 now walk green
5. Recover region-a → confirm re-join, no split-brain.

## UAT
- reset-uat already ran on fire (flushes hw261 evidence).
- Stamp the openbao-DR rows + region-kill G12 + the confounded SSO/funnel/per-Org
  rows that this validates. Run `scripts/uat-drift-guard.py` (no hw261 tokens).

## Guards
- ONE env at a time — hw261 is fully wiped; do NOT fire alongside anything.
- No live-patching during convergence (zero-touch is the proof).
- bastion `bastion-openova` / EIP 212.72.24.20 — NEVER touch.
- Do NOT merge any catalyst-api PR while hw262 is in-flight (roll abandons prov).
