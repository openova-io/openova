import { describe, expect, it } from 'vitest'
import { discountPreview, discountState, validateDiscount } from './discounts'

const groups = [
  { key: 'ecs.s6.large.2', total: 400 },
  { key: 'evs.ssd.gb', total: 100 },
]
const total = 500

describe('discountPreview', () => {
  it('takes a percentage off only the matching SKU', () => {
    const p = discountPreview({ kind: 'percent', value: 15, sku: 'ecs.s6.large.2' }, groups, total)
    expect(p.base).toBe(400)
    expect(p.saving).toBeCloseTo(60)
    expect(p.matched).toBe(true)
  })
  it('takes a percentage off the whole bill when no SKU is set', () => {
    const p = discountPreview({ kind: 'percent', value: 10, sku: '' }, groups, total)
    expect(p.base).toBe(500)
    expect(p.saving).toBeCloseTo(50)
  })
  // A fixed amount larger than the lines it applies to must not push the
  // bill negative: the saving is capped at the base, as the rating engine does.
  it('caps a fixed amount at the matching subtotal', () => {
    expect(discountPreview({ kind: 'fixed', value: 250, sku: 'evs.ssd.gb' }, groups, total).saving).toBe(100)
    expect(discountPreview({ kind: 'fixed', value: 250, sku: null }, groups, total).saving).toBe(250)
  })
  it('reports an unmatched SKU as zero effect, not as a whole-bill discount', () => {
    const p = discountPreview({ kind: 'percent', value: 50, sku: 'rds.mysql' }, groups, total)
    expect(p.base).toBe(0)
    expect(p.saving).toBe(0)
    expect(p.matched).toBe(false)
  })
  it('clamps nonsense input instead of producing a negative or >100 % saving', () => {
    expect(discountPreview({ kind: 'percent', value: 150, sku: '' }, groups, total).saving).toBe(500)
    expect(discountPreview({ kind: 'percent', value: -5, sku: '' }, groups, total).saving).toBe(0)
    expect(discountPreview({ kind: 'fixed', value: NaN, sku: '' }, groups, total).saving).toBe(0)
    expect(discountPreview({ kind: 'other', value: 10, sku: '' }, groups, total).saving).toBe(0)
  })
})

describe('discountState', () => {
  const now = new Date('2026-09-07T12:00:00Z')
  it('is inactive whenever the switch is off, regardless of dates', () => {
    expect(discountState({ active: false, starts_at: null, ends_at: null }, now)).toBe('inactive')
    expect(discountState({ active: false, starts_at: '2026-09-01T00:00:00Z', ends_at: '2026-09-30T00:00:00Z' }, now)).toBe('inactive')
  })
  it('is scheduled before the window, active inside it, ended after it', () => {
    expect(discountState({ active: true, starts_at: '2026-10-01T00:00:00Z', ends_at: null }, now)).toBe('scheduled')
    expect(discountState({ active: true, starts_at: '2026-09-01T00:00:00Z', ends_at: '2026-09-30T00:00:00Z' }, now)).toBe('active')
    expect(discountState({ active: true, starts_at: null, ends_at: '2026-09-07T12:00:00Z' }, now)).toBe('ended')
  })
  it('is active with no window at all', () => {
    expect(discountState({ active: true, starts_at: null, ends_at: null }, now)).toBe('active')
  })
})

describe('validateDiscount', () => {
  const ok = { name: 'Launch', kind: 'percent', value: '15', starts_at: '', ends_at: '' }
  it('accepts a sane percent and a sane fixed amount', () => {
    expect(validateDiscount(ok)).toEqual({})
    expect(validateDiscount({ ...ok, kind: 'fixed', value: '500' })).toEqual({})
  })
  it('rejects percent outside (0, 100] and non-numeric values', () => {
    expect(validateDiscount({ ...ok, value: '0' }).value).toBeTruthy()
    expect(validateDiscount({ ...ok, value: '101' }).value).toBeTruthy()
    expect(validateDiscount({ ...ok, value: '100' }).value).toBeUndefined()
    expect(validateDiscount({ ...ok, value: 'abc' }).value).toBeTruthy()
    expect(validateDiscount({ ...ok, value: '' }).value).toBeTruthy()
  })
  it('rejects a window that ends before it starts, and a missing name', () => {
    expect(validateDiscount({ ...ok, starts_at: '2026-09-10', ends_at: '2026-09-01' }).ends_at).toBeTruthy()
    expect(validateDiscount({ ...ok, name: '  ' }).name).toBeTruthy()
  })
})
