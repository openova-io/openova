import type { Component, LayerDefinition, PlatformLayer } from '../types';

export const components: Component[] = [
  // Infrastructure & Provisioning
  { name: 'OpenTofu', slug: 'opentofu', purpose: 'Bootstrap IaC (MPL 2.0)', category: 'infrastructure', type: 'core' },
  { name: 'Crossplane', slug: 'crossplane', purpose: 'Day-2 cloud resource provisioning', category: 'infrastructure', type: 'core' },

  // Networking & Service Mesh
  { name: 'Cilium', slug: 'cilium', purpose: 'CNI + Service Mesh (eBPF, mTLS, L7)', category: 'networking', type: 'core' },
  { name: 'Envoy', slug: 'envoy', purpose: 'L7 proxy (embedded in Cilium)', category: 'networking', type: 'core' },
  { name: 'Coraza', slug: 'coraza', purpose: 'WAF (OWASP CRS)', category: 'networking', type: 'core' },
  { name: 'ExternalDNS', slug: 'external-dns', purpose: 'DNS sync to provider', category: 'networking', type: 'core' },
  { name: 'k8gb', slug: 'k8gb', purpose: 'GSLB (authoritative DNS)', category: 'networking', type: 'core' },

  // GitOps & Git
  { name: 'Flux', slug: 'flux', purpose: 'GitOps engine', category: 'gitops', type: 'core' },
  { name: 'Gitea', slug: 'gitea', purpose: 'Internal Git + CI/CD', category: 'gitops', type: 'core' },

  // Security
  { name: 'cert-manager', slug: 'cert-manager', purpose: 'TLS certificates', category: 'security', type: 'core' },
  { name: 'External Secrets', slug: 'external-secrets', purpose: 'Secrets operator', category: 'security', type: 'core' },
  { name: 'OpenBao', slug: 'openbao', purpose: 'Secrets backend (per cluster, MPL 2.0)', category: 'security', type: 'core' },
  { name: 'Trivy', slug: 'trivy', purpose: 'Security scanning', category: 'security', type: 'core' },
  { name: 'Falco', slug: 'falco', purpose: 'Runtime security (eBPF)', category: 'security', type: 'core' },

  // Supply Chain Security
  { name: 'Sigstore', slug: 'sigstore', purpose: 'Container image signing + verification', category: 'supply-chain', type: 'core' },
  { name: 'Syft + Grype', slug: 'syft-grype', purpose: 'SBOM generation + vulnerability matching', category: 'supply-chain', type: 'core' },

  // Policy
  { name: 'Kyverno', slug: 'kyverno', purpose: 'Policy engine (validation, mutation, generation)', category: 'policy', type: 'core' },

  // Scaling
  { name: 'VPA', slug: 'vpa', purpose: 'Vertical autoscaling', category: 'scaling', type: 'core' },
  { name: 'KEDA', slug: 'keda', purpose: 'Event-driven horizontal autoscaling', category: 'scaling', type: 'core' },

  // Operations
  { name: 'Reloader', slug: 'reloader', purpose: 'Auto-restart on ConfigMap/Secret changes', category: 'operations', type: 'core' },

  // Observability
  { name: 'Grafana Stack', slug: 'grafana', purpose: 'Alloy, Loki, Mimir, Tempo, Grafana', category: 'observability', type: 'core' },
  { name: 'OpenTelemetry', slug: 'opentelemetry', purpose: 'Application tracing (auto-instrumentation)', category: 'observability', type: 'core' },
  { name: 'OpenSearch', slug: 'opensearch', purpose: 'Hot SIEM backend', category: 'observability', type: 'core' },

  // Registry
  { name: 'Harbor', slug: 'harbor', purpose: 'Container/artifact registry', category: 'registry', type: 'core' },

  // Storage
  { name: 'MinIO', slug: 'minio', purpose: 'Object storage', category: 'storage', type: 'core' },
  { name: 'Velero', slug: 'velero', purpose: 'Backup/restore', category: 'storage', type: 'core' },

  // Failover
  { name: 'Continuum', slug: 'continuum', purpose: 'Continuous availability orchestration', category: 'failover', type: 'core' },

  // A La Carte - Data
  { name: 'CNPG', slug: 'cnpg', purpose: 'PostgreSQL operator', category: 'data', type: 'alacarte' },
  { name: 'FerretDB', slug: 'ferretdb', purpose: 'MongoDB wire protocol on PostgreSQL', category: 'data', type: 'alacarte' },
  { name: 'Strimzi', slug: 'strimzi', purpose: 'Apache Kafka streaming', category: 'data', type: 'alacarte' },
  { name: 'Valkey', slug: 'valkey', purpose: 'Redis-compatible cache', category: 'data', type: 'alacarte' },
  { name: 'ClickHouse', slug: 'clickhouse', purpose: 'OLAP analytics', category: 'data', type: 'alacarte' },

  // A La Carte - Communication
  { name: 'Stalwart', slug: 'stalwart', purpose: 'Email server (JMAP/IMAP/SMTP)', category: 'communication', type: 'alacarte' },
  { name: 'STUNner', slug: 'stunner', purpose: 'K8s-native TURN/STUN (WebRTC)', category: 'communication', type: 'alacarte' },
  { name: 'LiveKit', slug: 'livekit', purpose: 'Video/audio (WebRTC SFU)', category: 'communication', type: 'alacarte' },
  { name: 'Matrix', slug: 'matrix', purpose: 'Team chat (federation)', category: 'communication', type: 'alacarte' },
  { name: 'Ntfy', slug: 'ntfy', purpose: 'Push notifications (HTTP/SSE/WebSocket)', category: 'communication', type: 'alacarte' },

  // A La Carte - Workflow
  { name: 'Temporal', slug: 'temporal', purpose: 'Saga orchestration', category: 'workflow', type: 'alacarte' },
  { name: 'Flink', slug: 'flink', purpose: 'Stream + batch processing', category: 'workflow', type: 'alacarte' },
  { name: 'Debezium', slug: 'debezium', purpose: 'Change data capture (CDC)', category: 'workflow', type: 'alacarte' },

  // A La Carte - Analytics
  { name: 'Iceberg', slug: 'iceberg', purpose: 'Open table format (data lakehouse)', category: 'analytics', type: 'alacarte' },
  { name: 'Superset', slug: 'superset', purpose: 'BI dashboards and data exploration', category: 'analytics', type: 'alacarte' },

  // A La Carte - AI/ML
  { name: 'KServe', slug: 'kserve', purpose: 'Model serving', category: 'ai-ml', type: 'alacarte' },
  { name: 'Knative', slug: 'knative', purpose: 'Serverless platform', category: 'ai-ml', type: 'alacarte' },
  { name: 'vLLM', slug: 'vllm', purpose: 'LLM inference', category: 'ai-ml', type: 'alacarte' },
  { name: 'Milvus', slug: 'milvus', purpose: 'Vector database', category: 'ai-ml', type: 'alacarte' },
  { name: 'Neo4j', slug: 'neo4j', purpose: 'Graph database', category: 'ai-ml', type: 'alacarte' },
  { name: 'LibreChat', slug: 'librechat', purpose: 'Chat UI', category: 'ai-ml', type: 'alacarte' },
  { name: 'BGE', slug: 'bge', purpose: 'Embeddings + reranking', category: 'ai-ml', type: 'alacarte' },
  { name: 'LLM Gateway', slug: 'llm-gateway', purpose: 'Subscription proxy for Claude Code', category: 'ai-ml', type: 'alacarte' },
  { name: 'Anthropic Adapter', slug: 'anthropic-adapter', purpose: 'OpenAI-to-Anthropic translation', category: 'ai-ml', type: 'alacarte' },

  // A La Carte - AI Safety
  { name: 'NeMo Guardrails', slug: 'nemo-guardrails', purpose: 'AI safety firewall', category: 'ai-safety', type: 'alacarte' },
  { name: 'LangFuse', slug: 'langfuse', purpose: 'LLM observability (traces, cost, eval)', category: 'ai-safety', type: 'alacarte' },

  // A La Carte - Identity & Monetization
  { name: 'Keycloak', slug: 'keycloak', purpose: 'FAPI Authorization Server', category: 'identity', type: 'alacarte' },
  { name: 'OpenMeter', slug: 'openmeter', purpose: 'Usage metering', category: 'identity', type: 'alacarte' },

  // A La Carte - Chaos Engineering
  { name: 'Litmus Chaos', slug: 'litmus', purpose: 'Chaos engineering experiments', category: 'operations', type: 'alacarte' },
];

