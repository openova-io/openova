import { describe, expect, it } from 'vitest'
import type { Contract, PriceItem, PriceTier } from '../api/types'
import { bandsOf, committedUnitPrice, contractFloor, contractItemText, daysToEnd, explainItem, hasShape, isCommitmentKind, noticeFrom, priceQuantity, quantityReading, renewalDate, renewalDue, termEnd, termText, tierProblem } from './contracts'

// The console's half of DESIGN.md §15. Two things are load-bearing here and
// both are asserted against the Go engine's own pinned figures: the WORDS
// under a tiered row must say what the engine charges, and the sample price
// the editor shows must be the price the engine would produce.

const ladder: PriceTier[] = [
  { up_to: '10240', price: '0.010' },
  { up_to: '102400', price: '0.008' },
  { up_to: null, price: '0.006' },
]

const tiered: PriceItem = { sku: 'object_gib', unit: 'gb-month', unit_price: '0.010', tier_mode: 'graduated', tiers: ladder }
const allowanceItem: PriceItem = { sku: 'eip.traffic_gb', unit: 'gb', unit_price: '0.05', allowance: '50' }

describe('terms and renewal dates', () => {
  it('a term ends the day BEFORE its anniversary, so it never overlaps its own renewal', () => {
    expect(termEnd('2026-01-01', 12)).toBe('2026-12-31')
    expect(termEnd('2026-01-01', 24)).toBe('2027-12-31')
    expect(termEnd('2026-03-15', 6)).toBe('2026-09-14')
    // A start date the anniversary month does not have ROLLS FORWARD, which
    // is what Go's AddDate does on the server: 29 February plus twelve
    // months is 1 March, so the term ends 28 February; 31 January plus one
    // month is 3 March, so the term ends 2 March. The preview and the date
    // the server writes must agree, and the server is the one that writes it.
    expect(termEnd('2024-02-29', 12)).toBe('2025-02-28')
    expect(termEnd('2026-01-31', 1)).toBe('2026-03-02')
    // Refusals, never a guess.
    expect(termEnd('', 12)).toBe('')
    expect(termEnd('2026-01-01', 0)).toBe('')
  })

  it('the renewal date and the notice window', () => {
    const c: Contract = {
      id: 'ct1', customer_id: 'c1', name: 'ACME 2026', starts_on: '2026-01-01', ends_on: '2026-12-31',
      term_months: 12, auto_renew: true, renewal_notice_days: 30, currency: 'OMR', status: 'active',
    }
    expect(renewalDate(c)).toBe('2027-01-01')
    expect(noticeFrom(c)).toBe('2026-12-01')
    // Outside the window, on its first day, and on the end date itself.
    expect(renewalDue(c, '2026-11-30')).toBe(false)
    expect(renewalDue(c, '2026-12-01')).toBe(true)
    expect(renewalDue(c, '2026-12-31')).toBe(true)
    expect(renewalDue(c, '2027-01-01')).toBe(false)
    // Only an ACTIVE contract is ever due.
    expect(renewalDue({ ...c, status: 'draft' }, '2026-12-15')).toBe(false)
    expect(renewalDue({ ...c, status: 'expired' }, '2026-12-15')).toBe(false)
    expect(daysToEnd(c, '2026-12-01')).toBe(30)
    expect(daysToEnd(c, '2027-01-15')).toBe(-15)
    expect(termText(c)).toBe('12 months · 2026-01-01 → 2026-12-31')
  })
})

describe('the tier ladder', () => {
  it('normalises the bands the way the engine reads them', () => {
    expect(bandsOf(tiered)).toEqual([
      { from: 0, upTo: 10240, price: 0.01 },
      { from: 10240, upTo: 102400, price: 0.008 },
      { from: 102400, upTo: null, price: 0.006 },
    ])
    // A ladder that STOPS is completed by carrying its last price upward,
    // rather than leaving volume above the last bound rating at nothing.
    expect(bandsOf({ tiers: [{ up_to: '100', price: '1' }] })).toEqual([
      { from: 0, upTo: 100, price: 1 },
      { from: 100, upTo: null, price: 1 },
    ])
    expect(bandsOf({ tiers: [] })).toEqual([])
  })

  it('names the problem with a ladder the engine would refuse', () => {
    expect(tierProblem(ladder)).toBe('')
    expect(tierProblem([{ up_to: '100', price: '1' }, { up_to: '50', price: '0.5' }])).toContain('not above the band before it')
    expect(tierProblem([{ up_to: '100', price: '1' }, { up_to: '100', price: '0.5' }])).toContain('not above the band before it')
    expect(tierProblem([{ up_to: null, price: '1' }, { up_to: '100', price: '0.5' }])).toContain('must be last')
    expect(tierProblem([{ up_to: '100', price: '-1' }])).toContain('below zero')
  })
})

