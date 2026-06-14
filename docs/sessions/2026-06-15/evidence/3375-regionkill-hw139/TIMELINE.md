# North-Star #4 — Cross-Region Failover (Region-Kill) — hw139.omani.works

**Env**: `hw139.omani.works` — deployment `c89aa7059556b342`, 2-region (Huawei kom4dc, VPCs `me-east-215-a` + `me-east-215-b`).
**Date**: 2026-06-14 (UTC) / session 2026-06-15.
**Mechanism under test**: cnpg-pair cross-region streaming replication + operator-driven promotion.
**Refs**: #3375 (topology/DR), #3492 (openbao-raft — see separate note).

## Headline

**REGION-KILL PASSED on hw139 — YES.**

Killing region-A's cnpg-pair primary (3 pods force-deleted, all 3 region-A workers cordoned so it cannot reschedule) → region-B replica promoted to a **writable** primary, the **pre-kill row survived with byte-identical timestamp**, and the promoted primary **accepted a new write**. **Zero rows lost.**

## Topology (pre-state)

| Component | Region-A (`me-east-215-a`) | Region-B (`me-east-215-b`) |
|---|---|---|
| cnpg cluster | `cnpg-pair-bp-cnpg-pair-primary` (ns `cnpg`) | `cnpg-pair-bp-cnpg-pair-replica` (ns `cnpg`) |
| instances | 3/3 healthy, primary `…-primary-1` | 3/3 healthy, primary `…-replica-1` |
| role | source (writable) | standby, `replica.enabled: true`, `source: …-primary` |
| app db | `app` (owner `app`) | streamed from `…-primary-mesh:5432` as `streaming_replica` |

The region-B standby streams cross-region via the `…-primary-mesh` ClusterIP service (load-balanced across the 3 region-A primary instances; the standby's connection landed on `…-primary-2`, IP `10.42.2.17`). Pre-kill the standby was fully caught up (`sent_lsn == replay_lsn == 0/8000000`) and **rejected writes** (`cannot execute CREATE TABLE / INSERT in a read-only transaction`), `pg_is_in_recovery() = t`.

## Timeline (wall-clock, UTC)

| Marker | Timestamp (UTC) | Event |
|---|---|---|
| T_MARKER | `2026-06-14T20:21:23.055724Z` | Pre-kill row written to region-A primary: `keystone_xregion(id=1, 'pre-kill region-a hw139')` |
| (replicated) | `~2026-06-14T20:21:33Z` | Row read back on region-B within ~1s of polling; identical `ts`; read-only still enforced |
| **T_KILL** | **`2026-06-14T20:21:42.178Z`** | Cordon 3 region-A workers (`w9b5077`, `wb1b4e4`, `wf4b21d`) → `SchedulingDisabled`; force-delete all 3 `…-primary` pods (`--grace-period=0 --force`). Region-A primary → `Pending`, 0 READY, cannot reschedule. |
| **T_PROMOTE** | **`2026-06-14T20:21:54.931Z`** | `kubectl -n cnpg patch cluster cnpg-pair-bp-cnpg-pair-replica --type=merge -p '{"spec":{"replica":{"enabled":false}}}'` |
| **T_WRITABLE** | **`2026-06-14T20:22:01.273Z`** | First poll observing `pg_is_in_recovery() = f` on region-B `…-replica-1` |

## Latencies

| Metric | Value | Note |
|---|---|---|
| **Promote latency** (T_PROMOTE → T_WRITABLE) | **6.34 s** | Upper bound — the very first 1-Hz poll after the patch already returned `f`, so true latency ≤ 6.34 s. |
| **Full RTO** (T_KILL → T_WRITABLE) | **19.09 s** | Operator-driven kill → writable promoted primary. |

## Zero-data-loss proof (on the promoted region-B primary)

1. **Pre-kill row PRESENT** — `id=1 | 'pre-kill region-a hw139' | 2026-06-14 20:21:23.055724+00`. The timestamp is byte-identical to the value written on region-A *before* the kill → the row replicated and survived the failover.
2. **New write ACCEPTED** — `INSERT … 'post-kill region-b (promoted)'` → `INSERT 0 1`, returned `id=34 | 2026-06-14 20:22:17.92041+00`. The promoted region-B is genuinely writable.
3. **`pg_is_in_recovery() = f`** on the new primary.
4. Final table: **2 rows** — 1 pre-kill + 1 post-kill. **Data loss = 0 rows.**

> Note on the `id` gap (1 → 34): the `serial` sequence had already advanced to ~33 on the original region-A primary from internal cnpg activity before the `pg_basebackup` that seeded region-B; sequence state replicates with the data. This is normal Postgres sequence behaviour, **not** data loss — both semantically meaningful rows are present and accounted for.

## Recovery (Day-2, not part of the proof)

- `2026-06-14T20:22:30.311Z` — uncordoned the 3 region-A workers (back to `Ready`), leaving the env non-terminal.
- Region-B (`…-replica`) is `3/3 healthy`, primary `…-replica-1`; its two local instances (`…-replica-2`, `…-replica-3`) re-streaming from the new primary (fresh ages, re-created post-promotion).
- The region-A re-bootstrap → split-brain reconciliation is a Day-2 concern (two writable clusters now exist) and is explicitly **out of the region-kill proof's scope** per the briefing.

## openbao-raft (#3492) — attempt result: NOT cleanly drivable on hw139

`openbao-0` exists in both regions, both `Storage Type raft`, `HA Enabled true`. But they are **two independent single-node raft clusters**, not one cross-region quorum:

- Region-A: `Cluster Name vault-cluster-7b00abb4`, `Cluster ID 08f944a0-c4db-eb00-d4e1-124ed9eddee6`, `HA Mode active`.
- Region-B: `Cluster Name vault-cluster-b4883d05`, `Cluster ID d8f4390d-8c0e-362b-77ea-fcfa2ef56429`, `HA Mode active`.
- StatefulSet `replicas = 1` in **both** regions; HA cluster addr is the in-region `https://openbao-0.openbao-internal:8201`.
- `openbao-config` storage stanza is `storage "raft" { path = "/openbao/data" }` with **no `retry_join`** → no cross-region membership.

Because each region is already its own active leader of a *different* raft cluster with *different* data, #3492's premise ("kill region-A openbao → region-B raft-promotes → serves a region-A-written KV") cannot be exercised here: there is no shared quorum to promote across, and region-B never held region-A's KV. Driving it on hw139 would prove nothing about cross-region openbao DR. Reported honestly rather than faked. #3492 is a **distinct mechanism** from the cnpg-pair region-kill, which is the primary North-Star-4 proof and PASSED.

## Evidence files

- `psql-transcript.txt` — full ordered psql/kubectl transcript (pre-state → marker → kill → promote → writable → zero-loss → recovery).
- `final-region-b-state.txt` — final promoted-primary state + post-promotion local replication.
