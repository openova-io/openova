// Discount helpers (DESIGN.md §2.6) — pure, unit-tested in discounts.test.ts.
import type { Discount } from '../api/types'

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
