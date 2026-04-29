# Product Families & Dependency Model

**Status:** Authoritative.
**Source of truth:** `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.ts`.
**Tracking issue:** [#175](https://github.com/openova-io/openova/issues/175).

This document describes the **two-layer dependency model** that governs
how the Sovereign Wizard (`StepComponents`) presents and selects
platform components. It records the operator-driven rationale for each
relationship so future changes don't quietly drop the constraints.

The hard rule, per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md)
#4 (never hardcode): **the only place these relationships are encoded is
`componentGroups.ts`.** This document is human-readable narrative —
derived from the same source. If they disagree, the code wins; update
this file.

---

## Two graphs

There are two dependency graphs in the platform:

### 1. Component graph

`ComponentDef.dependencies[]` — **"component X needs component Y at
runtime."** Cascading add/remove walks this graph: selecting Harbor
pulls in cnpg + seaweedfs + valkey; removing cnpg removes anything that
declared it as a dependency.

This is the well-known case. Every wizard build before #175 used only
this graph.

### 2. Product graph

`Product.familyDependencies[]` — **"product P implies product Q."** Used
when the components of P only make sense in the presence of Q's full
runtime, not just one of Q's primitives. Today **no product declares a
family-level dependency** (every entry in PRODUCTS carries
`familyDependencies: []`). The early shape (CORTEX → FABRIC) was
audited at #0b6bb3ea after operator feedback that "selecting Specter
brings the entire FABRIC family — there is no such dependency in
reality": CORTEX's only true cross-family needs are cnpg (LangFuse
backend) and ferretdb (LibreChat backend), both encoded at the
**component** level and resolved by the transitive-mandatory promotion
walk + the librechat → ferretdb → cnpg dep chain.

A second product-level flag, `Product.cascadeOnMemberSelection: boolean`,
controls whether selecting a single member of the product implies
selecting the entire family. This is the operator's #175
"Cortex-as-product" requirement: selecting BGE selects the rest of
CORTEX. CORTEX is the only product with the flag set today.

---

## Tier classification

| Tier | Semantics | Where it surfaces |
|------|-----------|-------------------|
| `mandatory` | Ships on every Sovereign. Operator cannot opt out. | Tab 2 ("Always Included") |
| `recommended` | Default-on at first wizard run. Operator can opt out. | Tab 1 ("Choose Your Stack") |
| `optional` | Default-off. Operator opts in. | Tab 1 ("Choose Your Stack") |

### Transitive-mandatory promotion (issue #175 fix A)

The catalog applies a **closure walk at module load time**: every
component reachable (via `dependencies[]`) from a `tier: mandatory` seed
is itself promoted to mandatory. This means:

- **cnpg** (recommended in source) → **mandatory** because Harbor /
  Gitea / PowerDNS / Keycloak (mandatory or transitively reached from
  mandatory) all depend on it.
- **valkey** (recommended in source) → **mandatory** because Harbor
  depends on it.

Currently promoted: `['cnpg', 'valkey']`. The list is exposed as
`TRANSITIVE_MANDATORY_PROMOTIONS` for tests and telemetry.

Without promotion, the operator would see cnpg as opt-in in Tab 1 even
though Harbor (mandatory) cannot run without it — a UX bug the operator
called out: *"cnpg was showing in the 'choose your stack' part despite
always-included ones depending on it."*

---

## Product registry

| Product | Tier | Cascade on member? | Family deps | Components |
|---------|------|-------------------|-------------|------------|
| **PILOT** | mandatory | no | — | flux, crossplane, gitea, opentofu, vcluster |
| **SPINE** | mandatory | no | — | cilium, coraza, powerdns, external-dns, envoy, frpc, netbird, strongswan |
| **SURGE** | mandatory | no | — | vpa, keda, reloader, continuum |
| **SILO** | mandatory | no | — | seaweedfs, velero, harbor |
| **GUARDIAN** | mandatory | no | — | falco, kyverno, trivy, syft-grype, sigstore, keycloak, openbao, external-secrets, cert-manager |
| **INSIGHTS** | recommended | no | — | grafana, opentelemetry, alloy, loki, mimir, tempo, opensearch, litmus, openmeter, specter |
| **FABRIC** | recommended | no | — | cnpg, valkey, strimzi, debezium, flink, temporal, clickhouse, ferretdb, iceberg, superset |
| **CORTEX** | optional | **yes** | — | kserve, knative, axon, neo4j, vllm, milvus, bge, langfuse, librechat |
| **RELAY** | optional | no | — | stalwart, livekit, stunner, matrix, ntfy |

`Cascade on member?` controls whether selecting any single component
triggers the full family. Only **CORTEX** has it today, per the
operator's explicit request: *"BGE alone doesn't have much meaning
unless we have Cortex. [...] when chosen the entire family needs to be
selected."*

---

## Cross-product cascade examples

### Selecting **Specter** (in INSIGHTS)

Specter's component-level deps: `[bge, milvus, langfuse, vllm, kserve]`
— all CORTEX members. The cascade resolves as:

1. `addComponent('specter')` selects Specter.
2. Component-deps cascade: bge, milvus, langfuse, vllm, kserve added.
3. The store's product-cascade walk sees that bge belongs to CORTEX,
   and CORTEX has `cascadeOnMemberSelection: true`.
