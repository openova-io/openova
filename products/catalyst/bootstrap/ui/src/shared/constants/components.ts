export type ComponentCategory =
  | 'networking'
  | 'security'
  | 'observability'
  | 'storage'
  | 'gitops'
  | 'data'
  | 'scaling'
  | 'registry'
  | 'backup'
  | 'dns'
  | 'iac'

export interface PlatformComponent {
  id: string
  name: string
  description: string
  category: ComponentCategory
  required: boolean
  dependencies: string[]
  version: string
  logoUrl?: string
}

export const PLATFORM_COMPONENTS: PlatformComponent[] = [
  // Mandatory
  { id: 'cilium', name: 'Cilium', description: 'eBPF-based networking, mTLS service mesh & Hubble observability', category: 'networking', required: true, dependencies: [], version: 'v1.16.5' },
  { id: 'flux', name: 'Flux', description: 'GitOps continuous delivery', category: 'gitops', required: true, dependencies: [], version: 'v2.4.0' },
  { id: 'cert-manager', name: 'cert-manager', description: 'Automated TLS certificate management', category: 'security', required: true, dependencies: [], version: 'v1.16.0' },
  { id: 'external-secrets', name: 'External Secrets', description: 'Sync secrets from external providers', category: 'security', required: true, dependencies: [], version: 'v0.10.0' },
  { id: 'kyverno', name: 'Kyverno', description: 'Policy-as-code: auto-generate PDBs, NetworkPolicies', category: 'security', required: true, dependencies: [], version: 'v1.12.0' },
  { id: 'reloader', name: 'Reloader', description: 'Auto-restart pods on ConfigMap/Secret changes', category: 'scaling', required: true, dependencies: [], version: 'v1.0.121' },

  // Optional — Data
  { id: 'cnpg', name: 'CloudNativePG', description: 'Enterprise PostgreSQL operator', category: 'data', required: false, dependencies: [], version: 'v1.24.0' },
  { id: 'valkey', name: 'Valkey', description: 'Redis-compatible in-memory datastore (open-source fork)', category: 'data', required: false, dependencies: [], version: 'v8.0.0' },
  { id: 'ferretdb', name: 'FerretDB', description: 'MongoDB-compatible layer on PostgreSQL', category: 'data', required: false, dependencies: ['cnpg'], version: 'v1.22.0' },
  { id: 'strimzi', name: 'Strimzi / Kafka', description: 'Apache Kafka on Kubernetes', category: 'data', required: false, dependencies: [], version: 'v0.43.0' },

  // Optional — Storage
  { id: 'seaweedfs', name: 'SeaweedFS', description: 'Unified S3 layer — encapsulates cloud archival storage with native hot/warm/cold tiering behind a single in-cluster endpoint', category: 'storage', required: false, dependencies: [], version: '3.71' },
  { id: 'velero', name: 'Velero', description: 'Backup & restore Kubernetes workloads (writes to SeaweedFS; cold tier auto-routes to cloud archival)', category: 'backup', required: false, dependencies: ['seaweedfs'], version: 'v1.14.0' },

  // Optional — Observability
  { id: 'grafana', name: 'Grafana Stack', description: 'Alloy + Loki + Mimir + Tempo + Grafana dashboards', category: 'observability', required: false, dependencies: [], version: 'v11.4.0' },

  // Optional — Secrets
  { id: 'openbao', name: 'OpenBao', description: 'Secrets backend — MPL 2.0 drop-in Vault replacement', category: 'security', required: false, dependencies: [], version: 'v2.1.0' },

  // Optional — DNS
  { id: 'external-dns', name: 'ExternalDNS', description: 'Sync Kubernetes services to DNS providers', category: 'dns', required: false, dependencies: [], version: 'v0.15.0' },

  // Optional — Registry
  { id: 'harbor', name: 'Harbor', description: 'Container registry with scanning & signing', category: 'registry', required: false, dependencies: ['cnpg', 'seaweedfs', 'valkey'], version: 'v2.11.0' },

  // Optional — Scaling
  { id: 'keda', name: 'KEDA', description: 'Event-driven autoscaler + scale-to-zero', category: 'scaling', required: false, dependencies: [], version: 'v2.15.0' },
  { id: 'vpa', name: 'VPA', description: 'Vertical Pod Autoscaler — right-size containers', category: 'scaling', required: false, dependencies: [], version: 'v1.2.1' },

  // Optional — IaC
  { id: 'crossplane', name: 'Crossplane', description: 'Day-2 cloud resource management via CRDs', category: 'iac', required: false, dependencies: [], version: 'v1.17.0' },
  { id: 'gitea', name: 'Gitea', description: 'Internal Git server with bidirectional mirroring', category: 'gitops', required: false, dependencies: ['cnpg'], version: 'v1.22.0' },

  // Optional — Remote access
  { id: 'guacamole', name: 'Apache Guacamole', description: 'Clientless remote-desktop gateway (RDP/VNC/SSH/kubectl-exec via browser, Keycloak SSO, full session recording to SeaweedFS for compliance)', category: 'security', required: false, dependencies: ['cnpg', 'keycloak', 'seaweedfs'], version: '1.5.5' },
]

export const COMPONENT_CATEGORIES: Record<ComponentCategory, { label: string; color: string }> = {
  networking: { label: 'Networking', color: 'text-[--color-info]' },
  security: { label: 'Security', color: 'text-[--color-warning]' },
  observability: { label: 'Observability', color: 'text-[--color-brand-400]' },
  storage: { label: 'Storage', color: 'text-[oklch(75%_0.15_180)]' },
  gitops: { label: 'GitOps', color: 'text-[--color-success]' },
  data: { label: 'Data', color: 'text-[oklch(75%_0.18_300)]' },
  scaling: { label: 'Scaling', color: 'text-[oklch(78%_0.17_60)]' },
  registry: { label: 'Registry', color: 'text-[oklch(72%_0.15_200)]' },
  backup: { label: 'Backup', color: 'text-[oklch(70%_0.12_30)]' },
  dns: { label: 'DNS', color: 'text-[oklch(75%_0.14_160)]' },
  iac: { label: 'IaC', color: 'text-[--color-brand-300]' },
}
