# ADR-0010 — Reusable, shareable backing-services model (declarative, no controller, no Crossplane)

Status: Accepted
Date: 2026-06-10

## Context

Today every Postgres-backed Blueprint (`bp-gitea`, `bp-harbor`, `bp-powerdns`,
`bp-newapi`, `bp-openova-flow-server`, `bp-powerdns-admin`, the SME tenant DB,
…) ships its **own** `postgresql.cnpg.io/v1.Cluster` CR inside its chart
(`templates/cnpg-cluster.yaml`). That means:

- **No sharing.** Seven near-identical single-instance CNPG Clusters run on
  every Sovereign, one per consumer, each with its own StatefulSet, PVC,
  superuser, monitoring config. There is no way for two apps to reuse one
  Postgres engine.
- **The dependency edge is wrong.** Every consumer's Flux HR `dependsOn:
  bp-cnpg` — the *operator*, not the data instance it actually uses. The
  console renders "depends on bp-cnpg" which is true-but-useless: it tells the
  operator nothing about *which Postgres* the app is bound to.
- **No first-class data instance.** A Postgres engine isn't an App card; it's an
  implementation detail buried inside each consumer's chart. The operator can't
  see "gitea-pg has 2 consumers" anywhere.

We want a **reusable, shareable backing-services model**: a Postgres *instance*
is a first-class object an operator can see, and N apps can *share* one engine
via isolated databases + per-consumer roles.

Two non-negotiable constraints from the founder:

1. **100% IaC.** Flux + declarative CNPG CRs + Flux `dependsOn`. The binding is
   YAML that Flux applies — never an imperative runtime call.
2. **NO custom controller, NO Crossplane** for in-cluster services. Crossplane
   stays confined to the cloud-infra plane (VPC/ECS/EVS/ELB/OBS). A bespoke
   "binding controller" or a Crossplane Composition for in-cluster DB binding
   is explicitly rejected.

CNPG ≥ 1.29 (live on hw124: 1.29.0) makes this possible **without any
controller** because the two primitives we need are themselves declarative CNPG
CRDs:

- `databases.postgresql.cnpg.io` — a declarative `kind: Database` that the CNPG
  operator reconciles into an isolated database (with owner) on a target
  `Cluster`. (Available on hw124.)
- `Cluster.spec.managed.roles[]` — declarative per-role management (CNPG
  reconciles `CREATE/ALTER ROLE` + password from a Secret on every reconcile).
  (Already used by `bp-gitea` for drift-correction.)

So the entire binding is **pure declarative YAML applied by Flux**. Ref-counting
is the Flux dependency graph itself — `dependency-graph-audit` already fails a PR
that prunes a depended-on instance.

## Decision

Adopt a **3-object model**, all expressed as declarative YAML:

### 1. Data-instance — `bp-postgres` Blueprint

A new `bp-postgres` Blueprint (`category: data`, `multiInstance.enabled: true`,
full `topology` block incl. `active-hot-standby`/`cnpg-pair`). Its chart
templates **one CNPG `Cluster`** plus the per-consumer `Database` CRs, roles, and
reflected connection Secrets driven by `values.yaml`. Each install of
`bp-postgres` = one CNPG engine = one first-class **App card**.

`bp-cnpg` (the operator + CRDs) stays `visibility: unlisted` — it is the engine
*class*, shown once under Platform/engines, never an instance card.

### 2. Binding — pure YAML, NO controller

A consumer binds to a `bp-postgres` instance via, on the instance's Cluster:

- one `kind: Database` per consumer — an **isolated** database on the shared
  Cluster (`spec.cluster.name: <instance>`, `spec.name: <consumer-db>`,
  `spec.owner: <consumer-role>`);
- one `Cluster.spec.managed.roles[]` entry per consumer — a per-consumer login
  role whose password CNPG reconciles from the consumer's app Secret;
- one connection **Secret** reflected into the consumer namespace (bp-reflector
  annotation, or ExternalSecret where the consumer ns differs from the source);
- one Flux `dependsOn` edge: the consumer HR `dependsOn` the **instance** HR
  (`bp-postgres-<name>`), *not* `bp-cnpg`.

**Sharing = N Databases + N roles on one Cluster.** Two consumers on one engine
are two `Database` CRs + two roles — strong logical isolation (separate DB,
separate owner role, no cross-DB visibility) on one shared StatefulSet/PVC.

**Ref-count = the Flux dependency graph.** Pruning an instance that still has a
consumer `dependsOn` edge is caught by `dependency-graph-audit`
(`scripts/check-bootstrap-deps.sh` + `expected-bootstrap-deps.yaml`).

### 3. Consumer declaration → generated binding

A consumer Blueprint declares its need in `spec`:

```yaml
backingServices:
  - type: postgres
    mode: shared          # private | shared
    instanceRef: shared-pg # required when mode=shared
    database: registry     # the isolated DB name
    role: harbor           # the per-consumer login role
