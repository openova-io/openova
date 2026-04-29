/**
 * componentGroups.ts — single source of truth for the platform component
 * catalog rendered by the Sovereign wizard's StepComponents page AND for
 * the **product-family dependency model** that drives cascade selection.
 *
 * ── Dependency model overview ────────────────────────────────────────
 *
 * There are two graphs encoded in this module:
 *
 *   1. **Component graph** — `ComponentDef.dependencies[]`.
 *      "Component X needs component Y" — e.g. Harbor needs cnpg + seaweedfs
 *      + valkey at runtime. Cascading add/remove walks this graph.
 *
 *   2. **Product graph** — `Product.familyDependencies[]`.
 *      "Selecting product P implicitly selects products Q, R" — e.g.
 *      Selecting any CORTEX component pulls in the entire CORTEX family
 *      AND the FABRIC family it relies on at runtime.
 *
 * Per `docs/INVIOLABLE-PRINCIPLES.md` #4 (never hardcode), the data here is
 * the single source of truth — UI code, store actions, vitest fixtures all
 * read this file. There is no "list of mandatory ids" hand-maintained
 * elsewhere; every derived structure is computed from GROUPS / PRODUCTS
 * at module load time.
 *
 * ── Transitive-mandatory promotion (issue #175 fix A) ────────────────
 *
 * If a component X is depended on (directly or transitively) by ANY
 * mandatory-tier component, X itself is promoted to `tier: 'mandatory'`
 * AT MODULE LOAD TIME. This ensures the UI never asks a user to opt into
 * a component that is, in fact, required by something they cannot opt out
 * of (e.g. Harbor requires cnpg → cnpg is mandatory).
 *
 * Promotion is performed in `applyTransitiveMandatoryPromotion()` and the
 * resulting list is exposed as the canonical `ALL_COMPONENTS`. The raw
 * (un-promoted) list is preserved in `RAW_COMPONENTS` for tests.
 *
 * ── Logo path convention (#173) ──────────────────────────────────────
 * Brand SVGs are vendored under
 *   `products/catalyst/bootstrap/ui/public/component-logos/<id>.svg`.
 * The UI is mounted at the Vite `base` path (`/sovereign/` in prod, `/`
 * in dev / test). To stay base-aware without hardcoding `/sovereign/`
 * (INVIOLABLE PRINCIPLE #4) every default logo URL is derived from
 * `path()` in `shared/config/urls.ts`, which itself reads
 * `import.meta.env.BASE_URL`. Change `base` in vite.config and every
 * logo URL follows automatically.
 *
 * `logoUrl: null` disables the SVG and renders the letter-mark fallback
 * (`IconFallback` in StepComponents.tsx). Use this for components with
 * no upstream brand SVG suitable for a square card tile.
 */

import { path as basePath } from '@/shared/config/urls'

export type Tier = 'mandatory' | 'recommended' | 'optional'

export interface ComponentDef {
  id: string
  name: string
  desc: string
  tier: Tier
  /**
   * IDs of components this component requires at runtime. The wizard's
   * dependency-aware selection cascades adds: choosing Harbor automatically
   * pulls in cnpg + seaweedfs + valkey. Removal cascades the other way:
   * removing cnpg also removes any component that lists 'cnpg' here.
   *
   * Mandatory components MUST list dependencies that have to come along —
   * even if those deps are themselves mandatory in another product group.
   * The cascade resolver treats the list as a directed graph and walks
   * transitively, so listing direct deps is sufficient.
   *
   * Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") the data here
   * is the single source of truth — StepComponents.tsx and the wizard
   * store read this list, no app-side knowledge of which components imply
   * which.
   */
  dependencies?: string[]
  /**
   * URL to the brand logo SVG vendored under
   * `products/catalyst/bootstrap/ui/public/component-logos/<id>.svg`.
   * Defaults to `/component-logos/<id>.svg` per id when omitted.
   * `null` means no upstream logo and the wizard will render the
   * letter-mark fallback. Per INVIOLABLE-PRINCIPLES #4 the value is
   * configuration, not code — swap the file under public/ to change
   * the rendered logo without touching application source.
   */
  logoUrl?: string | null
}

export interface GroupDef {
  id: string
  productName: string
  subtitle: string
  description: string
  required: boolean
  components: ComponentDef[]
}

