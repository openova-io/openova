export type CloudProvider = 'hetzner' | 'huawei' | 'oci'
export type RegionRole = 'primary' | 'dr'
export type NodeSize = 'cx22' | 'cx32' | 'cx42' | 'cx52'
export type DeploymentStatus = 'pending' | 'provisioning' | 'healthy' | 'degraded' | 'failed' | 'destroying'

export interface Region {
  id: string
  name: string
  location: string
  flag: string
  role: RegionRole
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

  // Step 2 — Cloud Provider
  provider: CloudProvider | null

  // Step 3 — Credentials
  hetznerToken: string
  credentialValidated: boolean

  // Step 4 — Infrastructure
  regions: Region[]
  controlPlaneSize: NodeSize
  workerSize: NodeSize
  workerCount: number
  haEnabled: boolean

  // Step 5 — Components
  selectedComponents: SelectedComponent[]

  // Meta
  currentStep: number
  completedSteps: number[]
}

export const INITIAL_WIZARD_STATE: WizardState = {
  orgName: '',
  orgDomain: '',
  orgEmail: '',
  provider: null,
  hetznerToken: '',
  credentialValidated: false,
  regions: [],
  controlPlaneSize: 'cx22',
  workerSize: 'cx22',
  workerCount: 0,
  haEnabled: false,
  selectedComponents: [],
  currentStep: 0,
  completedSteps: [],
}