```

The **catalyst layer generates** the binding YAML *declaratively* (the
`Database` CR + the `managed.roles` entry on the instance + the reflected Secret
+ the `dependsOn` edge) into the HR/IaC. It is never an imperative runtime call
against a live cluster. `mode: private` generates a dedicated `bp-postgres`
instance; `mode: shared` adds a Database+role to the referenced instance and
points the consumer at the reflected Secret. The consumer chart runs in
`externalDatabase` mode off the injected Secret.

### Why not a controller / Crossplane

- A **binding controller** would re-introduce imperative reconciliation for
  something CNPG already reconciles declaratively (`Database` + `managed.roles`).
  It adds an operator to run, RBAC to grant, and a failure mode (controller down
  ⇒ bindings stop reconciling) that pure-Flux+CNPG does not have.
- **Crossplane** for in-cluster DB binding would put a second control loop
  (Crossplane) in front of CNPG's own control loop, duplicate the
  `Database`/role primitives as XRs/Compositions, and violate the founder rule
  that Crossplane is confined to the cloud-infra plane. Rejected.

The binding is therefore **strictly** `Database` CR + `managed.roles` + reflected
Secret + Flux `dependsOn` — four declarative objects, zero new controllers.

## Consequences

- **Reuse is real.** harbor + gitea can run off ONE `bp-postgres` instance (two
  isolated `Database` CRs + two roles), proven on hw124. Migrating the other
  five consumers is incremental and per-consumer.
- **The dependency edge becomes meaningful.** Consumer `dependsOn` names its
  *instance*; the console renders "Backing services: shared-pg" instead of the
  coarse "depends on bp-cnpg".
- **Operator-visible inventory.** Each engine is an App card with a Consumers
  (bindings) table (app · database · role · mode · secret). The operator can see
  fan-out and reuse at a glance.
- **Migration is two-phase.** Phase 1 (visibility): model the 7 existing single-
  instance Clusters as `bp-postgres` instances 1:1 — *no behavior change*, just
  the card + the corrected dependency edge. Phase 2 (reuse proof): collapse
  harbor + gitea onto one shared instance.
- **Trade-off of sharing:** a shared engine is a shared blast radius (one
  StatefulSet, one PVC, one failover unit). `mode: private` stays the default for
  consumers that need isolation; `mode: shared` is opt-in for the consumer that
  declares `instanceRef`. Per-region DR (`active-hot-standby`) is a property of
  the *instance*, inherited by all its consumers.
- **Pillar 3 unchanged.** A `bp-postgres` instance with `topology:
  active-hot-standby` renders the same cnpg-pair primary/replica shape ADR-0004
  defines; the sync-replication contract is preserved per-instance.

## Scope

- **In scope:** Postgres first. The model generalises to other engines (Redis,
  ClickHouse) later via the same `backingServices[].type`, but only Postgres
  ships here.
- **Out of scope:** Crossplane for in-cluster services (rejected above);
  automatic *consumer* migration from private→shared (operator-initiated,
  declarative re-point); cross-engine bindings.

## Shipped via

- **#3188** — tracking issue (this model, end-to-end on hw124).
- `bp-postgres` Blueprint + chart (CNPG Cluster + Database CRs + managed roles +
  reflected Secrets, multiInstance + topology).
- `spec.backingServices` Blueprint field + the catalyst-layer generator.
- Phase 1 (visibility) — 7 existing Clusters modeled as `bp-postgres` instances.
- Phase 2 (reuse proof) — harbor + gitea on ONE shared `bp-postgres` instance.
- Console: PostgreSQL class card + data-instance App card with Consumers table +
  consumer backing-services row naming its instance.

## See also

- ADR-0004 — CNPG Pillar 3 synchronous replication (the per-instance DR shape).
- ADR-0001 — Catalyst control-plane architecture (Crossplane confined to cloud-
  infra plane).
- `docs/PRINCIPLES.md` — Principle #4 (never hardcode), #2 (no bespoke when off-
  the-shelf exists — CNPG `Database`/`managed.roles` is the off-the-shelf
  primitive).
- `platform/cnpg/` (operator), `platform/cnpg-pair/` (DR pair), `platform/reflector/`.
