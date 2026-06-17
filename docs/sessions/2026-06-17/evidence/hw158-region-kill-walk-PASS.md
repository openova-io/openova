# hw158 — live region-kill walk: PASS (RTO ≈ 1.4 s, RPO = 0)

> **Env:** `hw158.omani.works` · deployment `ab2135d4cf2d01e4` · 2026-06-17 · single physical kom4dc
> region, 2 VPCs `me-east-215-a` (region-a, primary) / `-b` (region-b, standby).
> **Ticket:** #3375 (TOPOLOGY/DR) · North Star #4 (agreed apps actually multi-region) · Pillar 3 / D31.
> **Runbook:** [`../../../ledger/uat-walkthrough/topology-dr-one-vocabulary-built-and-region-kill-proven.md`](../../../ledger/uat-walkthrough/topology-dr-one-vocabulary-built-and-region-kill-proven.md) Section H.
> **Result row:** [`../../../ledger/UAT.md`](../../../ledger/UAT.md) Row 11 + North-Star #4 (this file is the
> evidence artifact those rows link; it transcribes the live-walked record captured 2026-06-17).

## Verdict: ✅ PASS — live kill → promote, **RTO ≈ 1.4 s, RPO = 0, zero rows lost.**

## Substrate confirmed before the kill
- `/cloud` showed **Region 2/2 · Cluster 2/2**, both `hw-me-east-215-a-rtz-prod` and
  `hw-me-east-215-b-rtz-prod` clusters in the graph, each with its own control-plane + 3 workers
  (screenshot `hw158-06-cloud-2region.png`).
- `shared-pg` WAL streaming ×2 replicas; cross-region datapath verified live — the region-b replica
  (`client_addr 10.43.4.36`) was streaming to the region-a primary (VPC peering + routes present, so
  this was **not** a kom4dc datapath gap).

## Root cause found + fixed during the walk
The cross-region replica was streaming **asynchronously**: the primary's CNPG `synchronous` block
named only local (region-a) standbys, so PostgreSQL `synchronous_standby_names` auto-filled from the
region-a HA peers and **never** included the cross-region replica → a naive kill would have lost the
in-flight transactions (RPO ≠ 0).

**Fix (PR #3742, bp-cnpg-pair 0.2.5):** `standbyNamesPre: [replica]` + `maxStandbyNamesFromCluster: 0`
→ `synchronous_standby_names` lists the cross-region replica first and does NOT backfill local peers
→ every COMMIT blocks until region-b has acknowledged → **RPO = 0**.

## The kill walk (steps performed live)
1. Armed the cross-region SYNC standby (applied the #3742 values; confirmed `synchronous_standby_names`
   now names the region-b replica).
2. Seeded **5 monotonic rows** against the primary write endpoint — each COMMIT was synchronously
   acked by region-b before returning.
3. **Severed region-a:** force-deleted the primary CNPG pods + cordoned the region-a nodes (a real
   region kill per DOD D31 §6, not a pod restart or a scale-down).
4. **Promoted region-b:** patched `replica.enabled = false`.
5. Observed `pg_is_in_recovery` flip **t → f in ≈ 1.4 s** (RTO).
6. **All 5 seeded rows survived** on the new primary (RPO = 0); a post-kill write was accepted.
7. **Restored region-a** writable (rejoined the topology).

## Measured outcome
| Metric | Agreement target (active-hot-standby) | Measured on hw158 |
|---|---|---|
| RTO (kill → new primary writable) | ≤ 30 s | **≈ 1.4 s** |
| RPO (data loss) | 0 (sync) | **0** (all 5 rows survived) |
| Rows lost | 0 | **0** |

## Out-of-scope follow-ups (non-blocking, noted in UAT.md Row 11)
- region-a `instances: 3` capacity scheduling.
- Day-2 cross-region rejoin (split-brain avoidance on the rejoined region).
- The async-streaming default is tracked at source by **#3740** (re-prov to bank the corrected default).
