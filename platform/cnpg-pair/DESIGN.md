# bp-cnpg-pair — Design Notes (slice C-DB-1)

This file captures the architectural decisions made by slice C-DB-1
of EPIC-6 (#1101). It is the entry point for the **C-DB-3 (1M-row
acceptance test)** implementer and the K-Cont-2 reconciler author.

## Topology

```
            ┌──────────────────────────┐         ┌──────────────────────────┐
            │  Region A: primary       │         │  Region B: replica       │
            │  (e.g. hz-fsn-rtz-prod)  │         │  (e.g. hz-hel-rtz-prod) │
            │                          │         │                          │
            │  CNPG Cluster (primary)  │  WAL    │  CNPG Cluster (replica)  │
            │  3 instances             │ ──────► │  replica.enabled=true    │
            │  R/W                     │  stream │  3 instances, RO         │
            │                          │  over   │                          │
            │  Service: <name>-primary │  mesh   │  failover-readiness Pod  │
            │  (ClusterMesh global)    │         │  flips Ready when lag<T  │
            └──────────────────────────┘         └──────────────────────────┘
                          │                                 │
                          └───────── audit-config ──────────┘
                                       │
                          NATS catalyst.audit (consumed by K-Cont-2 + U-DR-1)
```

## Key decisions

### 1. Replication source-discovery — ClusterMesh global Service alias

The replica's `externalClusters[].connectionParameters.host` resolves
to the same DNS name (`<fullname>-primary-r`) inside both regions.
Cilium ClusterMesh's `service.cilium.io/global: "true"` annotation
makes the Service visible across the mesh; `affinity: local` keeps
the primary region's reads inside the region and falls through to
remote ONLY when no local backend matches the
`cnpg.io/cluster=<primary>, role=primary` selector — which is
exactly the replica region.

