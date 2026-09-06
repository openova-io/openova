import { describe, expect, it } from 'vitest'
import type { Anomaly } from '../api/types'
import { anomalyExplorerParams, anomalyKPIs, anomalyKey, anomalyRule, driverRows, keysForDay, sortAnomalies } from './anomalies'

const base: Anomaly = {
  day: '2026-09-03',
  customer_id: 'c-1',
  customer_name: 'ACME',
  dimension: 'kind',
  key: 'ecs',
  label: 'Elastic Cloud Server',
  expected: 100,
  actual: 180,
  impact: 80,
  score: 4.2,
  drivers: [
    { kind: 'sku', key: 'ecs.s6.large.2', label: 'ecs.s6.large.2', delta: 50 },
    { kind: 'resource', key: 'r-1', label: 'web-1', delta: 60 },
    { kind: 'sku', key: 'ecs.s6.small.1', label: 'ecs.s6.small.1', delta: -5 },
  ],
}

describe('anomaly helpers', () => {
  it('states the rule with the currency', () => {
    expect(anomalyRule('OMR')).toBe('No day exceeded 3σ of its 14-day baseline with ≥ 1 OMR impact')
    expect(anomalyRule('')).toBe('No day exceeded 3σ of its 14-day baseline with ≥ 1 unit impact')
  })
  it('keys a row by day, customer and dimension value', () => {
    expect(anomalyKey(base)).toBe('2026-09-03|c-1|kind|ecs')
    expect(anomalyKey({ ...base, key: 'evs' })).not.toBe(anomalyKey(base))
  })
  it('sorts newest first, then by impact', () => {
    const rows = sortAnomalies([
      { ...base, day: '2026-09-01', impact: 500 },
      { ...base, day: '2026-09-03', impact: 10, key: 'evs' },
      base,
    ])
    expect(rows.map((r) => `${r.day}:${r.impact}`)).toEqual(['2026-09-03:80', '2026-09-03:10', '2026-09-01:500'])
  })
  it('KPIs: count, total impact, and the day whose spikes add up to the most', () => {
    const k = anomalyKPIs([base, { ...base, key: 'evs', impact: 30 }, { ...base, day: '2026-09-01', impact: 100 }])
    expect(k.count).toBe(3)
    expect(k.totalImpact).toBe(210)
    // 80 + 30 on the 3rd beats 100 on the 1st — a single anomaly is not a day.
    expect(k.biggestDay).toEqual({ day: '2026-09-03', impact: 110 })
    expect(anomalyKPIs([]).biggestDay).toBeNull()
  })
  it('drivers rank by delta with the kind in the label, negatives last', () => {
    const rows = driverRows(base.drivers)
    expect(rows.map((r) => r.label)).toEqual(['Resource · web-1', 'SKU · ecs.s6.large.2', 'SKU · ecs.s6.small.1'])
    expect(rows.map((r) => r.value)).toEqual([60, 50, -5])
    expect(new Set(rows.map((r) => r.key)).size).toBe(3)
  })
  it('a SKU and a resource that share a key stay two bars', () => {
    const rows = driverRows([
      { kind: 'sku', key: 'x', label: 'x', delta: 1 },
      { kind: 'resource', key: 'x', label: 'x', delta: 1 },
    ])
    expect(rows).toHaveLength(2)
    expect(rows[0].key).not.toBe(rows[1].key)
  })
  it('tolerates no drivers', () => {
    expect(driverRows(null)).toEqual([])
    expect(driverRows([{ kind: 'sku', key: 'x', label: 'x', delta: Number.NaN }])).toEqual([])
  })
  it('explorer params pin the one day, by SKU, filtered to the flagged value (+ customer for the operator)', () => {
    const p = anomalyExplorerParams(base, true)
    expect(p.get('preset')).toBe('custom')
    expect(p.get('from')).toBe('2026-09-03')
    expect(p.get('to')).toBe('2026-09-04')
    expect(p.get('group_by')).toBe('sku')
    expect(p.get('kind')).toBe('ecs')
    expect(p.get('customer')).toBe('c-1')
    expect(anomalyExplorerParams(base, false).has('customer')).toBe(false)
  })
  it('honours a non-kind dimension', () => {
    expect(anomalyExplorerParams({ ...base, dimension: 'sku', key: 'eip' }, false).get('sku')).toBe('eip')
  })
  it('pre-expands the rows of ?day=', () => {
    const rows = [base, { ...base, key: 'evs' }, { ...base, day: '2026-09-01' }]
    expect(keysForDay(rows, '2026-09-03')).toEqual(['2026-09-03|c-1|kind|ecs', '2026-09-03|c-1|kind|evs'])
    expect(keysForDay(rows, null)).toEqual([])
  })
})
