export type CloudProvider = 'hetzner' | 'huawei' | 'oci' | 'aws' | 'azure'
export type NodeSize = 'cx22' | 'cx32' | 'cx42' | 'cx52'
export type DeploymentStatus = 'pending' | 'provisioning' | 'healthy' | 'degraded' | 'failed' | 'destroying'
export type TopologyTemplate = 'triangle' | 'dual' | 'zoned' | 'compact' | 'solo'

export interface Region {
  id: string; code: string; name: string; location: string; countryCode: string; flag: string
}
export interface NodeConfig {
  size: NodeSize; count: number; role: 'control-plane' | 'worker'
}
export interface SelectedComponent {
  id: string; name: string; version: string; category: string; required: boolean; dependencies: string[]
}

export interface WizardState {
  orgName: string; orgDomain: string; orgEmail: string; orgIndustry: string
  orgSize: string; orgHeadquarters: string; orgCompliance: string[]
  topology: TopologyTemplate | null
  regionProviders: Record<number, CloudProvider>
  regionCloudRegions: Record<number, string>
  providerTokens: Partial<Record<CloudProvider, string>>
  providerValidated: Partial<Record<CloudProvider, boolean>>
  provider: CloudProvider | null
  hetznerToken: string
  credentialValidated: boolean
  componentGroups: Record<string, string[]>
  regions: Region[]
  controlPlaneSize: NodeSize; workerSize: NodeSize; workerCount: number; haEnabled: boolean
  selectedComponents: SelectedComponent[]
  currentStep: number; completedSteps: number[]; deploymentId: string | null
}

export const ORG_DEFAULTS = {
  name: 'Acme Financial', domain: 'acme.io', email: 'platform@acme.io',
  industry: 'Financial Services', size: '2,000–10,000', headquarters: 'Frankfurt, Germany',
  compliance: ['PCI DSS', 'ISO 27001'],
}

export const PROVIDER_REGIONS: Record<CloudProvider, { id: string; label: string; location: string }[]> = {
  hetzner: [
    { id: 'fsn1',  label: 'FSN1', location: 'Falkenstein, Germany' },
    { id: 'nbg1',  label: 'NBG1', location: 'Nuremberg, Germany' },
    { id: 'hel1',  label: 'HEL1', location: 'Helsinki, Finland' },
    { id: 'ash',   label: 'ASH',  location: 'Ashburn, USA' },
    { id: 'hil',   label: 'HIL',  location: 'Hillsboro, USA' },
  ],
  huawei: [
    { id: 'eu-west-101',    label: 'eu-west-101',    location: 'Dublin, Ireland' },
    { id: 'eu-west-204',    label: 'eu-west-204',    location: 'Paris, France' },
    { id: 'cn-north-4',     label: 'cn-north-4',     location: 'Beijing, China' },
    { id: 'ap-southeast-1', label: 'ap-southeast-1', location: 'Hong Kong' },
    { id: 'me-east-1',      label: 'me-east-1',      location: 'Riyadh, Saudi Arabia' },
  ],
  oci: [
    { id: 'eu-frankfurt-1', label: 'eu-frankfurt-1', location: 'Frankfurt, Germany' },
    { id: 'eu-amsterdam-1', label: 'eu-amsterdam-1', location: 'Amsterdam, Netherlands' },
    { id: 'us-ashburn-1',   label: 'us-ashburn-1',   location: 'Ashburn, USA' },
    { id: 'us-phoenix-1',   label: 'us-phoenix-1',   location: 'Phoenix, USA' },
    { id: 'ap-singapore-1', label: 'ap-singapore-1', location: 'Singapore' },
  ],
  aws: [
    { id: 'eu-central-1',   label: 'eu-central-1',   location: 'Frankfurt, Germany' },
    { id: 'eu-west-1',      label: 'eu-west-1',      location: 'Dublin, Ireland' },
    { id: 'us-east-1',      label: 'us-east-1',      location: 'N. Virginia, USA' },
    { id: 'us-west-2',      label: 'us-west-2',      location: 'Oregon, USA' },
    { id: 'ap-southeast-1', label: 'ap-southeast-1', location: 'Singapore' },
  ],
  azure: [
    { id: 'westeurope',    label: 'westeurope',    location: 'Amsterdam, Netherlands' },
    { id: 'northeurope',   label: 'northeurope',   location: 'Dublin, Ireland' },
    { id: 'eastus',        label: 'eastus',        location: 'Virginia, USA' },
    { id: 'westus2',       label: 'westus2',       location: 'Washington, USA' },
    { id: 'southeastasia', label: 'southeastasia', location: 'Singapore' },
  ],
}

export const TOPOLOGY_REGION_COUNT: Record<TopologyTemplate, number> = {
  triangle: 3, dual: 2, zoned: 2, compact: 2, solo: 1,
}

export const TOPOLOGY_REGION_LABELS: Record<TopologyTemplate, string[]> = {
  triangle: ['CP Region — MGMT', 'DP Region 1 — DMZ + RTZ', 'DP Region 2 — DMZ + RTZ'],
  dual:     ['Region 1 — MGMT + DMZ + RTZ', 'Region 2 — MGMT + DMZ + RTZ'],
  zoned:    ['Region 1 — DMZ + Core', 'Region 2 — DMZ + Core'],
  compact:  ['Region 1 — Primary', 'Region 2 — Secondary'],
  solo:     ['Region 1 — All Components'],
}

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
  orgName: ORG_DEFAULTS.name, orgDomain: ORG_DEFAULTS.domain, orgEmail: ORG_DEFAULTS.email,
  orgIndustry: ORG_DEFAULTS.industry, orgSize: ORG_DEFAULTS.size,
  orgHeadquarters: ORG_DEFAULTS.headquarters, orgCompliance: [],
  topology: 'triangle' as TopologyTemplate,
  regionProviders: {}, regionCloudRegions: {},
  providerTokens: {}, providerValidated: {},
  provider: null, hetznerToken: '', credentialValidated: false,
  componentGroups: { ...DEFAULT_COMPONENT_GROUPS },
  regions: [], controlPlaneSize: 'cx22', workerSize: 'cx22', workerCount: 0,
  haEnabled: false, selectedComponents: [],
  currentStep: 1, completedSteps: [], deploymentId: null,
}
