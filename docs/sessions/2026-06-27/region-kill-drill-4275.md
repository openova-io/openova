# Region-kill failover drill — Pillar-3 (D31) acceptance · #4275

**Status:** finalized, executable · **DO NOT RUN without (1) a 2-region env AND (2) explicit founder GO.**
**Refs:** #4275 (region-kill counter-test) · #4212 (DR backbone) · ADR-0004 (RPO=0 sync standby) · DoD §Pillar-3

---

## What this drill proves

Pillar-3's load-bearing counter-test: on a 2-region Sovereign, hard-kill region-a's data tier and prove that the region-b synchronous standby promotes to primary **with zero committed-transaction loss (RPO=0) and RTO ≤ 30s**. Green plumbing is NOT acceptance — only a destructive kill + clean promotion + intact tx-counter + clean restore earns the ✅.

---

## Hard preconditions (all required before execution)

1. **A 2-region converged env.** Either the permanent `omantel.biz` Sovereign (with the caveats in §Permanent-env safety below) OR a fresh disposable 2-region prov. A fresh disposable 2-region prov is itself gated on the kom4dc EIP quota bump (10 → ≥16) — see `eip-quota-bump-request.md`. There is no other 2-region capacity today.
2. **Explicit founder GO.** This is a destructive drill. Per the autonomy mandate, destructive failover against the permanent serving env requires founder say-so and a low-traffic window with an operator present for the §(d) restore.
3. **Read-only prerequisite verification green** (the §0 checks below): cnpg-pair primary + region-b sync standby streaming, lag=0, Continuum Healthy with a held lease, ClusterMesh 2/2.

If any precondition is unmet, this runbook stays unexecuted. The single capacity gate for a disposable env is the EIP quota bump.

---

## Topology (the env this drill targets)

Two **independent** k3s clusters (not a stretched control plane), confirmed live 2026-06-27 on dep `91dc05917e44d1c1`:

| | region-a (primary) | region-b (standby) |
|---|---|---|
| cluster | `hw-me-east-215-a-rtz-prod` | `hw-me-east-215-b-rtz-prod` |
| apiserver | `212.72.24.12:6443` | `212.72.24.26:6443` |
| cnpg-pair | `cnpg-pair-bp-cnpg-pair-primary` 3/3, primary `…-primary-1` | `cnpg-pair-bp-cnpg-pair-replica` 3/3, `replica.enabled=true`, `pg_is_in_recovery=t` |
| nodes | 1 cp + 5 workers | 1 cp + 5 workers |

**Architectural fact that shapes the drill:** `continuum-controller` and `catalyst-api` run **only on region-a nodes**, and the arbitration Leases live in region-a's apiserver. On a *full* region-a kill the controller dies with the region, so promotion of region-b is necessarily **operator-driven** (`kubectl patch replica.enabled=false`). `spec.autoFailover=false` is locked (ADR contract) — the controller surfaces the alarm, it does not self-promote. Controller auto-promote applies only to a *partial* fault where region-a's apiserver survives. **This drill targets the faithful full-region-data-tier case → operator promotion.**

### Kubeconfig handles (set before running)

```bash
# region-a + region-b kubeconfigs — adjust paths to the env under test.
# On the permanent env (dep 91dc05917e44d1c1) these were:
KA=/home/openova/.kube/91dc/91dc05917e44d1c1.yaml        # region-a
KB=/home/openova/.kube/91dc/...-me-east-215-b-1.yaml     # region-b
# For a fresh disposable prov, use that dep's region-a / region-b kubeconfigs.
```

---

## 0. Pre-kill verification (READ-ONLY — confirm RPO=0 before touching anything)

