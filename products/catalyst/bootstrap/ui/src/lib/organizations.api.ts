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
 *   • the SUB-ORG rows derive from GET /api/v1/organizations — the same
 *     listOrgRecords() roster feed the legacy BSS page used (#3378 §3
 *     cites this as the org directory source). Each OrgRecord is one
 *     Organization CR.
 *
 * The model fields (kind / tier / billingMode / isolation) live on
 * OrganizationSpec (core/controllers/organization/internal/orgapi/
 * types.go:54-79). Today only the customer path stamps them, so the
 * directory maps every sub-org to its kind-derived defaults until the
 * roster feed surfaces the real spec fields (B1 wires the internal door
 * that varies them).
 */

import { listOrgRecords, type OrgRecord, type OrgStatus as PipelineOrgStatus } from '@/lib/bss.api'
import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

/** OrgKind — the universal-primitive discriminator (#3378 §2.1). */
export type OrgKind = 'internal' | 'customer'

/** OrgTier — drives EPIC-5 DMZ auto-provisioning; rendered as a badge. */
export type OrgTier = 'org' | 'corporate'

/** OrgBillingMode — real | chargeback | showback (#3378 §2.6). */
export type OrgBillingMode = 'real' | 'chargeback' | 'showback'

/** OrgIsolation — namespace (people/app/cost boundary) vs vcluster
 *  (own Kubernetes universe). Kind-derived default (#3378 §2.1). */
export type OrgIsolation = 'namespace' | 'vcluster'

/** OrgStatus — lifecycle status the directory renders as a pill. The
 *  parent is always 'active'; sub-orgs map from the provisioning pipeline. */
export type OrgStatus = PipelineOrgStatus

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
 * normalizeKind — coerce the roster-feed `kind` string onto the OrgKind
 * union. Anything that isn't the literal 'internal' (incl. empty/legacy
 * rows) reads as 'customer' — the marketplace external door is the safe
 * default for a row whose spec predates the B1 fields.
 */
function normalizeKind(raw: string | undefined): OrgKind {
  return String(raw ?? '').trim().toLowerCase() === 'internal' ? 'internal' : 'customer'
}

/**
 * normalizeTier — coerce the roster-feed `tier` onto OrgTier. 'corporate'
 * is honored verbatim; everything else (incl. empty) reads as 'org'.
 */
function normalizeTier(raw: string | undefined): OrgTier {
  return String(raw ?? '').trim().toLowerCase() === 'corporate' ? 'corporate' : 'org'
}

/**
 * normalizeBillingMode — coerce the roster-feed `billing_mode` onto the
 * OrgBillingMode union, falling back to the kind-derived default when the
 * field is empty or unrecognized (legacy rows that predate B1).
 */
function normalizeBillingMode(raw: string | undefined, kind: OrgKind): OrgBillingMode {
  const v = String(raw ?? '').trim().toLowerCase()
  if (v === 'real' || v === 'chargeback' || v === 'showback') return v
  return kindDefaults(kind).billingMode
}

/**
 * normalizeIsolation — coerce the roster-feed `isolation` onto the
 * OrgIsolation union, falling back to the kind-derived default when empty
 * or unrecognized.
 */
function normalizeIsolation(raw: string | undefined, kind: OrgKind): OrgIsolation {
  const v = String(raw ?? '').trim().toLowerCase()
  if (v === 'namespace' || v === 'vcluster') return v
  return kindDefaults(kind).isolation
}

/**
 * subOrgRowFromRecord — map an existing OrgRecord (one Organization CR) to
 * a directory row. The roster feed surfaces the real spec fields the
 * orchestrator stamps (kind / tier / billing_mode / isolation, issue
 * #3378 B1), so an Internal org badges Internal · showback · namespace —
 * NOT the old hardcoded customer/real/vcluster. Rows whose spec predates
 * the B1 fields (empty kind/tier/billing/isolation) fall back to the
 * kind-derived defaults so a legacy customer row still badges sensibly.
 */
export function subOrgRowFromRecord(t: OrgRecord): OrgRow {
  const kind = normalizeKind(t.kind)
  return {
    id: t.id,
    slug: t.subdomain || t.orgName || t.id,
    displayName: t.orgName || t.subdomain || t.id,
    kind,
    tier: normalizeTier(t.tier),
    billingMode: normalizeBillingMode(t.billingMode, kind),
    isolation: normalizeIsolation(t.isolation, kind),
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
  const [self, records] = await Promise.all([
    getSovereignSelf(),
    listOrgRecords().catch(() => [] as OrgRecord[]),
  ])
  const rows: OrgRow[] = [parentRowFromSelf(self)]
  for (const t of records) rows.push(subOrgRowFromRecord(t))
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
  /** True on the single synthetic "Platform overhead" row the API folds
   *  infra/Job usage into (org_consumption.go `json:"isPlatform"`). */
  isPlatform?: boolean
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
    res = await authedFetch(`${API_BASE}/v1/org/consumption`, {
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
    `${API_BASE}/v1/org/organizations/${encodeURIComponent(slug)}/enter`,
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