4. Every other CORTEX member added: knative, axon, neo4j, librechat.
5. Component-level deps of the new CORTEX members cascade — only
   `langfuse → cnpg` and `librechat → ferretdb → cnpg` fire; cnpg is
   already mandatory after promotion. **No FABRIC family pull.** The
   audit at #0b6bb3ea explicitly removed CORTEX's prior
   `familyDependencies: ['fabric']` because the runtime needs are
   localised at component-level, not family-level.

Net: selecting one component, Specter, brings in **the entire CORTEX
family** plus only the FABRIC primitives the dependency graph
literally requires (cnpg, ferretdb) — not Strimzi / Debezium / Flink /
Temporal / ClickHouse / Iceberg / Superset.

### Selecting **BGE** (in CORTEX)

1. `addComponent('bge')` selects BGE.
2. BGE has no component-level deps.
3. Product-cascade walk: BGE's product is CORTEX (cascade=true).
4. Every CORTEX member added.
5. Component-level deps of the new CORTEX members cascade as above
   (cnpg + ferretdb only).

Net: selecting BGE = selecting CORTEX + cnpg/ferretdb (the runtime
backends LangFuse / LibreChat actually need). *"BGE alone doesn't have
much meaning unless we have Cortex"* — verified, without the over-broad
FABRIC pull.

### Selecting **Harbor** (in SILO)

1. `addComponent('harbor')` selects Harbor.
2. Component-deps: cnpg, seaweedfs, valkey added (all mandatory after
   promotion, so already selected).
3. Harbor's product is SILO (mandatory). Mandatory products skip the
   cascade — their members are already selected by default.

Net: no new operator-visible additions because Harbor + its deps are
already shipped on every Sovereign.

### Selecting **ClickHouse** (FABRIC, à-la-carte)

1. `addComponent('clickhouse')` selects ClickHouse.
2. ClickHouse has no component-level deps.
3. ClickHouse's product is FABRIC (cascade=**false**).
4. **No family cascade.** ClickHouse, Strimzi, Temporal, Superset are
   independent stacks operators pick individually.

Net: only ClickHouse added. À-la-carte products don't drag the rest of
the family along.

---

## Cascade-remove semantics

Removing a component cascades the **other** way: every component that
listed the removed id as a dependency is also removed (recursive
closure). Mandatory components are protected — `removeComponent('cnpg')`
is a no-op because cnpg is mandatory after promotion.

### Confirmation modal

When the operator removes a component with dependents, the wizard shows
a confirmation modal listing every component that will be cascaded out:

> Remove Strimzi?
>
> Strimzi is used by 1 other component. Removing it will also remove:
>   • Debezium — Change data capture
>
> [Keep] [Remove all]

Modal copy is centralised in `stepComponentsCopy.ts` so translators can
replace it without touching JSX.

### `removeProduct(productId)`

Drops every non-mandatory member of the product, plus every cascading
dependent (mirrors `removeComponent` for each member). Mandatory
members are preserved — KServe (mandatory in CORTEX) survives a
`removeProduct('cortex')` call.

---

## UX surface

### Tab 1: "Choose Your Stack"

- Lists every non-mandatory component in a single flat marketplace
  card grid (no per-family section headers — those were removed at
  #b0ec0c43 because they fragmented the page; the family relationship
  is now surfaced via a clickable family chip on each card that links
  to the dedicated family portfolio page).
- Search field at the top filters by name / description / family.
- Category chips at the top filter to one product family at a time.
- Each card has three click affordances kept distinct so they never
  collide: the family chip routes to the family portfolio page; the
  card body routes to the product detail page; only the explicit
  Select / Selected button toggles the wizard store.

### Tab 2: "Always Included"

- Lists every mandatory component (post-promotion).
- Grouped by owning product so operators see the full family at a
  glance. cnpg/valkey appear under FABRIC even though FABRIC's tier is
  recommended — they're individually mandatory.
- Read-only — no toggle, no selection state.
- "INFRASTRUCTURE" pill instead of "MANDATORY" so the page reads as
  platform infra rather than a wizard option.

### Toasts

- **Cascade-add toast** (component or product family):

  > Selected BGE
  > Also added CORTEX family: Milvus, vLLM, KServe, …

- **Cascade-remove toast**:

  > Strimzi removed
  > Also removed: Debezium

- **Mandatory click toast**:

  > KServe is mandatory
  > Core platform components are always installed.

All toast text comes from `STEP_COMPONENTS_COPY` in
`stepComponentsCopy.ts` — never inline literal in the React component.

---

## Open questions / TODOs

(Items the operator should confirm. Filed against #175.)

- **INSIGHTS family-dependencies** — currently INSIGHTS does not declare
  a family-level dep on CORTEX, even though Specter (an INSIGHTS member)
  relies on CORTEX. We rely on Specter's component-level deps + the
  CORTEX member-selection cascade to do the right thing. **Operator:
  confirm this is the intended shape, or set INSIGHTS.familyDependencies
  to `['cortex']`?**
- **Product-level tier** — INSIGHTS, FABRIC are `recommended`. CORTEX,
  RELAY are `optional`. **Operator: confirm INSIGHTS as recommended (it
  defaults on) vs optional (operator opts in)?**
- **Cross-region replication for transitive-mandatory promotion** —
  PowerDNS depends on cnpg. cnpg is now mandatory. Multi-region
  Sovereigns currently run cnpg per-region; the promotion changes
  nothing operationally but is worth flagging.

---

*Part of [OpenOva](https://openova.io). Read this alongside
[`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) and
[`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md).*
