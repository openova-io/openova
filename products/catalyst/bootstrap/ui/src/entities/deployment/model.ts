export type CloudProvider = 'hetzner' | 'huawei' | 'oci' | 'aws' | 'azure'
export type NodeSize = 'cx22' | 'cx32' | 'cx42' | 'cx52'
export type DeploymentStatus = 'pending' | 'provisioning' | 'healthy' | 'degraded' | 'failed' | 'destroying'
export type TopologyTemplate = 'citadel' | 'triangle' | 'dual' | 'zoned' | 'compact' | 'solo'

export interface Region {
  id: string; code: string; name: string; location: string; countryCode: string; flag: string
}
export interface NodeConfig {
  size: NodeSize; count: number; role: 'control-plane' | 'worker'
}
export interface SelectedComponent {
  id: string; name: string; version: string; category: string; required: boolean; dependencies: string[]
}

export type DomainMode = 'pool' | 'byo'

export interface SovereignPoolDomain {
  /** Pool domain owned by OpenOva (or by a franchised Sovereign owner). Sovereign tenants pick a subdomain under it. */
  id: string
  domain: string
  description: string
}

export interface WizardState {
  orgName: string; orgDomain: string; orgEmail: string; orgIndustry: string
  orgSize: string; orgHeadquarters: string; orgCompliance: string[]
  /** 'pool' = use a shared OpenOva-provided domain like omani.works with a sovereign-chosen subdomain.
   *  'byo'  = customer brings their own domain. */
  sovereignDomainMode: DomainMode
  /** When sovereignDomainMode='pool', which pool domain to use (id, e.g. 'omani-works'). */
  sovereignPoolDomain: string
  /** When sovereignDomainMode='pool', the subdomain the customer types (e.g. 'omantel' → omantel.omani.works). */
  sovereignSubdomain: string
  /** When sovereignDomainMode='byo', the full domain the customer brings (e.g. 'sovereign.acme-bank.com'). */
  sovereignByoDomain: string
  topology: TopologyTemplate | null
  regionProviders: Record<number, CloudProvider>
  regionCloudRegions: Record<number, string>
  providerTokens: Partial<Record<CloudProvider, string>>
  providerValidated: Partial<Record<CloudProvider, boolean>>
  provider: CloudProvider | null
  hetznerToken: string
  /** Hetzner project ID — captured at the credentials step alongside the API token. */
  hetznerProjectId: string
  credentialValidated: boolean
  /**
   * SSH public key the OpenTofu module passes to the Hetzner API as the
   * `hcloud_ssh_key` resource attached to every server. Captured by the
   * StepCredentials SSH section in one of two modes:
   *
   *   • Mode A — auto-generate: catalyst-api emits an Ed25519 keypair, the
   *     wizard captures the public half here and triggers a one-time
   *     download of the private half.
   *   • Mode B — paste existing: operator pastes a single OpenSSH
   *     authorized_keys-style line; the regex below accepts ed25519, RSA,
   *     and the three nistp ECDSA variants.
   *
   * Per docs/INVIOLABLE-PRINCIPLES.md security floor (issue #160), the
   * provisioner rejects an empty value at apply time. The wizard's Next
   * button must therefore stay disabled until this string is populated.
   */
  sshPublicKey: string
  /** UI-only flag — true once a Mode-A generation has completed (used to
   *  show the one-time "private key shown once" warning). Not persisted
   *  across page reloads on purpose: a reload should re-prompt the user
   *  rather than reuse a key whose private half is already in their
   *  Downloads folder. */
  sshKeyGeneratedThisSession: boolean
  /** Mode A only — the private key blob the catalyst-api returned, held
   *  in memory just long enough for the user to click "Download .pem"
   *  again if the auto-trigger was blocked. Cleared as soon as a fresh
   *  paste replaces it. */
  sshPrivateKeyOnce: string
  /** SHA256 fingerprint of the public key — populated for both modes
   *  (computed server-side in Mode A; left empty in Mode B since we don't
   *  ship a JS hashing library just for the wizard preview). Shown in the
   *  Review step so operators can sanity-check what they're about to
   *  apply. */
  sshFingerprint: string
  componentGroups: Record<string, string[]>
  componentsAppliedForProfile: string | null
  /**
   * Selected Blueprints from the unified marketplace card grid in
   * StepComponents. Each entry is a full Blueprint id (e.g. "bp-wordpress").
   * Filtered by `visibility: 'listed'` — mandatory infra Blueprints are
   * `unlisted` and never appear in this list (they're auto-installed by the
   * bootstrap kit, regardless of the wizard user's choice). Per
   * docs/INVIOLABLE-PRINCIPLES.md #2, every Application is the same
   * `bp-<name>` shape regardless of category — no special cases per category.
   */
  selectedBlueprints: string[]
  regions: Region[]
  controlPlaneSize: NodeSize; workerSize: NodeSize; workerCount: number; haEnabled: boolean
  selectedComponents: SelectedComponent[]
  airgap: boolean
  currentStep: number; completedSteps: number[]; deploymentId: string | null
  /**
   * Provisioner result captured by StepProvisioning when the SSE stream
   * emits the terminal `event: done` with a result payload. Consumed by
   * StepSuccess to render the Sovereign's console URL, control-plane IP,
   * load-balancer IP, and GitOps repo URL. Null until provisioning finishes.
   */
  lastProvisionResult: ProvisionResult | null
}

