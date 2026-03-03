import type { MigrationPath } from '../types';

export const migrations: MigrationPath[] = [
  // Platform
  { from: 'Red Hat OpenShift', to: 'OpenOva (K3s + Cilium + Flux)', challenges: 'Operator compatibility, SCC → Kyverno, Routes → Gateway API', category: 'platform' },
  { from: 'VMware Tanzu', to: 'OpenOva', challenges: 'Container migration, NSX → Cilium, vSphere dependency removal', category: 'platform' },
  { from: 'Amazon EKS / GKE / AKS', to: 'OpenOva (self-hosted)', challenges: 'Cloud service dependency mapping, IAM → Keycloak', category: 'platform' },
  { from: 'Legacy VMs', to: 'OpenOva (containerized)', challenges: 'Application containerization, state management', category: 'platform' },

  // Database
  { from: 'Oracle Database', to: 'CNPG (PostgreSQL)', challenges: 'Schema conversion, PL/SQL → PL/pgSQL', category: 'database' },
  { from: 'Redis Enterprise', to: 'Valkey', challenges: 'Command compatibility (near-complete)', category: 'database' },
  { from: 'Confluent Kafka', to: 'Strimzi (Apache Kafka)', challenges: 'Protocol-compatible, config differences', category: 'database' },
  { from: 'MongoDB Atlas', to: 'FerretDB on CNPG', challenges: 'Wire protocol compatibility, data migration', category: 'database' },
  { from: 'Amazon RDS', to: 'CNPG (PostgreSQL)', challenges: 'WAL streaming setup, connection migration', category: 'database' },

  // Observability
  { from: 'Datadog', to: 'Grafana Stack', savings: '€200-400K', category: 'observability' },
  { from: 'Splunk', to: 'Loki + Grafana', savings: '€150-300K', category: 'observability' },
  { from: 'New Relic', to: 'OTel + Grafana', savings: '€100-250K', category: 'observability' },
  { from: 'Dynatrace', to: 'OTel + Mimir + Tempo + Grafana', savings: '€150-350K', category: 'observability' },

  // Security
  { from: 'Auth0', to: 'Keycloak', savings: '€50-100K', category: 'security' },
  { from: 'Okta', to: 'Keycloak', savings: '€50-150K', category: 'security' },
  { from: 'Prisma Cloud', to: 'Falco + OpenSearch SIEM + Kyverno + Specter', savings: '€100-200K', category: 'security' },
  { from: 'Aqua Security', to: 'Falco + OpenSearch SIEM + Kyverno + Specter', savings: '€80-150K', category: 'security' },

  // CI/CD
  { from: 'GitHub Actions', to: 'Gitea Actions (compatible syntax)', category: 'cicd' },
  { from: 'GitLab CI', to: 'Gitea Actions', category: 'cicd' },
  { from: 'Jenkins', to: 'Gitea Actions', category: 'cicd' },
  { from: 'CircleCI', to: 'Gitea Actions', category: 'cicd' },
];
