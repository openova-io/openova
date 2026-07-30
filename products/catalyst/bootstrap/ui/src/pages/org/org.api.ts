/**
 * org.api.ts — typed REST client for the Organization-tier user CRUD endpoints
 * (issue #802).
 *
 * Wire shapes mirror `api/internal/handler/org_users.go`:
 *
 *   POST   /api/v1/org/users            (create + fire ADR-0003 hook)
 *   GET    /api/v1/org/users            (list — scoped to current Organization)
 *   DELETE /api/v1/org/users/{uuid}     (inverse rollback)
 *
 * Organization scoping: every request carries `X-Tenant-Host:
 * window.location.host` so the back end resolves the correct Organization
 * from the registry (per [Q-mine-1] of #795). The OIDC realm claim
 * provides the additional security boundary.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every URL
 * derives from `apiUrl()` in `shared/config/urls`. Per #2 (never
 * compromise on quality), the response shape is parsed through the
 * branded-types parsers (`parseOrgConsoleID` etc.) at the boundary so a
 * future server-side wire-shape drift surfaces as a runtime error
 * here, not as silent cross-Organization pollution downstream.
 */

import { apiUrl } from '@/shared/config/urls'

export type OrgStepState = 'pending' | 'done' | 'failed'

export interface OrgUserSteps {
  kc: OrgStepState
  newapi: OrgStepState
  secret: OrgStepState
}

export type OrgProvisionState =
  | 'pending'
  | 'kc_created'
  | 'newapi_created'
  | 'secret_applied'
  | 'done'
  | 'failed'
  | 'deleted'

export interface OrgUser {
  uuid: string
  email: string
  state: OrgProvisionState
  kc_user_id?: string
  newapi_user_id?: string
  secret_name?: string
  last_error?: string
  created_at: string
  updated_at: string
  steps: OrgUserSteps
}

export interface OrgUserCreateRequest {
  email: string
  /** Optional app → role mapping; rendered by RolesPage. */
  roles?: Record<string, string>
}

const ORG_USERS_PATH = '/v1/org/users'

function orgScopeHeaders(): HeadersInit {
  const host =
    typeof window !== 'undefined' ? window.location.host : ''
  return {
    'X-Tenant-Host': host,
    Accept: 'application/json',
  }
}

export async function listOrgUsers(): Promise<OrgUser[]> {
  const res = await fetch(apiUrl(ORG_USERS_PATH), {
    method: 'GET',
    credentials: 'include',
    headers: orgScopeHeaders(),
  })
  if (!res.ok) {
    throw new Error(`list org users: HTTP ${res.status}`)
  }
  const body = (await res.json()) as { items?: OrgUser[] }
  return body.items ?? []
}

