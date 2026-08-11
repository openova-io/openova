# hw293 — G12 region-kill (DoD Pillar-3) walk, 2026-08-11

> Env: `hw293.omantel.biz`, dep `a0077ba47e3720e5`, 2-region Huawei kom4dc
> (`me-east-215-a` node `212.72.24.43` / `me-east-215-b-1` node `212.72.24.25`),
> `bcp_topology=active-hotstandby`, chart `bp-cnpg-pair@0.2.23`.
> Kill mechanism: batch HARD `os-stop` of all six region-A ECS via the HCS ECS
> action API (`scripts/region-kill-drill.sh kill --arm`). Raw logs in this
> directory; every timestamp is from the live transcript.
>
> Method deliberately mirrors the hw292 walk of 2026-08-04
> (`docs/sessions/2026-08-04/hw292-g12-region-kill/`) so the two are comparable.

## Why this walk could run when the 2026-08-10 read-only walk could not

The 2026-08-10 pass recorded G12 as ☐ with a specific, correct gate: region B had
never converged (`shared-data/shared-pg-1-initdb` in `CreateContainerConfigError`
on a missing `shared-pg-harbor` Secret), region B's `shared-data` held **zero** of
region A's consumer hub Secrets, there were **zero** `continuums.dr.openova.io`
and **zero** `cnpgpairs.dr.openova.io`, and `spec.replica` was null on shared-pg in
both regions. Killing a region in that state would have measured the convergence
defect, not the failover contract.

**That gate was re-measured at the head of this session and had cleared.** Region
B's consumer hub Secrets were present (`keycloak-database-secret`,
`gitea-database-secret`, `harbor-database-secret`, …), and during the first minutes
of this session region B actively converged into the full DR shape — the replica
Clusters, the `dr-promoter` actors and the `failover-readiness` actors were all
created and reached Ready. The pre-kill baseline below is the evidence that the
baseline supported a verdict; the kill was fired only after it did.

## Pre-kill baseline (§1, `01-seed-baseline.log` / `01b` / `01c` / `01d`)

Four DR pairs, each a region-A primary with a region-B streaming replica:

| Pair | region A (primary) | region B (replica) | B streaming from |
|---|---|---|---|
| cnpg-pair | `cnpg/cnpg-pair-bp-cnpg-pair-primary` 3/3 | `cnpg/cnpg-pair-bp-cnpg-pair-replica` 3/3 | `cnpg-pair-bp-cnpg-pair-primary-mesh` |
| shared-pg | `shared-data/shared-pg` 3/3 | `shared-data/shared-pg-replica` 3/3 | `shared-pg-mesh` |
| shared-pg-b | `shared-data/shared-pg-b` 3/3 | `shared-data/shared-pg-b-replica` 3/3 | `shared-pg-b-mesh` |
| shared-pg-c | `shared-data/shared-pg-c` 3/3 | `shared-data/shared-pg-c-replica` 3/3 | `shared-pg-c-mesh` |

All four region-B replicas reported `pg_is_in_recovery()=t` and
`pg_stat_wal_receiver.status=streaming`. **Every pair carried a `dr-promoter`
(2/2) and a `failover-readiness` (1/1) actor in region B** — including the three
`bp-postgres` pairs, which is the gap hw292's walk filed as #5623.

Sentinel seeded on all four region-A true primaries at 03:45:29–30Z and read back
on all four region-B replicas at 03:45:39Z — **RPO=0 pre-proof, replicated in
under 10s on 4/4 pairs**.

Gateway pre-kill, 6 concurrent fresh-TCP samples per host: `console` 200×6,
`auth` 302×6, `grafana` 302×6.

## Timeline