describe('the explained price says what the engine charges', () => {
  it('a graduated ladder, band by band', () => {
    // Word for word the sentence rating.ExplainItem produces; the Go test
    // TestExplainItem asserts the same string from the other side.
    expect(explainItem(tiered, 'OMR')).toBe(
      'Each band rates at its own price: up to 10240 at 0.01 OMR per gb-month, 10240-102400 at 0.008 OMR per gb-month, above 102400 at 0.006 OMR per gb-month.',
    )
  })

  it('all-units says the whole quantity moves', () => {
    const s = explainItem({ ...tiered, tier_mode: 'all_units' }, 'OMR')
    expect(s).toContain('The WHOLE billable quantity rates at the band it reaches')
    expect(s).toContain('up to 10240 at 0.01 OMR per gb-month')
  })

  it('an allowance says whether it lapses, and what the excess costs', () => {
    expect(explainItem(allowanceItem, 'OMR')).toBe('The first 50 gb each period are included; unused allowance lapses at the end of the period; the rest at 0.05 OMR per gb.')
    expect(explainItem({ ...allowanceItem, allowance_rollover: true }, 'OMR')).toContain('carries into the next period, once')
  })

  it('an item with no shape still reads as a price', () => {
    expect(explainItem({ sku: 'ecs.a', unit: 'hour', unit_price: '0.5' }, 'OMR')).toBe('The rest at 0.5 OMR per hour.')
    expect(hasShape(tiered)).toBe(true)
    expect(hasShape(allowanceItem)).toBe(true)
    expect(hasShape({})).toBe(false)
  })
})

describe('the sample price matches the engine to the last unit', () => {
  it('graduated and all-units differ on the same volume', () => {
    // The figures TestGraduatedAndAllUnitsDifferOnTheSameVolume pins in Go.
    expect(priceQuantity(tiered, 51200)).toBe(430.08)
    expect(priceQuantity({ ...tiered, tier_mode: 'all_units' }, 51200)).toBe(409.6)
    // On a boundary the two agree; one unit past it they do not.
    expect(priceQuantity(tiered, 10240)).toBe(102.4)
    expect(priceQuantity({ ...tiered, tier_mode: 'all_units' }, 10240)).toBe(102.4)
    expect(priceQuantity(tiered, 10241)).toBe(102.408)
    expect(priceQuantity({ ...tiered, tier_mode: 'all_units' }, 10241)).toBe(81.928)
  })

  it('the allowance comes off the quantity first', () => {
    expect(priceQuantity(allowanceItem, 30)).toBe(0)
    expect(priceQuantity(allowanceItem, 50)).toBe(0)
    expect(priceQuantity(allowanceItem, 130)).toBe(4)
    // The order of operations, as in TestOrderOfOperations: an allowance of
    // 100 against 200 units of a 1.00/0.10 ladder costs 100, not 10 — the
    // included quantity RESETS the ladder.
    const both: PriceItem = {
      sku: 'x', unit: 'gb', unit_price: '1.00', tier_mode: 'graduated', allowance: '100',
      tiers: [{ up_to: '100', price: '1.00' }, { up_to: null, price: '0.10' }],
    }
    expect(priceQuantity(both, 200)).toBe(100)
  })

  it('refuses a quantity it cannot price rather than showing a number', () => {
    expect(priceQuantity(tiered, -1)).toBeNull()
    expect(priceQuantity(tiered, Number.NaN)).toBeNull()
  })
})

describe('a contract line in words', () => {
  it('a commitment states its rate and what happens above it', () => {
    expect(contractItemText({ kind: 'commitment', sku: 'ecs.m7n.2xlarge.8', unit: 'instance-hour', quantity: '7440', discount_pct: '30' }, 'OMR')).toBe(
      '7440 instance-hour of ecs.m7n.2xlarge.8 committed each period at 30 % off list; anything above it at list.',
    )
    expect(contractItemText({ kind: 'commitment', sku: 'ecs.a', unit: 'hour', quantity: '100', committed_price: '0.35' }, 'OMR')).toContain('at 0.35 OMR per hour')
    // A line with neither is called incomplete, not shown as free.
    expect(contractItemText({ kind: 'commitment', sku: 'ecs.a', unit: 'hour', quantity: '100' }, 'OMR')).toContain('the line is incomplete')
  })

  it('an allowance states whether it carries over', () => {
    expect(contractItemText({ kind: 'allowance', sku: 'eip.traffic_gb', unit: 'gb', quantity: '100' }, 'OMR')).toContain('lapses at the end of each period')
    expect(contractItemText({ kind: 'allowance', sku: 'eip.traffic_gb', unit: 'gb', quantity: '100', rollover: true }, 'OMR')).toContain('carried into the next period if unused')
  })
})

