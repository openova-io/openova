# bp-postgres — reusable, shareable PostgreSQL data-instance

A reusable, shareable **PostgreSQL data-instance** Blueprint. Each install is
**one** `postgresql.cnpg.io/v1.Cluster` — the engine — and a first-class
**App card**.

## Role in Catalyst

- **Application Blueprint (data-instance).** `bp-postgres` is an *instance* of
  the PostgreSQL engine. `bp-cnpg` (the CNPG operator + CRDs) is the engine
  *class* — it stays `visibility: unlisted` and is shown once under
  Platform/engines. The N data-instances (`gitea-pg`, `harbor-pg`, a shared
  `shared-pg`, …) are `bp-postgres` App cards.
- Consumers **share** an instance via the declarative binding model in
  [ADR-0010](../../docs/adr/0010-reusable-shareable-backing-services.md):
  - one CNPG `Database` CR per consumer — an **isolated** database on the
    shared Cluster;
  - one `Cluster.spec.managed.roles[]` entry per consumer — a per-consumer
    login role whose password CNPG reconciles from a Secret;
  - one reflected connection Secret (bp-reflector) into the consumer namespace
    so the consumer chart runs in `externalDatabase` mode;
  - one Flux `dependsOn` edge: the consumer HR `dependsOn` the **instance**
    (`bp-postgres-<name>`), *not* `bp-cnpg`.

**Sharing = N Databases + N roles on one Cluster.** **NO custom controller** and
**NO Crossplane** — CNPG's own `Cluster`/`Database`/`managed.roles` CRDs are the
off-the-shelf declarative primitives, reconciled by Flux + the CNPG operator.
Ref-count = the Flux dependency graph (pruning a depended-on instance is caught
by `dependency-graph-audit`).

## Canonical doc

- [ADR-0010 — Reusable, shareable backing-services model](../../docs/adr/0010-reusable-shareable-backing-services.md)
- [ADR-0004 — CNPG Pillar 3 synchronous replication](../../docs/adr/0004-cnpg-sync-replication.md) (the per-instance DR shape)

## Configuration knobs (`chart/values.yaml`)

| Key | Default | Meaning |
|---|---|---|
| `instance.name` | `postgres` | Cluster name = data-instance identity + `dependsOn` target. |
| `instance.storageSize` | `5Gi` | Per-instance PVC size. |
| `instance.pgVersion` | `16` | PostgreSQL major (CNPG image tag). |
| `topology.mode` | `singleton` | `singleton` or `active-hot-standby`. |
| `topology.instances` | `1` | Instance count (≥2 for cross-region spread). |
| `topology.replication.mode` | `sync` | `sync` (Pillar-3) / `async` (forensic). |
| `databases[]` | `[]` | The bindings — one entry per consumer. |

Each `databases[]` entry: `name` (isolated DB), `owner` (login role),
`passwordSecret` (Secret CNPG reconciles the role password from; empty →
`<instance>-<owner>`), `consumer.{blueprint,mode}` (Consumers-table display), and
`reflect.{secretName,namespaces}` (where to mirror the connection Secret).

## Operational notes

- **Topology.** `active-hot-standby` renders the synchronous-replication shape
  (`synchronous_commit: remote_apply` + `synchronous_standby_names`) per
  ADR-0004 — the Pillar-3 zero-tx-loss bar. This is a property of the
  *instance*, inherited by every consumer that shares it.
- **Blast radius.** A shared engine is one StatefulSet / PVC / failover unit.
  Use a `private` instance for consumers that need isolation; `shared` is opt-in
  for the consumer that declares `instanceRef`.
- **No data loss on prune.** `Database` CRs use `databaseReclaimPolicy: retain`;
  reflected Secrets carry `helm.sh/resource-policy: keep`. Destructive teardown
  is an explicit operator action.
- **Cold-install ordering.** The Cluster + Database templates are gated on the
  `postgresql.cnpg.io/v1` Capabilities check, so a misordered overlay surfaces
  as "no Cluster yet" rather than a hard install failure. The Flux `dependsOn
  bp-cnpg` makes the gate a no-op in practice.

## Render gate

`bash chart/tests/postgres-render.sh` asserts: cold-install (no CRD) → zero
resources; shared render → 1 Cluster + 2 Databases + 2 reflected Secrets + 2
managed roles (the reuse proof); active-hot-standby + sync → the sync GUCs;
singleton → no sync GUCs.