export interface ProvisionResult {
  sovereignFQDN: string
  controlPlaneIP: string
  loadBalancerIP: string
  consoleURL: string
  gitopsRepoURL: string
}

export const ORG_DEFAULTS = {
  name: 'Acme Financial', domain: 'acme.io', email: 'platform@acme.io',
  industry: 'Financial Services', size: '2,000–10,000', headquarters: 'Frankfurt, Germany',
  compliance: ['PCI DSS', 'ISO 27001'],
}

/**
 * Pool domains a Sovereign tenant can pick when sovereignDomainMode='pool'.
 *
 * The first entry, omani.works, is OpenOva's primary pool domain — registered in the
 * Dynadot account managed by the dynadot-api-credentials K8s secret in openova-system
 * (which is account-scoped, so the same API key covers all OpenOva-owned domains).
 *
 * When a tenant picks a pool domain, the provisioner backend writes a CNAME or A record
 * for `<sovereignSubdomain>.<pool-domain>` pointing at the new Sovereign's load balancer
 * IP, and cert-manager handles TLS via Let's Encrypt DNS-01 against the same Dynadot
 * account.
 *
 * Future entries to this list represent franchise-acquired or partner-provided pool
 * domains that the Catalyst-Zero administrators expose to wizard users.
 */
export const SOVEREIGN_POOL_DOMAINS: SovereignPoolDomain[] = [
  {
    id: 'omani-works',
    domain: 'omani.works',
    description: 'OpenOva-provided pool — first franchised Sovereigns and SME marketplace tenants. DNS managed via Dynadot.',
  },
]

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
  citadel: 4, triangle: 3, dual: 2, zoned: 2, compact: 2, solo: 1,
}

export const TOPOLOGY_REGION_LABELS: Record<TopologyTemplate, string[]> = {
  citadel:  ['CP Region 1 — MGMT', 'CP Region 2 — MGMT', 'DP Region 1 — DMZ + RTZ', 'DP Region 2 — DMZ + RTZ'],
  triangle: ['CP Region — MGMT', 'DP Region 1 — DMZ + RTZ', 'DP Region 2 — DMZ + RTZ'],
  dual:     ['Region 1 — MGMT + DMZ + RTZ', 'Region 2 — MGMT + DMZ + RTZ'],
  zoned:    ['Region 1 — DMZ / RTZ / MGMT', 'Region 2 — DMZ / RTZ / MGMT'],
  compact:  ['Region 1 — Primary', 'Region 2 — Secondary'],
  solo:     ['Region 1 — All Components'],
}

