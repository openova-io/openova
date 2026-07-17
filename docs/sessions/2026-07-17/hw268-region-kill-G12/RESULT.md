# hw268 Region-Kill G12 — Pillar-3 DoD proof + #5178 dr-promoter validation

**Env:** hw268 (`catalyst-hw268-omantel-biz-7e7a0a1f`, region `me-east-215`, 2-VPC kom4dc mimic: region-a / region-b)
**Date:** 2026-07-17 (box UTC clock)
**Method:** REAL region-a outage simulated by **HARD-stopping the me-east-215-a ECS instances via the Huawei ECS API** (no in-cluster cordon — region-a kubeconfig was uncapturable/409 + node SSH firewalled). Observation point: region-b kubeconfig (`212.72.24.14:6443`), which survives region-a death.
**dr-promoter under test:** `cnpg/cnpg-pair-bp-cnpg-pair-dr-promoter` — signals `cloudnative-pg/postgresql:16.4-1`, actor `alpine/k8s:1.30.0`. Config: `PRIMARY_LIVENESS_ENABLED=true`, `PRIMARY_MESH_HOST=cnpg-pair-bp-cnpg-pair-primary-mesh`, `HOLD_SECONDS=120`, `PROBE_TIMEOUT=5`, `INTERVAL_SECONDS=10`.

## Verdict: PASS — #5178 fully validated, RPO=0, region-b auto-promoted, no split-brain

