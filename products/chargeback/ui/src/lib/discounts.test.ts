import { describe, expect, it } from 'vitest'
import type { Discount } from '../api/types'
import { discountPhase, discountScopeText, discountValueText, discountWindowText, isGlobalDiscount, sortDiscounts } from './discounts'

const d = (over: Partial<Discount>): Discount => ({
  id: 'd',
  customer_id: 'c-1',
  name: 'x',
  kind: 'percent',
  value: 10,
  sku: '',
  starts_at: null,
  ends_at: null,
  active: true,
  ...over,
})

describe('discount readers', () => {
  it('global = customer_id null; scope text names it', () => {
    expect(isGlobalDiscount(d({ customer_id: null }))).toBe(true)
    expect(isGlobalDiscount(d({}))).toBe(false)
    expect(discountScopeText(d({ customer_id: null }))).toBe('all customers')
    expect(discountScopeText(d({ customer_name: 'Acme' }))).toBe('Acme')
  })
  it('renders percent as a percentage and fixed as money in the customer currency', () => {
    expect(discountValueText(d({ kind: 'percent', value: 12.5 }), 'OMR')).toBe('12.5 %')
    expect(discountValueText(d({ kind: 'fixed', value: '25' as unknown as number }), 'OMR')).toBe('25.000 OMR')
  })
  it('describes the campaign window', () => {
    expect(discountWindowText(d({}))).toBe('always')
    expect(discountWindowText(d({ starts_at: '2026-09-01T00:00:00Z' }))).toBe('from 2026-09-01')
    expect(discountWindowText(d({ ends_at: '2026-12-31T00:00:00Z' }))).toBe('until 2026-12-31')
    expect(discountWindowText(d({ starts_at: '2026-09-01', ends_at: '2026-12-31' }))).toBe('2026-09-01 → 2026-12-31')
  })
  it('phase: inactive beats the window; scheduled before start; ended on the end day', () => {
    const now = new Date('2026-09-07T12:00:00Z')
    expect(discountPhase(d({ active: false, starts_at: '2026-01-01' }), now)).toBe('inactive')
    expect(discountPhase(d({ starts_at: '2026-10-01' }), now)).toBe('scheduled')
    expect(discountPhase(d({ ends_at: '2026-09-07' }), now)).toBe('ended')
    expect(discountPhase(d({ ends_at: '2026-09-08' }), now)).toBe('live')
    expect(discountPhase(d({}), now)).toBe('live')
  })
  it('sorts own live first, then own inactive, then global; newest first within', () => {
    const now = new Date('2026-09-07T12:00:00Z')
    const rows = [
      d({ id: 'g', customer_id: null, created_at: '2026-09-05' }),
      d({ id: 'own-old', created_at: '2026-09-01' }),
      d({ id: 'own-off', active: false, created_at: '2026-09-06' }),
      d({ id: 'own-new', created_at: '2026-09-03' }),
    ]
    expect(sortDiscounts(rows, now).map((r) => r.id)).toEqual(['own-new', 'own-old', 'own-off', 'g'])
  })
})
