import { describe, expect, it } from 'vitest'
import type { MarginReport, PriceItem } from '../api/types'
import { belowBuyOf, marginByCustomer, marginPct, marginTable, marginTotals, markupFor, partnerTerms, previewRetail, serviceOf, validateRetailRule } from './partners'

// The console's derivations must equal the server's (DESIGN.md §11): a
// preview that disagrees with the book it saves is worse than no preview.

const items: PriceItem[] = [
  { sku: 'ecs.m7n.xlarge.8', unit: 'instance-hour', unit_price: 100 },
  { sku: 'evs.ssd.gb', unit: 'gb-hour', unit_price: 10 },
]

describe('the retail-rule preview', () => {
  it('base = buy with a 5 % markup: list 100 − tier 30 % = buy 70, retail 73.5', () => {
    const rows = previewRetail(items, 30, { base: 'buy', markup_pct: 5 })
    expect(rows[0].list).toBe(100)
    expect(rows[0].buy).toBe(70)
    expect(rows[0].retail).toBeCloseTo(73.5, 8)
    expect(rows[0].margin).toBeCloseTo(3.5, 8)
    expect(rows[0].belowBuy).toBe(false)
    // The second SKU follows the same rule at its own list price.
    expect(rows[1].buy).toBeCloseTo(7, 8)
    expect(rows[1].retail).toBeCloseTo(7.35, 8)
  })

  it('base = list with a 5 % markup is 105, whatever the partner pays', () => {
    const rows = previewRetail(items, 30, { base: 'list', markup_pct: 5 })
    expect(rows[0].retail).toBeCloseTo(105, 8)
    expect(rows[0].buy).toBe(70)
  })

  it('a markup negative enough prices BELOW the buy price, and says so', () => {
    const rows = previewRetail(items, 30, { base: 'buy', markup_pct: -10 })
    expect(rows[0].retail).toBeCloseTo(63, 8)
    expect(rows[0].belowBuy).toBe(true)
    expect(rows[0].margin).toBeCloseTo(-7, 8)
    const below = belowBuyOf(rows)
    expect(below).toHaveLength(2)
    expect(below[0]).toMatchObject({ sku: 'ecs.m7n.xlarge.8', buy_unit_price: 70 })
    expect(below[0].retail_unit_price).toBeCloseTo(63, 8)
  })

  it('never prices below zero', () => {
    expect(previewRetail(items, 0, { base: 'list', markup_pct: -100 })[0].retail).toBe(0)
  })

  it('an override is most specific first: sku beats service beats the default', () => {
    const rule = { base: 'buy' as const, markup_pct: 5, overrides: [{ scope: 'service', key: 'ecs', markup_pct: 20 }, { scope: 'sku', key: 'ecs.m7n.xlarge.8', markup_pct: 50 }] }
    expect(markupFor(rule, 'ecs.m7n.xlarge.8')).toBe(50)
    expect(markupFor(rule, 'ecs.other')).toBe(20)
    expect(markupFor(rule, 'evs.ssd.gb')).toBe(5)
    const rows = previewRetail(items, 30, rule)
    expect(rows[0].retail).toBeCloseTo(105, 8) // 70 × 1.5
    expect(rows[1].retail).toBeCloseTo(7.35, 8) // 7 × 1.05
  })

  it('names a service by the SKU it is the head of', () => {
    expect(serviceOf('ecs.s6.large.2')).toBe('ecs')
    expect(serviceOf('plan.s')).toBe('plan')
    expect(serviceOf('eip')).toBe('eip')
  })

  it('refuses a rule it cannot derive from', () => {
    expect(validateRetailRule({ base: 'buy', markup_pct: 5 })).toBe('')
    expect(validateRetailRule({ base: 'cost', markup_pct: 5 })).toMatch(/base/)
    expect(validateRetailRule({ base: 'buy', markup_pct: -150 })).toMatch(/−100/)
    expect(validateRetailRule({ base: 'buy', markup_pct: 5, overrides: [{ scope: 'sku', key: '', markup_pct: 1 }] })).toMatch(/override/)
    expect(
      validateRetailRule({ base: 'buy', markup_pct: 5, overrides: [{ scope: 'sku', key: 'a', markup_pct: 1 }, { scope: 'sku', key: 'a', markup_pct: 2 }] }),
    ).toMatch(/twice/)
  })
})

const report: MarginReport = {
  partner_id: 'p1',
  period: '2026-08',
  currency: 'OMR',
  rows: [
    { customer_id: 'c1', customer_name: 'Alpha', service: 'ecs', customer_net: 90, partner_buy: 70, margin: 20, margin_pct: 22.22 },
    { customer_id: 'c1', customer_name: 'Alpha', service: 'evs', customer_net: 10, partner_buy: 8, margin: 2, margin_pct: 20 },
    { customer_id: 'c2', customer_name: 'Bravo', service: 'ecs', customer_net: 50, partner_buy: 50, margin: 0, margin_pct: 0 },
  ],
  totals: { customer_id: '', customer_name: '', service: '', customer_net: 150, partner_buy: 128, margin: 22, margin_pct: 14.67 },
}

describe('the margin table', () => {
  it('derives the margin from net and buy, never from the sent figure', () => {
    const rows = marginTable(report)
    expect(rows).toHaveLength(3)
    expect(rows[0]).toMatchObject({ customer: 'Alpha', service: 'ecs', net: 90, buy: 70, margin: 20 })
    expect(rows[0].pct).toBeCloseTo(22.2222, 3)
    // A zero margin is a real answer, not a missing one.
    expect(rows[2].margin).toBe(0)
    expect(rows[2].pct).toBe(0)
  })

  it('totals what it shows', () => {
    const t = marginTotals(marginTable(report))
    expect(t.net).toBe(150)
    expect(t.buy).toBe(128)
    expect(t.margin).toBe(22)
    expect(t.pct).toBeCloseTo(14.6667, 3)
  })

  it('rolls up per customer, sorted by name', () => {
    const rows = marginByCustomer(marginTable(report))
    expect(rows.map((r) => r.customer)).toEqual(['Alpha', 'Bravo'])
    expect(rows[0]).toMatchObject({ net: 100, buy: 78, margin: 22 })
    expect(rows[0].pct).toBeCloseTo(22, 6)
  })

  it('has no percentage when the net is zero', () => {
    expect(marginPct(0, 0)).toBeNull()
    expect(marginTable({ ...report, rows: [{ customer_id: 'c', customer_name: 'C', service: 's', customer_net: 0, partner_buy: 0, margin: 0 }] })[0].pct).toBeNull()
  })

  it('reads an empty report as an empty table', () => {
    expect(marginTable(null)).toEqual([])
    expect(marginTotals([])).toMatchObject({ net: 0, buy: 0, margin: 0, pct: null })
  })
})

describe('the partner directory line', () => {
  it('says the model and what sets the buy price', () => {
    expect(partnerTerms({ id: 'p', slug: 'p', name: 'P', bill_to: 'partner', status: 'active', tier_name: 'Gold' })).toBe('Resell · Gold')
    expect(partnerTerms({ id: 'p', slug: 'p', name: 'P', bill_to: 'customer', status: 'active', commission_pct: 20 })).toBe('Agent · 20 % commission')
    expect(partnerTerms({ id: 'p', slug: 'p', name: 'P', bill_to: 'partner', status: 'active' })).toBe('Resell · no tier')
  })
})