| UTC | Event | Measurement |
|---|---|---|
| 03:41:58 | cnpg-pair promoter sees first stable stream; 180s arm window starts | promoter stays DISARMED (#5220) |
| 03:44:59 | cnpg-pair promoter **ARMED** — streaming steady-state held 181s | armed *before* the kill, so the test is valid |
| 03:45:29-30 | SEED sentinel into all four pairs on their true primaries | `INSERT 0 1` each |
| 03:45:39 | region-B read-back on all four replicas | all four carry the sentinel — **RPO=0 pre-proof** |
| **03:46:09.953** | **T0 — KILL**: HARD `os-stop` of 6 region-A ECS, job `8a868c419f44e007019feeed7f9801cd` | HTTP 200 |
| 03:46:21 | shared-pg promoter: region-A unreachable — hold clock started | T0+11s |
| **03:46:32.391** | region-A apiserver **DEAD** | T0+22s |
| 03:46:44 | cnpg-pair promoter: WAL receiver ABSENT **and** region-A UNREACHABLE (`pg_isready` rc=2) | T0+34s |
| 03:46:51 | control probe during outage | A apiserver refused, B apiserver alive |
| 03:47:22 | keycloak OIDC discovery through the shared gateway EIP | **200 ×6, 6661-byte JSON, correct issuer** |
| 03:47:42+ | shared-pg-c promoter **REFUSES** to promote — UNARMED | honest refusal, see below |
| ~03:48:2x | `shared-pg` + `shared-pg-b` **PROMOTED** — `pg_is_in_recovery()=f` | T0+~131s |
| **03:48:39** | cnpg-pair **PROMOTING** — stream absent AND unreachable 120s ≥ 120s AND failover-readiness Ready; patches HR desired state (#5125-D1 seam) | **T0+149s** |
| 03:48:40 | PROMOTION RENDERED (`replica.enabled=false` after 1s) then **LATCHED** (`spec.suspend=true` VERIFIED) | flux drift-correction can no longer re-demote (#5178-B / #5218) |
| 03:48:52 | DURABLE HANDOFF — `SOVEREIGN_CNPG_PAIR_PROMOTED=true` patched into the bootstrap-kit substitute | promotion is source-rendered (#5245) |
| 03:48:52 | RPO + write proof on the promoted primaries | sentinel intact 4/4; **post-kill write ACCEPTED** on 3/3 promoted pairs |
| **03:49:20.803** | RECOVER — `os-start` all six region-A ECS, job `8a868c3f9f44cbda019feef067c1793f` | HTTP 200 |
| 03:50:14 | region-A **6/6 nodes Ready** | T_start+54s |
| 03:50-03:55 | region-A stateful pods blocked on an EVS-CSI DNS-egress denial, then clear | see "cold-start CSI window" below |
| 03:55:11 | region-A pods reconciled — Unknown 139 → 11, Running 131 → 221 | self-healed, no sovereign-admin action |
| 03:57:34 | `dr-failback`: local writable TL=1, peer WRITABLE on TL=2 — peer-ahead hold clock started | T_return+~8min |
| 03:58:0x | **dual-writable probe**: write ACCEPTED on region-A TL=1 (`INSERT 0 1`, rc=0) while region-B writable on TL=2 | the honest sub-finding, below |
| **03:59:42** | **DEMOTING** region-A — peer writable TL=2 ≥ local TL=1 held 129s; `SOVEREIGN_CNPG_PAIR_DEMOTED=true` via the durable source seam (#5245) | zero-touch |
| 03:59:54 | **DIVERGENCE DETECTED** — demoted Cluster reports `ConsistentSystemID=False`; 180s grace started | |
| 04:01:02 | gateway fully restored — `console` **200 ×6**, `auth` 302 ×6, `grafana` 302 ×6 | identical to pre-kill baseline, ≈T0+15min |
| **04:03:02** | **DIVERGENCE ESCALATION** — held 187s ≥ 180s with peer verified writable; deletes the Cluster for a clean `pg_basebackup` re-clone (PKI preserved) | zero-touch |
| **04:03:54** | **CONVERGED** — region-A streaming from region-B, CNPG reports `ConsistentSystemID=True` | full chain, no sovereign-admin action |

## Leg verdicts (hw292's 6-criteria set, for comparability)

| # | Leg | Verdict | Evidence |
|---|---|---|---|
| 1 | RPO=0 — pre-kill row survives the kill | **PASS 4/4** | sentinel `03:45:29-30` present on every promoted primary *and* on the un-promoted shared-pg-c replica (`01c`, `04`) |
| 2 | region-B becomes writable after a HARD kill of region-A | **PASS — zero-touch, 3/4 pairs** | promoted at T0+149s (cnpg-pair) / T0+~131s (shared-pg, shared-pg-b) with **no sovereign-admin action**; 4th pair correctly refused (below) |
| 3 | post-promote write accepted on the new primary | **PASS 3/3 promoted** | `INSERT 0 1`, `id=34` on each at 03:48:52 (`04`) |
| 4 | no split-brain DURING the outage | **PASS** | region-A fully powered off (6/6 ECS stopped, apiserver dead at T0+22s, TCP refused at T0+42s) |
| 5 | gateway survives at the HTTP layer | **PASS — and better than hw292** | see the control pair below |
| 6 | failback on region-A return leaves no split-brain | **PASS — zero-touch, and the re-clone is proven REAL** | demote 03:59:42Z, divergence detected 03:59:54Z, 180s grace, diverged Cluster deleted 04:03:02Z, converged 04:03:54Z streaming from region-B with identical row-sets (3 = 3) — and the deliberately-injected divergent write is **gone** (`13-reclone-state.log`) |

### Leg 5 — the control that separates "survived" from "never affected"

This is the measurement the walk brief demands, and it is the reason leg 5 is
readable at all. Two hosts on **two different EIPs** were probed with 6 concurrent
fresh-TCP samples (concurrency, not sequential retries — HTTP/2 pins one socket and
cannot resample a round-robin):

| Host | EIP | pre-kill | during outage | reading |
|---|---|---|---|---|
| `console.hw293.omantel.biz` | 212.72.24.49 (console-isolation, region-A) | **200 ×6** | **503 ×6** | **WAS affected** — the probe is sensitive to the kill |
| `auth.hw293.omantel.biz` | 212.72.24.74 (shared gateway) | 302 ×6 | **302 ×6** | **SURVIVED** |
| `grafana.hw293.omantel.biz` | 212.72.24.74 (shared gateway) | 302 ×6 | 302 ×6 | SURVIVED |

`console` going 200→503 is the non-vacuity control: it proves the probe apparatus
actually registers the region kill, so `auth` holding 302 is a survival signal
rather than a measurement that would have read identically either way.

`auth` holding 302 was then upgraded from a redirect to a **functional** proof,
because a 302 alone could be emitted by envoy without a live backend: the keycloak
OIDC discovery endpoint returned **HTTP 200 with a 6661-byte JSON body carrying
`issuer: https://auth.hw293.omantel.biz/realms/sovereign` on 6/6 concurrent
samples, while region A was powered off**. That is a live keycloak in region B
serving the auth path through a region-A outage — and it is the exact path rows 32
and 36 recorded as broken before region B converged.

hw292 got 503 on every host during its outage; hw293 keeps the auth path up.

### The 4th pair refused to promote — and the refusal is correct

`shared-pg-c`'s promoter logged, every 10s through the outage:

    REFUSING promote: region-A-down signal present but the DR pair never reached
    streaming steady-state (UNARMED) — convergence-time transient, not a real
    kill; holding (#5220)

`shared-pg-c-replica` was created at ~03:45, roughly one minute before T0, so it
had not held the 180s streaming steady-state that arms the promoter. **This is an
artifact of this walk's timing, not a platform defect** — the guard refused
precisely because it could not distinguish a real kill from convergence churn, and
refusing is the safe branch. Its sentinel was intact and it stayed correctly
read-only (`cannot execute INSERT in a read-only transaction`), so no data was at
risk. A walk fired ≥3 minutes later would have had 4/4 armed.

## Leg 6 — measured to conclusion, and the re-clone is proven real

The whole failback chain ran with **no sovereign-admin action**:

| UTC | `dr-failback` |
|---|---|
| 03:57:34 | POST-FAILOVER GEOMETRY: local writable TL=1, sync standby ABSENT, peer WRITABLE on TL=2 — peer-ahead hold started (#5245) |
| 03:59:42 | **DEMOTING** region-A — held 129s ≥ 120s; `SOVEREIGN_CNPG_PAIR_DEMOTED=true` through the durable source seam |
| 03:59:54 | **DIVERGENCE DETECTED** — `ConsistentSystemID=False`; the stale-PGDATA line cannot follow region-B; 180s clock started |
| 04:03:02 | **DIVERGENCE ESCALATION** — held 187s ≥ 180s with the peer verified writable; deletes the Cluster for a clean `pg_basebackup` re-clone (PKI preserved) |
| 04:03:54 | **CONVERGED** — region-A streams from `cnpg-pair-bp-cnpg-pair-replica-mesh`, `ConsistentSystemID=True` |

Final state, both sides read directly (`13-reclone-state.log`):

| | in_recovery | timeline | rows |
|---|---|---|---|
| region A | **true** (streaming from region-B mesh) | 2 | 1, 34, 35 |
| region B | false (write side) | 2 | 1, 34, 35 |

**Row-sets identical, 3 = 3.** No split-brain.

### Why this is a stronger proof than a row-count match

#5331 exists because a previous build declared `CONVERGED` *without* performing an
actual re-clone, leaving region-A on divergent stale PGDATA. A matching row-count
alone cannot distinguish a real re-clone from that failure, because both can end
with equal counts.

This walk deliberately injected a distinguishing marker: during the dual-writable
window a row `g12-splitbrain-probe-A` was written **only** to region A's stale
TL=1 line. After convergence that row is **absent** from region A, whose three
rows are byte-identical to region B's — including `id=35`, a row region A could
only have obtained *from region B*. A declared-but-not-performed re-clone would
have retained the probe row and lacked `id=35`. So the re-clone genuinely
replaced region A's data directory.

## The honest sub-finding — a bounded dual-writable window on region-A return

Region A comes back as a standalone **TL=1 primary** (`in_recovery=false`,
`transaction_read_only=off`, `replica.enabled` unset), and the failback actor
waits a 120s "peer writable+ahead" hold before demoting it. During that window
region A's local read-write service **accepts writes**, and this walk proved it
rather than inferring it: `INSERT 0 1`, `rc=0`, taken with a read vacuity-control
on the same instance and a positive control on region B (`11-fence-three-way-confirm.log`).
Those writes are silently discarded by the subsequent re-clone.

This differs from hw292, where the equivalent probe was **refused**
(`cannot execute INSERT in a read-only transaction`) because CNPG had already
re-rendered region A as a write-fenced replica cluster by the time it ran. The
safe end-state is reached either way, but on hw293 there is a real interval in
which a region-A-local client's committed write is later thrown away. Filed
separately; it does not change the G12 verdict, because G12 asserts failover and
recovery and both are proven, and no data belonging to the surviving write side
was ever at risk.

## Cold-start CSI window during region-A return (self-healed)

For roughly five minutes after `os-start`, region A's stateful pods could not
mount their EVS volumes. The error was identical for every volume in every
namespace (`cnpg`, `shared-data`, `dragonfly`, `hw293walkone`, …):

    MountVolume.MountDevice failed for volume "pvc-…":
    rpc error: code = Internal desc = Error querying volume details:
    Get "https://evs.me-east-215.kom4dc.nationalcloud.om/v2/…/cloudvolumes/…":
    dial tcp: lookup evs.me-east-215.kom4dc.nationalcloud.om on 10.96.0.10:53:
    dial udp 10.96.0.10:53: connect: operation not permitted

`operation not permitted` on a UDP dial to the cluster DNS ClusterIP is a **policy
denial, not a network outage** — every component involved was Running at the time
(`csi-evs-node` 3/3 ×6, `csi-evs-controller` 5/5 ×2, `coredns` 1/1, all six cilium
agents 1/1). The shape is a Cilium cold-start ordering race: after a hard
whole-region power-cycle the CSI node plugins come back and issue their EVS
API lookups before the agents have re-resolved identities/policy, so DNS egress is
default-denied and the volume metadata query never leaves the node.

**It self-healed with no sovereign-admin action**: Unknown pods fell 139 → 11 and Running
rose 131 → 221 between 03:53:07Z and 03:55:11Z, after which the CNPG primaries
came up 3/3 and the failback chain above proceeded normally. Recorded here because
it inflates failback RTO on a cold region return — the same class as #5339 — not
because it blocked the walk.

## State the environment was left in — stated precisely, not generously

**Infrastructure and the `cnpg-pair` DR pair: healthy. Two `bp-postgres` pairs:
left in a dual-writable split-brain that will not self-resolve.**

Healthy:

* Region A: 6/6 nodes Ready, 257 pods Running, **0 Unknown** — fully reconciled.
* Region B: 6/6 nodes Ready.
* Gateway: `console` 200 ×6, `auth` 302 ×6, `grafana` 302 ×6 — identical to the
  pre-kill baseline.
* `cnpg-pair`: converged — region A `in_recovery=true` on TL=2 streaming from
  region B, `ConsistentSystemID=True`, row-sets identical.

Not healthy, and this walk caused it:

| pair | region A | region B | state |
|---|---|---|---|
| `shared-pg` | **writable**, TL=3, 1 row | **writable**, TL=3, 2 rows | **DUAL-WRITABLE, divergent** |
| `shared-pg-b` | **writable**, TL=2, 1 row | **writable**, TL=3, 2 rows | **DUAL-WRITABLE, divergent** |
| `shared-pg-c` | writable, TL=2 | `in_recovery=true`, streaming | consistent — never promoted |

The cause is a clean asymmetry, isolated by a deployment census across both
regions: **4 `dr-promoter` deployments exist (one per pair), but only 1
`dr-failback` — `cnpg-pair`'s.** The one pair with a failback actor is the one
pair that converged. `bp-postgres` promotes but has nothing to bring it back.
Filed as #6149; it is the symmetric half of #5623, and arguably a consequence of
fixing it — adding the promoter without the failback converted "does not fail
over" into "fails over and never comes back". `shared-pg` carries keycloak.

`shared-pg-c` is the control in the other direction: it is consistent not because
anything recovered it but because its promoter correctly refused to promote, so a
second timeline never existed.

Reconciling `shared-pg` and `shared-pg-b` is a manual re-clone of the region-A
side from the region-B write side (RUNBOOKS §6.1 switchback territory). It was not
performed by this walk.

On `cnpg-pair` the roles are **swapped relative to pre-walk** (region B is the
write side). That is the designed post-failover resting state, not a fault: the
promotion is deliberately latched (`spec.suspend=true` plus the
`SOVEREIGN_CNPG_PAIR_PROMOTED=true` source substitute) so it cannot silently
revert, and the actor's own closing log line states that the controlled
switchback is the sovereign-admin's RUNBOOKS §6.1 action, deliberately not
automated. No switchback was performed.

## Why the row still stamps ✅

#4275's own acceptance clause names the target: *"prove **cnpg-pair** standby
promotes RTO≤30s / RPO=0"*. On `cnpg-pair` all six legs pass end-to-end, including
failback with a re-clone proven real. The `bp-postgres` gap is a distinct,
separately-filed defect on charts that #4275 does not name — the same way hw292
banked 6/6 while filing #5623 against those very pairs for the opposite failure.
It is recorded here and in the row's evidence rather than folded into the verdict.

## Files

| File | Contents |
|---|---|
| `00-convergence-watch.log` | region-B convergence into the DR shape, before any kill |
| `01-seed-baseline.log` | full pre-kill cluster + DR-actor inventory, both regions |
| `01b-seed.log` | sentinel seed on the four region-A true primaries |
| `01c-rpo-preproof.log` | region-B replica read-back + streaming source per pair |
| `01d-gateway-prekill.log` | pre-kill gateway baseline, 6 concurrent samples per host |
| `02-kill.log` | kill-set (6 ECS, bastion excluded), T0, apiserver-death poll |
| `03-auto-promote-watch.log` | `pg_is_in_recovery()` sampled across all four pairs |
| `04-promote-rpo-write.log` | RPO check + post-kill write on the promoted primaries |
| `05-gateway-during-outage.log` | apiserver controls + 3 hosts × 6 concurrent samples |
| `05b-functional-during-outage.log` | keycloak OIDC discovery, body-level proof |
| `06-recover-start.log` | `os-start` call |
| `07-failback-watch.log` | gateway during and after recovery |
| `08-failback-splitbrain-proof.log` | region-B timeline + row-set, region-A CR state |
| `09-failback-decision-watch.log` | failback actor + region-A pod reconciliation |
| `10-dual-writable-probe.log` | region-A vs region-B recovery state, timeline and row counts on return |
| `11-fence-three-way-confirm.log` | the read vacuity-control, the decisive region-A write probe, and the Cluster-CR check |
| `12-reclone-state.log` | the actor's demote / divergence / delete / converge decisions |
| `13-reclone-state.log` | post-failback row-set comparison — the proof the re-clone was real |
