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
  /** The PURCHASED plan (s|m|l|xl|flexi) off the Organization CR's
   *  spec.planSlug (#4292, UAT row 7). Distinct from `tier`, which is the
   *  isolation class — `isolation` below is DERIVED from this value, so the
   *  directory used to render a consequence of the plan while never naming
   *  the plan itself. Empty for the sovereign-root row and for records that
   *  declare none. */
  plan: string
  billingMode: OrgBillingMode
  /** A real Organization is namespace- or vcluster-isolated. The
   *  sovereign-root row is the CLUSTER itself — isolated within nothing —
   *  so it carries 'cluster'. #5489: it previously claimed 'vcluster', a
   *  hardcoded literal with no backing object; on hw291 the directory
   *  advertised a vCluster while `kubectl get vclusters -A` returned none.
   *
   *  '' when the feed does not report one (#6145). The catalyst-api derives
   *  this field from the boundary the org-controller was OBSERVED to have
   *  authored (status.vcluster), so an empty value means "not measured" — and
   *  the renderers show an em dash rather than guessing. */
  isolation: OrgIsolation | 'cluster' | ''
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

/**
 * ORG_PLAN_SLUGS — the purchasable catalog plans, in tier order (UAT row G7,
 * Refs #4293/#4292).
 *
 * Mirrors `catalogPlanSlugs` in
 * products/catalyst/bootstrap/api/internal/handler/organization_provisioning.go:361,
 * the ONE list the create handler normalises against: a `plan_slug` outside it
 * is silently coerced to `s`. Kept as a single exported constant so the create
 * form's picker and any future plan surface offer exactly the set the server
 * accepts — a hand-typed subset would render options that vanish on submit.
 */
export const ORG_PLAN_SLUGS = ['s', 'm', 'l', 'xl', 'flexi'] as const

export type OrgPlanSlug = (typeof ORG_PLAN_SLUGS)[number]

/**
 * isolationForPlan — the #4292 TIER GATE, front-end side.
 *
 * The boundary primitive an Organization gets is decided by the plan slug
 * ALONE, never by `kind`. free/S share the host `<slug>` namespace; every paid
 * tier from M up gets a dedicated Org-vCluster. This is the exact predicate of
 * `isolationForTier` (organization_provisioning.go:346) and of the renderer
 * that actually authors the boundary, `boundaryIsVcluster`
 * (core/controllers/organization/internal/gitops/manifests.go:151).
 *
 * It exists so the create form can show the isolation the CHOSEN PLAN will
 * deliver. Before UAT row G7 the form showed `kindDefaults(kind).isolation` —
 * 'vcluster' for every customer Org — while the form had no plan input at all,
 * so the server normalised the plan to `s` and authored a host namespace. The
 * page advertised a boundary it could not order.
 */
export function isolationForPlan(planSlug: string): OrgIsolation {
  switch (planSlug.trim().toLowerCase()) {
    case '':
    case 's':
    case 'free':
      return 'namespace'
    default:
      return 'vcluster'
  }
}

interface SovereignSelf {
  deploymentId: string
  sovereignFQDN: string
}

/**
 * SOVEREIGN_PARENT_SLUG — the stable route identity of the sovereign-root row
 * (UAT row 25). Exported so the directory, the detail route and their tests
 * all name the same constant instead of re-deriving it; a key that is
 * recomputed in two places is how it came to differ between two renders.
 */
export const SOVEREIGN_PARENT_SLUG = 'sovereign'

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
 * 100% of consumption attributes to the parent"), isolation=cluster
 * (the sovereign root IS the cluster; it is not isolated inside one).
 *
 * This doc-comment said `isolation=vcluster` until #5489 — it kept
 * asserting the value AFTER the code below was corrected, so a reader
 * trusting the comment would still believe the directory claims a
 * vCluster. A comment that contradicts the line it documents is the same
 * declared-vs-actual defect the fix itself removed.
 */