| Check | Result | Evidence |
|---|---|---|
| Region-a ECS stopped (6 nodes) | YES — all → SHUTOFF | 5 workers + 1 CP, all `me-east-215-a`; bastion + all region-b excluded |
| dr-promoter auto-promoted region-b | **YES** | actor `PROMOTING … 128s >= 120s … Ready` @ 17:11:58Z |
| #5178 liveness-gate fired on REAL region-a death | **YES** | signals `WAL receiver ABSENT and region-A UNREACHABLE (pg_isready rc=2 @ …primary-mesh) — primary loss; hold clock started` @ 17:09:55Z |
| RTO (kill → writable) | **~147s** (~2m27s; 120s is the deliberate HOLD safety window) | T_kill 17:09:37Z → writable 17:12:04Z |
| RPO | **0** (zero committed tx lost) | pre-kill replica fully caught up: `recv=replay=0/B000060`, primary `latest_end_lsn=0/B000060`, zero lag |
| region-b writable after promote | **YES** | `CREATE TABLE 1 / INSERT 0 1`; `g12_sentinel` rowcount=1 @ 17:12:04Z; TL1→TL2 |
| LATCH (anti-re-demote, #5178 Defect-B) | **YES** | actor `LATCHED: HR … spec.suspend=true` @ 17:12:08Z; HR `suspend=true promoted=true` |
| openbao Sealed=false held | **YES** | region-b `openbao-0` Sealed=false throughout (HA active since 15:13:52Z, never re-sealed; #5157 region-local unseal-reconciler) |
| keycloak realm preserved | **YES** | realms `master`,`sovereign` before + during + after; `keycloak-0` 1/1 Running, 0 restarts |
| No split-brain | **YES** | region-b sole primary (HR latched); region-a old cnpg-pair diverged off source TL → re-clone as secondary (anti-flap `/shared/diverged`) |
| region-a restored | **YES** | batch-start @ 17:12:48Z → all 6 back to ACTIVE by 17:13:09Z |

## Timeline (from T_kill = 2026-07-17T17:09:37Z, job 8a868c41…)

| +s | UTC | Event |
|---|---|---|
| 0 | 17:09:37 | 6× region-a ECS HARD-stop accepted |
| +18 | 17:09:55 | **signals: liveness gate arms** — `pg_isready rc=2 @ primary-mesh` (region-A positively unreachable) → hold clock started |
| +18…+130 | 17:09:55–17:11:47 | actor: 12× `region-A unreachable Ns < hold 120s — waiting` |
| +141 | 17:11:58 | actor: **PROMOTING** (down-clock 128s ≥ 120s, failover-readiness Ready) → patch HR `promoted=true` |
| +147 | 17:12:04 | replica-1 leaves recovery (`pg_is_in_recovery=false`), **TL1→TL2**; writable INSERT succeeds |
| +148 | 17:12:05 | signals: `state=promoted (local cluster writable)` — hold clock cleared |
| +151 | 17:12:08 | actor: **LATCHED** `HR spec.suspend=true` (flux can no longer re-demote survivor) |
| +191 | 17:12:48 | region-a batch-start (restore) |
| +212 | 17:13:09 | all 6 region-a ECS → ACTIVE; region-b remains sole primary (cluster healthy) |

## Why this validates #5178 (0.2.14)

The 0.2.13 bug (hw266) promoted on the LOCAL `noreceiver` signal alone — a mere replication fault would trigger a spurious promote / split-brain. #5178 requires a **positive region-A-unreachable proof** before arming the clock. Here the signals container observed `noreceiver` AND probed `primary-mesh` via `pg_isready`, got **rc=2 (unreachable)**, and only then armed the hold clock — exactly the gated path. Because region-a was genuinely dead (all ECS SHUTOFF), rc=2 was truthful and promotion was correct. The LATCH (`spec.suspend=true`, #5178 Defect-B) then made the promotion durable against flux drift-correction. Data safety was preserved: replica was fully caught up at kill → RPO=0.

## Region-a ECS targets stopped (guarded: exactly 6, all me-east-215-a, none bastion, none region-b)

```
fd504199-8c84-406b-aa3a-23a2f1054405  catalyst-hw268-omantel-biz-7e7a0a1f-me-east-215-a-w2c3138
dd300d9a-7ee2-45c0-abcf-53c31c2ec39b  catalyst-hw268-omantel-biz-7e7a0a1f-me-east-215-a-w673c81
9a0e043b-0f3f-4668-988b-4ff67f322677  catalyst-hw268-omantel-biz-7e7a0a1f-me-east-215-a-w208815
ae5e64da-9c8a-4828-b159-e309dfcad54b  catalyst-hw268-omantel-biz-7e7a0a1f-me-east-215-a-w6af407
1dfc6797-40b7-492f-adf9-878fa9849e14  catalyst-hw268-omantel-biz-7e7a0a1f-me-east-215-a-wf79ba2
fad04ed1-6669-4207-bd45-d9065a7e793e  catalyst-hw268-omantel-biz-7e7a0a1f-me-east-215-a-cp1-c05208
```
Excluded (never touched): all 6 `…-me-east-215-b-…` nodes + `bastion-openova` (EIP 212.72.24.20).

## Full dr-promoter log (region-b, since kill)

```
[signals] 2026-07-17T17:09:55Z WAL receiver ABSENT and region-A UNREACHABLE (pg_isready rc=2 @ cnpg-pair-bp-cnpg-pair-primary-mesh) — primary loss; hold clock started
[signals] 2026-07-17T17:12:05Z state=promoted (local cluster writable) — hold clock cleared
[actor]   2026-07-17T17:09:55Z region-A unreachable 5s < hold 120s — waiting
[actor]   … (10s ticks: 16,26,36,46,56,67,77,87,97,107,117) …
[actor]   2026-07-17T17:11:58Z PROMOTING: region-A WAL stream absent AND region-A unreachable 128s >= 120s AND failover-readiness Ready — patching HR desired state (#5125-D1 seam)
[actor]   helmrelease.helm.toolkit.fluxcd.io/bp-cnpg-pair patched
[actor]   2026-07-17T17:11:58Z PROMOTED (step 1/2): HR flux-system/bp-cnpg-pair spec.values.cnpgPair.replica.promoted=true — helm-controller renders replica.enabled:false → CNPG promotes; the suspend LATCH fires next tick once it renders (#5178)
[actor]   2026-07-17T17:12:08Z LATCHED: HR flux-system/bp-cnpg-pair spec.suspend=true — promotion durable; flux drift-correction can no longer revert spec.values and re-demote the survivor (#5178 Defect-B)
```

## HR / cluster final state (post-promote, region-b)

```
HR bp-cnpg-pair: suspend=true  promoted=true
  promoted-at=2026-07-17T17:11:58Z  latched-at=2026-07-17T17:12:08Z
  reason="region-A WAL stream absent and region-A unreachable 128s; failover-readiness Ready (#5137/#5178)"
Cluster cnpg-pair-bp-cnpg-pair-replica: replica.enabled=false  currentPrimary=replica-1  phase="Cluster in healthy state"
replica-1: pg_is_in_recovery=false  tli=2  (writable primary)
g12_sentinel: id=1 note=hw268-region-kill-postpromote at=2026-07-17 17:12:04.519677+00
```

## Notes / honest caveats

- **Scope:** the #5178 dr-promoter under test guards the dedicated **`cnpg-pair`** DR-pair (bp-cnpg-pair), the Pillar-3 region-kill test instance (empty `app` db). The Org data-plane `shared-pg` clusters in `shared-data` (which keycloak/openbao-data use) have **no** dr-promoter — their region-b replicas stayed read-only during the outage; keycloak realm data remained **readable/intact** on `shared-pg-replica-1` (region-b, already on TL2 from prior history) and keycloak-0 never restarted. Automatic shared-pg promotion is a separate mechanism, out of scope for this #5178 walk.
- **RPO=0 proof is structural** (not a written sentinel on the old primary): no app/superuser password secret exists for the cnpg-pair, so a TCP write to the region-a primary via primary-mesh wasn't possible. Instead: at kill the region-b replica had `received_lsn == replay_lsn == primary.latest_end_lsn = 0/B000060` (zero replication lag, nothing in flight/unreplayed) → zero acknowledged transactions could be lost. The promoted primary continued from that exact position on TL2.
- **RTO** includes the deliberate `HOLD_SECONDS=120` data-safety window; detection+promote overhead beyond the hold was ~27s.
- All region-a ECS restored to ACTIVE; env left recoverable, region-b sole primary (no split-brain). No NodePorts used anywhere.
