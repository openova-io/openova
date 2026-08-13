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
  /** #5489/#5501 — absent for a namespace-isolated Org (no vCluster is
   *  authored for that tier), and the bare slug for a vcluster-tier one:
   *  the name the org-controller stamps at status.vcluster.name, never a
   *  client-side `vc-` synthesis. */
  vcluster_name?: string
  /** Legacy BE wire key — see org_tenant_id note above. */
  tenant_namespace: string
  console_host: string
  commit_sha?: string
  last_error?: string
  steps: OrgProvisionSteps
  /** #5501 — the OBSERVED phase of the Org's boundary as the
   *  org-controller reports it (Ready | Provisioning | Pending | Failed).
   *  Absent when the substrate has never been observed. */
  boundary_phase?: string
  /** #5501 — absent when genuinely unknown; the API no longer publishes a
   *  Go zero timestamp (0001-01-01T00:00:00Z) as a measurement. */
  created_at?: string
  updated_at?: string
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
   *  billingMode=real) so the marketplace funnel is unaffected.
   *
   *  `isolation` is NOT part of that default and must not be sent as one
   *  (#5857): the server DERIVES it from the #4292 tier gate so the label
   *  matches the backing, and a valid explicit value bypasses that gate.
   *  Send it only for a deliberate operator override. This comment
   *  previously read "isolation=vcluster", which was the pre-tier-gate
   *  behaviour and is exactly the value that made the label wrong. kind='internal' stamps the department shape (showback +
   *  namespace) and skips the voucher dependency. These map onto the
   *  OrganizationSpec fields (Kind/Tier/BillingMode + Isolation). */
  kind?: 'internal' | 'customer'
  tier?: 'org' | 'corporate'
  billing_mode?: 'real' | 'chargeback' | 'showback'
  isolation?: 'namespace' | 'vcluster'
  /** The PURCHASED catalog plan (s|m|l|xl|flexi) — UAT row G7, Refs
   *  #4293/#4292.
   *
   *  This field is what makes the console door capable of ordering a
   *  vcluster-isolation Organization at all. The server has accepted
   *  `plan_slug` since #4292 (organization_provisioning.go:290) and derives
   *  BOTH the boundary primitive (`boundaryIsVcluster`) and the
   *  ResourceQuota/LimitRange from it — but this request type never carried
   *  it, so every Organization created through the console arrived with no
   *  plan, was normalised to `s`, and was authored onto the host `<slug>`
   *  namespace. The dual-door clause ("both Org doors land a
   *  vcluster-isolation Org") could not be satisfied from this door by
   *  construction; the funnel door has carried the slug since #4473.
   *
   *  Omitted ⇒ the server's `s` default, i.e. the previous behaviour. */
  plan_slug?: string
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
