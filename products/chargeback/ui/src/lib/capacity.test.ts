import { describe, expect, it } from 'vitest'
import type { CapacityOverview } from '../api/types'
import { asOfLabel, familyDef, footprintSummary, formatAmount, formatExhaustion, formatUtilisation, hasTotal, headroom, heatClass, parseFootprintForm, parseTotal, utilisationStatus, zoneRows } from './capacity'

// The threshold colouring is the server's rule (capacity.Status): unset
// without a total, warn from 70 %, critical from 85 %, and the boundaries
// belong to the higher class.
describe('utilisation thresholds', () => {
  it('classifies the boundaries like the server', () => {
    expect(utilisationStatus(null)).toBe('unset')
    expect(utilisationStatus(undefined)).toBe('unset')
    expect(utilisationStatus(Number.NaN)).toBe('unset')
    expect(utilisationStatus(0)).toBe('ok')
    expect(utilisationStatus(69.9)).toBe('ok')
    expect(utilisationStatus(70)).toBe('warn')
    expect(utilisationStatus(84.9)).toBe('warn')
    expect(utilisationStatus(85)).toBe('critical')
    expect(utilisationStatus(200)).toBe('critical')
  })
  it('follows the thresholds the document ships', () => {
    const t = { warn_pct: 50, critical_pct: 90 }
    expect(utilisationStatus(55, t)).toBe('warn')
    expect(utilisationStatus(89.9, t)).toBe('warn')
    expect(utilisationStatus(90, t)).toBe('critical')
  })
  it('maps a status onto the heat cell class', () => {
    expect(heatClass('ok')).toBe('heat ok')
    expect(heatClass('warn')).toBe('heat warn')
    expect(heatClass('critical')).toBe('heat critical')
    expect(heatClass('unset')).toBe('heat unset')
    expect(heatClass('anything-else')).toBe('heat unset')
  })
})

// The headroom rule, on the figures the Go integration test derives: zone a
// has 4 vCPU and 20 GiB left; m7n.2xlarge.8 (8 vCPU, 64 GiB) fits 0 more,
// bound by vCPU (first at the tie); m7n.xlarge.8 (4 vCPU, 32 GiB) fits 0,
// bound by memory; a family without a total is skipped; no totals → null.
describe('headroom', () => {
  const avail = { vcpu: 4, memory_gib: 20, block_ssd_gib: 820 }
  const totals = { vcpu: true, memory_gib: true, block_ssd_gib: true }
  it('takes the fewest units over the sized families and names the binding one', () => {
    expect(headroom({ vcpu: 8, memory_gib: 64 }, avail, totals)).toEqual({ units: 0, binding: 'vcpu' })
    expect(headroom({ vcpu: 4, memory_gib: 32 }, avail, totals)).toEqual({ units: 0, binding: 'memory_gib' })
    expect(headroom({ vcpu: 2, memory_gib: 4 }, avail, totals)).toEqual({ units: 2, binding: 'vcpu' })
    expect(headroom({ vcpu: '1', memory_gib: '8' }, avail, totals)).toEqual({ units: 2, binding: 'memory_gib' })
    expect(headroom({ block_ssd_gib: 1 }, avail, totals)).toEqual({ units: 820, binding: 'block_ssd_gib' })
  })
  it('skips a family without a total and is null when none has one', () => {
    expect(headroom({ vcpu: 8, memory_gib: 64 }, avail, { vcpu: true })).toEqual({ units: 0, binding: 'vcpu' })
    expect(headroom({ vcpu: 1, eip_addresses: 1 }, { vcpu: 40 }, { vcpu: true })).toEqual({ units: 40, binding: 'vcpu' })
    expect(headroom({ vcpu: 8, memory_gib: 64 }, avail, {})).toEqual({ units: null, binding: '' })
    expect(headroom({ eip_addresses: 1 }, { eip_addresses: 0 }, {})).toEqual({ units: null, binding: '' })
  })
  it('lets a cap bind only when it is the lower bound', () => {
    expect(headroom({ block_ssd_gib: 1 }, avail, totals, { total: 500, consumed: 180 })).toEqual({ units: 320, binding: 'cap' })
    expect(headroom({ block_ssd_gib: 1 }, avail, totals, { total: 5000, consumed: 180 })).toEqual({ units: 820, binding: 'block_ssd_gib' })
    expect(headroom({ block_ssd_gib: 1 }, avail, totals, { total: 100, consumed: 180 })).toEqual({ units: 0, binding: 'cap' })
    // A cap alone, with no sized family, still yields a number.
    expect(headroom({ eip_addresses: 1 }, {}, {}, { total: 10, consumed: 3 })).toEqual({ units: 7, binding: 'cap' })
  })
  it('never goes negative on a clamped pool', () => {
    expect(headroom({ vcpu: 2 }, { vcpu: -6 }, { vcpu: true })).toEqual({ units: 0, binding: 'vcpu' })
  })
})

