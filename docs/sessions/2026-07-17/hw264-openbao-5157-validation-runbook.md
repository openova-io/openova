# hw264 — region-kill G12 revalidation on the #5157-fixed train (task #960)

**Goal:** prove the **#5157 fix** end-to-end on a fresh prov — that after a
region-kill + region-a recovery, openbao **auto-unseals with zero operator
action**, where the hw263 CronJob left it SEALED for 40+ min.

**Env:** hw264.omani.works, dep `4585c8a9f92d4e8e`, 2-region Huawei kom4dc,
train carries **bp-openbao 1.2.63** (unseal-reconciler = **Deployment**, not
CronJob — verified on main: `kind: Deployment`).

## The delta vs hw263 (why this should now pass)
hw263 (1.2.62) shipped the reconciler as a **CronJob**. The region-kill took
down region-a's single k3s control-plane (the CronJob **scheduler**); on
recovery the CronJob controller did not resume scheduling the reconciler for
40+ min (Kyverno fail-closed `FailedDelete` wedged its loop) → openbao stayed
SEALED until manual exec-unseal (#5157).

1.2.63 makes the reconciler a **continuous Deployment**: a long-running pod that
runs the same idempotent unseal check in a `while true; sleep 120` loop. After a
region-kill, once the pod's region-a node recovers, the pod restarts and resumes
its loop the instant openbao is reachable — **no CronJob scheduler, no per-tick
job admission**. So the auto-unseal is expected to complete within ~2–4 min of
openbao-0 becoming reachable post-recovery.

## Sequence (reuse hw263 mechanics — scripts/region-kill-drill.sh)
0. **Pre-req**: cutoverComplete=true (Pillar 5) + all 4 CNPG DR pairs streaming
   (region-b replicas `pg_stat_wal_receiver=streaming`, RPO=0 baseline).
1. **Pre-kill**: seed a `g12_sentinel` row into the cnpg-pair region-a primary
   (`synchronous_commit=on`), verify present on the region-b replica.
   Confirm `kubectl -n openbao get deploy openbao-unseal-reconciler` exists
   (**Deployment**, 1/1) and openbao-0 `Sealed=false`.
2. **Kill**: `HW_TFVARS=/tmp/preflight-tfvars.json scripts/region-kill-drill.sh kill --arm`
   — HARD os-stop the 4 region-a ECS (bastion 212.72.24.20 hard-excluded);
   region-a apiserver dies.
3. **Failover (CNPG RPO=0)**: patch `spec.replica.enabled=false` on the 4
   region-b replica clusters → promoted writable primaries in seconds; sentinel
   survives (RPO=0); post-kill write accepted.
4. **Recover**: `scripts/region-kill-drill.sh recover --arm` — os-start region-a;
   wait for all region-a nodes Ready (~10–15 min Huawei boot).
5. **THE #5157 PROOF**: openbao-0 restarts SEALED → the reconciler **Deployment
   pod** resumes its loop → openbao `Sealed=false` within ~2–4 min of openbao-0
   becoming reachable, **NO manual intervention, NO CronJob-scheduler dependency**.
   Capture:
   - `kubectl -n openbao get deploy openbao-unseal-reconciler` (1/1, NOT a CronJob)
   - the Deployment pod's continuous-loop log: `re-unsealed (sealed=false) —
     region-kill recovery restored the secrets layer`
   - `bao status` → Sealed=false; openbao-0 → 1/1 Ready
   - fleet HRs recover (no 65-HR-degraded tail like hw263's sealed-openbao window)

## UAT
- reset-uat already ran on fire (flushed the 13 hw263 cells → pending hw264).
- Re-stamp on hw264: G11 cutover, **G12 region-kill (now with openbao
  auto-unseal PROVEN, not the #5157 caveat)**, R12/R13 DR, + the 11 backing rows.
- Run `scripts/uat-drift-guard.py` — no predecessor tokens on ✅ rows.
- If the openbao auto-unseal resumes cleanly → **close #5157** with the evidence.

## Guards
- ONE env at a time — hw263 fully wiped; nothing coexists.
- No live-patching during convergence/cutover (zero-touch is the proof).
- bastion `bastion-openova` / EIP 212.72.24.20 — NEVER touch (driver excludes it).
- Do NOT merge any catalyst-api PR while hw264 is in-flight (roll abandons prov).
- NodePorts ABSOLUTELY FORBIDDEN (§854) — gateway served DIRECT.
