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
- Bank-tier RPO target per `docs/EPICS-1-6-unified-design.md` §9.6
  is "<5s write disruption" on operator-initiated switchover.
  Pre-checking lag <30s before switchover gives a 25s safety margin.
- For sub-second-RPO scenarios (synchronous_commit), a future
  Blueprint will lower this default — out of scope here.

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

---

## Deferred — C-DB-3 acceptance test plan

C-DB-3 is the 1M-row write + replica-promote acceptance test, NOT
shipped here. The future implementer's brief:

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
5. **Assert no data loss** within RPO: query the new primary,
   assert count == 1_000_000 (no async-replication tail loss
   beyond the configured `targetLagSeconds`).
6. **Restore the original region**, run the failback path
   (Continuum K-Cont-2 manual-approval gate), assert the original
   primary becomes the new replica.

### Pass criteria (per master-brief acceptance gate)

- 1M rows written to primary, replicated to replica, count
  matches on the new primary post-promote.
- Switchover completes in <60s.
- Write disruption <5s.
- 2× consecutive GREEN qa-loop run on the test (founder rule).

The fixture lives under `platform/cnpg-pair/tests/acceptance/`
when authored; `chart/tests/cnpg-pair-render.sh` (this slice) is
the LIGHT chart-render gate that runs in CI on every push.
