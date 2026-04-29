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
   * Per docs/INVIOLABLE-PRINCIPLES.md #4 ("never hardcode") the data here is
   * the single source of truth — StepComponents.tsx and the wizard store
   * read this list, no app-side knowledge of which components imply which.
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
      { id: 'powerdns',     name: 'PowerDNS',     desc: 'Authoritative DNS + DNSSEC + lua-records', tier: 'mandatory',   dependencies: ['cnpg'] },
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
      { id: 'grafana',       name: 'Grafana',       desc: 'Dashboards & alerting',      tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'opentelemetry', name: 'OpenTelemetry', desc: 'Unified telemetry pipeline', tier: 'recommended', dependencies: [] },
      { id: 'alloy',         name: 'Alloy',         desc: 'Telemetry agent',            tier: 'recommended', dependencies: [] },
      { id: 'loki',          name: 'Loki',          desc: 'Log aggregation',            tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'mimir',         name: 'Mimir',         desc: 'Metrics store',              tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'tempo',         name: 'Tempo',         desc: 'Distributed tracing',        tier: 'recommended', dependencies: ['seaweedfs'] },
      { id: 'opensearch',    name: 'OpenSearch',    desc: 'Search & analytics',         tier: 'recommended', dependencies: [] },
      { id: 'litmus',        name: 'Litmus',        desc: 'Chaos engineering',          tier: 'optional',    dependencies: [] },
      { id: 'openmeter',     name: 'OpenMeter',     desc: 'Usage metering',             tier: 'optional',    dependencies: ['cnpg'] },
      { id: 'specter',       name: 'Specter',       desc: 'AIOps brain',                tier: 'optional',    dependencies: [] },
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
      { id: 'kserve',    name: 'KServe',    desc: 'Model serving platform',      tier: 'mandatory', dependencies: [] },
      { id: 'knative',   name: 'Knative',   desc: 'Serverless runtime',          tier: 'optional',  dependencies: [] },
      { id: 'axon',      name: 'Axon',      desc: 'LLM gateway (SaaS)',          tier: 'recommended', dependencies: [] },
      { id: 'neo4j',     name: 'Neo4j',     desc: 'Graph database',              tier: 'optional',  dependencies: [] },
      { id: 'vllm',      name: 'vLLM',      desc: 'LLM inference engine',        tier: 'optional',  dependencies: [] },
      { id: 'milvus',    name: 'Milvus',    desc: 'Vector database',             tier: 'optional',  dependencies: ['seaweedfs'] },
      { id: 'bge',       name: 'BGE',       desc: 'Embedding model server',      tier: 'optional',  dependencies: [] },
      { id: 'langfuse',  name: 'LangFuse',  desc: 'LLM observability & tracing', tier: 'optional',  dependencies: ['cnpg'] },
      { id: 'librechat', name: 'LibreChat', desc: 'AI chat interface',           tier: 'optional',  dependencies: ['cnpg'] },
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
 * Flat catalog index — every component across every group, with the group
 * stamped in for breadcrumb display. The wizard's dependency resolver and
 * the marketplace card grid both read this; the GROUPS shape is preserved
 * for StepReview's grouped summary view.
 */
export interface ComponentEntry extends ComponentDef {
  groupId: string
  groupName: string
  groupSubtitle: string
}

export const ALL_COMPONENTS: ComponentEntry[] = GROUPS.flatMap(g =>
  g.components.map(c => ({
    ...c,
    dependencies: c.dependencies ?? [],
    // Default logo path: vendored SVG keyed by component id.
    // null in the source overrides the default and tells the UI to draw
    // the letter-mark fallback (used for components with no upstream logo).
    logoUrl: c.logoUrl === undefined ? `/component-logos/${c.id}.svg` : c.logoUrl,
    groupId: g.id,
    groupName: g.productName,
    groupSubtitle: g.subtitle,
  })),
)

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

/** All ids of components with `tier: mandatory`. */
export const MANDATORY_COMPONENT_IDS: string[] = ALL_COMPONENTS
  .filter(c => c.tier === 'mandatory')
  .map(c => c.id)

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
