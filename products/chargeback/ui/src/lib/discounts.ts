import type { Discount } from '../api/types'
import { formatMoney, formatPct } from './money'

/** Display readers over a discount row (#6867). */

const n = (v: unknown): number => {
  const x = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(x) ? x : 0
}

export function isGlobalDiscount(d: Discount): boolean {
  return d.customer_id === null || d.customer_id === undefined || d.customer_id === ''
}

/** "10.0 %" for percent, "25.000 OMR" for fixed. */
export function discountValueText(d: Discount, currency: string): string {
  return d.kind === 'percent' ? formatPct(n(d.value)) : formatMoney(n(d.value), currency)
}

export function discountScopeText(d: Discount): string {
  return isGlobalDiscount(d) ? 'all customers' : (d.customer_name ?? 'this customer')
}

const day = (s: string | null | undefined) => (s ? s.slice(0, 10) : '')

/** "always" · "from 2026-09-01" · "until 2026-12-31" · "2026-09-01 → 2026-12-31". */
export function discountWindowText(d: Discount): string {
  const a = day(d.starts_at)
  const b = day(d.ends_at)
  if (a && b) return `${a} → ${b}`
  if (a) return `from ${a}`
  if (b) return `until ${b}`
  return 'always'
}

export type DiscountPhase = 'live' | 'scheduled' | 'ended' | 'inactive'

/** Whether the discount takes money off a bill run today. */
export function discountPhase(d: Discount, now = new Date()): DiscountPhase {
  if (!d.active) return 'inactive'
  const t = now.toISOString().slice(0, 10)
  const a = day(d.starts_at)
  const b = day(d.ends_at)
  if (a && t < a) return 'scheduled'
  if (b && t >= b) return 'ended'
  return 'live'
}

export function phaseTone(p: DiscountPhase): 'ok' | 'warn' | 'bad' | 'info' {
  return p === 'live' ? 'ok' : p === 'scheduled' ? 'info' : p === 'ended' ? 'warn' : 'bad'
}

/** Own discounts first, then global campaigns; live before inactive; newest first. */
export function sortDiscounts(rows: Discount[], now = new Date()): Discount[] {
  const rank = (d: Discount) => (isGlobalDiscount(d) ? 10 : 0) + (discountPhase(d, now) === 'live' ? 0 : 1)
  return [...rows].sort((a, b) => rank(a) - rank(b) || (b.created_at ?? '').localeCompare(a.created_at ?? ''))
}
