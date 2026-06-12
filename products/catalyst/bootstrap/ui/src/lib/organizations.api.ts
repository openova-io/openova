/**
 * lib/organizations.api.ts — typed client for the Organizations menu
 * (issue #3378). The Organizations directory is the parent organization's
 * complete view of everything beneath it: the parent itself is the first
 * citizen, and every sub-organization is one row.
 *
 * Wire path (rides EXISTING endpoints — no new directory endpoint per the
 * #3378 §6 "Any new endpoint beyond B1-B3 = FAIL" rule):
 *
 *   • the PARENT row derives from GET /api/v1/sovereign/self (FQDN +
 *     deployment id) — the parent org IS the sovereign itself (#3378 §2.2:
 *     "the parent org is the sovereign itself, root, parentOrg empty").
 *   • the SUB-ORG rows derive from GET /api/v1/sme/tenants — the same
 *     listTenants() feed the BSS Tenants page used (#3378 §3 cites this
 *     as the org directory source). Each Tenant is one Organization CR.
 *
 * The model fields (kind / tier / billingMode / isolation) live on
 * OrganizationSpec (core/controllers/organization/internal/orgapi/
 * types.go:54-79). Today only the customer path stamps them, so the
 * directory maps every sub-org to its kind-derived defaults until the
 * tenants feed surfaces the real spec fields (B1 wires the internal door
 * that varies them).
 */

import { listTenants, type Tenant, type TenantStatus } from '@/lib/bss.api'
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/** OrgKind — the universal-primitive discriminator (#3378 §2.1). */
export type OrgKind = 'internal' | 'customer'

/** OrgTier — drives EPIC-5 DMZ auto-provisioning; rendered as a badge. */
export type OrgTier = 'sme' | 'corporate'

/** OrgBillingMode — real | chargeback | showback (#3378 §2.6). */
export type OrgBillingMode = 'real' | 'chargeback' | 'showback'

/** OrgIsolation — namespace (people/app/cost boundary) vs vcluster
 *  (own Kubernetes universe). Kind-derived default (#3378 §2.1). */
export type OrgIsolation = 'namespace' | 'vcluster'

/** OrgStatus — lifecycle status the directory renders as a pill. The
 *  parent is always 'active'; sub-orgs map from the tenant pipeline. */
export type OrgStatus = TenantStatus

/**
 * OrgRow — one row of the Organizations directory. The parent row and
 * every sub-org row share this shape so the directory table is uniform
 * (#3378 §4: "one row per org incl. THE PARENT FIRST").
 */
export interface OrgRow {
  /** Stable identifier. For the parent this is the deployment id (or
   *  '__parent__' when self-discovery hasn't resolved one yet). */
  id: string
  /** Canonical slug (the parent uses the FQDN; sub-orgs the subdomain). */
  slug: string
  /** Human-readable display name. */
  displayName: string
  kind: OrgKind
  tier: OrgTier
  billingMode: OrgBillingMode
  isolation: OrgIsolation
  status: OrgStatus
  /** True for the sovereign-root row (parentOrg empty). The Enter-org
   *  button is hidden on this row (#3378 §5: "you are already inside it"). */
  isParent: boolean
  /** Owner email when known (sub-orgs carry it; the parent does not). */
  ownerEmail: string
  /** Console host for the org (parent: console.<fqdn>; sub-orgs: their
   *  per-org console host). Empty when not yet resolvable. */
  consoleHost: string
}

/**
 * kindDefaults — the kind-derived isolation + billingMode defaults the
 * internal door renders before the advanced override (#3378 §2.3):
 *   internal → showback + namespace (department: a people/app/cost
 *              boundary, no own Kubernetes universe, no voucher step)
 *   customer → real + vcluster (the marketplace funnel's external door)
 */
export function kindDefaults(kind: OrgKind): {
  billingMode: OrgBillingMode
  isolation: OrgIsolation
} {
  return kind === 'internal'
    ? { billingMode: 'showback', isolation: 'namespace' }
    : { billingMode: 'real', isolation: 'vcluster' }
}

interface SovereignSelf {
  deploymentId: string
  sovereignFQDN: string
}

/**
 * getSovereignSelf — resolve the parent org's identity (the sovereign
 * itself). Tolerates 404 (mothership / not-a-sovereign) and 5xx by
 * returning null so the directory still renders sub-org rows.
 */
