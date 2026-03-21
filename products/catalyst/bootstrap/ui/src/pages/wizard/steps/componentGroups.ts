export type Tier = 'mandatory' | 'recommended' | 'optional'

export interface ComponentDef {
  id: string
  name: string
  desc: string
  tier: Tier
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
      { id: 'flux',       name: 'Flux CD',    desc: 'GitOps delivery engine',          tier: 'mandatory' },
      { id: 'crossplane', name: 'Crossplane', desc: 'Cloud CRDs / IaC',               tier: 'mandatory' },
      { id: 'gitea',      name: 'Gitea',      desc: 'Internal Git server',            tier: 'mandatory' },
      { id: 'opentofu',   name: 'OpenTofu',   desc: 'IaC (Terraform fork)',           tier: 'mandatory' },
      { id: 'vcluster',   name: 'vCluster',   desc: 'Virtual cluster isolation layer', tier: 'mandatory' },
    ],
  },
  {
    id: 'spine', productName: 'SPINE', subtitle: 'Networking & Service Mesh',
    description: 'CNI, service mesh, load balancing, WAF, and encrypted VPN connectivity',
    required: true,
    components: [
      { id: 'cilium',       name: 'Cilium',       desc: 'CNI & eBPF service mesh',         tier: 'mandatory' },
      { id: 'coraza',       name: 'Coraza WAF',   desc: 'L7 web application firewall',     tier: 'mandatory' },
      { id: 'external-dns', name: 'External DNS', desc: 'DNS record automation',           tier: 'mandatory' },
      { id: 'envoy',        name: 'Envoy',        desc: 'L7 proxy',                        tier: 'mandatory' },
      { id: 'k8gb',         name: 'k8gb',         desc: 'Global server load balancing',    tier: 'mandatory' },
      { id: 'frpc',         name: 'frpc',         desc: 'Reverse tunnel',                  tier: 'recommended' },
      { id: 'netbird',      name: 'NetBird',      desc: 'Mesh VPN',                        tier: 'mandatory' },
      { id: 'strongswan',   name: 'strongSwan',   desc: 'IPsec gateway',                   tier: 'optional' },
    ],
  },
  {
    id: 'surge', productName: 'SURGE', subtitle: 'Scaling & Resilience',
    description: 'Autoscaling, config-change reloading, and high-availability orchestration',
    required: true,
    components: [
      { id: 'vpa',       name: 'VPA',       desc: 'Vertical pod autoscaling',  tier: 'mandatory' },
      { id: 'keda',      name: 'KEDA',      desc: 'Event-driven autoscaling',  tier: 'mandatory' },
      { id: 'reloader',  name: 'Reloader',  desc: 'Config-change pod reload',  tier: 'mandatory' },
      { id: 'continuum', name: 'Continuum', desc: 'HA orchestration',          tier: 'recommended' },
    ],
  },
  {
    id: 'silo', productName: 'SILO', subtitle: 'Storage & Registry',
    description: 'Multi-protocol distributed storage (S3 / NFS / FUSE / HDFS), backup & DR, and container registry',
    required: true,
    components: [
      { id: 'seaweedfs', name: 'SeaweedFS', desc: 'Multi-protocol distributed storage', tier: 'mandatory' },
      { id: 'velero',    name: 'Velero',    desc: 'Backup & disaster recovery',         tier: 'mandatory' },
      { id: 'harbor',    name: 'Harbor',    desc: 'Container registry',                 tier: 'mandatory' },
    ],
  },
  /* ── SIDE (cross-cutting, always present) ─────────────────────── */
  {
    id: 'guardian', productName: 'GUARDIAN', subtitle: 'Security & Identity',
    description: 'Policy enforcement, secrets vault, certificates, scanning, and identity management',
    required: true,
    components: [
      { id: 'falco',            name: 'Falco',           desc: 'Runtime threat detection',     tier: 'recommended' },
      { id: 'kyverno',          name: 'Kyverno',         desc: 'Policy as code',               tier: 'mandatory' },
      { id: 'trivy',            name: 'Trivy',           desc: 'Vulnerability scanning',       tier: 'recommended' },
      { id: 'syft-grype',       name: 'Syft + Grype',    desc: 'SBOM & CVE analysis',          tier: 'recommended' },
      { id: 'sigstore',         name: 'Sigstore',        desc: 'Supply chain trust',           tier: 'recommended' },
      { id: 'keycloak',         name: 'Keycloak',        desc: 'Identity & access management', tier: 'recommended' },
      { id: 'openbao',          name: 'OpenBao',         desc: 'Secrets vault',                tier: 'mandatory' },
      { id: 'external-secrets', name: 'External Secrets',desc: 'K8s secret sync (ESO)',        tier: 'mandatory' },
      { id: 'cert-manager',     name: 'Cert-Manager',    desc: 'TLS certificate automation',   tier: 'mandatory' },
    ],
  },
  {
    id: 'insights', productName: 'INSIGHTS', subtitle: 'AIOps & Observability',
    description: 'Unified metrics, logs, traces, dashboards, and AI-powered operations',
    required: true,
    components: [
      { id: 'grafana',       name: 'Grafana',       desc: 'Dashboards & alerting',      tier: 'recommended' },
      { id: 'opentelemetry', name: 'OpenTelemetry', desc: 'Unified telemetry pipeline', tier: 'recommended' },
      { id: 'alloy',         name: 'Alloy',         desc: 'Telemetry agent',            tier: 'recommended' },
      { id: 'loki',          name: 'Loki',          desc: 'Log aggregation',            tier: 'recommended' },
      { id: 'mimir',         name: 'Mimir',         desc: 'Metrics store',              tier: 'recommended' },
      { id: 'tempo',         name: 'Tempo',         desc: 'Distributed tracing',        tier: 'recommended' },
      { id: 'opensearch',    name: 'OpenSearch',    desc: 'Search & analytics',         tier: 'recommended' },
      { id: 'litmus',        name: 'Litmus',        desc: 'Chaos engineering',          tier: 'optional' },
      { id: 'openmeter',     name: 'OpenMeter',     desc: 'Usage metering',             tier: 'optional' },
      { id: 'specter',       name: 'Specter',       desc: 'AIOps brain',                tier: 'optional' },
    ],
  },
  /* ── À LA CARTE ───────────────────────────────────────────────── */
  {
    id: 'fabric', productName: 'FABRIC', subtitle: 'Data & Integration',
    description: 'Event streaming, CDC, workflow orchestration, and analytics databases',
    required: false,
    components: [
      { id: 'cnpg',       name: 'CloudNative PG', desc: 'PostgreSQL operator',       tier: 'recommended' },
      { id: 'valkey',     name: 'Valkey',         desc: 'Redis-compatible cache',     tier: 'recommended' },
      { id: 'strimzi',    name: 'Strimzi',        desc: 'Apache Kafka operator',      tier: 'recommended' },
      { id: 'debezium',   name: 'Debezium',       desc: 'Change data capture',        tier: 'recommended' },
      { id: 'flink',      name: 'Apache Flink',   desc: 'Stream processing',          tier: 'optional' },
      { id: 'temporal',   name: 'Temporal',       desc: 'Workflow orchestration',     tier: 'optional' },
      { id: 'clickhouse', name: 'ClickHouse',     desc: 'Analytics database',         tier: 'optional' },
      { id: 'ferretdb',   name: 'FerretDB',       desc: 'MongoDB-compatible DB',      tier: 'optional' },
      { id: 'iceberg',    name: 'Iceberg',        desc: 'Data lakehouse format',      tier: 'optional' },
      { id: 'superset',   name: 'Superset',       desc: 'BI & dashboards',            tier: 'optional' },
    ],
  },
  {
    id: 'cortex', productName: 'CORTEX', subtitle: 'AI & Machine Learning',
    description: 'Model serving, LLM inference, vector search, embeddings, and AI observability',
    required: false,
    components: [
      { id: 'kserve',    name: 'KServe',    desc: 'Model serving platform',      tier: 'mandatory' },
      { id: 'knative',   name: 'Knative',   desc: 'Serverless runtime',          tier: 'optional' },
      { id: 'axon',      name: 'Axon',      desc: 'LLM gateway (SaaS)',          tier: 'recommended' },
      { id: 'neo4j',     name: 'Neo4j',     desc: 'Graph database',              tier: 'optional' },
      { id: 'vllm',      name: 'vLLM',      desc: 'LLM inference engine',        tier: 'optional' },
      { id: 'milvus',    name: 'Milvus',    desc: 'Vector database',             tier: 'optional' },
      { id: 'bge',       name: 'BGE',       desc: 'Embedding model server',      tier: 'optional' },
      { id: 'langfuse',  name: 'LangFuse',  desc: 'LLM observability & tracing', tier: 'optional' },
      { id: 'librechat', name: 'LibreChat', desc: 'AI chat interface',           tier: 'optional' },
    ],
  },
  {
    id: 'relay', productName: 'RELAY', subtitle: 'Communication',
    description: 'Self-hosted email, WebRTC video conferencing, federated messaging, and push notifications',
    required: false,
    components: [
      { id: 'stalwart', name: 'Stalwart', desc: 'SMTP/IMAP/JMAP mail server',   tier: 'recommended' },
      { id: 'livekit',  name: 'LiveKit',  desc: 'WebRTC video & audio',         tier: 'recommended' },
      { id: 'stunner',  name: 'STUNner',  desc: 'Kubernetes TURN/STUN gateway', tier: 'recommended' },
      { id: 'matrix',   name: 'Matrix',   desc: 'Federated messaging',          tier: 'optional' },
      { id: 'ntfy',     name: 'Ntfy',     desc: 'Push notifications',           tier: 'optional' },
    ],
  },
]
