# hw292 — G12 region-kill (DoD Pillar-3) walk, 2026-08-03

> Env: `hw292.omani.works`, dep `1c56518035a83e03`, 2-region kom4dc (me-east-215-a / -b),
> `cutoverComplete=true`, charts `bp-cnpg-pair@0.2.23` + `bp-continuum@0.1.12`.
> Kill mechanism: batch HARD `os-stop` of all four region-A ECS via the HCS ECS action API
> (`scripts/region-kill-drill.sh kill --arm`; bastion `bastion-openova` hard-excluded by the
> script's three guard layers). Raw logs in this directory; every timestamp is from the live
> transcript.

## Timeline

| UTC | Event | Measurement |
|---|---|---|
| 19:59:50 / 20:00:13-14 | SEED sentinel row into all four DR pairs (cnpg-pair + shared-pg, -b, -c) on their true primaries | `INSERT 0 1` each |
| 20:00:14 | region-B read-back on all four replicas | all four carry the sentinel — **RPO=0 pre-proof, replicated <5s** |
| **20:01:27.733** | **T0 — KILL**: HARD os-stop of 4 region-A ECS, job `8a868c419f44e007019fc93789037f19` | HTTP 200 |
| 20:01:45 | `dr-promoter/signals`: WAL receiver ABSENT **and** region-A UNREACHABLE (`pg_isready` rc=2) — hold clock started | T0+17s |
| 20:01:50 | region-A apiserver DEAD | T0+22s |
| 20:02:52-20:03:32 | actor logs the hold honestly each 10s (`unreachable 72s/82s/92s/102s/112s < hold 120s — waiting`) | no premature promote |
| **20:03:43** | **PROMOTING** — region-A stream absent AND unreachable 123s ≥ 120s AND failover-readiness Ready; patches HR desired state (#5125-D1 seam, not a live Cluster-CR patch) | T0+136s |
| 20:03:44 | PROMOTION RENDERED (`replica.enabled=false` after 1s) then **LATCHED** (`spec.suspend=true` verified) | flux drift-correction can no longer re-demote (#5178-B / #5218) |
| 20:03:48 | watcher observes `cnpg-pair … pg_is_in_recovery()=f` | region-B writable |
| 20:04:05 | DURABLE HANDOFF — `SOVEREIGN_CNPG_PAIR_PROMOTED=true` written into the bootstrap-kit substitute so the promotion is source-rendered (#5245) | audit annotations on the HR |
| 20:04:12 | RPO + write proof on the promoted primary | sentinel intact; **post-kill write ACCEPTED** (`id=34`) |
| 20:04:37 | gateway probe during outage, 3 hosts + 4 fresh-TCP samples | **503 on every sample** — envoy alive, no healthy backend; the #5244 black-hole criterion EXCLUDED |
| **20:05:52** | RECOVER — `os-start` all four region-A ECS | HTTP 200 |
| 20:06:52 | region-A 4/4 nodes Ready | T_start+60s |
| 20:11:06 | console + api on hw292 | **HTTP 200** — full service restored ≈T0+12min |

## Leg verdicts

| # | Leg | Verdict | Evidence |
|---|---|---|---|
| 1 | RPO=0 — pre-kill row survives the kill | **PASS** | `04-promote-rpo-write.log` — sentinel `19:59:50.340756+00` present on the promoted primary |
| 2 | region-B becomes writable after a HARD kill of region-A | **PASS — and zero-touch** | promoted at T0+136s with **no operator action**; the prior walk needed a manual patch |
| 3 | post-promote write accepted on the new primary | **PASS** | `INSERT 0 1`, `id=34` at 20:04:12 |
| 4 | no split-brain DURING the outage | **PASS** | region-A fully powered off (4/4 ECS stopped, apiserver dead at T0+22s) |
| 5 | gateway survives at the HTTP layer | **PASS** | 503 (not a black hole) on 4 independent fresh-TCP samples; 200 again by 20:11:06 |
| 6 | failback on region-A return leaves no split-brain | **PASS — zero-touch** | demote 20:17:12Z, divergence detected 20:17:23Z, 180s grace, diverged Cluster deleted 20:20:31Z for a clean re-clone, converged 20:21:49Z streaming from region-B with identical row-sets (4=4) |

## The leg-6 measurement, stated precisely

On region-A's return its **local HA** promoted `cnpg-pair-bp-cnpg-pair-primary-2` to a writable
primary on **timeline 2** carrying only the pre-kill row (`n=1`), while region-B stayed writable
on **timeline 2** with the post-kill history (`n=3`). Both sides writable, divergent content.

A first probe of `primary-1` reported `in_recovery=true` streaming from
`cnpg-pair-bp-cnpg-pair-primary-**rw**` — region-A's own local service, not region-B. That reading
is *consistent with* a completed failback and would have produced a false PASS; the pod is
region-A's local replica. The verdict above is taken from region-A's **role=primary** pod.

Then the second correction, in the opposite direction. That reading looks like a live
dual-writable split-brain and would have produced a false **FAIL** — until the write probe:

    ERROR:  cannot execute INSERT in a read-only transaction

with `transaction_read_only=on`, reads succeeding on the same instance (vacuity control), and
region-B accepting the identical write (positive control, `id=36`). Region-A's Cluster CR carries
`replica.enabled=true`: CNPG had already re-rendered it as a **replica cluster**, whose designated
primary is not in recovery but *is* write-fenced. Divergence was never possible.

The actor then closed it out with no operator action:

| UTC | `dr-failback` |
|---|---|
| 20:17:12 | **DEMOTING** region-A — peer writable on TL=2 ≥ local TL=2 held 130s; `SOVEREIGN_CNPG_PAIR_DEMOTED=true` through the durable source seam |
| 20:17:23 | **DIVERGENCE DETECTED** — the in-place-demoted Cluster reports `ConsistentSystemID=False`; the stale-PGDATA line cannot follow region-B; 180s clock started |
| 20:20:31 | **DIVERGENCE ESCALATION** — held 188s ≥ 180s with the peer verified writable; deletes the Cluster for a clean `pg_basebackup` re-clone (PKI preserved) |
| 20:21:49 | **CONVERGED** — region-A streams from `cnpg-pair-bp-cnpg-pair-replica-mesh`, row-sets identical (4 = 4) |

**Leg 6 PASSES.** No data split-brain, and the whole failover-and-return chain ran without a
single operator action.

## Files

| File | Contents |
|---|---|
| `01-seed-baseline.log` | sentinel seed on all four pairs + region-B RPO=0 read-back |
| `02-kill.log` | kill-set (4 ECS, bastion excluded), T0, apiserver-death poll |
| `03-auto-promote-watch.log` | `pg_is_in_recovery()` sampled across all four pairs through the promotion |
| `04-promote-rpo-write.log` | promoter actor log, HR audit annotations, RPO check, post-kill write |
| `05-gateway-during-outage.log` | 3 hosts + 4 fresh-TCP samples during the outage |
| `06-recover-start.log` | `os-start` call |
| `07-failback-watch.log` | node recovery + first (pod-level) recovery sampling |
| `08-failback-splitbrain-proof.log` | replication direction, write-fence probe, timeline + row-set comparison |
| `09-failback-decision-watch.log` | the dr-failback actor's decision window |
| `10-dual-writable-probe.log` | the write probe that refuted the dual-writable reading |
| `11-fence-three-way-confirm.log` | GUC + Cluster CR + region-B positive control |
| `12-reclone-state.log` | the actor's demote / divergence / delete decisions |
| `13-reclone-watch.log` | re-clone to convergence (row-sets 4 = 4) |

## Sub-gap found by this walk — filed, not swept

**#5623** — only `bp-cnpg-pair` ships a `dr-promoter`. The three `bp-postgres` DR pairs
(`shared-pg`, `-b`, `-c`) have no promoter Deployment and stayed `pg_is_in_recovery()=t` for the
whole outage. shared-pg carries keycloak, so the auth path cannot recover in region-B even in
principle. Discovered by walking, not by reading code.