describe('formatting', () => {
  it('footprints read in words, in family order', () => {
    expect(footprintSummary({ memory_gib: 64, vcpu: 8 })).toBe('8 vCPU · 64 GiB')
    expect(footprintSummary({ block_ssd_gib: '1.000000' })).toBe('1 GiB')
    expect(footprintSummary({ eip_addresses: 1, bandwidth_mbps: 0 })).toBe('1 addresses')
    expect(footprintSummary(null)).toBe('')
    expect(familyDef('vcpu').label).toBe('vCPU')
    expect(familyDef('vcpu', [{ family: 'vcpu', label: 'Cores', unit: 'core' }]).label).toBe('Cores')
    expect(familyDef('gpu').label).toBe('gpu')
  })
  it('amounts, utilisation and exhaustion', () => {
    expect(formatAmount('820.000000')).toBe('820')
    expect(formatAmount(1234.5678)).toBe('1,234.568')
    expect(formatAmount(null)).toBe('—')
    expect(formatUtilisation(80)).toBe('80.0 %')
    expect(formatUtilisation(33.333)).toBe('33.3 %')
    expect(formatUtilisation(null)).toBe('—')
    expect(formatExhaustion(null)).toBe('—')
    expect(formatExhaustion(0)).toBe('now')
    expect(formatExhaustion(0.4)).toBe('< 1 day')
    expect(formatExhaustion(1)).toBe('1 day')
    expect(formatExhaustion(12.4)).toBe('12 days')
    expect(formatExhaustion(82)).toBe('2.7 months')
    expect(formatExhaustion(800)).toBe('2.2 years')
    expect(asOfLabel('2026-09-09T09:00:00Z')).toBe('2026-09-09 09:00Z')
    expect(asOfLabel(null)).toBe('')
  })
})

describe('forms', () => {
  it('parses the footprint editor: blanks and zeros drop, bad input names the family', () => {
    expect(parseFootprintForm({ vcpu: ' 8 ', memory_gib: '64', block_ssd_gib: '', eip_addresses: '0' })).toEqual({ families: { vcpu: '8', memory_gib: '64' }, error: '' })
    expect(parseFootprintForm({ vcpu: '1,024' })).toEqual({ families: { vcpu: '1024' }, error: '' })
    expect(parseFootprintForm({ vcpu: '-1' }).error).toContain('vCPU')
    expect(parseFootprintForm({ memory_gib: 'lots' }).error).toContain('Memory')
    expect(parseFootprintForm({})).toEqual({ families: {}, error: '' })
  })
  it('parses a pool total', () => {
    expect(parseTotal('20')).toEqual({ total: '20', error: '' })
    expect(parseTotal(' 1,000.5 ')).toEqual({ total: '1000.5', error: '' })
    expect(parseTotal('').error).toBeTruthy()
    expect(parseTotal('-3').error).toBeTruthy()
    expect(parseTotal('abc').error).toBeTruthy()
  })
})

describe('zone rows', () => {
  const ov = {
    as_of: null,
    sources: 0,
    lagging_sources: 0,
    thresholds: { warn_pct: 70, critical_pct: 85 },
    families: [],
    regions: [
      { id: 'r1', code: 'me-east-215', name: 'Muscat', cloud_source_kind: 'huawei-project', zones: [{ id: 'z1', code: 'a', name: '', is_default: true, pools: [{ id: 'p', zone_id: 'z1', family: 'vcpu', total: 20, reserved: 0, source: 'manual', note: '', updated_by: '', updated_at: '', label: 'vCPU', unit: 'vCPU', consumed: 16, available: 4, utilisation_pct: 80, status: 'warn', clamped: false, overcommit: 0, zone_unknown: 8, growth_per_day: null, exhaustion_days: null, history_days: 0, series: [] }], skus: [] }] },
      { id: 'r2', code: 'eu-west-101', name: '', cloud_source_kind: 'huawei-project', zones: [{ id: 'z2', code: 'b', name: '', is_default: true, pools: [], skus: [] }] },
    ],
    unmapped_skus: [],
    unmapped_regions: [],
    summary: { regions: 2, zones: 2, pools: 1, pools_with_total: 1, pools_warn: 1, pools_critical: 0, pools_below_threshold: 1, skus: 0, unmapped_skus: 0 },
  } satisfies CapacityOverview
  it('flattens zones with their region and filters by region', () => {
    expect(zoneRows(ov).map((r) => `${r.region}/${r.zone.code}`)).toEqual(['me-east-215/a', 'eu-west-101/b'])
    expect(zoneRows(ov, 'eu-west-101').map((r) => r.zone.code)).toEqual(['b'])
    expect(zoneRows(ov, 'all')).toHaveLength(2)
    expect(zoneRows(null)).toEqual([])
    expect(hasTotal(ov.regions[0].zones[0].pools[0])).toBe(true)
    expect(hasTotal({ total: '0.000000' })).toBe(false)
    expect(hasTotal(undefined)).toBe(false)
  })
})