```bash
# Primary sees region-b as the SYNC standby, lag = 0
kubectl --kubeconfig=$KA exec -n cnpg cnpg-pair-bp-cnpg-pair-primary-1 -c postgres -- \
  psql -U postgres -x -c "SELECT application_name,sync_state,sync_priority,replay_lsn FROM pg_stat_replication;"
# Expect: application_name=cnpg-pair-bp-cnpg-pair-replica, sync_state=sync, sync_priority=1

kubectl --kubeconfig=$KA exec -n cnpg cnpg-pair-bp-cnpg-pair-primary-1 -c postgres -- \
  psql -U postgres -c "SHOW synchronous_standby_names; SHOW synchronous_commit;"
# Expect: FIRST 1 ("cnpg-pair-bp-cnpg-pair-replica") ; remote_apply   (every COMMIT blocks on region-b ack ⇒ RPO=0)

# Continuum healthy + lease held by region-a
kubectl --kubeconfig=$KA get continuum -n cnpg cnpg-pair-bp-cnpg-pair-continuum \
  -o jsonpath='{.status.phase} lease={.status.leaseHolder} held={.status.leaseHeld} lag={.status.replicationLagSeconds}{"\n"}'
# Expect: Healthy lease=hw-me-east-215-a-rtz-prod held=true lag=0

# ClusterMesh 2/2 (run from each region's cilium pod)
kubectl --kubeconfig=$KA -n kube-system exec ds/cilium -- cilium-dbg troubleshoot clustermesh
kubectl --kubeconfig=$KB -n kube-system exec ds/cilium -- cilium-dbg troubleshoot clustermesh
# Expect: both peers resolve, TCP+TLS+etcd OK
```

**Gate:** do not proceed unless `sync_state=sync`, lag=0, Continuum `Healthy` + lease held, ClusterMesh both-ways OK.

---

## (a) Seed the monotonic tx-counter + confirm region-b is caught up

```bash
# seed 1000 rows — each COMMIT is sync-acked by region-b BEFORE returning
kubectl --kubeconfig=$KA exec -n cnpg cnpg-pair-bp-cnpg-pair-primary-1 -c postgres -- psql -U postgres -d app -c \
 "CREATE TABLE IF NOT EXISTS d31_counter(n bigint PRIMARY KEY, ts timestamptz DEFAULT now());
  INSERT INTO d31_counter(n) SELECT g FROM generate_series(1,1000) g;"

# primary count == 1000
kubectl --kubeconfig=$KA exec -n cnpg cnpg-pair-bp-cnpg-pair-primary-1 -c postgres -- \
  psql -U postgres -d app -tAc "SELECT max(n) FROM d31_counter;"   # expect 1000

# region-b ALREADY has all 1000 (sync) BEFORE the kill — the RPO=0 pre-proof
kubectl --kubeconfig=$KB exec -n cnpg cnpg-pair-bp-cnpg-pair-replica-1 -c postgres -- \
  psql -U postgres -d app -tAc "SELECT max(n) FROM d31_counter;"   # expect 1000

date -u +%FT%T.%3NZ   # T0 marker — record it
```

---

## (b) Kill region-a (least-destructive faithful — data-tier kill)

The faithful counter-test severs region-a's data plane so the region-b sync standby MUST promote. The least-destructive faithful form is **cordon region-a nodes + force-delete the 3 region-a cnpg primary pods** — a real kill of the data tier (not a pod restart / scale-down), and cleanly restorable on the permanent env. A full node-poweroff is more faithful but NOT cleanly restorable on a serving env → use a disposable env for that variant only.

```bash
# cordon every region-a node so cnpg cannot reschedule the primary inside region-a
for n in $(kubectl --kubeconfig=$KA get nodes -o name); do
  kubectl --kubeconfig=$KA cordon "${n#node/}"
done

# sever the data tier: force-delete the region-a cnpg-pair primary pods
kubectl --kubeconfig=$KA delete pod -n cnpg \
  -l cnpg.io/cluster=cnpg-pair-bp-cnpg-pair-primary --grace-period=0 --force

date -u +%FT%T.%3NZ   # T-kill marker — record it
```

---

## (c) Failover proof — operator-promote region-b + measure RTO/RPO

```bash
# operator promotion (continuum-controller is dead with region-a; autoFailover=false)
kubectl --kubeconfig=$KB patch cluster.postgresql.cnpg.io cnpg-pair-bp-cnpg-pair-replica -n cnpg \
  --type merge -p '{"spec":{"replica":{"enabled":false}}}'

# watch pg_is_in_recovery flip t -> f  (RTO = delta from the patch to this flip)
while true; do
  R=$(kubectl --kubeconfig=$KB exec -n cnpg cnpg-pair-bp-cnpg-pair-replica-1 -c postgres -- \
        psql -U postgres -tAc "SELECT pg_is_in_recovery();" 2>/dev/null)
  echo "$(date -u +%FT%T.%3NZ) in_recovery=$R"
  [ "$R" = "f" ] && break
  sleep 0.2
done

# RPO proof: all 1000 rows survived on the NEW primary
kubectl --kubeconfig=$KB exec -n cnpg cnpg-pair-bp-cnpg-pair-replica-1 -c postgres -- \
  psql -U postgres -d app -tAc "SELECT count(*),max(n) FROM d31_counter;"   # expect 1000 | 1000  -> RPO=0

# new primary accepts a fresh write
kubectl --kubeconfig=$KB exec -n cnpg cnpg-pair-bp-cnpg-pair-replica-1 -c postgres -- \
  psql -U postgres -d app -c "INSERT INTO d31_counter(n) VALUES (1001);"
```