export const platformLayers: LayerDefinition[] = [
  // Core platform layers
  { id: 'networking', label: 'Networking', color: '#3B82F6', type: 'core', categories: ['networking', 'infrastructure'] },
  { id: 'security', label: 'Security', color: '#EF4444', type: 'core', categories: ['security', 'supply-chain', 'policy'] },
  { id: 'gitops', label: 'GitOps', color: '#8B5CF6', type: 'core', categories: ['gitops'] },
  { id: 'observability', label: 'Observability', color: '#F59E0B', type: 'core', categories: ['observability'] },
  { id: 'storage', label: 'Storage & Registry', color: '#6366F1', type: 'core', categories: ['storage', 'registry', 'failover'] },
  { id: 'scaling', label: 'Scaling & Ops', color: '#14B8A6', type: 'core', categories: ['scaling', 'operations'] },
  // A la carte layers
  { id: 'data', label: 'Data Services', color: '#EC4899', type: 'alacarte', categories: ['data'] },
  { id: 'ai', label: 'AI / ML', color: '#F97316', type: 'alacarte', categories: ['ai-ml', 'ai-safety'] },
  { id: 'communication', label: 'Communication', color: '#06B6D4', type: 'alacarte', categories: ['communication'] },
  { id: 'identity', label: 'Identity', color: '#84CC16', type: 'alacarte', categories: ['identity', 'workflow', 'analytics'] },
];

export function getComponentsByLayer(layerId: PlatformLayer): Component[] {
  const layer = platformLayers.find(l => l.id === layerId);
  if (!layer) return [];
  return components.filter(c => layer.categories.includes(c.category));
}

export const categoryLabels: Record<string, string> = {
  'infrastructure': 'Infrastructure',
  'networking': 'Networking & Service Mesh',
  'gitops': 'GitOps & Git',
  'security': 'Security',
  'supply-chain': 'Supply Chain',
  'policy': 'Policy',
  'scaling': 'Scaling',
  'operations': 'Operations',
  'observability': 'Observability',
  'registry': 'Registry',
  'storage': 'Storage',
  'failover': 'Failover',
  'data': 'Data Services',
  'communication': 'Communication',
  'workflow': 'Workflow & Processing',
  'analytics': 'Analytics',
  'ai-ml': 'AI / ML',
  'ai-safety': 'AI Safety & Observability',
  'identity': 'Identity & Monetization',
};
