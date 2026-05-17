/**
 * lib/bss.api.ts — typed REST client for the Sovereign-side BSS surfaces.
 *
 * Wire paths:
 *
 *   browser ──/api/v1/sme/bss/overview──▶ catalyst-api ──▶ per-tenant rollups
 *   browser ──/api/v1/sme/billing/vouchers/{issue,list,revoke}──▶ catalyst-api
 *           ──▶ billing service (#117 — core/services/billing/handlers/vouchers.go)
 *
 * The overview endpoint is the FE-facing rollup for the BSS landing KPI
 * cards. The vouchers endpoints back Wave 6 PR 5's native vouchers
 * surface. Both gracefully tolerate 404 / 5xx so the page still renders
 * its target-state chrome on first paint (per INVIOLABLE-PRINCIPLES.md
 * #1) — overview returns zero-filled with `pendingApi: true`; voucher
 * list throws so the page can surface the API error inline.
 */

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'

export interface BssOverview {
  /** Set when the BE returned a non-2xx — the cards still render but
   *  surface the "API pending" pill mirroring SettingsPage's pattern. */
  pendingApi: boolean
  billing: {
    /** Monthly recurring revenue, in cents (BE always returns integer
     *  cents; the FE formats to display currency). */
    mrrCents: number
    /** Period-over-period delta, signed percentage with 1-decimal
     *  precision. Positive = growth. Null when prior period is empty. */
    deltaPct: number | null
  }
  orders: {
    pending: number
    /** Age of the oldest pending order in whole days. Null when the
     *  pending queue is empty. */
    oldestDays: number | null
  }
  vouchers: {
    active: number
    /** Lifetime redemption rate (redeemed / issued), 0-100. Null when
     *  no vouchers have been issued. */
    redeemRate: number | null
  }
  tenants: {
    active: number
    newThisWeek: number
  }
  revenue: {
    /** Trailing 30-day revenue, in cents. */
    last30dCents: number
    /** Delta vs the prior 30-day window, signed percentage. */
    deltaPct: number | null
    /** Up to 30 daily revenue points for the inline sparkline, oldest
     *  first. Empty array is a valid signal (no revenue yet). */
    sparkline: number[]
  }
}

const ZERO_OVERVIEW: BssOverview = {
  pendingApi: true,
  billing: { mrrCents: 0, deltaPct: null },
  orders: { pending: 0, oldestDays: null },
  vouchers: { active: 0, redeemRate: null },
  tenants: { active: 0, newThisWeek: 0 },
  revenue: { last30dCents: 0, deltaPct: null, sparkline: [] },
}

/**
 * getBssOverview — fetch the KPI rollup for the BSS landing.
 *
 * Returns a fully-shaped object even on backend failure (404 / 5xx /
 * network error), with `pendingApi=true` so the page can flag the
 * "API pending" state to the operator without crashing the surface.
 */
