# hw266 Region-Kill G12 DR Proof (Pillar-3) — RESULT

- **Env**: hw266, dep `b85cb3b3a565893a`, FQDN `hw266.omani.works`, regions `me-east-215-a` (primary) + `me-east-215-b` (replica).
- **Date**: 2026-07-17 (probe window ~12:55–13:00Z)
- **Operator**: read-only investigation. **No kill, no seed, no patch performed.**

## VERDICT: ❌ BLOCKED — region-kill NOT executed. Pre-existing P0 defect in the #5137 dr-promoter.

The region-kill was **not run** because the mandatory pre-kill RPO=0 datapath gate **FAILS**: region-b is not
streaming from region-a and the two clusters have diverged onto **incompatible Postgres timelines (region-a TL1
vs region-b TL24)**. Running the kill anyway would fabricate a green result on a broken substrate (theater), which
the dispatch guardrails explicitly forbid. Root cause is a **#5137 dr-promoter regression**, captured below.

## Evidence table

| Proof | Result | Evidence |
|---|---|---|
| Pre-kill RPO0 gate (region-b sync-streaming from region-a) | ❌ **FAIL** | region-a `pg_stat_replication` shows only its 2 in-region replicas; region-b `pg_stat_wal_receiver` = **0 rows**; region-a `pg_replication_slots` has **no slot** for the region-b replica |
| RTO (actual seconds) | **N/A** — kill not performed | gate failed |
| RPO (0 or count) | **N/A / would be >0** | region-b never received region-a's post-bootstrap WAL; timelines diverged (TL1 vs TL24) |
| #5137 dr-promoter AUTO-promotes | ⚠️ **YES but PATHOLOGICAL (regression)** | promoter promotes a **healthy** replica against a **live** region-a; false-positive "WAL receiver DOWN" 16s after a healthy stream; flap loop advanced timeline 2→24 |
| #5157 openbao region-b Sealed=false | ✅ **YES (currently)** | `bao status` → `Sealed=false, Initialized=true, HA active`; `openbao-unseal-reconciler` is a **Deployment** (1/1, 107m) per #5157. Across-kill hold: **not tested** (blocked) |
| keycloak realm preserved region-b | ⚪ **NOT TESTED** (blocked) | `keycloak-0` Running + Ready=True in region-b; across-kill preservation not exercised |
| region-a restored healthy | **N/A** | region-a was never killed; remained healthy/writable throughout (`writable=true`) |

## Root cause (timeline reconstructed from region-b replica-1 postgres log + dr-promoter log)

1. **11:38:01Z** — region-b replica-1 bootstrapped (pg_basebackup), entered standby, reached consistent recovery,
   and **`started streaming WAL from primary at 0/4000000 on timeline 1`**. Cross-region streaming was HEALTHY.
2. **11:38:17Z** — dr-promoter `signals` container: `WAL receiver DOWN — primary stream dead; hold clock started`
   — a **FALSE POSITIVE**, only 16s after the stream came up healthy, region-a fully alive.
3. **11:40:19Z** — after the 120s hold, dr-promoter `actor`: `PROMOTING … patching HR desired state (#5125-D1 seam)`
   → region-b `selected new timeline ID: 2`. **Spurious promotion of a healthy replica against a live primary.**
4. The promotion is **not durable**: dr-promoter patches the live `HelmRelease/bp-cnpg-pair`
   (`spec.values.cnpgPair.replica.promoted=true`), but Flux reconciles the HR back to Git desired state (current
   `values.cnpgPair.replica` = `{"region":"hw-me-east-215-b-rtz-prod"}` — **no `promoted`/`enabled` field**). CNPG
   demotes region-b back to replica mode; it re-attempts to stream from region-a (TL1) but now fails
   `FATAL: highest timeline 1 of the primary is behind recovery timeline N`. The promoter sees the receiver "down"
   again → re-promotes → timeline runs away **2 → 3 → 4 → … → 24**. Loop has run continuously since 11:38Z.
5. region-a is **reachable** the whole time — the walreceiver receives a *timeline-level* rejection FROM region-a's
   TL1 primary, so this is NOT a clustermesh/connectivity outage; it is a pure promoter-induced timeline divergence.

