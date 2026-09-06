import { describe, expect, it } from 'vitest'
import fixture from '../api/fixtures/summary.json'
import type { Summary } from '../api/types'
import { readDaily, readGroups, readKPIs } from './summary'

// The fixture is written by the Go side (internal/api/contract_test.go). If
// this test fails, one lane renamed a key: fix the reader or the writer,
// never the fixture by hand.
const s = fixture as unknown as Summary

describe('summary wire contract', () => {
  it('reads non-zero KPIs from the Go document', () => {
    const k = readKPIs(s)
    expect(k.currency).toBe('OMR')
    expect(k.mtd).toBeCloseTo(104.16, 6)
    expect(k.mtdDays).toBe(8)
    expect(k.forecastMonthEnd).toBeGreaterThan(k.mtd)
    expect(k.forecastConfidence).toBe('medium')
    expect(k.lastMonth).toBeCloseTo(42, 6)
    expect(k.lastMonthPeriod).toBe('2026-08')
    expect(k.momDeltaPct).not.toBeNull()
    expect(k.avgDaily).toBeGreaterThan(0)
    expect(k.resourcesLive).toBe(126)
    expect(k.unpricedCount).toBe(1)
    expect(k.customersActive).toBe(2)
    expect(k.sourcesVerified).toBe(3)
    expect(k.lastCollectedAt).toBeTruthy()
    expect(k.draftStatements).toBe(1)
    expect(k.issuedStatements).toBe(1)
  })
  it('reads the daily series with data flags', () => {
    const d = readDaily(s)
    expect(d.buckets.length).toBe(7)
    expect(d.values.every((v) => v > 0)).toBe(true)
    expect(d.missing.every((m) => m === false)).toBe(true)
  })
  it('reads by_customer and by_kind groups with labels and shares', () => {
    const c = readGroups(s.by_customer)
    expect(c.map((g) => g.label)).toEqual(['Acme', 'Bravo'])
    expect(c[0].value).toBeCloseTo(100.8, 6)
    const k = readGroups(s.by_kind)
    expect(k[0].label).toBe('Elastic Cloud Server')
    expect(k.find((g) => g.key === 'other')).toBeTruthy()
  })
  it('carries budgets and anomalies rows in the agreed shape', () => {
    expect(s.budgets[0].status).toBe('warning')
    expect(s.budgets[0].thresholds.map((t) => t.pct)).toEqual([50, 80, 100])
    expect(s.anomalies[0].drivers[0].key).toBe('vm-2')
  })
  it('tolerates a document with nothing in it (renders zeros, never throws)', () => {
    const k = readKPIs({} as Summary)
    expect(k.mtd).toBe(0)
    expect(k.forecastMonthEnd).toBeNull()
    expect(readDaily({} as Summary).buckets).toEqual([])
    expect(readGroups(undefined)).toEqual([])
  })
})