export async function getBssOverview(): Promise<BssOverview> {
  let res: Response
  try {
    res = await authedFetch(`${API_BASE}/v1/sme/bss/overview`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return ZERO_OVERVIEW
  }
  if (!res.ok) {
    return ZERO_OVERVIEW
  }
  try {
    const body = (await res.json()) as Partial<BssOverview> | null
    if (!body || typeof body !== 'object') return ZERO_OVERVIEW
    return {
      pendingApi: false,
      billing: {
        mrrCents: Number(body.billing?.mrrCents ?? 0),
        deltaPct:
          body.billing?.deltaPct === null || body.billing?.deltaPct === undefined
            ? null
            : Number(body.billing.deltaPct),
      },
      orders: {
        pending: Number(body.orders?.pending ?? 0),
        oldestDays:
          body.orders?.oldestDays === null || body.orders?.oldestDays === undefined
            ? null
            : Number(body.orders.oldestDays),
      },
      vouchers: {
        active: Number(body.vouchers?.active ?? 0),
        redeemRate:
          body.vouchers?.redeemRate === null || body.vouchers?.redeemRate === undefined
            ? null
            : Number(body.vouchers.redeemRate),
      },
      tenants: {
        active: Number(body.tenants?.active ?? 0),
        newThisWeek: Number(body.tenants?.newThisWeek ?? 0),
      },
      revenue: {
        last30dCents: Number(body.revenue?.last30dCents ?? 0),
        deltaPct:
          body.revenue?.deltaPct === null || body.revenue?.deltaPct === undefined
            ? null
            : Number(body.revenue.deltaPct),
        sparkline: Array.isArray(body.revenue?.sparkline)
          ? body.revenue!.sparkline.map((n) => Number(n))
          : [],
      },
    }
  } catch {
    return ZERO_OVERVIEW
  }
}

/* ── Revenue (Wave 6 PR 4) ──────────────────────────────────────────
 *
 * Deeper cut of the BSS revenue rollup. The landing's overview surfaces
 * a single MRR number + 30-day sparkline; the /bss/revenue page exposes
 * per-day points for a richer chart and a per-plan breakdown table with
 * tenant count + MRR + YoY delta. Wire shape mirrors getBssOverview:
 * zero-filled fallback + pendingApi=true on 404 / 5xx / network error
 * so the page renders the full target-state surface on first paint
 * (per INVIOLABLE-PRINCIPLES.md #1 — waterfall).
 */

export interface PlanBreakdown {
  /** Stable plan id used as the React key + sort tiebreaker. */
  id: string
  /** Operator-facing plan label (e.g. "Starter", "Growth", "Enterprise"). */
  plan: string
  /** Number of tenants currently subscribed to this plan. */
  tenants: number
  /** Monthly recurring revenue contribution from this plan, in cents. */
  mrrCents: number
  /** Year-over-year delta, signed percentage with 1-decimal precision.
   *  Null when this plan has no prior-year baseline (new plan). */
  yoyPct: number | null
}

export interface RevenueKpi {
  /** Trailing 30-day revenue, in cents. */
  last30dCents: number
  /** Month-over-month delta, signed percentage. Null when no prior window. */
  momPct: number | null
  /** Highest-MRR tenant label; empty when no tenants. */
  topTenant: string
  /** Highest-MRR plan label; empty when no plans. */
  topPlan: string
}

export interface BssRevenue {
  /** Set when the BE returned a non-2xx — page still renders but the
   *  "API pending" pill is shown, mirroring the overview pattern. */
  pendingApi: boolean
  kpi: RevenueKpi
  /** Up to 30 daily revenue points (cents), oldest first. */
  sparkline: number[]
  breakdown: PlanBreakdown[]
}

const ZERO_REVENUE: BssRevenue = {
  pendingApi: true,
  kpi: { last30dCents: 0, momPct: null, topTenant: '', topPlan: '' },
  sparkline: [],
  breakdown: [],
}

/**
 * getRevenue — fetch the /bss/revenue analytics payload.
 *
 * Tolerates 404 / 5xx / network error by returning ZERO_REVENUE so the
 * page can render its full target-state shape + the "API pending" pill
 * without crashing (mirrors getBssOverview).
 *
 * Backend wire path (when shipped):
 *   browser ──/api/v1/sme/billing/revenue──▶ catalyst-api ──▶ rollup
 */
export async function getRevenue(): Promise<BssRevenue> {
  let res: Response
  try {
    res = await authedFetch(`${API_BASE}/v1/sme/billing/revenue`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return ZERO_REVENUE
  }
  if (!res.ok) {
    return ZERO_REVENUE
  }
  try {
    const body = (await res.json()) as Partial<BssRevenue> | null
    if (!body || typeof body !== 'object') return ZERO_REVENUE
    const breakdown = Array.isArray(body.breakdown)
      ? body.breakdown.map((row, i) => ({
          id: String(row?.id ?? `row-${i}`),
          plan: String(row?.plan ?? ''),
          tenants: Number(row?.tenants ?? 0),
          mrrCents: Number(row?.mrrCents ?? 0),
          yoyPct:
            row?.yoyPct === null || row?.yoyPct === undefined
              ? null
              : Number(row.yoyPct),
        }))
      : []
    return {
      pendingApi: false,
      kpi: {
        last30dCents: Number(body.kpi?.last30dCents ?? 0),
        momPct:
          body.kpi?.momPct === null || body.kpi?.momPct === undefined
            ? null
            : Number(body.kpi.momPct),
        topTenant: String(body.kpi?.topTenant ?? ''),
        topPlan: String(body.kpi?.topPlan ?? ''),
      },
      sparkline: Array.isArray(body.sparkline)
        ? body.sparkline.map((n) => Number(n))
        : [],
      breakdown,
    }
  } catch {
    return ZERO_REVENUE
  }
}

/* ── Vouchers (Wave 6 PR 5) ──────────────────────────────────────────
 *
 * Wire shape mirrors core/services/billing/store.PromoCode (the BE
 * "voucher" is the user-facing label for what the storage layer calls
 * a "PromoCode" — same row in promo_codes). Snake_case keys match the
 * Go json tags verbatim.
 *
 * Status is derived FE-side from the row fields rather than persisted
 * server-side — `revoked` when DeletedAt is set, `inactive` when Active
 * is false, `exhausted` when MaxRedemptions>0 && TimesRedeemed>=Max,
 * `active` otherwise. This keeps the table semantics on a single source
 * of truth (the row) without a server round-trip per filter.
 */

export interface Voucher {
  /** Canonical uppercase voucher code (BE normalises on upsert). */
  code: string
  /** Credit amount in OMR (integer; BE stores OMR not cents). */
  credit_omr: number
  description: string
  /** Operator-toggleable enable flag (separate from soft-delete). */
  active: boolean
  /** 0 = unlimited; otherwise hard cap on redemptions. */
  max_redemptions: number
  /** Lifetime redeemed count. */
  times_redeemed: number
  /** RFC3339 issue timestamp. */
  created_at: string
  /** Soft-delete timestamp (revoke); omitted while voucher is live. */
  deleted_at?: string
}

/**
 * Derived status pill for the table. Combines the four BE fields into
 * a single bucket so the operator can scan + filter without translating
 * `active=false + deleted_at=null` themselves.
 */
export type VoucherStatus = 'active' | 'inactive' | 'exhausted' | 'revoked'

export function voucherStatus(v: Voucher): VoucherStatus {
  if (v.deleted_at) return 'revoked'
  if (!v.active) return 'inactive'
  if (v.max_redemptions > 0 && v.times_redeemed >= v.max_redemptions) {
    return 'exhausted'
  }
  return 'active'
}

export interface IssueVoucherRequest {
  /** Voucher code (uppercased server-side on save). */
  code: string
  /** Credit amount in OMR (integer). */
  credit_omr: number
  description?: string
  active?: boolean
  /** 0 = unlimited (server default). */
  max_redemptions?: number
  /** Optional — fires a one-shot "voucher-issued" email via notification
   *  service. Not persisted on the row. */
  recipient_email?: string
}

const VOUCHERS_BASE = `${API_BASE}/v1/sme/billing/vouchers`

/**
 * listVouchers — GET /v1/sme/billing/vouchers/list. Returns live + soft-
 * deleted rows (the BE filter omits soft-deleted; the table renders
 * tombstones only when the BE chooses to include them in a future
 * audit-view expansion). Throws on non-2xx so the page can render the
 * error inline.
 */
export async function listVouchers(): Promise<Voucher[]> {
  const res = await authedFetch(`${VOUCHERS_BASE}/list`, {
    headers: { Accept: 'application/json' },
  })
  if (!res.ok) {
    throw new Error(`list vouchers: HTTP ${res.status}`)
  }
  const body = (await res.json()) as Voucher[] | { items?: Voucher[] } | null
  if (!body) return []
  if (Array.isArray(body)) return body
  return body.items ?? []
}

/**
 * issueVoucher — POST /v1/sme/billing/vouchers/issue. Upserts (re-issue
 * of the same code resurrects a soft-deleted row per #91). Returns the
 * persisted Voucher row on success. Surfaces the BE's `detail` / `error`
 * field on non-2xx so the modal shows the registrar's actual message.
 */
export async function issueVoucher(req: IssueVoucherRequest): Promise<Voucher> {
  const res = await authedFetch(`${VOUCHERS_BASE}/issue`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify(req),
  })
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // non-JSON body — keep the status-line message
    }
    throw new Error(`issue voucher: ${detail}`)
  }
  return (await res.json()) as Voucher
}

