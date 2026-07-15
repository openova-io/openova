# hw256 REGION-KILL walk (G12 / #4275) — full ECS-poweroff variant — 2026-07-15 18:28–18:56 UTC

> Pillar-3 destructive proof on `hw256.omani.works` (dep `ea60815abb0cc186`, 2-region
> me-east-215-a/b, pre-cutover: cutover fired but wedged at step-03 harbor-prewarm →
> `cutoverComplete=false`). Deliberately the LAST walk on this env (destructive to region-a).
> Kill mechanism: **batch HARD os-stop of ALL 4 region-a ECS via the Huawei API** (the full
> poweroff variant reserved for disposable envs per `docs/sessions/2026-06-27/region-kill-drill-4275.md`).
> Every timestamp below is from the live transcript (raw logs in this directory). Nothing mocked.

## Topology under test

| | region-a (primary) | region-b (standby) |
|---|---|---|
| nodes | 1 cp + 3 workers (`catalyst-hw256-…-a-*`) | 1 cp + 3 workers |
| cnpg-pair (ns `cnpg`) | `…-primary` 3/3, primary `-1` | `…-replica` 3/3, **sync_state=sync, remote_apply → RPO=0 by design** |
| shared-pg trio (ns `shared-data`) | `shared-pg`/`-b`/`-c` 3/3 each | `shared-pg[-b|-c]-replica` 3/3 each, **cross-region sync_state=async** (local quorum sync only — RPO=0 NOT structurally guaranteed for shared-pg) |
| Continuum | 8 CRs all Healthy, lease=region-a, lag=0 (incl. `dr-shared-pg` — the #4986 gap is FIXED on hw256) | CRD absent on region-b (controller runs region-a only) |
| ClusterMesh | 1/1 ready both directions | |

## The walk (all UTC, 2026-07-15)

| T | Step | Result |
|---|---|---|
| 18:28:27 | SEED: `g12_marker` (shared-pg/postgres) + `d31_counter` (cnpg-pair/app) — 1000 rows + timestamped sentinel each | INSERT 1000 + sentinel committed |
| 18:28:32 | region-b pre-kill verify | **both replicas 1001\|100000 + sentinel — replicated <5.3s (RPO=0 pre-proof)** |
| 18:30:33.498 | **T0 — KILL**: batch HARD os-stop of all 4 region-a ECS (job `8a868c3f9f44cbda019f670b7b6d086a`) | accepted |
| 18:30:37.929 | region-a apiserver DEAD | T0+4.43s |
| 18:30:38.308 | PROMOTE: patch `replica.enabled=false` on all 4 region-b replica clusters | T0+4.81s |
| 18:30:39.184 | cnpg-pair-replica `pg_is_in_recovery` t→f | **RTO 5.685s from T0** (1.25s from patch) |
| 18:30:39.640 | shared-pg-replica promoted | **RTO 6.141s** |
| 18:30:40.242 / 40.653 | shared-pg-b / -c promoted | RTO 6.74s / 7.15s |
| 18:30:40.7 | RPO proof on both promoted primaries | **1001\|100000 + sentinel intact → RPO=0** |
| 18:30:41.6 | post-kill INSERTs accepted on both new primaries | **write-availability at T0+8.2s** |
| 18:30:41.8 | keycloak DB on promoted shared-pg | realms `sovereign,master` intact (#3572 class OK) |
| 18:30:33–40 | region-b read poller through the kill | marker served on every tick — **zero read unavailability** |
| 18:31:13 | Huawei API confirms all 4 region-a ECS `SHUTOFF` (bastion + region-b untouched) | kill state proven |
| **18:32:40.810** | 🛑 **helm-controller (region-b) `correct cluster drift` REVERTS the cnpg-pair promote patch** — re-demotes the survivor to replica **while region-a is still down** | see Defect-1 |
| 18:32:45.507 | **RESTORE**: batch os-start region-a (job `8a868c3f9f44cbda019f670d7f1408ad`) | accepted |
| 18:33:34.861 | region-a apiserver BACK | +49s |
| ~18:34–18:38 | region-a pods resume; transient EVS-CSI `FailedMount`/probe-500s self-heal | all PG pods Running by ~18:38 |
| 18:44:46 | **split-brain window captured**: region-a shared-pg `in_recovery=f` AND region-b shared-pg-replica `in_recovery=f` simultaneously; divergence region-a=1001 vs region-b=1002 rows (cnpg-pair already force-demoted by the flux revert) | CR-level dual-primary evidence |
| 18:44:49 | **split-brain guard**: demote region-b (patch `replica.enabled=true` ×4; cnpg-pair "no change" — already reverted by flux) | post-kill writes on region-b intentionally discarded |
| 18:45–18:49 | rejoin WEDGES: all 4 region-b replica clusters in walreceiver crash-loop — `FATAL: highest timeline 2 of the primary is behind recovery timeline 3` (shared-pg/-b), timeline-2-vs-2 divergent-history loop (cnpg-pair, ×34/3min) | Defect-2 (hw128 finding-#3 class, now with signature) |
| 18:49:48–18:52:04 | HEAL canary: delete `shared-pg-replica` Cluster CR + re-apply (PVCs GC'd) → CNPG re-bootstraps via `pg_basebackup` from region-a mesh | healthy 3/3 in **2m16s**; count back to 1001 (divergent row discarded) |
| 18:52:26–18:55:08 | same heal on `-b`, `-c`, `cnpg-pair` in parallel | all 3/3 healthy |
| 18:55:33 | **FINAL RE-GATE** | ALL GREEN (below) |

**Totals: kill→promote 5.7s · kill→write-availability 8.2s · restore leg 22m23s · full walk 25m00s (budget 30m).**

## Final re-gate state (18:55:33Z, `14-final-regate.log`)

- cnpg-pair: cross-region replica **streaming sync** again, `synchronous_standby_names=FIRST 1 ("cnpg-pair-bp-cnpg-pair-replica")` → RPO=0 restored.
- shared-pg: replica streaming async (designed baseline); post-restore write (row 200000) replicated to region-b <4s.
- Data converged 1001|100000 both regions on both pairs.
- CNPG clusters: 9/9 region-a + 8/8 region-b "Cluster in healthy state" (exception: `openova-flow-pg` region-a status-probe error, instances 2/2 Ready — residual quirk, Defect-4).
- Continuum 8/8 Healthy, lease re-pinned region-a; nodes 8/8 Ready; ClusterMesh 1/1 both ways; console front-door **200**.

## Verdicts

| Bar | Verdict |
|---|---|
| RTO ≤ 30s | ✅ **5.7s** (cnpg-pair, T0→writable-primary; operator-driven promote per ADR contract `autoFailover=false`) |
| RPO = 0 | ✅ **0 committed rows lost** (1001/1001 + sentinel on both pairs; cnpg-pair structurally sync/remote_apply; shared-pg async-by-design but verified-replicated pre-kill) |
| Region-b promotes + serves | ✅ writes at T0+8.2s, reads never interrupted, keycloak realms intact |
| Split-brain guard | ✅ dual-primary window captured + deterministically resolved to declared topology (fail-back), divergent post-kill writes discarded by design |
| Restore + re-gate ≤ 30 min | ✅ 22m23s, but **NOT hands-off** — required manual re-bootstrap of all 4 replica clusters (Defect-2) |
| Front-door continuity | ❌ structural (pre-cutover): DNS kept resolving to dead region-a EIPs; region-b gateway is ClusterIP-only (no EIP); console 000 during kill, 200 only after restore. Continuum's flip-dns sequencer step exists but the controller lives in region-a and `autoFailover=false` |

## Defect signatures (REPORTED, not filed — env is a wipe candidate)

1. 🛑 **Flux drift-correction re-demotes the promoted survivor mid-outage** — helm-controller on region-b logged `detected changes in cluster state: changed: 1` → `running 'correct cluster drift'` at **18:32:40.810Z** (2m02s after promotion, region-a still SHUTOFF), reverting `spec.replica.enabled=false` on `cnpg-pair-bp-cnpg-pair-replica`. The survivor's write-availability lasted ~2 minutes, then it went back to read-only recovery against a dead source. The documented one-command failover patch (#4275 runbook / bp-cnpg-pair chart) is **not durable on drift-enabled HRs** — a real DR would need HR suspend-then-patch (or the Continuum sequencer patching through helm values). Contrast: the `bp-postgres-shared*` HRs did NOT revert (shared-pg stayed promoted 14 min until manual demote).
2. 🛑 **Day-2 rejoin wedge (timeline divergence) on ALL cross-region replica clusters** — after region-a resumed, every region-b replica cluster crash-looped its walreceiver (`highest timeline 2 of the primary is behind recovery timeline 3` for shared-pg/-b; divergent-history loop for -c/cnpg-pair). Neither CNPG nor Continuum auto-heals; the Continuum 7-step switchover sequencer has **no rejoin/re-clone step**. Manual heal (delete replica Cluster CR + re-apply → `pg_basebackup` re-clone) converges in ~2-3 min/cluster. Same class as hw128 finding-#3, now with concrete signatures + a proven heal.
3. **In-region primary drift on crash-resume** — region-a cnpg-pair primary moved `-1`→`-2` and shared-pg-c `-2`→`-1` during poweroff-resume (timeline bumps to 2), which is what made the region-b timelines (3) unreachable. Also produced transient EVS-CSI `FailedMount` (`dial udp 10.96.0.10:53: operation not permitted` from the CSI node plugin during cilium warm-up) + probe-500s for ~2-4 min — self-healed, no action needed.
4. **`openova-flow-pg` (region-a) status-probe error persists post-restore** — `Instance Status Extraction Error: HTTP communication issue` while instances 2/2 Ready.
5. **Region-b powerdns-anycast EIP (212.72.24.12:53) does not answer DNS from an external vantage** during the kill (shares the EIP with clustermesh-apiserver via lbipam sharing-key) — front-door names kept resolving to dead region-a EIPs via the mothership NS chain.
6. Pre-existing on hw256 (NOT region-kill-caused, for the wipe decision): cutover wedged at step-03 harbor-prewarm (`cutoverComplete=false`, job Failed ×4); `openova-mcp` CrashLoopBackOff + CreateContainerConfigError in catalyst-system (V7 relevance); `bp-cluster-autoscaler-hcloud`/`bp-hcloud-ccm`/`bp-velero` HRs not-ready (hcloud charts on a Huawei prov).

## Raw evidence files (this directory)

`01-seed-baseline.log` · `02-regionb-spine-baseline.log` · `03-kill-promote.log` ·
`03b-regionb-read-availability.log` · `04-during-kill-state.log` · `05-dns-promoted-state.log` ·
`06-restore-start.log` · `07-splitbrain-failback.log` · `08-restore-diag.log` ·
`09-regate-watch.log` (as `tasks/b2aqajnox`) · `10-regiona-plane-recovery.log` ·
`11-divergence-assessment.log` · `12-rebootstrap-canary.log` · `13-rebootstrap-all.log` ·
`14-final-regate.log`

Refs #4275 #960 #3375 #4986 — precedents: `docs/sessions/2026-06-11/hw128-region-kill-walk-PASS.md`, `docs/sessions/2026-06-27/region-kill-drill-4275.md`, hw235 Wave-F.
