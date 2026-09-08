import { describe, expect, it } from 'vitest'
import { DISCOUNT_RULES, RULE_EXAMPLE, combineDiscounts, discountRuleLabel, isDiscountRule, ruleExampleRows } from './discountRule'

// The discriminating fixture of internal/rating/discount_test.go: SKU A 50,
// SKU B 50; a global 10 % and a SKU-A 20 %. The numbers here are the engine's.
const lines = [
  { sku: 'A', amount: 50 },
  { sku: 'B', amount: 50 },
]
const global10 = { id: 'g', kind: 'percent', value: 10, sku: '' }
const skuA20 = { id: 'a', kind: 'percent', value: 20, sku: 'A' }

describe('combineDiscounts mirrors rating.ApplyDiscounts', () => {
  it('most-specific 15 · highest 15 · stack 20 · compound 19', () => {
    expect(combineDiscounts(lines, [global10, skuA20], 'most-specific').total).toBe(15)
    expect(combineDiscounts(lines, [global10, skuA20], 'highest').total).toBe(15)
    expect(combineDiscounts(lines, [global10, skuA20], 'stack').total).toBe(20)
    expect(combineDiscounts(lines, [global10, skuA20], 'compound').total).toBe(19)
    const ms = combineDiscounts(lines, [global10, skuA20], 'most-specific')
    expect(ms.percentByLine).toEqual([10, 5])
    expect(ms.byDiscount).toEqual({ g: 5, a: 10 })
    // The global still applied on B, so nothing is superseded.
    expect(ms.superseded).toEqual({})
    const cp = combineDiscounts(lines, [global10, skuA20], 'compound')
    // A: 20 % of 50 = 10, then 10 % of 40 = 4 → 14 (= 28 %); B: 5.
    expect(cp.percentByLine).toEqual([14, 5])
    expect(cp.byDiscount).toEqual({ a: 10, g: 9 })
  })
  it('global 30 % against SKU 20 %: highest 30, most-specific 25, and names the superseded one', () => {
    const global30 = { ...global10, value: 30 }
    const hi = combineDiscounts(lines, [global30, skuA20], 'highest')
    expect(hi.total).toBe(30)
    expect(hi.percentByLine).toEqual([15, 15])
    expect(hi.superseded).toEqual({ a: 'g' })
    expect(hi.byDiscount.a).toBe(0)
    const ms = combineDiscounts(lines, [global30, skuA20], 'most-specific')
    expect(ms.total).toBe(25)
    expect(ms.percentByLine).toEqual([10, 15])
    expect(ms.superseded).toEqual({})
    expect(combineDiscounts(lines, [global30, skuA20], 'stack').total).toBe(40)
    expect(combineDiscounts(lines, [global30, skuA20], 'compound').total).toBe(37)
  })
  it('a stackable SKU discount adds on top of the winner: 10 % + 20 % on A', () => {
    const stackable = { ...skuA20, stackable: true }
    const ms = combineDiscounts(lines, [global10, stackable], 'most-specific')
    expect(ms.total).toBe(20)
    expect(ms.percentByLine).toEqual([15, 5])
    expect(combineDiscounts(lines, [global10, stackable], 'highest').total).toBe(20)
    // The flag changes nothing under stack and compound.
    expect(combineDiscounts(lines, [global10, stackable], 'stack').total).toBe(20)
    expect(combineDiscounts(lines, [global10, stackable], 'compound').total).toBe(19)
  })
  it('fixed amounts come off after the percentages, clamped at the bill', () => {
    const credit = { id: 'f', kind: 'fixed', value: 100, sku: '' }
    const out = combineDiscounts(lines, [global10, skuA20, credit], 'most-specific')
    expect(out.total).toBe(100)
    expect(out.byDiscount.f).toBe(85)
    expect(combineDiscounts(lines, [global10, skuA20, { ...credit, value: 7 }], 'stack').total).toBe(27)
  })
  it('ignores zero, negative and non-numeric values', () => {
    const junk = [
      { id: 'z', kind: 'percent', value: 0 },
      { id: 'n', kind: 'percent', value: NaN },
      { id: 'neg', kind: 'fixed', value: -5 },
    ]
    expect(combineDiscounts(lines, junk, 'stack').total).toBe(0)
    expect(combineDiscounts([], [global10], 'stack').total).toBe(0)
  })
})

describe('rule catalogue', () => {
  it('lists the four rules with most-specific first', () => {
    expect(DISCOUNT_RULES.map((r) => r.value)).toEqual(['most-specific', 'highest', 'stack', 'compound'])
  })
  it('labels known rules, echoes unknown ones, blank for none', () => {
    expect(discountRuleLabel('most-specific')).toBe('Most specific wins')
    expect(discountRuleLabel('stack')).toBe('Stack')
    expect(discountRuleLabel('weird')).toBe('weird')
    expect(discountRuleLabel(null)).toBe('')
    expect(isDiscountRule('compound')).toBe(true)
    expect(isDiscountRule('average')).toBe(false)
  })
  it('the page example (list 100, 10 % global, 20 % on a SKU worth 50) reproduces the engine numbers', () => {
    expect(RULE_EXAMPLE.lines.reduce((n, l) => n + l.amount, 0)).toBe(100)
    expect(ruleExampleRows().map((r) => [r.rule, r.a, r.b, r.total])).toEqual([
      ['most-specific', 10, 5, 15],
      ['highest', 10, 5, 15],
      ['stack', 15, 5, 20],
      ['compound', 14, 5, 19],
    ])
  })
})