/**
 * Product — a selectable family of components with cross-family
 * dependencies. Every group in GROUPS has a corresponding Product entry
 * with the same `id`. Products are the unit operators reason about
 * ("install CORTEX") even though the actual install primitive is the
 * Blueprint OCI artifact per component.
 *
 * Per INVIOLABLE-PRINCIPLES #4 the data is the source of truth — UI code,
 * store actions, and tests all read PRODUCTS / productByComponent() /
 * componentsByProduct().
 */
export interface Product {
  id: string
  name: string
  subtitle: string
  description: string
  /**
   * Product tier. `mandatory` means the entire product is shipped on every
   * Sovereign and the operator cannot opt out. `recommended` and
   * `optional` surface in Tab 1 ("Choose Your Stack") with a "select
   * entire product" button on the product header.
   */
  tier: Tier
  /** Component ids belonging to this product family (must match GROUPS). */
  components: string[]
  /**
   * When true, picking any single component of this product implicitly
   * selects the entire product (every other component) AND cascades
   * through `familyDependencies`. When false (the default for à-la-carte
   * products like FABRIC and RELAY), members can be picked independently
   * and the operator must use the product header's "Select entire …"
   * CTA to add the whole family.
   *
   * Per operator (issue #175) CORTEX is a product, not just an abstract
   * layer — selecting BGE means selecting the CORTEX family. This flag
   * encodes that semantic without forcing every other product into the
   * same all-or-nothing shape.
   */
  cascadeOnMemberSelection: boolean
  /**
   * Other product ids this product depends on. Selecting any component of
   * THIS product (or the product itself) cascades selecting every
   * component of the dependency products.
   *
   * Example: CORTEX components rely on FABRIC primitives at runtime (cnpg
   * for langfuse / librechat). Therefore selecting any CORTEX component
   * cascades all CORTEX components AND every component of FABRIC, then
   * the component-level cascade adds runtime deps.
   *
   * In practice many cross-product needs are already covered by the
   * component-level dependency graph (e.g. langfuse → cnpg). Family
   * dependencies are reserved for "you can't run product P without
   * product Q at all" relationships.
   */
  familyDependencies: string[]
}