/** Base defaults: all M + all R in required blocks; optional blocks empty */
export const DEFAULT_COMPONENT_GROUPS: Record<string, string[]> = {
  pilot:    ['flux', 'crossplane', 'gitea', 'opentofu', 'vcluster'],
  spine:    ['cilium', 'coraza', 'external-dns', 'envoy', 'k8gb', 'frpc', 'netbird'],
  surge:    ['vpa', 'keda', 'reloader', 'continuum'],
  silo:     ['seaweedfs', 'velero', 'harbor'],
  guardian: ['kyverno', 'openbao', 'external-secrets', 'cert-manager', 'falco', 'trivy', 'syft-grype', 'sigstore', 'keycloak'],
  insights: ['grafana', 'opentelemetry', 'alloy', 'loki', 'mimir', 'tempo', 'opensearch'],
  fabric:   [],
  cortex:   [],
  relay:    [],
}

/**
 * Profile-based defaults — adjusts optional block recommendations based on
 * org industry, compliance requirements, and size.
 */
export function getProfileDefaults(
  orgIndustry: string,
  orgCompliance: string[],
  orgSize: string,
): Record<string, string[]> {
  const ind  = orgIndustry.toLowerCase()
  const comp = orgCompliance.map(c => c.toLowerCase())

  const isFinancial  = /financ|bank|insur|fintech/.test(ind)
  const isHealthcare = /health|pharma|life sci|medical/.test(ind)
  const isTech       = /tech|software|saas|cloud|it services/.test(ind)
  const isRetail     = /retail|commerce|ecomm/.test(ind)
  const isLarge      = /10[,.]?000|50[,.]?000|100[,.]?000/.test(orgSize)
  const hasAuditComp = comp.some(c => ['pci dss','hipaa','soc 2','iso 27001','gdpr'].some(r => c.includes(r)))

  const defaults: Record<string, string[]> = { ...DEFAULT_COMPONENT_GROUPS }

  // FABRIC: data-heavy and regulated industries
  if (isFinancial || isHealthcare || isRetail || isLarge || comp.includes('gdpr')) {
    defaults.fabric = ['cnpg', 'valkey', 'strimzi', 'debezium']
  }

  // CORTEX: AI/tech companies and large enterprises
  if (isTech || isLarge) {
    defaults.cortex = ['kserve', 'knative', 'axon']
  }

  // RELAY: keep opt-in; no profile auto-enables it

  // openmeter for usage billing (financial, SaaS, retail)
  if (isFinancial || isTech || isRetail) {
    defaults.insights = [...defaults.insights, 'openmeter']
  }

  // strongSwan IPsec: compliance-heavy or financial
  if (isFinancial || isHealthcare || hasAuditComp) {
    defaults.spine = [...defaults.spine, 'strongswan']
  }

  return defaults
}