### Two compounding defects (both #5137 / #5125 dr-promoter, NOT #5157, NOT keycloak)
- **Defect A — false-positive promote trigger**: dr-promoter declared the primary stream dead 16s after a verified
  healthy stream and promoted with region-a alive. This is the root cause of the divergence.
- **Defect B — non-durable promotion seam (#5125-D1)**: promotion by patching the live HR is reverted by Flux (the
  desired state is not written to Git), producing a promote/demote flap that runs the timeline away. In a REAL
  region-a death this same non-durability would prevent the promotion from sticking.

Net effect: cross-region replication is **permanently broken** on hw266 (region-b on an orphaned timeline that can
never re-stream from region-a's TL1), so RPO=0 cannot be proven and the G12 walk is blocked.

## Key command outputs

```
# region-a primary — current WAL LSN + timeline (healthy, writable)
0/9000000|1
writable=true
# region-a pg_stat_replication — only 2 in-region replicas, NO region-b client
application_name=cnpg-pair-bp-cnpg-pair-primary-3  state=streaming  sync_state=async
application_name=cnpg-pair-bp-cnpg-pair-primary-2  state=streaming  sync_state=async
# region-a pg_replication_slots — NO slot for the region-b replica
_cnpg_cnpg_pair_bp_cnpg_pair_primary_2  active=t  restart_lsn=0/9001FD8
_cnpg_cnpg_pair_bp_cnpg_pair_primary_3  active=t  restart_lsn=0/9001FD8

# region-b replica-1 — in recovery, receive/replay LSN + timeline (DIVERGED)
pg_is_in_recovery = t
pg_last_wal_receive_lsn=0/26000000  pg_last_wal_replay_lsn=0/260000A0  timeline=24
pg_stat_wal_receiver = (0 rows)          # walreceiver DOWN

# region-b replica-1 postgres log (repeating, terminal)
FATAL: highest timeline 1 of the primary is behind recovery timeline 24  (backend_type=walreceiver)

# region-b HelmRelease bp-cnpg-pair — no promoted/enabled field (Flux reverts the promoter's patch)
spec.values.cnpgPair.replica = {"region":"hw-me-east-215-b-rtz-prod"}

# region-b openbao (#5157) — Deployment reconciler, currently unsealed
Sealed=false  Initialized=true  HA Mode=active
openbao-unseal-reconciler   1/1  (Deployment, 107m)

# region-b keycloak
keycloak-0   1/1 Running   Ready=True
```

## dr-promoter log excerpt (the flap)
```
11:38:17Z [dr-promoter/signals] WAL receiver DOWN — primary stream dead; hold clock started
11:40:28Z [dr-promoter/signals] state=promoted — hold clock cleared
... (repeats every ~5 min) ...
12:42:47Z [dr-promoter/actor] PROMOTING: WAL stream down 126s >= 120s AND failover-readiness Ready — patching HR desired state (#5125-D1 seam)
12:42:47Z [dr-promoter/actor] PROMOTED: HR ... replica.promoted=true → helm-controller renders replica.enabled:false → CNPG promotes the survivor writable
(then Flux reverts → re-stream fails on timeline → re-promote → timeline 2→…→24)
```

## Recommended fix (for the fix-train owner — not applied here)
1. **Gate the promote trigger on genuine region-a loss**, not solely on "local walreceiver down N seconds": add a
   direct liveness probe of the region-a primary (TCP/health via the mesh) and only arm the hold clock when region-a
   is actually unreachable. A healthy stream that just (re)established must reset/suppress the clock; add a startup
   grace so a 16s-old healthy stream can never trip it.
2. **Make the promotion durable**: write the promoted desired-state to the GitOps source (or use a mechanism Flux
   will not revert) so a real failover latches instead of flapping. Latch once-promoted; never re-promote a cluster
   already on a diverged timeline.
3. hw266 is **not recoverable in place** for a clean G12 proof (region-b is on an orphaned TL24). After the promoter
   fix lands, re-clone the region-b replica from region-a (fresh pg_basebackup) or fresh-prov, then re-run this walk.

## State left behind
Untouched. All operations were read-only (`get`/`logs`/`exec psql SELECT`). No kill, no seed, no HR/cluster patch,
no PVC/NodePort/deployment changes. region-a remains healthy and writable; the pre-existing dr-promoter flap
continues (it was already running before this session and was not started by it).