Alternative considered: explicit DNS A-records via PDM's lua-record
machinery. Rejected because (1) it's an additional moving part on
the critical replication path, (2) PDM is itself a CNPG consumer
(circular dependency for the management cluster's PDM Postgres),
and (3) ClusterMesh is the ADR-0001 §9 canonical inter-region
transport — replication MUST use it.

**Decision**: ClusterMesh global Service alias. Logged as a new seam
in `01-canonical-seams.md` "Platform" table.

### 2. Lag threshold — `walStreaming.targetLagSeconds` (default 30s)

The failover-readiness probe polls
`SELECT EXTRACT(EPOCH FROM (now() - pg_last_xact_replay_timestamp()))`
on the replica's `-r` Service. When the result drops below the
threshold, `/tmp/ready` is written and the exec readinessProbe
surfaces Ready=True.

Why 30s default:

- Hetzner inter-region (FSN ↔ HEL) RTT is ~10ms; sustained 30s lag
  means either the replica is partitioned, the primary is under
  WAL-flush pressure, or replication is broken altogether.
- Bank-tier RPO target per `docs/ARCHITECTURE.md` §9.6
  is "<5s write disruption" on operator-initiated switchover.
  Pre-checking lag <30s before switchover gives a 25s safety margin.
- For zero-RPO (synchronous_commit=remote_apply) — chart 0.1.2 SHIPS
  this as the default replication mode (`replication.mode: sync`,
  see §7 below). With sync mode the primary's COMMIT blocks until
  the replica has REPLAYED the WAL, so the replica's lag is bounded
  by the round-trip time, not the streaming buffer — the 30s
  threshold then guards against the replica being partitioned (sync
  ACK never returns, primary blocks) or genuinely degraded.

**Per-Application override** is exposed via configSchema; cluster
overlays may bump or lower it.

### 3. Why Continuum (not CNPG) drives switchover

CNPG can promote/demote instances within a single Cluster CR via
`primaryUpdateMethod: switchover`, but CROSS-cluster switchover
(primary CR ↔ replica CR) requires:

- Coordinated annotation flip on both CRs in a single transaction
  (atomic per-Application — Continuum lease-protected).
- DNS failover (lua-record probes via PDM `/v1/commit`) so resolver
  clients converge on the new primary.
- HTTPRoute weight=0 drain on the old primary to prevent in-flight
  writes during the cross-over.
- NATS audit emission for compliance.

CNPG has none of these. It owns the per-Cluster pieces; Continuum
owns the cross-Cluster orchestration. Their boundaries are clean:
this Blueprint emits the Cluster CRs + audit-config; Continuum
reads them.

### 4. audit-config co-location

The audit-config ConfigMap is a NEW seam. Until now, audit subjects
were only declared globally in `platform/nats-jetstream/chart/templates/streams.yaml`
and discovered by consumers via hardcoded subject names. The
cluster-pair pattern needs PER-PAIR audit-types
(`cnpg-pair-replica-promotable`, etc.) so the U-DR-1 UI can filter
the history panel by Application; co-locating the ConfigMap with
the Blueprint that emits the events lets K-Cont-2 + U-DR-1
discover them via label selector
(`catalyst.openova.io/audit-config=true`).

Future bp-*-pair Blueprints follow this pattern.

### 5. Default-OFF + fail-fast

Per Inviolable Principle #1 (target-state shape): the chart ships
the FULL pair (primary + replica + service + probe + audit) — there
is no "single-region only" rendering path. `enabled=false` skips
everything (so platform overlays can include the chart for
linting/validation without provisioning), `enabled=true` renders all
8 resources with strict region/image validation up front.

Per Inviolable Principle #4a: empty `image.tag` triggers
`required` failure at template-render time (NOT at apply-time).
This is the canonical seam for any other CNPG-extending Blueprint —
copy `_helpers.tpl`'s `cnpg-pair.imageRef` shape.

### 6. Single-region → pair migration (sketch)

A future slice (likely under `platform/cnpg-pair/migration/` or
exposed via a Catalyst MigrationJob CRD) will:

1. Take a snapshot of the existing single-region CNPG Cluster.
2. Install bp-cnpg-pair pointing the primary at the existing
   region (CNPG promotes the existing Cluster CR to the
   `<fullname>-primary` name via a CNPG `Backup` + restore-as-
   primary pattern).
3. Bootstrap the replica region from the now-primary's WAL via
   ClusterMesh.

This is OUT OF SCOPE for C-DB-1; documented here so the migration-
slice author has the design context.

### 7. Synchronous replication (Pillar 3 zero-transactions-lost)

Canonical claim per `CLAUDE.md §0` (Pillar 3): two independent CNPG
clusters with ReplicaCluster sync over Cilium ClusterMesh + region-
kill failover with **zero transactions lost**. Asynchronous-streaming
replication CANNOT meet this claim — any in-flight transaction at
primary-kill time is lost.

Chart 0.1.2 ships synchronous replication as the **default** mode:

| Knob | Default | Rendered into primary's `postgresql.parameters` |
|---|---|---|
| `replication.mode` | `sync` | (gates the block below) |
| `replication.sync.commit` | `remote_apply` | `synchronous_commit: "remote_apply"` |
| `replication.sync.numSync` | `1` | `synchronous_standby_names: "FIRST 1 (<replica-cluster-name>)"` |

How the replica is identified to the primary: the replica's
`externalClusters[].connectionParameters` opens a streaming
replication connection whose `application_name` defaults to the
replica Cluster CR's name (`<fullname>-replica`). PostgreSQL's
`synchronous_standby_names` matcher then resolves the entry to the
live walreceiver attached to that connection.

#### Failure modes (documented for operators)

1. **Replica unreachable / partitioned:** primary BLOCKS new
   transactions on COMMIT until the replica's walreceiver returns
   or the operator runs a break-glass downgrade
   (`ALTER SYSTEM SET synchronous_standby_names = ''; SELECT
   pg_reload_conf();`). This is by design — losing availability is
   the price of zero-tx-loss durability. Continuum K-Cont-2's
   switchover path treats a replica-down event as a "freeze writes
   on primary" signal, not a "promote replica" signal (the replica
   may be behind by more than `targetLagSeconds`).
