import { KIT_COMPONENT_IDS } from '@/shared/lib/kitComponents'

// Re-export the branded `DeploymentID` (and helpers) so existing
// imports from `@/entities/deployment/model` continue to work and
// new call sites can reach for the brand without a second import.
// Closes #749 + #754.
export type { DeploymentID } from '@/shared/types/deployment'
export { parseDeploymentID, isDeploymentID } from '@/shared/types/deployment'

/** The name of a provider pack, as GET /api/v1/providers lists it (lib/providers.api.ts). */
export type CloudProvider = string
/**
 * NodeSize is a flavor name of the cloud profile of the chosen provider
 * (compute.flavors[].name, served by GET /api/v1/providers/{provider}).
 * The OpenTofu module receives the literal string.
 */
export type NodeSize = string
export type DeploymentStatus = 'pending' | 'provisioning' | 'healthy' | 'degraded' | 'failed' | 'destroying'
export type TopologyTemplate = 'dual' | 'solo'

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
  /** Pool domain owned by OpenOva (or by a franchised Sovereign owner). Sovereign operators pick a subdomain under it. */
  id: string
  domain: string
  description: string
}

/**
 * Marketplace brand — purely cosmetic per-Sovereign theming for the
 * `marketplace.<sovereign-fqdn>` storefront when the operator opts into
 * Marketplace mode (issue #710 wave 3a). All three fields are optional;
 * the chart only consumes `marketplaceEnabled` today, but the brand
 * fields persist on the wizard state so a future settings page can
 * surface them without re-prompting on every wizard run. Values that are
 * empty strings mean "use the platform default" — never "unset".
 */
