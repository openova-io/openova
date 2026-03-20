export type CloudProvider = 'hetzner' | 'huawei' | 'oci' | 'aws' | 'azure'
export type NodeSize = 'cx22' | 'cx32' | 'cx42' | 'cx52'
export type DeploymentStatus = 'pending' | 'provisioning' | 'healthy' | 'degraded' | 'failed' | 'destroying'
export type TopologyTemplate = 'titan' | 'triangle' | 'dual' | 'compact' | 'solo'

export interface Region {
  id: string
  code: string
  name: string
  location: string
  countryCode: string
  flag: string
}

export interface NodeConfig {
  size: NodeSize
  count: number
  role: 'control-plane' | 'worker'
}

export interface SelectedComponent {
  id: string
  name: string
  version: string
  category: string
  required: boolean
  dependencies: string[]
}

export interface WizardState {
  // Step 1 — Organization
  orgName: string
  orgDomain: string
  orgEmail: string
  orgIndustry: string
  orgSize: string
  orgHeadquarters: string
  orgCompliance: string[]

  // Step 2 — Topology
  topology: TopologyTemplate | null

  // Step 3 — Cloud Provider
  provider: CloudProvider | null

  // Step 4 — Credentials
  hetznerToken: string
  credentialValidated: boolean

  // Step 5 — Components (groupId → selected component ids)
  componentGroups: Record<string, string[]>

  // Legacy (kept for API compat)
  regions: Region[]
  controlPlaneSize: NodeSize
  workerSize: NodeSize
  workerCount: number
  haEnabled: boolean
  selectedComponents: SelectedComponent[]

  // Meta
  currentStep: number
  completedSteps: number[]
  deploymentId: string | null
}

// These ARE real values used as defaults — not just placeholder text
export const ORG_DEFAULTS = {
  name: 'Acme Financial',
  domain: 'acme.io',
  email: 'platform@acme.io',
  industry: 'Financial Services',
  size: '500–2,000 employees',
  headquarters: 'Frankfurt, Germany',
  compliance: ['PCI DSS', 'ISO 27001'],
}

// Default component group selections (pre-selected = recommended baseline)
export const DEFAULT_COMPONENT_GROUPS: Record<string, string[]> = {
  security:     ['falco', 'kyverno', 'trivy', 'syft-grype', 'coraza', 'sigstore'],
  identity:     ['keycloak', 'openbao', 'external-secrets'],
  networking:   ['cilium', 'cert-manager', 'external-dns'],
  gitops:       ['flux', 'crossplane', 'reloader', 'vpa'],
  observability:['grafana', 'opentelemetry'],
  data:         ['cnpg', 'valkey', 'minio'],
  resilience:   ['velero', 'keda'],
  ai:           [],
  events:       [],
  comms:        [],
}

export const INITIAL_WIZARD_STATE: WizardState = {
  orgName: ORG_DEFAULTS.name,
  orgDomain: ORG_DEFAULTS.domain,
  orgEmail: ORG_DEFAULTS.email,
  orgIndustry: ORG_DEFAULTS.industry,
  orgSize: ORG_DEFAULTS.size,
  orgHeadquarters: ORG_DEFAULTS.headquarters,
  orgCompliance: [...ORG_DEFAULTS.compliance],
  topology: null,
  provider: null,
  hetznerToken: '',
  credentialValidated: false,
  componentGroups: { ...DEFAULT_COMPONENT_GROUPS },
  regions: [],
  controlPlaneSize: 'cx22',
  workerSize: 'cx22',
  workerCount: 0,
  haEnabled: false,
  selectedComponents: [],
  currentStep: 1,
  completedSteps: [],
  deploymentId: null,
}