export const INITIAL_WIZARD_STATE: WizardState = {
  orgName: ORG_DEFAULTS.name, orgDomain: ORG_DEFAULTS.domain, orgEmail: ORG_DEFAULTS.email,
  orgIndustry: ORG_DEFAULTS.industry, orgSize: ORG_DEFAULTS.size,
  orgHeadquarters: ORG_DEFAULTS.headquarters, orgCompliance: [],
  // Sovereign domain — defaults to the OpenOva-provided pool. Customer can switch to BYO
  // in StepOrg. The first pool entry (omani.works) is what every wizard run defaults to.
  sovereignDomainMode: 'pool',
  sovereignPoolDomain: SOVEREIGN_POOL_DOMAINS[0]!.id,
  sovereignSubdomain: '',
  sovereignByoDomain: '',
  topology: 'zoned',
  regionProviders: {}, regionCloudRegions: {},
  providerTokens: {}, providerValidated: {},
  provider: null, hetznerToken: '', hetznerProjectId: '', credentialValidated: false,
  sshPublicKey: '', sshKeyGeneratedThisSession: false, sshPrivateKeyOnce: '', sshFingerprint: '',
  componentGroups: { ...DEFAULT_COMPONENT_GROUPS },
  componentsAppliedForProfile: null,
  // Empty by default — the user opts in to marketplace Blueprints in
  // StepComponents. Mandatory infra (bp-cilium, bp-flux, bp-crossplane, ...)
  // is `visibility: unlisted` and installed by the bootstrap kit regardless.
  selectedBlueprints: [],
  regions: [], controlPlaneSize: 'cx22', workerSize: 'cx22', workerCount: 0,
  haEnabled: false, selectedComponents: [],
  airgap: false,
  currentStep: 1, completedSteps: [], deploymentId: null,
  lastProvisionResult: null,
}

/**
 * Resolve the customer's chosen Sovereign domain into a single fully-qualified hostname.
 * Returns the empty string if the chosen mode hasn't been filled in yet — callers should
 * treat that as "not yet ready, disable Next button".
 */
export function resolveSovereignDomain(state: Pick<WizardState, 'sovereignDomainMode' | 'sovereignPoolDomain' | 'sovereignSubdomain' | 'sovereignByoDomain'>): string {
  if (state.sovereignDomainMode === 'pool') {
    const pool = SOVEREIGN_POOL_DOMAINS.find(p => p.id === state.sovereignPoolDomain)
    if (!pool || !state.sovereignSubdomain) return ''
    return `${state.sovereignSubdomain}.${pool.domain}`
  }
  return state.sovereignByoDomain.trim()
}

/** Validate that the chosen subdomain is a syntactically valid DNS label per RFC 1035. */
export function isValidSubdomain(subdomain: string): boolean {
  if (!subdomain) return false
  if (subdomain.length > 63) return false
  // RFC 1035: starts with letter, ends alphanumeric, only [a-z0-9-]
  return /^[a-z]([a-z0-9-]*[a-z0-9])?$/.test(subdomain)
}

/**
 * Validate an OpenSSH authorized_keys-style public key line.
 *
 * Accepts the algorithms `infra/hetzner/variables.tf` already accepts via
 * its regex validator: ed25519, RSA, and the three NIST-P ECDSA variants.
 * The base64 body is required; the trailing comment is optional.
 *
 * Rejects:
 *   • empty / whitespace-only strings (security floor — never an empty key)
 *   • lines whose algorithm prefix is not in the allow-list
 *   • lines whose middle field isn't a syntactically valid base64 blob
 *
 * Closes #160.
 */
export function isValidSSHPublicKey(line: string): boolean {
  const trimmed = line.trim()
  if (trimmed === '') return false
  // Algorithm prefix list mirrors the regex in infra/hetzner/variables.tf:
  //   ssh-rsa | ssh-ed25519 | ecdsa-sha2-nistp256 | ecdsa-sha2-nistp384 | ecdsa-sha2-nistp521
  const m = trimmed.match(
    /^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521)\s+([A-Za-z0-9+/=]+)(?:\s+.+)?$/,
  )
  if (!m) return false
  // Reject suspiciously short base64 — a real ed25519 wire-format public key
  // is ~80 base64 chars, RSA is ≥ 200, ECDSA ≥ 130. Anything < 30 chars is a
  // typo or a placeholder pasted by mistake.
  return (m[2]?.length ?? 0) >= 30
}

/** Validate a BYO domain (e.g. sovereign.acme-bank.com). */
export function isValidDomain(domain: string): boolean {
  const d = domain.trim()
  if (!d || d.length > 253) return false
  // Each label same rule as subdomain; at least 2 labels for a public domain
  return d.split('.').every(isValidSubdomain) && d.split('.').length >= 2
}