export interface MarketplaceBrand {
  /** Display name shown in the storefront header (e.g. "Acme Marketplace"). */
  name: string
  /** Short tagline rendered under the storefront name. */
  tagline: string
  /** CSS-style hex colour (e.g. "#38BDF8") used for the storefront accent. */
  primaryColor: string
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
  /** The Sovereign's own IANA time zone (INF-054); '' until the operator picks
   *  one, and the wizard then proposes the browser's (browserTimeZone). The
   *  Sovereign restarts its nodes between 01:00 and 05:00 in it by default. */
  sovereignTimeZone: string
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
  /** Per region: the code of a region of the cloud profile (the request's cloudRegion). */
  regionCloudRegions: Record<number, string>
  /** Per region: a zone of that region of the cloud profile. */
  regionZones: Record<number, string>
  /**
   * Per-region flavor selection. Each array is indexed parallel to
   * regionCloudRegions: index 0 holds Region 1's choices,
   * index 1 holds Region 2's, and so on for whatever count the chosen
   * topology dictates (plus 1 for the AIR-GAP add-on when enabled).
   *
   * The legacy single-valued controlPlaneSize / workerSize / workerCount
   * fields are still present for back-compat (the migration in store.merge()
   * hydrates these arrays from them on first load), but every code path
   * that reads sizing post per-provider rework uses the per-region arrays.
   * The request sent to catalyst-api carries the Regions[] array.
   */
  regionControlPlaneSizes: string[]
  regionWorkerSizes: string[]
  regionWorkerCounts: number[]
  /** The provider pack of every region of the Sovereign (the request's provider). */
  provider: CloudProvider | null
  /** The installation document laid over the pack (the request's installation); '' for the pack alone. */
  installation: string
  /** The credential map, keyed by the credential fields of the cloud profile. Never persisted. */
  credentials: Record<string, string>
  /** True once POST /api/v1/credentials/validate accepted the credential map. Never persisted. */
  credentialValidated: boolean
  /**
   * SSH public key the OpenTofu module attaches to every server. Captured by the
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
   * Default storage class for the Sovereign (issue #4057). OPTIONAL —
   * an empty string means "let the backend fill the default class of the
   * cloud profile" (storage.defaultClass). The catalyst-api
   * Request struct (`provisioner.Request.StorageClass`) already accepts
   * this field; this is the UI
   * seam so an operator can override the durable cloud-volume class.
   * `local-path` is intentionally NOT a permitted value (durability).
   */
  storageClass: string
  /**
   * Marketplace mode toggle (issue #710 wave 3a). When true, the
   * provisioner sets `marketplace_enabled=true` on the OpenTofu var that
   * cloud-init substitutes into the bp-catalyst-platform HelmRelease,
   * which in turn renders the marketplace HTTPRoutes (storefront +
   * per-Org subdomain shells). The backend Request struct
   * (`provisioner.Request.MarketplaceEnabled`) and the chart values
   * (`ingress.marketplace.enabled`) are already wired — this flag is the
   * UI seam.
   */
  marketplaceEnabled: boolean
  /**
   * Optional brand fields shown on `marketplace.<sovereign-fqdn>` when
   * Marketplace mode is on. Never required — empty strings mean "use the
   * platform default banner". Persisted in the wizard store so a return
   * visit (or a future settings page) doesn't lose them.
   */
  marketplaceBrand: MarketplaceBrand
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
  /**
   * Stable per-deployment identifier minted by catalyst-api's `newID()`
   * (16 lowercase hex chars from `crypto/rand.Read(8)` →
   * `hex.EncodeToString`). Branded via `DeploymentID` so the compiler
   * rejects any raw `string` assignment that hasn't been routed through
   * `parseDeploymentID()` — closes the recurring 1-char truncation bug
   * (issues #749 + #754).
   */
  currentStep: number; completedSteps: number[]; deploymentId: import('@/shared/types/deployment').DeploymentID | null
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

/**
 * ORG_DEFAULTS — the wizard's starting Organization identity.
 *
 * #5401: this shipped as `{name:'Acme Financial', domain:'acme.io',
 * email:'platform@acme.io', industry:'Financial Services', …}` and was seeded
 * directly into INITIAL_WIZARD_STATE below. An operator who did not overwrite
 * every field provisioned a real Organization literally named "Acme Financial"
 * with `platform@acme.io` as its billing contact — a fictitious company's
 * details entering production as live data.
 *
 * Prefilled identity fields are actively harmful here: unlike a placeholder,
 * a prefilled value survives an inattentive Next. These are now EMPTY so the
 * required-field validation does its job and the operator must state who the
 * Organization actually is.
 *
 * Empty strings, not `undefined` — WizardState types these as `string`, and
 * the form binds them as controlled inputs (an undefined value would flip the
 * field to uncontrolled and warn on first keystroke).
 */
export const ORG_DEFAULTS = {
  name: '', domain: '', email: '',
  industry: '', size: '', headquarters: '',
  compliance: [] as string[],
}

/**
 * RETIRED_ORG_DEFAULTS_5401 — the fabricated identity ORG_DEFAULTS used to
 * ship, kept ONLY so the persist `migrate` below can recognise and drop it.
 *
 * Emptying ORG_DEFAULTS fixes INITIAL_WIZARD_STATE and nothing else. The
 * wizard store is `persist`-wrapped, `partialize()` strips only the
 * credential fields, and `merge()` returns `{ ...current, ...persisted }` —
 * so the persisted payload WINS over INITIAL_WIZARD_STATE. Every browser that
 * ran the wizard against the pre-fix bundle still holds these six values in
 * `localStorage['openova-catalyst-wizard']` and rehydrates them verbatim,
 * which makes the #5401 fix invisible on exactly the machines most likely to
 * walk UAT rows W1/W2 — an operator's own browser, which has run the wizard
 * before.
 *
 * W2 is the sharper half: `orgHeadquarters: 'Frankfurt, Germany'` was matched by
 * the headquarters map StepProvider had until CLD-122, which derived a provider
 * and its regions from it and rendered the "★ Pre-selected based on HQ" banner —
 * cloud provider and region derived from a fabricated value, which is
 * precisely what W2 forbids. A provider and regions persisted from that
 * derivation are still dropped.
 *
 * The migration is deliberately VALUE-MATCHED, not a blanket wipe: a field is
 * dropped only when it is still verbatim-identical to the retired default, so
 * an operator who had already typed their real identity keeps it. See the
 * CONTROL cases in `store.org-defaults-rehydrate-5401.test.ts`.
 *
 * Do not "clean this up" by deleting it — removing this constant silently
 * restores the fabricated company on every returning operator's browser.
 */
export const RETIRED_ORG_DEFAULTS_5401: Readonly<Record<string, string>> = {
  orgName: 'Acme Financial',
  orgDomain: 'acme.io',
  orgEmail: 'platform@acme.io',
  orgIndustry: 'Financial Services',
  orgSize: '2,000–10,000',
  orgHeadquarters: 'Frankfurt, Germany',
}

/**
 * Persist schema version for `localStorage['openova-catalyst-wizard']`.
 *
 * 1 — #5401: drop the retired fabricated ORG_DEFAULTS (and any
 *     provider/region derivation computed from the fabricated HQ) from
 *     payloads written by the pre-fix bundle.
 */
export const WIZARD_PERSIST_VERSION = 1

/**
 * Pool domains a Sovereign operator can pick when sovereignDomainMode='pool'.
 *
 * The first entry, omani.works, is OpenOva's primary pool domain — registered in the
 * Dynadot account managed by the dynadot-api-credentials K8s secret in openova-system
 * (which is account-scoped, so the same API key covers all OpenOva-owned domains).
 *
 * When an operator picks a pool domain, the provisioner backend writes a CNAME or A record
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
    description: 'OpenOva-provided pool — first franchised Sovereigns and Organization marketplace customers. DNS managed via Dynadot.',
  },
]

export const TOPOLOGY_REGION_COUNT: Record<TopologyTemplate, number> = {
  dual: 2, solo: 1,
}

export const TOPOLOGY_REGION_LABELS: Record<TopologyTemplate, string[]> = {
  dual: ['Region 1', 'Region 2'],
  solo: ['Region 1'],
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
  sovereignTimeZone: '',
  registrarType: null,
  registrarToken: '',
  registrarTokenValidated: false,
  topology: 'dual',
  regionCloudRegions: {}, regionZones: {},
  regionControlPlaneSizes: [],
  regionWorkerSizes: [],
  regionWorkerCounts: [],
  provider: null, installation: '', credentials: {}, credentialValidated: false,
  sshPublicKey: '', sshKeyGeneratedThisSession: false, sshPrivateKeyOnce: '', sshFingerprint: '',
  componentsAppliedForProfile: null,
  // Empty by default — the user opts in to marketplace Blueprints in
  // StepComponents. Mandatory infra (bp-cilium, bp-flux, bp-crossplane, ...)
  // is `visibility: unlisted` and installed by the bootstrap kit regardless.
  selectedBlueprints: [],
  // Legacy single-valued sizing kept for back-compat. The per-region
  // arrays above are canonical from the per-provider rework onwards.
  // controlPlaneSize / workerSize default to '' so a first-load that
  // hasn't visited StepProvider yet carries no flavor of any profile.
  // workerCount defaults to 2 so the first-time operator lands a
  // 1×CP + 2×worker multi-node Sovereign — restores the horizontal-
  // scale agreement (issue #733). Operators can dial it back to 0
  // explicitly for solo dev/POC topologies.
  regions: [], controlPlaneSize: '', workerSize: '', workerCount: 2,
  // Default storage class (issue #4057) — empty means "accept the
  // default class of the cloud profile".
  // Never seed a class here: provider is null at init, so a
  // hardcoded class would be wrong for whichever provider the operator
  // ends up picking. The StepProvider override input fills it on demand.
  storageClass: '',
  haEnabled: false, selectedComponents: [...KIT_COMPONENT_IDS],
  // Marketplace mode (issue #710 wave 3a) — DEFAULT ON for D27 zero-touch.
  // Founder ruling 2026-05-16: a Sovereign is provisioned ready to host
  // hosted Organizations (D27-D31). Operator can still flip the toggle
  // off on StepMarketplace if they explicitly want a private Sovereign.
  marketplaceEnabled: true,
  marketplaceBrand: { name: '', tagline: '', primaryColor: '' },
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
 * Accepts ed25519, RSA, and the three NIST-P ECDSA variants.
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

/** The time zones step 1 of the wizard offers for the Sovereign (INF-054):
 *  UTC and the browser's IANA list, sorted. */
export function offeredTimeZones(): string[] {
  let listed: string[] = []
  try {
    listed = typeof Intl.supportedValuesOf === 'function' ? Intl.supportedValuesOf('timeZone') : []
  } catch {
    listed = []
  }
  return Array.from(new Set(['UTC', ...listed])).sort()
}

/** The browser's IANA time zone, the wizard's proposal for the Sovereign's own
 *  time zone (INF-054), only when the list of offeredTimeZones holds it; else
 *  UTC, so that the request never carries a zone the person did not pick and
 *  the list does not offer. */
export function browserTimeZone(): string {
  let zone = ''
  try {
    zone = Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    zone = ''
  }
  return zone !== '' && offeredTimeZones().includes(zone) ? zone : 'UTC'
}