/**
 * revokeVoucher — DELETE /v1/sme/billing/vouchers/revoke/{code}. Soft-
 * deletes (preserves the audit trail for promo_redemptions FK). Past
 * redemptions remain attributed; only NEW redemptions are blocked.
 */
export async function revokeVoucher(code: string): Promise<void> {
  const res = await authedFetch(
    `${VOUCHERS_BASE}/revoke/${encodeURIComponent(code)}`,
    {
      method: 'DELETE',
      headers: { Accept: 'application/json' },
    },
  )
  if (!res.ok && res.status !== 204) {
    let detail = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // ignore
    }
    throw new Error(`revoke voucher: ${detail}`)
  }
}

/* ── Orders (Wave 6 PR 3) ────────────────────────────────────────── */

export type OrderStatus = 'pending' | 'completed' | 'failed' | 'cancelled'

export interface Order {
  /** Stable per-order id (e.g. `ord_01HX...`); used as the row key
   *  and as the drill-in URL slug. */
  id: string
  /** Tenant organisation that placed the order. Empty string when the
   *  BE hasn't projected the tenant join yet (rare; renders an em-dash). */
  tenantOrg: string
  /** Marketplace catalogue item the order is for. */
  product: string
  status: OrderStatus
  /** ISO-8601 creation timestamp. Empty string is tolerated and renders
   *  as an em-dash so the table never blows up on malformed rows. */
  createdAt: string
  /** ISO-8601 last-status-change timestamp. Empty string tolerated. */
  updatedAt: string
  /** Order total in cents. */
  totalCents: number
  /** ISO-4217 currency code; defaults to USD when absent. */
  currency: string
}