export const GROUPS: GroupDef[] = [
  /* ── CORE ─────────────────────────────────────────────────────── */
  {
    id: 'pilot', productName: 'PILOT', subtitle: 'GitOps & IaC',
    description: 'Continuous delivery engine with GitOps workflows, infrastructure as code, and virtual cluster isolation',
    required: true,
    components: [
      { id: 'flux',       name: 'Flux CD',    desc: 'GitOps delivery engine',          tier: 'mandatory', dependencies: [] },
      { id: 'crossplane', name: 'Crossplane', desc: 'Cloud CRDs / IaC',               tier: 'mandatory', dependencies: [] },
      { id: 'gitea',      name: 'Gitea',      desc: 'Internal Git server',            tier: 'mandatory', dependencies: ['cnpg'] },
      { id: 'opentofu',   name: 'OpenTofu',   desc: 'IaC (Terraform fork)',           tier: 'mandatory', dependencies: [] },
      { id: 'vcluster',   name: 'vCluster',   desc: 'Virtual cluster isolation layer', tier: 'mandatory', dependencies: [] },
    ],
  },
  {
    id: 'spine', productName: 'SPINE', subtitle: 'Networking & Service Mesh',
    description: 'CNI, service mesh, load balancing, WAF, and encrypted VPN connectivity',
    required: true,
    components: [
      { id: 'cilium',       name: 'Cilium',       desc: 'CNI & eBPF service mesh',                                  tier: 'mandatory',   dependencies: [] },
      { id: 'coraza',       name: 'Coraza WAF',   desc: 'L7 web application firewall',                              tier: 'mandatory',   dependencies: [] },
      // PowerDNS (#167) — authoritative DNS for every Sovereign zone, DNSSEC + lua-records.
      // Lua-records (ifurlup, pickclosest, ifportup) cover geo + health-checked failover
      // natively — see docs/MULTI-REGION-DNS.md for the failover patterns.
      // PowerDNS has no single-glyph upstream brand mark suitable for a
      // square card tile — render the letter-mark fallback instead (#173).
      { id: 'powerdns',     name: 'PowerDNS',     desc: 'Authoritative DNS + DNSSEC + lua-records', tier: 'mandatory',   dependencies: ['cnpg'], logoUrl: null },
      { id: 'external-dns', name: 'External DNS', desc: 'DNS record automation',                                    tier: 'mandatory',   dependencies: ['powerdns'] },
      { id: 'envoy',        name: 'Envoy',        desc: 'L7 proxy',                                                 tier: 'mandatory',   dependencies: [] },
      { id: 'frpc',         name: 'frpc',         desc: 'Reverse tunnel',                                           tier: 'recommended', dependencies: [] },
      { id: 'netbird',      name: 'NetBird',      desc: 'Mesh VPN',                                                 tier: 'mandatory',   dependencies: [] },
      { id: 'strongswan',   name: 'strongSwan',   desc: 'IPsec gateway',                                            tier: 'optional',    dependencies: [] },
    ],
  },
  {
    id: 'surge', productName: 'SURGE', subtitle: 'Scaling & Resilience',
    description: 'Autoscaling, config-change reloading, and high-availability orchestration',
    required: true,
    components: [
      { id: 'vpa',       name: 'VPA',       desc: 'Vertical pod autoscaling',  tier: 'mandatory',   dependencies: [] },
      { id: 'keda',      name: 'KEDA',      desc: 'Event-driven autoscaling',  tier: 'mandatory',   dependencies: [] },
      { id: 'reloader',  name: 'Reloader',  desc: 'Config-change pod reload',  tier: 'mandatory',   dependencies: [] },
      { id: 'continuum', name: 'Continuum', desc: 'HA orchestration',          tier: 'recommended', dependencies: [] },
    ],
  },
  {
    id: 'silo', productName: 'SILO', subtitle: 'Storage & Registry',
    description: 'Multi-protocol distributed storage (S3 / NFS / FUSE / HDFS), backup & DR, and container registry',
    required: true,
    components: [
      { id: 'seaweedfs', name: 'SeaweedFS', desc: 'Multi-protocol distributed storage', tier: 'mandatory', dependencies: [] },
      { id: 'velero',    name: 'Velero',    desc: 'Backup & disaster recovery',         tier: 'mandatory', dependencies: ['seaweedfs'] },
      { id: 'harbor',    name: 'Harbor',    desc: 'Container registry',                 tier: 'mandatory', dependencies: ['cnpg', 'seaweedfs', 'valkey'] },
    ],
  },
  /* ── SIDE (cross-cutting, always present) ─────────────────────── */
  {
    id: 'guardian', productName: 'GUARDIAN', subtitle: 'Security & Identity',
    description: 'Policy enforcement, secrets vault, certificates, scanning, and identity management',
    required: true,
    components: [
      { id: 'falco',            name: 'Falco',           desc: 'Runtime threat detection',     tier: 'recommended', dependencies: [] },
      { id: 'kyverno',          name: 'Kyverno',         desc: 'Policy as code',               tier: 'mandatory',   dependencies: [] },
      { id: 'trivy',            name: 'Trivy',           desc: 'Vulnerability scanning',       tier: 'recommended', dependencies: [] },
      { id: 'syft-grype',       name: 'Syft + Grype',    desc: 'SBOM & CVE analysis',          tier: 'recommended', dependencies: [] },
      { id: 'sigstore',         name: 'Sigstore',        desc: 'Supply chain trust',           tier: 'recommended', dependencies: [] },
      { id: 'keycloak',         name: 'Keycloak',        desc: 'Identity & access management', tier: 'recommended', dependencies: ['cnpg'] },
      { id: 'openbao',          name: 'OpenBao',         desc: 'Secrets vault',                tier: 'mandatory',   dependencies: [] },
      { id: 'external-secrets', name: 'External Secrets',desc: 'K8s secret sync (ESO)',        tier: 'mandatory',   dependencies: ['openbao'] },
      { id: 'cert-manager',     name: 'Cert-Manager',    desc: 'TLS certificate automation',   tier: 'mandatory',   dependencies: ['external-dns'] },
    ],
  },
  {
    id: 'insights', productName: 'INSIGHTS', subtitle: 'AIOps & Observability',
    description: 'Unified metrics, logs, traces, dashboards, and AI-powered operations',
    required: true,
    components: [
      // Grafana itself stores users / dashboards / alerts in SQLite (default)
      // or PostgreSQL/MySQL when scaled HA — it does NOT require object
      // storage. Loki / Mimir / Tempo (its companion stores) need seaweedfs;
      // Grafana the dashboard server does not. (audit 2026-04 — was
      // listing seaweedfs as a hard dep, which over-cascaded SILO-internal
      // coupling onto every Grafana selection.)
      { id: 'grafana',       name: 'Grafana',       desc: 'Dashboards & alerting',      tier: 'recommended', dependencies: [] },
      { id: 'opentelemetry', name: 'OpenTelemetry', desc: 'Unified telemetry pipeline', tier: 'recommended', dependencies: [] },
      { id: 'alloy',         name: 'Alloy',         desc: 'Telemetry agent',            tier: 'recommended', dependencies: [] },
      { id: 'loki',          name: 'Loki',          desc: 'Log aggregation',            tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'mimir',         name: 'Mimir',         desc: 'Metrics store',              tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'tempo',         name: 'Tempo',         desc: 'Distributed tracing',        tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'opensearch',    name: 'OpenSearch',    desc: 'Search & analytics',         tier: 'recommended', dependencies: [] },
      { id: 'litmus',        name: 'Litmus',        desc: 'Chaos engineering',          tier: 'optional',    dependencies: [] },
      { id: 'openmeter',     name: 'OpenMeter',     desc: 'Usage metering',             tier: 'optional',    dependencies: ['cnpg'] },
      // Specter — AIOps brain (anomaly + correlation). Per operator's
      // dependency-model feedback (issue #175): Specter requires the
      // entire CORTEX family at runtime — vector store (Milvus),
      // embeddings (BGE), LLM observability (LangFuse), serving (KServe),
      // inference (vLLM). The relationship is encoded BOTH at component
      // level (specter → bge, milvus, langfuse, vllm, kserve) AND at
      // product level (CORTEX is auto-selected when any CORTEX member
      // appears, and Specter's component deps include CORTEX members so
      // selecting Specter triggers the CORTEX product cascade through
      // the store's `addComponent` path). Result: selecting Specter adds
      // the full CORTEX family even if the user never opens the CORTEX
      // chip.
      { id: 'specter',       name: 'Specter',       desc: 'AIOps brain',                tier: 'optional',    dependencies: ['bge', 'milvus', 'langfuse', 'vllm', 'kserve'] },
    ],
  },
  /* ── À LA CARTE ───────────────────────────────────────────────── */
  {
    id: 'fabric', productName: 'FABRIC', subtitle: 'Data & Integration',
    description: 'Event streaming, CDC, workflow orchestration, and analytics databases',
    required: false,
    components: [
      { id: 'cnpg',       name: 'CloudNative PG', desc: 'PostgreSQL operator',       tier: 'recommended', dependencies: [] },
      { id: 'valkey',     name: 'Valkey',         desc: 'Redis-compatible cache',     tier: 'recommended', dependencies: [] },
      { id: 'strimzi',    name: 'Strimzi',        desc: 'Apache Kafka operator',      tier: 'recommended', dependencies: [] },
      { id: 'debezium',   name: 'Debezium',       desc: 'Change data capture',        tier: 'recommended', dependencies: ['strimzi'] },
      { id: 'flink',      name: 'Apache Flink',   desc: 'Stream processing',          tier: 'optional',    dependencies: [] },
      { id: 'temporal',   name: 'Temporal',       desc: 'Workflow orchestration',     tier: 'optional',    dependencies: ['cnpg'] },
      { id: 'clickhouse', name: 'ClickHouse',     desc: 'Analytics database',         tier: 'optional',    dependencies: [] },
      { id: 'ferretdb',   name: 'FerretDB',       desc: 'MongoDB-compatible DB',      tier: 'optional',    dependencies: ['cnpg'] },
      { id: 'iceberg',    name: 'Iceberg',        desc: 'Data lakehouse format',      tier: 'optional',    dependencies: ['seaweedfs'] },
      { id: 'superset',   name: 'Superset',       desc: 'BI & dashboards',            tier: 'optional',    dependencies: ['cnpg'] },
    ],
  },
  {
    id: 'cortex', productName: 'CORTEX', subtitle: 'AI & Machine Learning',
    description: 'Model serving, LLM inference, vector search, embeddings, and AI observability',
    required: false,
    components: [
      // CORTEX members — per operator (issue #175): "BGE alone doesn't
      // have much meaning unless we have Cortex. Cortex is not an
      // abstract layer but also a product we're going to develop, which
      // uses multiple dependencies. Therefore Cortex needs to be a
      // product itself, and when chosen the entire family needs to be
      // selected."
      //
      // Encoded via `addProduct('cortex')` in the wizard store: selecting
      // any CORTEX member triggers the family cascade, adding every
      // remaining CORTEX component and every component of FABRIC (CORTEX's
      // family dependency).
      { id: 'kserve',    name: 'KServe',    desc: 'Model serving platform',      tier: 'mandatory', dependencies: [] },
      { id: 'knative',   name: 'Knative',   desc: 'Serverless runtime',          tier: 'optional',  dependencies: [] },
      { id: 'axon',      name: 'Axon',      desc: 'LLM gateway (SaaS)',          tier: 'recommended', dependencies: [] },
      { id: 'neo4j',     name: 'Neo4j',     desc: 'Graph database',              tier: 'optional',  dependencies: [] },
      { id: 'vllm',      name: 'vLLM',      desc: 'LLM inference engine',        tier: 'optional',  dependencies: [] },
      { id: 'milvus',    name: 'Milvus',    desc: 'Vector database',             tier: 'optional',  dependencies: ['seaweedfs'] },
      { id: 'bge',       name: 'BGE',       desc: 'Embedding model server',      tier: 'optional',  dependencies: [] },
      { id: 'langfuse',  name: 'LangFuse',  desc: 'LLM observability & tracing', tier: 'optional',  dependencies: ['cnpg'] },
      // LibreChat persists conversations / users / presets in MongoDB.
      // OpenOva's MongoDB drop-in is FerretDB (FABRIC), which itself runs
      // on cnpg — so cnpg comes along transitively via FerretDB. The
      // earlier dep `['cnpg']` was wrong: LibreChat does not speak
      // PostgreSQL and would not start with cnpg alone. (audit 2026-04 —
      // confirmed against https://www.librechat.ai/docs/user_guides/mongodb)
      { id: 'librechat', name: 'LibreChat', desc: 'AI chat interface',           tier: 'optional',  dependencies: ['ferretdb'] },
    ],
  },
  {
    id: 'relay', productName: 'RELAY', subtitle: 'Communication',
    description: 'Self-hosted email, WebRTC video conferencing, federated messaging, and push notifications',
    required: false,
    components: [
      { id: 'stalwart', name: 'Stalwart', desc: 'SMTP/IMAP/JMAP mail server',   tier: 'recommended', dependencies: [] },
      { id: 'livekit',  name: 'LiveKit',  desc: 'WebRTC video & audio',         tier: 'recommended', dependencies: [] },
      { id: 'stunner',  name: 'STUNner',  desc: 'Kubernetes TURN/STUN gateway', tier: 'recommended', dependencies: [] },
      { id: 'matrix',   name: 'Matrix',   desc: 'Federated messaging',          tier: 'optional',    dependencies: ['cnpg'] },
      { id: 'ntfy',     name: 'Ntfy',     desc: 'Push notifications',           tier: 'optional',    dependencies: [] },
    ],
  },
]