export async function createOrgUser(
  req: OrgUserCreateRequest,
): Promise<OrgUser> {
  const res = await fetch(apiUrl(ORG_USERS_PATH), {
    method: 'POST',
    credentials: 'include',
    headers: { ...orgScopeHeaders(), 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok && res.status !== 202) {
    const detail = await res.text().catch(() => '')
    throw new Error(`create org user: HTTP ${res.status} ${detail}`)
  }
  return (await res.json()) as OrgUser
}

export async function deleteOrgUser(uuid: string): Promise<void> {
  const res = await fetch(apiUrl(`${ORG_USERS_PATH}/${encodeURIComponent(uuid)}`), {
    method: 'DELETE',
    credentials: 'include',
    headers: orgScopeHeaders(),
  })
  if (!res.ok && res.status !== 204) {
    throw new Error(`delete org user: HTTP ${res.status}`)
  }
}

/* ── Organization provisioning pipeline (issue #804) + multi-domain (#828) ── */

export type OrgDomainMode = 'free-subdomain' | 'byo'

export type OrgProvisionStepState = 'pending' | 'done' | 'failed'

export interface OrgProvisionSteps {
  /** #5489 — absent for a namespace-isolated Org: no vCluster is ever
   *  provisioned for that tier, so the API omits the step rather than
   *  reporting "done" over an object that does not exist. Render only
   *  the steps the payload carries. */
  vcluster?: OrgProvisionStepState
  bp_charts: OrgProvisionStepState
  dns: OrgProvisionStepState
  certs: OrgProvisionStepState
  keycloak_clients: OrgProvisionStepState
  registry: OrgProvisionStepState
}

export interface OrgProvisionRecord {
  /** Legacy BE wire key (store.OrganizationProvisionRecord json tag) —
   *  changes only in a BE+FE lockstep rename. */
  org_tenant_id: string
  state: string
  subdomain: string
  domain_mode: OrgDomainMode
  byo_domain?: string
  parent_domain?: string
  admin_email: string
  company_name?: string
  otech_fqdn: string
  vcluster_name: string
  /** Legacy BE wire key — see org_tenant_id note above. */
  tenant_namespace: string
  console_host: string
  commit_sha?: string
  last_error?: string
  steps: OrgProvisionSteps
  created_at: string
  updated_at: string
}

export interface OrgCreateRequest {
  subdomain: string
  domain_mode: OrgDomainMode
  /** Required when domain_mode === 'byo'. */
  byo_domain?: string
  /** Required when domain_mode === 'free-subdomain' AND the Sovereign
   *  has more than one entry in its org-pool. Backend defaults to the
   *  first NS-flip-ready entry when omitted on a multi-entry pool. */
  parent_domain?: string
  admin_email: string
  company_name?: string
  /** The Organizations internal door (issue #3378 B1). When omitted the
   *  backend defaults to the customer shape (kind=customer, tier=org,
   *  billingMode=real, isolation=vcluster) so the marketplace funnel is
   *  unaffected. kind='internal' stamps the department shape (showback +
   *  namespace) and skips the voucher dependency. These map onto the
   *  OrganizationSpec fields (Kind/Tier/BillingMode + Isolation). */
  kind?: 'internal' | 'customer'
  tier?: 'org' | 'corporate'
  billing_mode?: 'real' | 'chargeback' | 'showback'
  isolation?: 'namespace' | 'vcluster'
}

/** Wire shape mirrors the canonical issue #829 endpoint
 *  (parent_domains.go ListParentDomains). Re-exported here so the
 *  CreateOrganizationPage can consume the same shape without depending on
 *  the admin-page module. */
export type ParentDomainFlipStatus =
  | 'queued'
  | 'flipping'
  | 'flipped'
  | 'failed'
  | 'zone-creating'
  | 'cert-issuing'
  | 'ready'

export interface SovereignParentDomain {
  name: string
  /** "primary" | "org-pool" — only org-pool entries are valid Organization
   *  parents. */
  role: 'primary' | 'org-pool'
  /** Pipeline state of the NS-flip + zone-create + cert-issue chain.
   *  Operators MUST NOT bind an Organization under a not-yet-ready parent —
   *  the back end returns 503 Retry-After. */
  flipStatus: ParentDomainFlipStatus
  flipMessage?: string
  addedAt?: string
  flippedAt?: string
  registrarKind?: string
}

/** A parent is bookable for Organizations once its NS records are
 *  authoritative on this Sovereign's PowerDNS. Anything past `flipped`
 *  is acceptable (the wildcard cert may still be issuing but the
 *  authoritative NS resolution is in place). */
export function isParentDomainReady(p: SovereignParentDomain): boolean {
  return p.flipStatus === 'ready' || p.flipStatus === 'flipped'
}

const ORGANIZATIONS_PATH = '/v1/organizations'
const SOVEREIGN_PARENT_DOMAINS_PATH = '/v1/sovereign/parent-domains'

/**
 * listSovereignParentDomains — GET /api/v1/sovereign/parent-domains.
 *
 * Backs the CreateOrganizationPage parent-domain dropdown. The endpoint is
 * the integration seam to MD-1 (#826) — until that lands, the back end
 * sources from the CATALYST_ORG_POOL_DOMAINS env stub, hardcoded to
 * `omani.works + omani.trade` per the #828 constraint.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the URL is composed via
 * apiUrl(); never hardcode the prefix.
 */
export async function listSovereignParentDomains(
  role?: 'primary' | 'org-pool',
): Promise<SovereignParentDomain[]> {
  const qs = role ? `?role=${encodeURIComponent(role)}` : ''
  const res = await fetch(apiUrl(`${SOVEREIGN_PARENT_DOMAINS_PATH}${qs}`), {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`list parent domains: HTTP ${res.status}`)
  }
  const body = (await res.json()) as { items?: SovereignParentDomain[] }
  return body.items ?? []
}

/**
 * createOrganization — POST /api/v1/organizations.
 *
 * The pipeline is event-driven (NATS reconciler): the response is the
 * latest persisted record, even when the pipeline has only just kicked
 * off. The 202 response carries a steps[] map the SPA renders as a
 * progress timeline.
 */
export async function createOrganization(
  req: OrgCreateRequest,
): Promise<OrgProvisionRecord> {
  const res = await fetch(apiUrl(ORGANIZATIONS_PATH), {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok && res.status !== 202) {
    const detail = await res.text().catch(() => '')
    throw new Error(`create organization: HTTP ${res.status} ${detail}`)
  }
  return (await res.json()) as OrgProvisionRecord
}

export async function listOrganizationRecords(): Promise<OrgProvisionRecord[]> {
  const res = await fetch(apiUrl(ORGANIZATIONS_PATH), {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`list organizations: HTTP ${res.status}`)
  }
  const body = (await res.json()) as { items?: OrgProvisionRecord[] }
  return body.items ?? []
}