export interface OrdersResponse {
  /** True when the BE returned a non-2xx — the table still renders but
   *  surfaces the "API pending" pill (mirrors BssLandingPage). */
  pendingApi: boolean
  orders: Order[]
}

const EMPTY_ORDERS: OrdersResponse = { pendingApi: true, orders: [] }

/**
 * getOrders — fetch the BSS Orders list for the per-section page.
 *
 * Mirrors getBssOverview: tolerates 404 / 5xx / network error by
 * returning `{ pendingApi: true, orders: [] }` so OrdersPage renders
 * its full table chrome + empty state on first paint with the "API
 * pending" pill in the toolbar (per INVIOLABLE-PRINCIPLES.md #1 —
 * waterfall, first paint is the target-state shape).
 *
 * Backend wire path (when shipped):
 *   browser ──/api/v1/sme/orders──▶ catalyst-api ──▶ sme orders rollup
 */
export async function getOrders(): Promise<OrdersResponse> {
  let res: Response
  try {
    res = await authedFetch(`${API_BASE}/v1/sme/orders`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return EMPTY_ORDERS
  }
  if (!res.ok) {
    return EMPTY_ORDERS
  }
  try {
    const body = (await res.json()) as { orders?: unknown } | null
    if (!body || typeof body !== 'object' || !Array.isArray(body.orders)) {
      return { pendingApi: false, orders: [] }
    }
    const orders: Order[] = body.orders
      .map((raw): Order | null => {
        if (!raw || typeof raw !== 'object') return null
        const r = raw as Record<string, unknown>
        const id = typeof r.id === 'string' ? r.id : ''
        if (id === '') return null
        const status = normalizeOrderStatus(r.status)
        return {
          id,
          tenantOrg: typeof r.tenantOrg === 'string' ? r.tenantOrg : '',
          product: typeof r.product === 'string' ? r.product : '',
          status,
          createdAt: typeof r.createdAt === 'string' ? r.createdAt : '',
          updatedAt: typeof r.updatedAt === 'string' ? r.updatedAt : '',
          totalCents:
            typeof r.totalCents === 'number' && Number.isFinite(r.totalCents)
              ? r.totalCents
              : 0,
          currency:
            typeof r.currency === 'string' && r.currency !== '' ? r.currency : 'USD',
        }
      })
      .filter((o): o is Order => o !== null)
    return { pendingApi: false, orders }
  } catch {
    return EMPTY_ORDERS
  }
}

function normalizeOrderStatus(raw: unknown): OrderStatus {
  if (typeof raw !== 'string') return 'pending'
  const s = raw.toLowerCase()
  if (s === 'pending' || s === 'completed' || s === 'failed' || s === 'cancelled') {
    return s
  }
  return 'pending'
}

/* ── Billing subscriptions (Wave 6 PR 2) ─────────────────────────── */

export type SubscriptionStatus = 'paid' | 'trial' | 'overdue' | 'cancelled'

export interface Subscription {
  /** Stable per-subscription id; used as the row key. */
  id: string
  /** Tenant organisation that holds the subscription. Empty string when
   *  the BE hasn't projected the org join yet (rare; renders an em-dash). */
  tenantOrg: string
  /** Plan label (Starter / Pro / Enterprise / etc.). */
  plan: string
  /** Monthly recurring amount in cents. */
  mrrCents: number
  /** ISO-4217 currency code; defaults to USD when absent. */
  currency: string
  /** ISO-8601 last-paid timestamp. Empty string is tolerated and renders
   *  as an em-dash so the table never blows up on malformed rows. */
  lastPaidAt: string
  status: SubscriptionStatus
}

export interface SubscriptionsResponse {
  /** True when the BE returned a non-2xx — the table still renders but
   *  surfaces the "API pending" pill (mirrors BssLandingPage). */
  pendingApi: boolean
  subscriptions: Subscription[]
}

const EMPTY_SUBSCRIPTIONS: SubscriptionsResponse = {
  pendingApi: true,
  subscriptions: [],
}

/**
 * getBillingSubscriptions — fetch the BSS Billing subscription list.
 *
 * Mirrors getBssOverview / getOrders: tolerates 404 / 5xx / network
 * error by returning `{ pendingApi: true, subscriptions: [] }` so the
 * BillingPage can render its full table chrome + empty state on first
 * paint, surfacing the "API pending" pill in the toolbar (per
 * INVIOLABLE-PRINCIPLES.md #1 — waterfall, first paint is the
 * target-state shape).
 *
 * Backend wire path (when shipped):
 *   browser ──/api/v1/sme/billing/subscriptions──▶ catalyst-api
 */
export async function getBillingSubscriptions(): Promise<SubscriptionsResponse> {
  let res: Response
  try {
    res = await authedFetch(`${API_BASE}/v1/sme/billing/subscriptions`, {
      headers: { Accept: 'application/json' },
    })
  } catch {
    return EMPTY_SUBSCRIPTIONS
  }
  if (!res.ok) {
    return EMPTY_SUBSCRIPTIONS
  }
  try {
    const body = (await res.json()) as { subscriptions?: unknown } | null
    if (!body || typeof body !== 'object' || !Array.isArray(body.subscriptions)) {
      return { pendingApi: false, subscriptions: [] }
    }
    const subscriptions: Subscription[] = body.subscriptions
      .map((raw): Subscription | null => {
        if (!raw || typeof raw !== 'object') return null
        const r = raw as Record<string, unknown>
        const id = typeof r.id === 'string' ? r.id : ''
        if (id === '') return null
        return {
          id,
          tenantOrg: typeof r.tenantOrg === 'string' ? r.tenantOrg : '',
          plan: typeof r.plan === 'string' ? r.plan : '',
          mrrCents:
            typeof r.mrrCents === 'number' && Number.isFinite(r.mrrCents)
              ? r.mrrCents
              : 0,
          currency:
            typeof r.currency === 'string' && r.currency !== '' ? r.currency : 'USD',
          lastPaidAt: typeof r.lastPaidAt === 'string' ? r.lastPaidAt : '',
          status: normalizeSubscriptionStatus(r.status),
        }
      })
      .filter((s): s is Subscription => s !== null)
    return { pendingApi: false, subscriptions }
  } catch {
    return EMPTY_SUBSCRIPTIONS
  }
}

function normalizeSubscriptionStatus(raw: unknown): SubscriptionStatus {
  if (typeof raw !== 'string') return 'paid'
  const s = raw.toLowerCase()
  if (s === 'paid' || s === 'trial' || s === 'overdue' || s === 'cancelled') {
    return s
  }
  return 'paid'
}
