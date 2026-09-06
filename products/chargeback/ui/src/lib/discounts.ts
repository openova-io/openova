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

// Discount helpers (DESIGN.md §2.6) — pure, unit-tested in discounts.test.ts.

export type DiscountState = 'active' | 'scheduled' | 'ended' | 'inactive'

/** Where a discount is in its life, derived from the switch and the campaign window. */
export function discountState(d: Pick<Discount, 'active' | 'starts_at' | 'ends_at'>, now = new Date()): DiscountState {
  if (!d.active) return 'inactive'
  const t = now.getTime()
  if (d.starts_at && new Date(d.starts_at).getTime() > t) return 'scheduled'
  if (d.ends_at && new Date(d.ends_at).getTime() <= t) return 'ended'
  return 'active'
}

export interface PreviewGroup {
  key: string
  total: number
}

export interface DiscountPreview {
  /** The list-price amount the discount applies to (the whole bill, or the SKU's lines). */
  base: number
  /** What the discount would take off `base`. */
  saving: number
  /** Whether a SKU scope found any usage to apply to. */
  matched: boolean
}

/**
 * The effect of a discount on a set of SKU totals — the same arithmetic the
 * rating engine applies at statement time (internal/store: percent off the
 * matching lines; fixed off the matching subtotal, never below zero).
 */
export function discountPreview(d: { kind: string; value: number; sku?: string | null }, groups: PreviewGroup[], total: number): DiscountPreview {
  const sku = (d.sku ?? '').trim()
  const matching = sku ? groups.filter((g) => g.key === sku) : null
  const base = matching ? matching.reduce((n, g) => n + g.total, 0) : Math.max(0, total)
  const matched = matching ? matching.length > 0 : true
  const value = Number.isFinite(d.value) ? d.value : 0
  let saving = 0
  if (d.kind === 'percent') saving = (base * Math.min(100, Math.max(0, value))) / 100
  else if (d.kind === 'fixed') saving = Math.min(Math.max(0, value), base)
  return { base, saving, matched }
}

export interface DiscountDraft {
  name: string
  kind: string
  value: string
  starts_at: string
  ends_at: string
}

/** Field-level validation for the create / edit form; empty object = valid. */
export function validateDiscount(d: DiscountDraft): Partial<Record<keyof DiscountDraft, string>> {
  const errors: Partial<Record<keyof DiscountDraft, string>> = {}
  if (!d.name.trim()) errors.name = 'Name is required'
  if (d.kind !== 'percent' && d.kind !== 'fixed') errors.kind = 'Kind must be percent or fixed'
  const v = Number(d.value)
  if (d.value.trim() === '' || !Number.isFinite(v)) errors.value = 'Value must be a number'
  else if (d.kind === 'percent' && (v <= 0 || v > 100)) errors.value = 'Percent must be above 0 and at most 100'
  else if (d.kind === 'fixed' && v <= 0) errors.value = 'Amount must be above 0'
  if (d.starts_at && d.ends_at && d.ends_at <= d.starts_at) errors.ends_at = 'End must be after start'
  return errors
}
