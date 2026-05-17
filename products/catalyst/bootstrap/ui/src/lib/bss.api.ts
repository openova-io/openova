/**
 * lib/bss.api.ts — typed REST client for the Sovereign-side BSS overview.
 *
 * Wire path:
 *
 *   browser ──/api/v1/sme/bss/overview──▶ catalyst-api ──▶ per-tenant rollups
 *
 * The endpoint is the FE-facing rollup for the BSS landing KPI cards.
 * Until the BE handler (`handler/bss_overview.go`) lands, the client
 * tolerates 404 / 5xx by returning a zero-filled response with
 * `pendingApi: true` so the landing page still renders its full
 * target-state chrome on first paint (per INVIOLABLE-PRINCIPLES.md #1).
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
