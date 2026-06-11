# hw128 region-kill walk (T2+T3) — PASS — 2026-06-11 14:03–14:15 UTC

> North-star row 1. Executed live on `hw128.omani.works` (`5cc5f21df5f64ea7`), the clean
> zero-touch 2-region (2-VPC kom4dc mimic) Sovereign. Every command + timestamp below is
> from the live transcript; nothing mocked, nothing hand-waved.

## Topology under test (all zero-touch after the 2026-06-11 fix chain)
- ClusterMesh **1/1 remote clusters ready in BOTH directions** + the `primary-mesh` global
  service synced — first fully bidirectional agent-level mesh on kom4dc, ever.
- `cnpg-pair-bp-cnpg-pair-primary` (region-a, 2 instances) → WAL-streaming →
  `cnpg-pair-bp-cnpg-pair-replica` (region-b) — `pg_stat_replication`:
  `cnpg-pair-bp-cnpg-pair-replica | streaming | async`.
- Reached via the #3241 six-layer fix chain (#3290 #3299 #3303 #3304 + the VPC peering
  + stale-peer cleanup, all recorded on the issue).

## The walk
| T (UTC) | Step | Result |
|---|---|---|
| 14:03:54 | `INSERT INTO walk_proof` on the **region-a primary** | id=1 committed |
| 14:03:59 | `SELECT` the row on the **region-b replica** | row present (cross-VPC WAL replication proven with data) |
| 14:09:04 | **HARD batch-stop ALL 4 region-a ECS** (cp1 + 3 workers, job `8a868c40…2ea3`) | all 4 `SHUTOFF`; region-a kube API dead |
| 14:09–14:14 | region-b replica polled every ~15s through the kill | the proof row served on EVERY tick — zero data unavailability |
| 14:14:22 | Promote: `kubectl patch cluster …-replica --type=merge -p '{"spec":{"replica":{"enabled":false}}}'` (the chart's documented failover path) | patched |
| **14:14:25** | `pg_is_in_recovery()` flips `t`→`f` | **PROMOTED IN 3 SECONDS** (DoD target ≤60s) |
| 14:14:40 | `INSERT` on the promoted region-b primary | id=34 accepted — survivor is read-write |
| 14:15:15 | Batch-start region-a (job `8a868c40…2eb3`) | recovery initiated |

## Honest findings (defects observed during the walk)
1. **Promotion is operator-driven, not automated.** `continuum.enabled` defaults false → no
   Continuum CR exists; the continuum-controller lives on the PRIMARY cluster, which is the
   thing that dies in a region-kill. The 3s promote was the documented one-command patch —
   excellent RTO, but a human (or external automation) must issue it. Folds into #3189/#3195.
2. **failover-readiness probe lag sentinel broken**: reports `lag=999999s — NOT promotable`
   while replication is verifiably streaming (proof row replicated in <5s). Its LSN
   comparison path needs the same mesh-service host the basebackup uses.
3. **Region-a re-join is a Day-2 gap**: after restart, region-a's pair-half resumes as its
   own primary (CR-level split-brain across the two CNPG clusters). Demote-and-rejoin
   needs the Continuum switchover sequencing.
4. **VPC peering was missing from the Huawei IaC** (#3241 layer 6): the 2-VPC mimic had NO
   cross-VPC route — pod datapath structurally impossible on every kom4dc prov until now.
   Live-fixed via the platform's own Huawei creds (peering `a53097cd…` + routes both ways);
   tofu source fix required so the NEXT prov gets it zero-touch.

Refs #3241 #3263 #2737