export async function getSovereignSelf(): Promise<SovereignSelf | null> {
  let res: Response
  try {
    res = await authedFetch(`${API_BASE}/v1/sovereign/self`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return null
  }
  if (!res.ok) return null
  try {
    const body = (await res.json()) as Partial<SovereignSelf> | null
    if (!body || typeof body !== 'object') return null
    const fqdn = String(body.sovereignFQDN ?? '').trim()
    if (!fqdn) return null
    return {
      deploymentId: String(body.deploymentId ?? '').trim(),
      sovereignFQDN: fqdn,
    }
  } catch {
    return null
  }
}

/**
 * parentRowFromSelf — build the parent-org directory row from the
 * sovereign self-discovery payload. The parent is kind=internal (it is
 * the sovereign-root department), tier=corporate (the operator's own
 * estate), billingMode=showback (#3378 §5: "showback works day one —
 * 100% of consumption attributes to the parent"), isolation=vcluster
 * (the sovereign cluster itself).
 */
export function parentRowFromSelf(self: SovereignSelf | null): OrgRow {
  const fqdn = self?.sovereignFQDN ?? ''
  return {
    id: self?.deploymentId || '__parent__',
    slug: fqdn || 'sovereign',
    displayName: fqdn || 'Sovereign',
    kind: 'internal',
    tier: 'corporate',
    billingMode: 'showback',
    isolation: 'vcluster',
    status: 'active',
    isParent: true,
    ownerEmail: '',
    consoleHost: fqdn ? `console.${fqdn}` : '',
  }
}

/**
 * subOrgRowFromTenant — map an existing Tenant (one Organization CR) to a
 * directory row. The tenants feed today is the customer path, so kind
 * defaults to 'customer' with the customer-derived isolation/billing
 * unless the feed later surfaces the real spec fields. tier defaults to
 * 'sme' (the customer tier).
 */
export function subOrgRowFromTenant(t: Tenant): OrgRow {
  const kind: OrgKind = 'customer'
  const def = kindDefaults(kind)
  return {
    id: t.id,
    slug: t.subdomain || t.orgName || t.id,
    displayName: t.orgName || t.subdomain || t.id,
    kind,
    tier: 'sme',
    billingMode: def.billingMode,
    isolation: def.isolation,
    status: t.status,
    isParent: false,
    ownerEmail: t.ownerEmail,
    consoleHost: t.consoleHost,
  }
}

/**
 * listOrganizations — the directory feed. Always returns the parent as
 * the first row (#3378 §4 + §5: "never blank", "the parent org as first
 * citizen"), followed by every sub-org. Both source calls tolerate
 * failure: a sub-org fetch error still yields a parent-only directory
 * rather than an empty menu.
 */
export async function listOrganizations(): Promise<OrgRow[]> {
  const [self, tenants] = await Promise.all([
    getSovereignSelf(),
    listTenants().catch(() => [] as Tenant[]),
  ])
  const rows: OrgRow[] = [parentRowFromSelf(self)]
  for (const t of tenants) rows.push(subOrgRowFromTenant(t))
  return rows
}

/* ── B3 consumption / showback feed (#3378 §6 B3, DoD 3) ───────────── */

/** AppConsumption — one application's showback slice within an org. */
export interface AppConsumption {
  application: string
  namespace: string
  costUnits: number
  cpuMilli: number
  memoryGiB: number
  storageGiB: number
  percent: number
}

/** OrgConsumption — one org's consumption rollup (parent first). */
export interface OrgConsumption {
  org: string
  isParent: boolean
  costUnits: number
  cpuMilli: number
  memoryGiB: number
  storageGiB: number
  apps: AppConsumption[]
}

/** SovereignConsumption — the metering feed wire shape. */
export interface SovereignConsumption {
  totalCostUnits: number
  orgs: OrgConsumption[]
  /** True when the metrics cache wasn't ready — the panel flags "metering
   *  warming up" instead of asserting a false zero. */
  pending: boolean
}

const EMPTY_CONSUMPTION: SovereignConsumption = {
  totalCostUnits: 0,
  orgs: [],
  pending: true,
}

/**
 * getConsumption — fetch the per-org showback rollup (the B3 feed). The
 * FIRST deliverable is parent self-showback: on a zero-sub-org estate the
 * only org row is the parent carrying 100% (§5 day-one). Tolerates
 * failure by returning the pending empty shape so the panel renders its
 * chrome rather than crashing.
 */
export async function getConsumption(): Promise<SovereignConsumption> {
  let res: Response
  try {
    res = await authedFetch(`${API_BASE}/v1/sme/consumption`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return EMPTY_CONSUMPTION
  }
  if (!res.ok) return EMPTY_CONSUMPTION
  try {
    const body = (await res.json()) as Partial<SovereignConsumption> | null
    if (!body || typeof body !== 'object') return EMPTY_CONSUMPTION
    return {
      totalCostUnits: Number(body.totalCostUnits ?? 0),
      orgs: Array.isArray(body.orgs) ? (body.orgs as OrgConsumption[]) : [],
      pending: Boolean(body.pending),
    }
  } catch {
    return EMPTY_CONSUMPTION
  }
}

/* ── B2 Enter-org support session (#3378 §6 B2, DoD 6) ─────────────── */

/** EnterOrgResult — the minted support session (the org-console handover
 *  URL + the audited support principal + the ≤60min expiry). */
export interface EnterOrgResult {
  handoverURL: string
  supportPrincipal: string
  org: string
  expiresAt: string
  auditEventId: string
}

/**
 * enterOrg — mint an audited, time-boxed support session into a sub-org's
 * own console (§2.4). POSTs to the catalyst-api Enter-org endpoint; the
 * caller opens the returned handoverURL. Throws on failure so the button
 * surfaces the error (an unauthorized caller, an attempt to enter the
 * parent, or an unwired handover signer).
 */
export async function enterOrg(slug: string): Promise<EnterOrgResult> {
  const res = await authedFetch(
    `${API_BASE}/v1/sme/organizations/${encodeURIComponent(slug)}/enter`,
    {
      method: 'POST',
      headers: { Accept: 'application/json' },
    },
  )
  if (!res.ok) {
    const detail = await res.text().catch(() => '')
    throw new Error(`enter org: HTTP ${res.status} ${detail}`.trim())
  }
  return (await res.json()) as EnterOrgResult
}