describe('a spend commitment', () => {
  it('states the amount, the percentage and the true-up, in words', () => {
    expect(contractItemText({ kind: 'spend', sku: '', quantity: '0', amount: '1000', discount_pct: '50' }, 'OMR')).toBe(
      '1000 OMR committed each period, whatever is used, for 50 % off everything on the bill; a period below it carries a true-up line.',
    )
  })

  it('the floor is the larger of the header minimum and the spend amount, and says which', () => {
    const spend = { kind: 'spend', sku: '', quantity: '0', amount: '1000', discount_pct: '50' }
    expect(contractFloor({ minimum_commitment: null, items: [] })).toEqual({ amount: 0, source: 'none', minimum: 0, spend: 0 })
    expect(contractFloor({ minimum_commitment: '1500', items: [] })).toEqual({ amount: 1500, source: 'minimum', minimum: 1500, spend: 0 })
    expect(contractFloor({ minimum_commitment: null, items: [spend] })).toEqual({ amount: 1000, source: 'spend', minimum: 0, spend: 1000 })
    expect(contractFloor({ minimum_commitment: '1500', items: [spend] })).toEqual({ amount: 1500, source: 'both', minimum: 1500, spend: 1000 })
    expect(contractFloor({ minimum_commitment: '800', items: [spend] })).toEqual({ amount: 1000, source: 'both', minimum: 800, spend: 1000 })
    expect(isCommitmentKind('spend')).toBe(true)
    expect(isCommitmentKind('commitment')).toBe(true)
    expect(isCommitmentKind('allowance')).toBe(false)
  })

  it('the unit price a committed-use line results in, from the list price', () => {
    expect(committedUnitPrice(0.15213927, { discount_pct: '30' })).toBe(0.10649749)
    expect(committedUnitPrice(0.5, { committed_price: '0.3', discount_pct: '30' })).toBe(0.3)
    expect(committedUnitPrice(null, { discount_pct: '30' })).toBeNull()
    expect(committedUnitPrice(0.5, { discount_pct: '' })).toBeNull()
  })
})

describe('the human reading of a time-integrated quantity', () => {
  it('reads every per-hour unit as "this many, always on", at 730 hours a month', () => {
    expect(quantityReading('744000', 'gb-hour')).toBe('≈ 1,019 GB always on')
    expect(quantityReading(2976000, 'gb-hour')).toBe('≈ 4,077 GB always on')
    expect(quantityReading('73000', 'gib-hour')).toBe('≈ 100 GiB always on')
    expect(quantityReading('7440', 'mbps-hour')).toBe('≈ 10 Mbps always on')
    expect(quantityReading('5952', 'instance-hour')).toBe('≈ 8.2 servers all month')
    expect(quantityReading('5840', 'instance-hour')).toBe('≈ 8 servers all month')
    expect(quantityReading('730', 'instance-hour')).toBe('≈ 1 server all month')
    expect(quantityReading('744', 'plan-hour')).toBe('≈ 1 plan all month')
    expect(quantityReading('1460', 'plan-hour')).toBe('≈ 2 plans all month')
    expect(quantityReading('2920', 'vcpu-hour')).toBe('≈ 4 vCPU always on')
    expect(quantityReading('3285', 'vcpu-hour')).toBe('≈ 4.5 vCPU always on')
  })

  it('rounds the way a person would: whole GB and Mbps, one decimal on counts only when not whole', () => {
    expect(quantityReading('365', 'gb-hour')).toBe('≈ 1 GB always on') // 0.5 rounds up
    expect(quantityReading('100', 'gb-hour')).toBe('≈ 0 GB always on') // 0.137 GB is nothing always on
    expect(quantityReading('7300.5', 'instance-hour')).toBe('≈ 10 servers all month') // 10.0007 → whole
    expect(quantityReading('7373', 'instance-hour')).toBe('≈ 10.1 servers all month')
    expect(quantityReading('1460', 'GB-Hour')).toBe('≈ 2 GB always on') // the unit's case does not matter
  })

  it('has nothing to say about a unit that is not per hour, or about no quantity', () => {
    expect(quantityReading('100', 'gb')).toBe('')
    expect(quantityReading('100', 'hour')).toBe('')
    expect(quantityReading('100', 'period')).toBe('')
    expect(quantityReading('100', '')).toBe('')
    expect(quantityReading('0', 'gb-hour')).toBe('')
    expect(quantityReading('', 'gb-hour')).toBe('')
    expect(quantityReading(null, 'gb-hour')).toBe('')
  })
})