/**
 * PRODUCTS — the product-family layer. One entry per GroupDef, hand-curated
 * tier and familyDependencies. The product-tier values reflect operator
 * intent (PILOT/SPINE/SURGE/SILO/GUARDIAN ship on every Sovereign;
 * INSIGHTS/FABRIC are recommended; CORTEX/RELAY are optional). The
 * familyDependencies value encodes cross-product needs that aren't
 * obvious from component-level deps alone.
 *
 * See `docs/PRODUCT-FAMILIES.md` for the dependency map and rationale.
 */
export const PRODUCTS: Product[] = [
  {
    id: 'pilot',
    name: 'PILOT',
    subtitle: 'GitOps & IaC',
    description: 'Continuous delivery engine with GitOps workflows, infrastructure as code, and virtual cluster isolation',
    tier: 'mandatory',
    components: ['flux', 'crossplane', 'gitea', 'opentofu', 'vcluster'],
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
  {
    id: 'spine',
    name: 'SPINE',
    subtitle: 'Networking & Service Mesh',
    description: 'CNI, service mesh, load balancing, WAF, and encrypted VPN connectivity',
    tier: 'mandatory',
    components: ['cilium', 'coraza', 'powerdns', 'external-dns', 'envoy', 'frpc', 'netbird', 'strongswan'],
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
  {
    id: 'surge',
    name: 'SURGE',
    subtitle: 'Scaling & Resilience',
    description: 'Autoscaling, config-change reloading, and high-availability orchestration',
    tier: 'mandatory',
    components: ['vpa', 'keda', 'reloader', 'continuum'],
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
  {
    id: 'silo',
    name: 'SILO',
    subtitle: 'Storage & Registry',
    description: 'Multi-protocol distributed storage (S3 / NFS / FUSE / HDFS), backup & DR, and container registry',
    tier: 'mandatory',
    components: ['seaweedfs', 'velero', 'harbor'],
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
  {
    id: 'guardian',
    name: 'GUARDIAN',
    subtitle: 'Security & Identity',
    description: 'Policy enforcement, secrets vault, certificates, scanning, and identity management',
    tier: 'mandatory',
    components: ['falco', 'kyverno', 'trivy', 'syft-grype', 'sigstore', 'keycloak', 'openbao', 'external-secrets', 'cert-manager'],
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
  {
    id: 'insights',
    name: 'INSIGHTS',
    subtitle: 'AIOps & Observability',
    description: 'Unified metrics, logs, traces, dashboards, and AI-powered operations',
    tier: 'recommended',
    components: ['grafana', 'opentelemetry', 'alloy', 'loki', 'mimir', 'tempo', 'opensearch', 'litmus', 'openmeter', 'specter'],
    cascadeOnMemberSelection: false,
    // INSIGHTS as a family does not pull CORTEX — only Specter (a member
    // of INSIGHTS) needs CORTEX, and that's encoded via Specter's
    // component-level deps + CORTEX's family-level cascade. Selecting the
    // INSIGHTS product as a whole brings in Specter, which in turn pulls
    // CORTEX through the component->product cascade chain.
    familyDependencies: [],
  },
  {
    id: 'fabric',
    name: 'FABRIC',
    subtitle: 'Data & Integration',
    description: 'Event streaming, CDC, workflow orchestration, and analytics databases',
    tier: 'recommended',
    components: ['cnpg', 'valkey', 'strimzi', 'debezium', 'flink', 'temporal', 'clickhouse', 'ferretdb', 'iceberg', 'superset'],
    // FABRIC is à-la-carte — Strimzi, Temporal, ClickHouse, Superset are
    // independent stacks operators pick individually. Selecting one
    // doesn't imply the others.
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
  {
    id: 'cortex',
    name: 'CORTEX',
    subtitle: 'AI & Machine Learning',
    description: 'Model serving, LLM inference, vector search, embeddings, and AI observability',
    tier: 'optional',
    components: ['kserve', 'knative', 'axon', 'neo4j', 'vllm', 'milvus', 'bge', 'langfuse', 'librechat'],
    // CORTEX is the operator's archetypal "all or nothing" product (issue
    // #175): "BGE alone doesn't have much meaning unless we have Cortex.
    // [...] when chosen the entire family needs to be selected." So
    // selecting any CORTEX member cascades the rest of the family.
    cascadeOnMemberSelection: true,
    // No family-level dependencies. Audit 2026-04 (issue: "selecting
    // Spector brings the entire fabric family"): the previous
    // `['fabric']` value was over-broad — the only real cross-family
    // need from CORTEX was cnpg (LangFuse) and a Mongo-compatible store
    // (LibreChat). Both are encoded at the COMPONENT level via
    // `dependencies` (langfuse → cnpg, librechat → ferretdb → cnpg) and
    // the only one that's truly always-on (cnpg) is mandatory by
    // transitive promotion. CORTEX has no runtime requirement on
    // Strimzi / Debezium / Flink / Temporal / ClickHouse / Iceberg /
    // Superset, so dragging the entire FABRIC family in when an operator
    // picks Specter or BGE was incorrect.
    familyDependencies: [],
  },
  {
    id: 'relay',
    name: 'RELAY',
    subtitle: 'Communication',
    description: 'Self-hosted email, WebRTC video conferencing, federated messaging, and push notifications',
    tier: 'optional',
    components: ['stalwart', 'livekit', 'stunner', 'matrix', 'ntfy'],
    // RELAY is à-la-carte — Stalwart (mail) and LiveKit (video) are
    // distinct workloads with their own decision criteria.
    cascadeOnMemberSelection: false,
    familyDependencies: [],
  },
]

/**
 * Flat catalog index — every component across every group, with the group
 * stamped in for breadcrumb display. The wizard's dependency resolver and
 * the marketplace card grid both read this; the GROUPS shape is preserved
 * for StepReview's grouped summary view.
 */
export interface ComponentEntry extends ComponentDef {
  groupId: string
  groupName: string
  groupSubtitle: string
  /** Same as groupId — encodes "this component belongs to product X". */
  product: string
}

/**
 * Raw flat list — the catalog before transitive-mandatory promotion.
 * Used by tests to verify that promotion lifted exactly the components
 * reachable from the mandatory set. UI / store code MUST read
 * `ALL_COMPONENTS` instead.
 */
export const RAW_COMPONENTS: ComponentEntry[] = GROUPS.flatMap(g =>
  g.components.map(c => ({
    ...c,
    dependencies: c.dependencies ?? [],
    // Default logo path: vendored SVG keyed by component id, prefixed
    // with the Vite `base` so the URL works behind /sovereign/ (prod)
    // or / (dev / test). `null` in the source overrides the default
    // and tells the UI to draw the letter-mark fallback (components
    // without a vendored SVG). See the `Logo path convention` block at
    // the top of this file for the full rationale.
    logoUrl: c.logoUrl === undefined ? basePath(`component-logos/${c.id}.svg`) : c.logoUrl,
    groupId: g.id,
    groupName: g.productName,
    groupSubtitle: g.subtitle,
    product: g.id,
  })),
)

/**
 * Transitive-mandatory promotion — issue #175 fix A. Walk the dependency
 * graph from every mandatory-tier component; every component reached
 * (directly or transitively) is itself mandatory in the catalog the UI
 * sees. Without this step, cnpg / valkey would surface in Tab 1 ("Choose
 * Your Stack") even though Harbor / Gitea / Keycloak (mandatory) cannot
 * function without them.
 *
 * Implementation note: we mutate the tier on a CLONE of each entry so
 * `RAW_COMPONENTS` keeps the source-of-truth shape (handy for tests).
 * The set of promoted ids is exposed via `TRANSITIVE_MANDATORY_PROMOTIONS`
 * for documentation / debugging.
 */
function applyTransitiveMandatoryPromotion(input: readonly ComponentEntry[]): {
  promoted: ComponentEntry[]
  promotions: string[]
} {
  const byId = new Map(input.map(c => [c.id, c]))
  const seedIds = input.filter(c => c.tier === 'mandatory').map(c => c.id)

  // BFS over the dep graph starting from every mandatory id. Every
  // visited id ends up in `closure` and therefore gets the mandatory
  // tier.
  const closure = new Set<string>(seedIds)
  const queue: string[] = [...seedIds]
  while (queue.length > 0) {
    const next = queue.shift()!
    const entry = byId.get(next)
    if (!entry) continue
    for (const dep of entry.dependencies ?? []) {
      if (!closure.has(dep)) {
        closure.add(dep)
        queue.push(dep)
      }
    }
  }

  const promotions: string[] = []
  const promoted: ComponentEntry[] = input.map(c => {
    if (closure.has(c.id) && c.tier !== 'mandatory') {
      promotions.push(c.id)
      return { ...c, tier: 'mandatory' as Tier }
    }
    return c
  })

  return { promoted, promotions }
}

const _promotionResult = applyTransitiveMandatoryPromotion(RAW_COMPONENTS)

/**
 * Component ids whose tier was lifted from recommended/optional to
 * mandatory by the transitive-closure walk. Stable across loads — the
 * graph is static. Currently: ['cnpg', 'valkey'] (Harbor needs both;
 * Gitea / PowerDNS / Keycloak / Temporal / OpenMeter / FerretDB /
 * Superset / Matrix / LangFuse / LibreChat all share cnpg).
 *
 * Exposed for tests + telemetry; UI code reads `ALL_COMPONENTS` directly.
 */
export const TRANSITIVE_MANDATORY_PROMOTIONS: readonly string[] = _promotionResult.promotions

/**
 * Canonical flat catalog — what the UI / store / tests SHOULD read. Every
 * tier reflects post-promotion classification (so cnpg / valkey are
 * mandatory in this list even though `RAW_COMPONENTS` shows them as
 * recommended).
 */
export const ALL_COMPONENTS: ComponentEntry[] = _promotionResult.promoted

/** Lookup a component by id, or undefined when the id is not in the catalog. */
export function findComponent(id: string): ComponentEntry | undefined {
  return ALL_COMPONENTS.find(c => c.id === id)
}

/** True when the component is `tier: mandatory` somewhere in the catalog. */
export function isMandatory(id: string): boolean {
  return findComponent(id)?.tier === 'mandatory'
}

/**
 * Resolve all transitive dependencies for the given component id. Returns
 * the set of dep ids reachable from `id` via `dependencies` — the id itself
 * is NOT included. Cycles are tolerated (visited set).
 */
export function resolveTransitiveDependencies(id: string): string[] {
  const out = new Set<string>()
  const stack: string[] = [...(findComponent(id)?.dependencies ?? [])]
  while (stack.length > 0) {
    const next = stack.pop()!
    if (out.has(next)) continue
    out.add(next)
    const more = findComponent(next)?.dependencies ?? []
    for (const d of more) {
      if (!out.has(d)) stack.push(d)
    }
  }
  return [...out]
}

/**
 * Reverse lookup — every component id whose `dependencies` list contains
 * the given id (i.e. the components that would break if `id` were removed).
 * The cascade-remove flow uses this to compute the impact set.
 */
export function findDependents(id: string): string[] {
  return ALL_COMPONENTS.filter(c => (c.dependencies ?? []).includes(id)).map(c => c.id)
}

/**
 * Recursively compute every component that (directly or transitively)
 * depends on `id`. Used for cascade-remove confirmation messaging.
 */
export function resolveTransitiveDependents(id: string): string[] {
  const out = new Set<string>()
  const stack: string[] = [...findDependents(id)]
  while (stack.length > 0) {
    const next = stack.pop()!
    if (out.has(next)) continue
    out.add(next)
    for (const d of findDependents(next)) {
      if (!out.has(d)) stack.push(d)
    }
  }
  return [...out]
}

/** All ids of components with `tier: mandatory` (post-promotion). */
export const MANDATORY_COMPONENT_IDS: string[] = ALL_COMPONENTS
  .filter(c => c.tier === 'mandatory')
  .map(c => c.id)

/* ── Product helpers ──────────────────────────────────────────────── */

/** Lookup a product by id. */
export function findProduct(id: string): Product | undefined {
  return PRODUCTS.find(p => p.id === id)
}

/** Product that owns a given component (groupId === product id). */
export function productByComponent(componentId: string): Product | undefined {
  const entry = findComponent(componentId)
  if (!entry) return undefined
  return findProduct(entry.product)
}

/** Component entries belonging to a product, in catalog order. */
export function componentsByProduct(productId: string): ComponentEntry[] {
  const p = findProduct(productId)
  if (!p) return []
  return p.components
    .map(id => findComponent(id))
    .filter((c): c is ComponentEntry => !!c)
}

/**
 * Resolve the transitive set of products implied by selecting `productId`
 * — walks `familyDependencies` and returns every reachable product id
 * (including the seed). Used by `addProduct` cascade.
 */
export function resolveProductFamilyClosure(productId: string): string[] {
  const out = new Set<string>([productId])
  const stack: string[] = [productId]
  while (stack.length > 0) {
    const next = stack.pop()!
    const p = findProduct(next)
    if (!p) continue
    for (const dep of p.familyDependencies) {
      if (!out.has(dep)) {
        out.add(dep)
        stack.push(dep)
      }
    }
  }
  return [...out]
}

/**
 * Compute every component id that selecting `productId` would cascade in:
 *   - every component in the product itself
 *   - every component in every product reached via familyDependencies
 *   - every transitive component-level dependency of those (already
 *     covered, but added for safety)
 *
 * Mandatory components are NOT filtered out — they're already selected,
 * but adding them to the closure is idempotent in the store.
 */
export function resolveProductComponentClosure(productId: string): string[] {
  const productClosure = resolveProductFamilyClosure(productId)
  const components = new Set<string>()
  for (const pid of productClosure) {
    for (const c of componentsByProduct(pid)) {
      components.add(c.id)
      for (const dep of resolveTransitiveDependencies(c.id)) {
        components.add(dep)
      }
    }
  }
  return [...components]
}

/**
 * For an arbitrary component selection, decide whether the entire family
 * is selected (i.e. every component of the product is in `selected`).
 */
export function isProductFullySelected(
  productId: string,
  selected: ReadonlySet<string>,
): boolean {
  const comps = componentsByProduct(productId)
  if (comps.length === 0) return false
  return comps.every(c => selected.has(c.id))
}

/**
 * Default selection: every mandatory component, every recommended
 * component, plus the transitive deps each of them implies. Optional
 * components are off by default (the user opts in).
 */
export function computeDefaultSelection(): string[] {
  const out = new Set<string>()
  for (const c of ALL_COMPONENTS) {
    if (c.tier === 'mandatory' || c.tier === 'recommended') {
      out.add(c.id)
      for (const dep of resolveTransitiveDependencies(c.id)) {
        out.add(dep)
      }
    }
  }
  return [...out]
}