export function parentRowFromSelf(self: SovereignSelf | null): OrgRow {
  const fqdn = self?.sovereignFQDN ?? ''
  return {
    id: self?.deploymentId || '__parent__',
    // UAT row 25 — a CONSTANT, never the FQDN.
    //
    // This slug is simultaneously the directory's link target
    // (OrganizationsDirectoryPage passes params={{ org: org.slug }}) and the
    // detail page's resolution key (rows.find(o => o.slug === org)). It used
    // to be `fqdn || 'sovereign'`, and getSovereignSelf() collapses EVERY
    // failure — network throw, non-2xx, unparseable body — to null. So a
    // single 503 changed the row's IDENTITY: the link built on one render
    // pointed at /organizations/<fqdn> while the detail page resolving it on
    // the next render only knew 'sovereign', and one of the two always 404'd
    // into org-detail-not-found. The value was never wrong so much as
    // NON-DETERMINISTIC, which is why it read as an intermittent bug.
    //
    // The FQDN is display data and stays on displayName/consoleHost below.
    // It is a poor key regardless: it is absent exactly when the enrichment
    // call fails, and its dots make it an awkward URL path segment.
    //
    // Resolution stays deterministic even if a sub-org were ever named
    // `sovereign`: listOrganizations always emits the parent FIRST and
    // `find` takes the first match.
    slug: SOVEREIGN_PARENT_SLUG,
    displayName: fqdn || 'Sovereign',
    kind: 'internal',
    tier: 'corporate',
    // The sovereign root is not a purchased Organization — it has no plan
    // and must not borrow one from a customer record.
    plan: '',
    billingMode: 'showback',
    // #5489 — the sovereign root IS the cluster; it is not isolated inside
    // one. `GET /api/v1/sovereign/self` returns only {deploymentId,
    // sovereignFQDN}, so there is no field to derive a namespace/vcluster
    // answer from, and inventing 'vcluster' made the directory claim an
    // object that does not exist.
    isolation: 'cluster',
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
 * OrgIsolation union, or '' when the feed does not say.
 *
 * #6145 (UAT row 101). This used to fall back to `kindDefaults(kind).isolation`
 * — 'vcluster' for every customer Org — so a feed that omitted the field made
 * the identity card assert a dedicated Kubernetes control plane for an
 * Organization nobody had measured. That is not a "safe default": every other
 * badge on this card degrades into a milder version of itself, while this one
 * invents infrastructure. The same reasoning already removed the parent row's
 * hardcoded 'vcluster' (#5489) and the record mapper's fabricated plan (#4292).
 *
 * Unknown renders as an em dash. `kindDefaults` survives for the CREATE form,
 * where pre-selecting a boundary the User can still change is a default rather
 * than a claim.
 */
function normalizeIsolation(raw: string | undefined): OrgIsolation | '' {
  const v = String(raw ?? '').trim().toLowerCase()
  if (v === 'namespace' || v === 'vcluster') return v
  return ''
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
    // #4292 / UAT row 7 — the purchased plan, verbatim. Deliberately NOT
    // defaulted: an Org that declares no plan reads as empty, and the
    // detail page omits the field rather than naming a plan nobody bought.
    plan: String(t.planSlug ?? '').trim(),
    billingMode: normalizeBillingMode(t.billingMode, kind),
    isolation: normalizeIsolation(t.isolation),
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
  /** True on the single synthetic "Unowned namespaces" row (#6114,
   *  org_consumption.go `json:"isUnowned"`): real consumption whose
   *  namespace carries an `openova.io/organization` label that no
   *  Organization CR claims. It is NOT an Organization and must never be
   *  labelled as one — the panel says so explicitly, because the whole
   *  reason this row exists is that the alternative to billing a phantom
   *  Org is showing the orphan, not hiding it. */
  isUnowned?: boolean
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
  /** The `openova.io/organization` label values drawing consumption with
   *  no Organization CR behind them (#6114). Empty on a healthy estate. */
  unownedOrgs?: string[]
  /** True when the metrics cache wasn't ready — the panel flags "metering
   *  warming up" instead of asserting a false zero. */
  pending: boolean
}

const EMPTY_CONSUMPTION: SovereignConsumption = {
  totalCostUnits: 0,
  orgs: [],
  unownedOrgs: [],
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
      // #6114: this mapper rebuilds the object field by field, so a field
      // it does not name is dropped no matter what the API sends. Omitting
      // unownedOrgs here would leave the orphan warning permanently
      // unrenderable while every panel-level test still passed — they seed
      // the feed through initialOverride and never traverse this function.
      unownedOrgs: Array.isArray(body.unownedOrgs) ? (body.unownedOrgs as string[]) : [],
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
