import { computeDefaultSelection } from '@/pages/wizard/steps/componentGroups'

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

/**
 * Domain delegation mode. Closes #169.
 *
 *  - 'pool'       OpenOva-managed pool (e.g. omani.works) — PDM owns DNS.
 *  - 'byo-manual' Customer brings their own domain. They paste the OpenOva
 *                 nameservers into their registrar's UI by hand. Wizard
 *                 polls until propagation completes, then continues.
 *  - 'byo-api'    Customer brings their own domain AND provides registrar
 *                 API credentials. catalyst-api/PDM flips the NS records
 *                 via the registrar's REST API on their behalf.
 *
 * Legacy 'byo' is preserved as an alias for 'byo-manual' so persisted
 * Zustand state from earlier wizard runs still loads cleanly. The store's
 * merge() coerces the legacy value.
 */
export type DomainMode = 'pool' | 'byo-manual' | 'byo-api'

/** Legacy persistence alias. Treat 'byo' as 'byo-manual' on load. */
export const LEGACY_BYO_MODE = 'byo' as const

/**
 * Registrar identifier for BYO-api mode. Mirrors the adapter names PDM
 * exposes via /api/v1/registrar/{registrar}/set-ns. Keep this list in sync
 * with `core/pool-domain-manager/internal/registrar/` subpackages.
 */
export type RegistrarType = 'cloudflare' | 'namecheap' | 'godaddy' | 'ovh' | 'dynadot'

export interface RegistrarOption {
  id: RegistrarType
  label: string
  tokenHint: string
}

export const REGISTRAR_OPTIONS: RegistrarOption[] = [
  { id: 'cloudflare', label: 'Cloudflare', tokenHint: 'API token with Zone:Edit + DNS:Edit permission for the domain' },
  { id: 'namecheap',  label: 'Namecheap',  tokenHint: 'apiUser:apiKey:clientIP — Namecheap requires the calling IP' },
  { id: 'godaddy',    label: 'GoDaddy',    tokenHint: 'API key:secret (production tier)' },
  { id: 'ovh',        label: 'OVH',        tokenHint: 'appKey:appSecret:consumerKey' },
  { id: 'dynadot',    label: 'Dynadot',    tokenHint: 'API key from Dynadot account → Tools → API' },
]

/**
 * Default OpenOva nameservers the customer must delegate their BYO domain
 * to. These are OpenOva-operated PowerDNS instances reachable from the
 * public internet.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4: when a runtime endpoint is wired
 * (catalyst-api /api/v1/dns/nameservers, future), the wizard will fetch
 * these instead of importing the constant.
 */
export const OPENOVA_NAMESERVERS: readonly string[] = [
  'ns1.openova.io',
  'ns2.openova.io',
  'ns3.openova.io',
] as const

export interface SovereignPoolDomain {
  /** Pool domain owned by OpenOva (or by a franchised Sovereign owner). Sovereign tenants pick a subdomain under it. */
  id: string
  domain: string
  description: string
}

export interface WizardState {
  orgName: string; orgDomain: string; orgEmail: string; orgIndustry: string
  orgSize: string; orgHeadquarters: string; orgCompliance: string[]
  /** Three-mode selector — see DomainMode docstring. Closes #169. */
  sovereignDomainMode: DomainMode
  /** When sovereignDomainMode='pool', which pool domain to use (id, e.g. 'omani-works'). */
  sovereignPoolDomain: string
  /** When sovereignDomainMode='pool', the subdomain the customer types (e.g. 'omantel' → omantel.omani.works). */
  sovereignSubdomain: string
  /** When sovereignDomainMode in ('byo-manual','byo-api'), the full domain
   *  the customer brings (e.g. 'acme.com' or 'sovereign.acme-bank.com'). */
  sovereignByoDomain: string
  /** When sovereignDomainMode='byo-api', the registrar adapter to use. */
  registrarType: RegistrarType | null
  /**
   * When sovereignDomainMode='byo-api', the customer's API token. Held in
   * memory ONLY — partialize() in the store strips it before localStorage
   * persist (same hygiene as the SSH private key per
   * docs/INVIOLABLE-PRINCIPLES.md #10).
   */
  registrarToken: string
  /** True after POST /api/v1/registrar/{r}/validate (validation-only call)
   *  reports the credentials work and the domain is in the account. */
  registrarTokenValidated: boolean
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
  /**
   * Selected platform components from the corporate StepComponents grid
   * (the 60+ catalog defined in pages/wizard/steps/componentGroups.ts).
   *
   * Stored as a sorted, de-duplicated string[] of component ids. The
   * wizard exposes Set-shaped helpers via the store actions
   * (addComponent / removeComponent / setSelectedComponents) but persists
   * the array form so Zustand's storage middleware can JSON-serialise it
   * across sessions.
   *
   * The selection is dependency-aware:
   *  - addComponent(id) walks `dependencies` transitively and adds every
   *    reachable component, so picking Harbor auto-pulls cnpg + seaweedfs +
   *    valkey.
   *  - removeComponent(id) walks the reverse graph (`findDependents`) and
   *    also removes anything that listed `id` as a dep. The wizard UI is
   *    expected to confirm with the user BEFORE calling removeComponent
   *    when the cascade is non-empty.
   *  - Components with `tier: mandatory` cannot be removed.
   *
   * Replaces the previous `SelectedComponent[]` legacy structure (still
   * exported as a type for any old call site that imported it) — the wizard
   * UI now uses just the id list with the catalog providing the tier /
   * description / dependency metadata.
   */
  selectedComponents: string[]
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
  spine:    ['cilium', 'coraza', 'powerdns', 'external-dns', 'envoy', 'frpc', 'netbird'],
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
  registrarType: null,
  registrarToken: '',
  registrarTokenValidated: false,
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
  haEnabled: false, selectedComponents: [...computeDefaultSelection()].sort(),
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
  // BYO modes both consume the same byoDomain field.
  return state.sovereignByoDomain.trim()
}

/** Convenience type-guard for the two BYO variants. */
export function isByoMode(mode: DomainMode): mode is 'byo-manual' | 'byo-api' {
  return mode === 'byo-manual' || mode === 'byo-api'
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
