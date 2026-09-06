import { describe, expect, it } from 'vitest'
import type { Recommendation } from '../api/types'
import {
  actionFor,
  filterFromParams,
  filterRecommendations,
  groupRecommendations,
  paramsFromFilter,
  severityCounts,
  sortRecommendations,
  totalSaving,
  typeMeta,
} from './recommendations'
import { customerLens, lensFor } from './scope'

const rec = (over: Partial<Recommendation>): Recommendation => ({
  id: over.id ?? Math.random().toString(36).slice(2),
  type: 'stopped-instance-billed',
  severity: 'medium',
  customer_id: 'c-1',
  customer_name: 'ACME',
  resource_id: 'r-1',
  resource_name: 'web-1',
  kind: 'ecs',
  title: 'Stopped 9 days, still billed',
  detail: 'ecs.s6.large.2 billed 730 h/month',
  monthly_saving: 12,
  currency: 'OMR',
  evidence: { source_id: 's-1' },
  ...over,
})

const operator = lensFor({ email: 'op@example.test', role: 'operator' })
const customer = lensFor({ email: 'c@example.test', role: 'customer-admin', customer_id: 'c-1' })
const pinned = customerLens('c-1')

describe('recommendation ranking', () => {
  it('sorts high → medium → low, then by saving, then by title', () => {
    const rows = sortRecommendations([
      rec({ id: 'a', severity: 'low', monthly_saving: 900 }),
      rec({ id: 'b', severity: 'high', monthly_saving: 1 }),
      rec({ id: 'c', severity: 'medium', monthly_saving: 5, title: 'B' }),
      rec({ id: 'd', severity: 'medium', monthly_saving: 5, title: 'A' }),
      rec({ id: 'e', severity: 'medium', monthly_saving: 50 }),
    ])
    expect(rows.map((r) => r.id)).toEqual(['b', 'e', 'd', 'c', 'a'])
  })
  it('an unknown severity ranks last, not first', () => {
    const rows = sortRecommendations([rec({ id: 'x', severity: 'critical' }), rec({ id: 'y', severity: 'low' })])
    expect(rows.map((r) => r.id)).toEqual(['y', 'x'])
  })
  it('groups by type, biggest total saving first, rule order for saving-less types', () => {
    const groups = groupRecommendations([
      rec({ type: 'unbound-eip', monthly_saving: 3 }),
      rec({ type: 'stopped-instance-billed', monthly_saving: 10 }),
      rec({ type: 'stopped-instance-billed', monthly_saving: 20 }),
      rec({ type: 'no-price-book', monthly_saving: 0, resource_id: null }),
      rec({ type: 'unpriced-sku', monthly_saving: 0, resource_id: null }),
    ])
    expect(groups.map((g) => g.type)).toEqual(['stopped-instance-billed', 'unbound-eip', 'unpriced-sku', 'no-price-book'])
    expect(groups[0].saving).toBe(30)
    expect(groups[0].rows.map((r) => r.monthly_saving)).toEqual([20, 10])
    expect(groups[0].meta.label).toBe('Stopped instances still billed')
  })
  it('counts severities and sums savings, ignoring non-finite values', () => {
    const rows = [rec({ severity: 'high' }), rec({ severity: 'high', monthly_saving: Number.NaN }), rec({ severity: 'low', monthly_saving: 0.5 })]
    expect(severityCounts(rows)).toEqual({ high: 2, medium: 0, low: 1 })
    expect(totalSaving(rows)).toBe(12.5)
  })
  it('names an unknown type without crashing', () => {
    expect(typeMeta('idle-nat-gateway').label).toBe('Idle nat gateway')
    expect(typeMeta('unbound-eip').rule).toMatch(/every hour/)
  })
})

describe('recommendation filter', () => {
  const rows = [rec({ id: 'a', severity: 'high', type: 'unbound-eip' }), rec({ id: 'b', severity: 'low', type: 'unbound-eip' }), rec({ id: 'c', severity: 'high', type: 'stale-source' })]
  it('empty filter keeps everything; both dimensions must match', () => {
    expect(filterRecommendations(rows, { severities: [], types: [] })).toHaveLength(3)
    expect(filterRecommendations(rows, { severities: ['high'], types: [] }).map((r) => r.id)).toEqual(['a', 'c'])
    expect(filterRecommendations(rows, { severities: ['high'], types: ['unbound-eip'] }).map((r) => r.id)).toEqual(['a'])
  })
  it('round-trips through the URL and drops junk severities', () => {
    const f = filterFromParams(new URLSearchParams('severity=high,urgent,low&type=unbound-eip'))
    expect(f).toEqual({ severities: ['high', 'low'], types: ['unbound-eip'] })
    expect(paramsFromFilter(f).toString()).toBe('severity=high%2Clow&type=unbound-eip')
    expect(paramsFromFilter({ severities: [], types: [] }).toString()).toBe('')
  })
})

describe('actionFor', () => {
  it('resource-shaped rows open the resource on every lens', () => {
    expect(actionFor(rec({}), operator)).toEqual({ to: '/resources/s-1/r-1', label: 'Open resource' })
    expect(actionFor(rec({}), pinned)?.to).toBe('/resources/s-1/r-1')
    expect(actionFor(rec({}), customer)?.to).toBe('/my/resources/s-1/r-1')
  })
  it('without a source id the list search stands in for the detail route', () => {
    expect(actionFor(rec({ evidence: null }), operator)).toEqual({ to: '/resources?q=r-1', label: 'Find resource' })
    expect(actionFor(rec({ evidence: null }), pinned)?.to).toBe('/customers/c-1?tab=resources&q=r-1')
  })
  it('configuration rows go to the operator page that fixes them', () => {
    expect(actionFor(rec({ type: 'unpriced-sku', resource_id: null }), operator)?.to).toBe('/pricebooks')
    expect(actionFor(rec({ type: 'stale-source', resource_id: null }), operator)?.to).toBe('/customers/c-1?tab=sources')
    expect(actionFor(rec({ type: 'no-price-book', resource_id: null }), operator)?.to).toBe('/customers/c-1?tab=settings')
  })
  it('a customer cannot edit price books, but can look at its own sources', () => {
    expect(actionFor(rec({ type: 'unpriced-sku', resource_id: null }), customer)).toBeNull()
    expect(actionFor(rec({ type: 'no-price-book', resource_id: null }), customer)).toBeNull()
    expect(actionFor(rec({ type: 'stale-source', resource_id: null }), customer)?.to).toBe('/my/sources')
  })
  it('an unknown type without a resource has no link', () => {
    expect(actionFor(rec({ type: 'mystery', resource_id: null }), operator)).toBeNull()
  })
})