**Pass bar:** RTO ≤ 30s (prior hw158 walk measured ~1.4s) · `count=1000` (zero committed-tx loss → RPO=0) · post-kill write accepted on the new primary.

---

## (d) Restore region-a → steady state (split-brain guard)

**Order matters: demote region-a to standby BEFORE uncordoning, or you risk two primaries.**

```bash
# 1) re-home region-a as a STANDBY of the new (region-b) primary FIRST
kubectl --kubeconfig=$KA patch cluster.postgresql.cnpg.io cnpg-pair-bp-cnpg-pair-primary -n cnpg \
  --type merge -p '{"spec":{"replica":{"enabled":true}}}'

# 2) only now uncordon — region-a pods reschedule and re-stream from region-b primary-mesh
for n in $(kubectl --kubeconfig=$KA get nodes -o name); do
  kubectl --kubeconfig=$KA uncordon "${n#node/}"
done

# 3) verify region-a is now a streaming standby (in recovery) and caught up
kubectl --kubeconfig=$KA exec -n cnpg cnpg-pair-bp-cnpg-pair-primary-1 -c postgres -- \
  psql -U postgres -tAc "SELECT pg_is_in_recovery();"   # expect t

# 4) SPLIT-BRAIN GUARD — if any old-primary region-a pod came up writable, demote it immediately
#    (a region-a pod returning pg_is_in_recovery=f after step 1 is the danger signal)
```

### Optional fail-back (return primary to region-a for steady state)

Reverse (c)/(d): patch region-b `replica.enabled=true`, region-a `replica.enabled=false`, re-confirm sync standby + lease pinned to region-a. Optional — region-b can remain primary.

### Steady-state re-confirmation (after restore)

```bash
kubectl --kubeconfig=$KA get continuum -n cnpg cnpg-pair-bp-cnpg-pair-continuum \
  -o jsonpath='{.status.phase} lease={.status.leaseHolder} held={.status.leaseHeld}{"\n"}'
# Expect: Healthy + lease re-pinned + cross-region standby back to sync_state=sync
```

---

## Permanent-env safety (if running on `omantel.biz` instead of a disposable env)

The cordon+force-delete data-tier form above is **permanent-env-safe** with two honest caveats — neither corrupts data:

1. **Write-availability pause during the kill window.** With `synchronous_commit=remote_apply` and region-b as the only sync standby, while region-a primary is down and before region-b is promoted, any in-flight COMMIT on the old primary blocks. This is the by-design durability-over-availability contract (ADR-0004); the old primary is being killed anyway. Region-b is writable the instant it promotes.
2. **Day-2 rejoin / split-brain on fail-back.** Bringing region-a back is the delicate step — §(d) avoids it by patching region-a to `replica.enabled=true` BEFORE uncordoning, so it rejoins strictly as a follower.

The spine apps (gitea/harbor/keycloak/openbao) each carry their own Continuum already lease-held by region-a, so they ride the same arbitration. Running on the permanent env requires founder GO during a low-traffic window with an operator present to confirm §(d) reaches steady state before walking away. **A full node-poweroff variant is NOT recommended on the permanent env — it is not cleanly restorable; use a disposable 2-region prov for that.**

---

## Evidence to capture (for the #4275 acceptance comment)

- §0 pre-kill `pg_stat_replication` showing `sync_state=sync`, lag=0 + Continuum `Healthy` lease-held.
- §(a) T0 marker + region-b `max(n)=1000` pre-kill.
- §(b) T-kill marker + the cordon/force-delete output.
- §(c) the `in_recovery t→f` timeline (RTO delta) + `count=1000,max=1000` (RPO=0) + accepted post-kill write.
- §(d) region-a back to `pg_is_in_recovery=t` + Continuum re-Healthy + lease re-pinned.
- A wire/log capture or screenshot timeline showing RTO ≤ 30s.

Acceptance = a destructive kill walked end-to-end with the counter intact (RPO=0), RTO ≤ 30s, and a clean restore. Post the timeline + evidence as a comment on #4275 — close only after that operator-walk lands on the issue.