2. **Replica slow / WAL lag spike:** every COMMIT on the primary
   waits for the replica's replay. On Hetzner FSN ↔ HEL (~10 ms
   RTT) the per-commit latency is bounded by the round-trip plus
   the replica's WAL replay time (typically <5 ms for small txs).
   On geographically distant pairs (~100 ms RTT) every commit sees
   that latency. Operators selecting such pairs MUST be aware of
   the latency tax.
3. **Async opt-out:** `replication.mode: async` exists for forensic
   / lab use only; the rendered primary then omits both
   `synchronous_commit` and `synchronous_standby_names` and falls
   back to PostgreSQL's defaults (`synchronous_commit = on`, no
   forced standbys). Production deployments MUST run `sync`.

#### Why `remote_apply` (not `remote_write` or `on`)

`remote_apply` requires the replica to have *replayed* the WAL
before COMMIT returns — meaning a SELECT on the replica
immediately after COMMIT will see the row. This is the bar required
for Pillar 3: if the primary region dies one tick after a
successful COMMIT, the replica already has the data visible and
queryable. `remote_write` (replica received but didn't fsync) allows
a replica-OS crash to lose the tx; `on` is local-fsync-only with no
remote ordering guarantee. Operators MAY relax to `remote_write`
via `replication.sync.commit: remote_write` if their fault model
excludes simultaneous replica-OS crash, but the chart default is
the strongest mode.

#### Bookkeeping

- Companion fix: bp-wordpress-tenant chart (which renders the same
  pair pattern inline for tenant Postgres) carries the identical
  synchronous block, guarded by the same `replication.mode` value
  passed through from the Application spec.
- Continuum K-Cont-2's switchover sequencer does NOT need to know
  about the sync mode — it operates on the Cluster CR
  `replica.enabled` flip; the WAL durability bar is held by the
  primary's postgresql.conf alone.
- Acceptance test (C-DB-3, deferred): the 1M-row + region-kill
  assertion now becomes "count == 1_000_000 EXACTLY on the
  promoted replica" (zero-tx-loss), no longer "within the
  configured async-lag tolerance."

### 8. Split-side rendering — each cluster applies ITS half (chart 0.2.0)

A 2-region Sovereign is two SEPARATE k3s clusters joined by Cilium
ClusterMesh (kom4dc mimics the second region with a second VPC) —
**not** one stretched cluster. Chart ≤0.1.x rendered both Cluster CRs
in a single release on the primary cluster and relied on
`openova.io/region` node-affinity to "schedule each side to its
region". That only works on a stretched cluster: on separate clusters
the replica Cluster CR + failover-readiness Deployment pinned to
region-B labels can NEVER schedule on cluster-A (hw126:
`FailedScheduling 0/4 nodes — affinity requires
openova.io/region=hw-me-east-215-b-rtz-prod`).

Chart 0.2.0 keys the render off `cnpgPair.side`
(primary|replica; `secondary` aliases replica so the bootstrap-kit
can substitute the per-region SOVEREIGN_REGION_ROLE verbatim):

- `side=primary` (cluster-A): primary Cluster CR + the CNPG-managed
  `-primary-mesh` global Service + audit-config ConfigMap (the
  bp-continuum prerequisite probe reads it on the primary cluster) +
  replication-ingress NetworkPolicy + the optional Continuum CR +
  helm-test resources.
- `side=replica` (cluster-B): replica Cluster CR (pg_basebackup +
  externalClusters — the canonical CNPG separate-cluster replica
  pattern, unchanged) + a local `-primary-mesh` Service stub (Cilium
  merges global services BY NAME+NAMESPACE across clusters; without a
  local Service object the externalCluster host is NXDOMAIN on
  cluster-B; its selector matches zero local Pods so traffic crosses
  the mesh) + failover-readiness Deployment + probe NetworkPolicies.

Slot 16b applies the HR on BOTH control planes (the former
SECONDARY_HR_SUSPEND suspend is removed) and the post-mesh #3238 flip
patches SOVEREIGN_ENABLE_CNPG_PAIR onto ALL region Kustomizations —
a primary-only flip would leave cluster-B rendering an empty release
(no replica, no WAL stream).

Cross-cluster prerequisite unchanged from 0.1.x but now explicit: the
replica's streaming auth mounts the primary's `-replication` / `-ca`
Secrets, which must be synced cluster-A → cluster-B (ESO/reflector
per the per-Sovereign overlay).

### 9. Region-B automatic DR promotion — the dr-promoter (chart 0.2.13, #5137)

hw261 region-kill G12 (2026-07-16) exposed the last Pillar-3 gap: the
DR *orchestrator* was single-region. The Continuum controller and its
`dr.openova.io/v1` CR live on region-A (bp-continuum slot 62 is
SECONDARY_HR_SUSPEND'ed on secondaries, and the Continuum CRD ships in
bp-catalyst-platform slot 13, also suspended), so a real region-kill
takes the orchestrator down WITH the region it exists to fail away
from. failover-readiness correctly said "promotable" (#5133); nothing
acted; the operator promoted by hand.

`side=replica` therefore renders a `<fullname>-dr-promoter`
**Deployment** (never a CronJob — #5157: the CronJob scheduler is the
regional control plane and does not reliably resume) with two
containers sharing an emptyDir:

- **signals** (cnpg postgres image, `streaming_replica` client-cert
  auth — the #5133 pattern): polls the LOCAL replica `-rw`. While in
  recovery, a non-`streaming` `pg_stat_wal_receiver` means the WAL
  stream from region-A is dead; the hold clock starts on the
  streaming→down transition and clears on resume/promotion/psql
  failure (fail-safe).
- **actor** (alpine/k8s, kubectl): promotes only when the stream has
  been dead continuously ≥ `autoPromote.primaryDownHoldSeconds` (120s)
  AND failover-readiness is Ready AND the local Cluster CR is still a
  replica AND the HR value is not already promoted. The promotion is
  ONE merge-patch of the region-B HelmRelease DESIRED state
  (`spec.values.cnpgPair.replica.promoted: true` — the #5125-D1 seam,
  byte-identical to the RUNBOOKS §6.1 operator command), never a live
  Cluster-CR patch (hw256 G12: drift-correction reverts those).

**Why acting on a region-local signal is split-brain-safe** (and why
the promoter renders only when `replication.mode=sync`): with
`synchronous_commit=remote_apply` + `FIRST 1` pinned to the
cross-region replica + `dataDurability: required` (#3740), the old
primary CANNOT COMMIT while its sync standby is unreachable — during
the outage/partition nothing committed exists on A that B lacks
(RPO=0), and after recovery the stale primary stays write-fenced (its
walsender is rejected on the diverged timeline) until re-cloned
(#5125 Defect-2). A `k8s-lease` witness cannot arbitrate two SEPARATE
clusters (each side would hold "its own" lease in its own apiserver);
the sync-rep fence is the arbiter that actually holds through a
region-kill. A third-site witness generalizing this (and letting the
full Continuum sequencer — DNS flip, HTTPRoute drain — run from the
survivor) is follow-up on #5137.

### 10. Region-A automatic DR failback — the dr-failback + durable seams (chart 0.2.18, #5245)

hw275 G12 (2026-07-19) proved the DR chain had a forward leg only.
After a clean promote (region-B writable, TL2, latched), `recover
--arm` os-started region-A and nothing converged: region-A resumed its
OLD TL1 primary role (it never knew it was killed), region-B could
never re-stream — a TL2 line cannot follow a TL1 source (walreceiver
`FATAL 55000 "highest timeline 1 of the primary is behind recovery
timeline 2"`, every 5 s, forever) — and the promote latch froze the HR
with no owner for the return leg. Chart 0.2.18 closes it with four
pieces:

**(a) Latch durability — custom field managers.** The observed
mid-recovery re-demote had a precise mechanism: kustomize-controller's
server-side-apply cleanup replaces every field manager matching the
`kubectl` prefix with itself on EACH reconcile of the applying
Kustomization and drops the claimed fields absent from its manifest
(its documented "undo kubectl drift" behavior). The #5219 suspend
latch was written with the default `kubectl-patch` manager, so every
bootstrap-kit reconcile stripped it: on hw275, kustomize Apply
06:55:20 → helm redeployed the reverted values 06:55:23 (release v4 —
the re-demote that manufactured the FATAL-loop state) → the self-heal
re-latched 06:55:27, nineteen times in 30 minutes. Every DR-actor
write now carries `--field-manager=dr-promoter` / `dr-failback` —
managers the cleanup list does not match (live precedent: the
catalyst-api substitute flips persist under manager `catalyst-api`).

**(b) Durable handoff + latch lift — the substitute seam.** The
suspend latch remains the mid-outage mechanism (helm-only; works from
the cached chart artifact with zero kustomize/source dependency), but
it is no longer the end state. Slot 16b now renders
`replica.promoted: ${SOVEREIGN_CNPG_PAIR_PROMOTED:-false}` and
`primary.demoted: ${SOVEREIGN_CNPG_PAIR_DEMOTED:-false}`; once a
promotion is latched+rendered, the promoter patches
`SOVEREIGN_CNPG_PAIR_PROMOTED="true"` onto the bootstrap-kit
Kustomization's `postBuild.substitute` (the catalyst-api
`SOVEREIGN_ENABLE_CNPG_PAIR` precedent — the Kustomization CR is
applied once by cloud-init and never re-applied, so a custom-manager
patch persists), verifies the HR renders `promoted: true` from source,
then UNSUSPENDS. Drift-correction thereafter re-affirms the failed-over
topology; flux is fully reconciled (chart bumps flow again). If the
substitute is ever lost, the per-tick self-heal re-latches.

**(c) TL-ahead re-promote — the #5220-B wedge made actionable.** A
standby whose OWN timeline is strictly higher than its REACHABLE
source's holds a line the source can never supersede: before the
divergence it was the mandatory `remote_apply` sync target (so it had
every acked commit), and after it the lower-TL side is write-fenced by
its unsatisfiable `FIRST 1`. The promoter reads both timelines via
`IDENTIFY_SYSTEM` on replication-protocol connections (the one surface
`streaming_replica` is guaranteed — no `pg_read_all_stats`, the #5239
lesson) and, once the #5220 divergence wedge has held, re-promotes the
TL-ahead standby through the substitute seam. This deliberately
bypasses the #5220 arm gate (a TL-ahead standby can never stream, so
it could never arm) — sound because the timeline arithmetic is a
strictly stronger proof than the streaming heuristic: no link flap or
never-converged pair can manufacture `own-TL > source-TL`. Guards: max
once per pod lifetime, never after `/shared/diverged` (the hw266
runaway guard), wedge must have held `timelineDivergenceHoldSeconds`.

**(d) dr-failback — demote + re-clone region-A onto the promoted
line.** `side=primary` renders a `<fullname>-dr-failback` Deployment
(same #5157 two-container shape) gated on sync mode AND
`crossRegionPeerClusters` (without the peer probe there is no safe
action, so nothing renders). Signals: local cluster WRITABLE on TL n +
the pinned sync standby ABSENT from `pg_stat_replication` (walsender
rows run AS `streaming_replica` — same-user rows are fully visible) +
the peer REACHABLE, WRITABLE and on an EQUAL-OR-HIGHER timeline over
the new `-replica-mesh` global Service (the reverse mirror of
`-primary-mesh`: a CNPG-managed `rw` additional Service on the replica
Cluster + a zero-backend stub on cluster-A). `TL_peer >= TL_local`,
never strict (chart 0.2.19, #5245 hw277): region-A's os-start recovery
can run a LOCAL HA failover that bumps its timeline to the SAME number
as region-B's promote (TL2 == TL2 live-observed — a 57-minute
split-brain under the earlier strict gate). Timeline numbers on two
independently-promoted lines are not ordered lineage, so equality
carries no authority signal; the sync fence is the authority proof,
and the single-actor asymmetry rests on side gating (only cluster-A
renders a dr-failback actor) plus the dr-promoter's anti-flap. A peer
TL strictly BEHIND local is refused loudly (fail-safe, RUNBOOKS §6.1),
and every peer-probe failure reason (NXDOMAIN / unreachable /
in-recovery / TL-unreadable / TL-behind) logs on its transition with a
~5-minute heartbeat while it persists. After the proof holds
`peerAheadHoldSeconds`, the actor flips
`SOVEREIGN_CNPG_PAIR_DEMOTED="true"`, waits for kustomize to render
the demoted shape into the HR (primary Cluster CR as a replica cluster
of region-B: `replica.enabled: true` + `bootstrap.pg_basebackup` from
the `-replica` externalCluster, synchronous block omitted), and then
performs the ONE destructive act: it deletes the stale Cluster CR so
helm re-creates it fresh and `pg_basebackup` clones region-B's current
timeline (PVCs are CNPG-owned and garbage-collect).

*Why this direction, and why it is data-safe*: the same sync fence
that authorizes the forward promote proves region-B's line ⊇ every
ACKED commit region-A ever made — before the kill B was the mandatory
sync target; the dead window adds nothing; post-recovery A is
write-fenced by its unsatisfiable `FIRST 1`. Discarding A's line loses
only never-acked local WAL, which the RPO=0 contract already
discards. The reverse re-clone (B from A) would destroy acknowledged
post-promote writes and is never automatic. *Why not `pg_rewind`*:
CNPG has no cross-cluster rewind trigger, and rewind's fork-point
arithmetic depends on WAL-tail geometry the actor cannot verify; the
basebackup re-clone is deterministic for every geometry (cost: a full
copy — acceptable on the recovery leg, disclosed here). *Bounded
destruction*: delete only the resourceNames-pinned Cluster CR, only
with a <60 s-fresh peer-writable-and-ahead proof at that exact tick,
and NEVER a Cluster created after the failback episode began — an
in-place-demoted stale standby (if the webhook admits the shape change
on the live CR) is re-cloned only via the wedge escalation
(`wedgeHoldSeconds` unable to stream, `creationTimestamp` predating
`dr-failback-started-at`) or, since 0.2.20, the ConsistentSystemID
divergence escalation (same pre-episode bound, see below); a fresh
clone is never deleted.

*Auth*: the rejoin stream and the peer probe present region-A's own
`-primary-replication` client cert. Region-B accepts it because the
replica Cluster pins `certificates.clientCASecret` to the shared
`-primary-ca` (already synced A→B with `ca.key` at the post-mesh flip
— live-verified hw275; CNPG generates `<replica>-replication` from a
BYO client CA when `replicationTLSSecret` is unset, so every existing
consumer keeps its mounts).

*Re-clone convergence (chart 0.2.20, the hw278 residual)*: hw278 G12
live-proved the chain above fires clean and the re-clone ITSELF can
still wedge, silently. Three hardenings: (1) the rejoin stream carries
**no `sslRootCert`** — the dialed server is region-B's replica
Cluster, signed by region-B's own `-replica-ca` which cluster-A does
not hold, and libpq/pgx upgrade `sslmode=require` to verify-ca
semantics whenever a root cert IS supplied (hw278: pgbasebackup
FATAL-looped 5+ hours on x509; the forward stream keeps its pin —
there the CA matches); (2) **`preserve_pki`** strips the Cluster
ownerReferences from the CNPG-issued `-ca`/`-replication`/`-server`
Secrets before every bounded delete — otherwise the delete GC's the CA
and CNPG re-issues a fresh one that region-B's one-shot synced
`clientCASecret` copy can never trust; (3) the actor gains a
**ConsistentSystemID divergence escalation** (CNPG's own
cannot-consistently-follow condition held False for
`failback.divergenceGraceSeconds`, pre-episode-bounded, fresh
writable-peer-guarded — no dependence on a 600 s-contiguous psql wedge
clock that resets on every psql-dark tick), an **episode-marker
self-heal** (a lost `dr-failback-started-at` write silently disabled
every escalation), and a **post-re-clone convergence watch** (phase +
ConsistentSystemID logged every ~30 ticks until streaming) — a wedged
re-clone names itself.

End state after a kill+recover cycle: region-B primary, region-A
streaming replica, both HRs unsuspended, the failed-over topology
rendered from source. The controlled switchback to the original roles
(demote B, restore A as primary — flip both substitutes back) stays a
sovereign-admin action per RUNBOOKS §6.1; automating it needs the
CNPG demotion-token handshake and is follow-up on #5245.

---

## C-DB-3 acceptance test — implementer brief (HARNESS SHIPPED, awaits operator walk)

> **2026-05-20 — Refs #2067 / TBD-V16:** the harness CODE now lives at
> `platform/cnpg-pair/tests/acceptance/`. It is a self-contained Go
> binary (stdlib-only, drives `psql` + `kubectl`) packaged as
> `ghcr.io/openova-io/openova/d31-acceptance:<sha>`. Per CLAUDE.md §0
> the Pillar 3 issue closes **after** the operator runs the harness on
> a fresh 2-region Sovereign with exit-code 0 + screenshots attached
> to #2067 — NOT on merge. The implementer brief below is preserved
> as the canonical test spec the harness's code mirrors.

### Test fixture

- Provision a fresh bp-cnpg-pair on a 2-region Sovereign (test
  scenario — `hz-fsn-rtz-prod` + `hz-hel-rtz-prod` in a non-prod
  namespace).
- Wait for both Cluster CRs to reach `Ready=True`.
- Wait for the failover-readiness probe Pod to flip Ready=True
  (cold-start replication catch-up).

### Test phases

1. **Write 1M rows** to the primary via `psql` against the primary's
   `-rw` Service. Schema: `(id BIGSERIAL PRIMARY KEY, payload BYTEA)`.
   Each row 1KB payload → 1GB total. Use `pg_bench`-style
   parallelism (8 workers × 125k rows each).
2. **Confirm WAL streamed** to the replica: query
   `SELECT count(*) FROM rows` on the replica's `-r` Service;
   assert count == 1_000_000 within 60s of last write.
3. **Kill the primary region**: scale all instances of the primary
   Cluster CR to 0 (simulates region outage; CNPG operator does NOT
   auto-promote because the replica is in a different Cluster CR).
4. **Continuum K-Cont-2 promotes** the replica via the
   replica.enabled flip.
5. **Assert no data loss** (zero-tx-loss bar, chart 0.1.2+):
   query the new primary, assert count == 1_000_000 EXACTLY.
   Synchronous replication (`replication.mode: sync`,
   `synchronous_commit: remote_apply`) guarantees every committed
   tx is replayed on the replica before COMMIT returns; the
   replica-promote path therefore loses zero transactions on
   region-kill. (Async-mode runs of the same fixture document a
   bounded tail loss; sync-mode runs do not.)
6. **Restore the original region**, run the failback path
   (Continuum K-Cont-2 manual-approval gate), assert the original
   primary becomes the new replica.

### Pass criteria (per master-brief acceptance gate)

- 1M rows written to primary, replicated to replica, count
  matches on the new primary post-promote.
- Switchover completes in <60s.
- Write disruption <5s.
- 2× consecutive GREEN qa-loop run on the test (founder rule).

The fixture **lives** under `platform/cnpg-pair/tests/acceptance/`
(harness shipped 2026-05-20); the binary's per-flag operator brief
is `platform/cnpg-pair/tests/acceptance/README.md`. Phases 1-5 of the
plan above are encoded in the harness's `cmd/d31-acceptance/main.go`
(see "Exit codes" in the README for the PASS/FAIL contract). Phase 6
(failback) is a future slice — the harness verifies the forward path
only; the failback path lives in Continuum K-Cont-2's manual-approval
sequencer and gets its own acceptance run when wired.
`chart/tests/cnpg-pair-render.sh` remains the LIGHT chart-render gate
that runs in CI on every push; the heavy region-kill harness above is
operator-invoked on a fresh prov, not in unit CI.
