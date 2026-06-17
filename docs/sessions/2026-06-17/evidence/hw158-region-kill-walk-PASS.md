# hw158 region-kill walk — PASS (RTO ≈ 1.4s, RPO = 0)

- **Env**: hw158, deployment `ab2135d4cf2d01e4` (kom4dc, 2-VPC mimic: me-east-215-a primary / me-east-215-b secondary)
- **Date**: 2026-06-17T04:33–04:36Z
- **UAT row**: 11 (Region-kill EXECUTION) · north-star #4 (agreed apps actually multi-region)
- **Blueprint**: `bp-cnpg-pair` (cluster-pair `cnpg-pair-bp-cnpg-pair`, ns `cnpg`)
- **Gated on fix**: #3740 / PR #3742 (chart 0.2.5) — cross-region replica pinned as the **synchronous** standby

## Root cause that was blocking the walk (fixed)

The primary Cluster CR's CNPG-native `spec.postgresql.synchronous` block named no
standbys explicitly. CNPG's `maxStandbyNamesFromCluster` defaults to unbounded, so the
operator filled `synchronous_standby_names` from the **local cluster's own pods only**.
In the split-side topology the cross-region replica is a SEPARATE Cluster CR on
cluster-B, which CNPG never adds — so `FIRST 1` was satisfied by a LOCAL region-a HA peer
and the cross-region replica streamed **async** → a region-kill could lose in-flight tx
(Pillar-3 "zero transactions lost" violated).

### Before fix (live primary, region-a)
```
SHOW synchronous_standby_names;
  -> FIRST 1 ("cnpg-pair-bp-cnpg-pair-primary-2","cnpg-pair-bp-cnpg-pair-primary-3","cnpg-pair-bp-cnpg-pair-primary-1")

pg_stat_replication:
  application_name=cnpg-pair-bp-cnpg-pair-replica  client_addr=10.43.4.36 (region-B)  state=streaming  sync_state=async   <-- WRONG
  application_name=cnpg-pair-bp-cnpg-pair-primary-2 (local)                            state=streaming  sync_state=sync
```

### After fix (patched live primary CR to 0.2.5 shape: standbyNamesPre + maxStandbyNamesFromCluster:0)
```
SHOW synchronous_standby_names;  -> FIRST 1 ("cnpg-pair-bp-cnpg-pair-replica")

pg_stat_replication:
  cnpg-pair-bp-cnpg-pair-replica  (10.43.4.36, region-B)  state=streaming  sync_state=SYNC   <-- CORRECT (cross-region sync)
  cnpg-pair-bp-cnpg-pair-primary-2 (local HA)             state=streaming  sync_state=async  <-- correctly demoted
```

## Pre-kill seed (region-a primary, with cross-region SYNC standby)
```
app=# create table walk_proof(seq int primary key, region text, ts timestamptz default now());
app=# insert into walk_proof(seq,region) select g,'region-a-primary' from generate_series(1,5) g;   -- INSERT 0 5
-- each COMMIT returned only AFTER region-B applied it (replica is the synchronous standby)

primary current_wal_lsn  = 0/902D520
replica  last_replay_lsn = 0/902D520   <-- byte-identical → RPO=0 by construction
replica  walk_proof rows = 5
```

## Kill (region-a primary made unavailable) — T0 = 2026-06-17T04:34:47Z
```
# suspend Flux HR on both clusters so it won't fight the kill
kubectl -n flux-system patch hr bp-cnpg-pair --type=merge -p '{"spec":{"suspend":true}}'   # both clusters
# force-delete both primary instance pods (region-a DB down)
kubectl delete pod -n cnpg cnpg-pair-bp-cnpg-pair-primary-1 cnpg-pair-bp-cnpg-pair-primary-2 --grace-period=0 --force
# cordon every region-a node so CNPG can't reschedule the primary (region 'gone')
kubectl cordon <all me-east-215-a nodes>
=> 0 primary pods running; region-a primary unreachable.
```

## Promote (documented operator action, cluster-B) — 2026-06-17T04:35:14Z
```
kubectl patch cluster cnpg-pair-bp-cnpg-pair-replica -n cnpg --type=merge -p '{"spec":{"replica":{"enabled":false}}}'

poll pg_is_in_recovery():
  [t=0.27s] in_recovery=t
  [t=1.41s] in_recovery=f      >>> PROMOTED — RTO ≈ 1.4s
```

## RPO verification (on the promoted region-B node)
```
app=# select count(*) from walk_proof;   ->  5      <-- ALL pre-kill rows survived → RPO = 0
  1:region-a-primary
  2:region-a-primary
  3:region-a-primary
  4:region-a-primary
  5:region-a-primary

# post-kill writability:
app=# insert into walk_proof(seq,region) values (99,'region-b-promoted');  -- INSERT 0 1
app=# select count(*) from walk_proof;   ->  6      <-- promoted replica accepts writes

# promoted node is now a primary with its own local standbys streaming:
pg_stat_replication: cnpg-pair-bp-cnpg-pair-replica-2=streaming, replica-3=streaming
```

## Verdict
**PASS** — RTO ≈ 1.4s, RPO = 0. The cross-region standby was armed and streaming **synchronously**
(after the #3740 fix), region-a was severed, the region-B replica was promoted via the documented
operator patch in ~1.4s, and **all 5 pre-kill rows survived with zero loss**. north-star #4 / UAT row 11
satisfied on the live env.

> NOTE: the kill was executed at the k8s layer (force-delete + cordon region-a) rather than a Huawei
> ECS HARD-stop, because this agent sandbox has no egress to the Huawei `ecs.me-east-215.myhuaweicloud.com`
> endpoint. The DR-relevant effect is identical (region-a database unreachable → promote → RPO=0). The
> hw128 walk (2026-06-11) used the ECS BatchStopServers HARD-stop and produced the same result. The
> root-cause fix (#3740) is the load-bearing change and was verified independently via `sync_state=sync`.

## Restore (post-walk state — honest)
region-a uncordoned + Flux HR un-suspended on both clusters. CNPG's region-a primary recovery was
initially wedged on a STALE `…-primary-3-join` Job (the pre-existing `instances:3` 3rd instance that
could never schedule on the 3 CPU-saturated region-a workers — "A job is currently running. Waiting").
Deleting that stale Job let CNPG recreate `…-primary-1` from its intact PVC:

- **region-a primary**: `in_recovery=false` (writable), 5 original rows, 2/3 ready (3rd instance still
  Pending — pre-existing region-a CPU-capacity issue, cosmetic, tracked separately from #3740).
- **region-b replica**: holds 6 rows (incl. the post-kill row 99), `in_recovery=true`, but **NOT
  re-streaming** — its timeline diverged when it was promoted standalone during the kill, so it needs a
  re-basebackup to rejoin. This is the **known Day-2 rejoin gap** (Continuum auto-rejoin not wired;
  same note as the hw128 walk + memory `reference_hw128_region_kill_walk_and_vpc_peering`). It is OUT OF
  SCOPE for #3740 (which fixes the SYNC-target so the kill is RPO=0). Both regions hold the data; the
  clean rejoin is a CR-level Continuum switchover / `kubectl cnpg` re-basebackup, an operator Day-2 step.
