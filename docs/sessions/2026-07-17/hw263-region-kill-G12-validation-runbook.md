# hw263 — region-kill G12 DR-recovery validation on the FIXED train (task #960)

**Goal:** prove — on a clean prov of the *now-fixed* train — that a region-kill
auto-recovers the full DR surface with **zero operator action**:
1. **openbao auto-unseal** via the `openbao-unseal-reconciler` CronJob
   (`bp-openbao 1.2.62`). hw262 ran the **buggy 1.2.61** whose reconciler
   mis-captured `bao status`' rc (`if ! bao status; then RC=$?` → the `!`
   negation clobbers `$?` to 0 → guard `[ $RC != 2 ]` true → **permanent
   no-op**), so openbao stayed SEALED 6+ min post-kill. 1.2.62 fixes it
   (`RC=0; bao status || RC=$?`) — this prov is where the fix is proven
   end-to-end by the *real CronJob* (hw262 only proved it via exec-simulation).
2. **CNPG failover** region-a→region-b (RTO ~sec, RPO=0), as on hw256.
3. **sso-bridge healthy** — hw262 hit `#5150` ImagePullBackOff (alpine/k8s
   tag `1.30.0` unresolvable via `proxy-dockerhub`); `bp-sso-bridge 0.2.25`
   pins `1.31.4`. Expect sso-bridge Running (no ImagePullBackOff) on hw263.

## Preconditions
- hw262 (dep `9e8f4383e79bc943`) **fully wiped** — record GONE + live Huawei
  ground-truth ZERO remnants (verify before fire; one-env gate). ⏳ (in flight)
- Train 4-surface lockstep PASS + GHCR-published:
  - `bp-openbao 1.2.62` ✅ (ghcr + bootstrap-kit slot 08 + catalog-seed)
  - `bp-sso-bridge 0.2.25` ✅ (ghcr + bootstrap-kit slot 13b + catalog-seed)
- Reconciler chart verified on main: corrected `RC=0; bao status || RC=$?`
  (unseal-reconciler.yaml L110-111), buggy pattern gone from executable code,
  CronJob schedule `*/2 * * * *`, `reconcile.enabled: true`. ✅
- Do NOT merge any catalyst-api PR while hw263 is in-flight (roll abandons prov).

## Fire (match hw262's PROVEN sizing — it fully converged AND cut over)
```bash
export CATALYST_MOTHERSHIP_KEY=/tmp/hw-priv.pem
CP_SIZE=m7n.xlarge.8 WORKER_SIZE=m7n.2xlarge.8 WORKER_COUNT=3 \
  QATEST=false FIRECUT=true \
  scripts/sovereign-lifecycle.sh fire hw263 omani.works
# fire() = prov-preflight (fail-closed, one-env gate) → reset-uat → POST.
# If preflight flags m7n.2xlarge.8 pool exhausted, fall back to the script
# default WORKER_SIZE=m7n.xlarge.8 WORKER_COUNT=5 (verified-ACTIVE flavor).
```

## Converge (zero-touch — NO hand-patching; that IS the proof)
- dep status → `ready`; ~60 component HRs installed.
- Handover auto-fires → (FIRECUT=true) 11-step auto-cutover chain runs → watch
  `self-sovereign-cutover-status` cm to `cutoverComplete=true`.
- openbao reaches unsealed/Ready on first convergence (init-Job path);
  sso-bridge Running (validates #5151 on a fresh prov).

## Region-kill G12 + the openbao-reconciler proof
Mechanics proven hw256 (`docs/sessions/2026-07-15/hw256-region-kill/`): batch
HARD os-stop of all region-a ECS via HCS API → region-b promote. Known
CNPG-orthogonal defects to expect (heal, don't abort):
- **Defect-1:** region-b helm-controller "correct cluster drift" reverts the
  cnpg promote patch mid-kill; re-patch if it re-demotes the survivor.
- **Defect-2:** rejoin walreceiver crash-loop (timeline N behind N+1). Heal =
  delete region-b replica Cluster CR + re-apply → pg_basebackup (~2m/cluster).

Steps:
1. Pre-kill: `bao status` → Sealed=false; sso-bridge Running; seed g12_marker
   (shared-pg) + d31_counter (cnpg-pair); verify region-b RPO=0.
2. Region-kill: batch os-stop region-a ECS (NOT delete) via HCS API; promote
   region-b replicas.
3. **THE openbao PROOF (what hw262's 1.2.61 could NOT do):** openbao pods
   restart SEALED (shamir) → within ~2 min the `openbao-unseal-reconciler`
   CronJob fires, reads persisted `openbao-unseal-keys`, runs `bao operator
   unseal` → **Sealed=false with ZERO hand-intervention.** Capture:
   - `kubectl get cronjob openbao-unseal-reconciler` (schedule */2)
   - the reconciler Job logs showing the unseal (rc=2 → PROCEED → unsealed)
   - `bao status` Sealed=false after the reconciler run
   - SSO/funnel/per-Org rows that openbao-gates now walk green
4. Recover region-a → confirm re-join, no split-brain.

## UAT
- reset-uat ran on fire (flushes hw262 evidence).
- Stamp: region-kill G12, openbao-DR auto-unseal, CNPG RPO=0 failover, the
  sso-bridge #5151 row, + the openbao-confounded SSO/funnel/per-Org rows.
- Run `scripts/uat-drift-guard.py` — no hw262/hw255 tokens on ✅ rows.

## Guards
- ONE env at a time — hw262 fully wiped before fire; nothing coexists.
- No live-patching during convergence (zero-touch is the proof).
- bastion `bastion-openova` / EIP 212.72.24.20 — NEVER touch.
- NodePorts ABSOLUTELY FORBIDDEN (§854) — gateway served DIRECT.
