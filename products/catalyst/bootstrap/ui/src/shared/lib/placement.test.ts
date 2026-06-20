import { describe, it, expect } from 'vitest'

import {
  MULTI_PRIMARY_NOT_SUPPORTED,
  type PlacementTarget,
  canAddPrimary,
  derivePattern,
  normalizeCapability,
  targetsFromLegacy,
  validatePlacement,
} from './placement'

const primaryA: PlacementTarget = { region: 'region-a', cluster: 'mgmt-A', vcluster: 'mgmt', role: 'Primary' }
const hotB: PlacementTarget = { region: 'region-b', cluster: 'mgmt-B', vcluster: 'mgmt', role: 'Standby', standbyType: 'Hot' }
const coldB: PlacementTarget = { region: 'region-b', cluster: 'mgmt-B', vcluster: 'mgmt', role: 'Standby', standbyType: 'Cold' }
const primaryB: PlacementTarget = { region: 'region-b', cluster: 'mgmt-B', vcluster: 'mgmt', role: 'Primary' }

describe('derivePattern (#3969 §7.3)', () => {
  it('1 Primary -> singleton', () => {
    expect(derivePattern([primaryA])).toBe('singleton')
  })
  it('Primary + Hot standby -> active-hot-standby', () => {
    expect(derivePattern([primaryA, hotB])).toBe('active-hot-standby')
  })
  it('Primary + Cold standby -> active-passive', () => {
    expect(derivePattern([primaryA, coldB])).toBe('active-passive')
  })
  it('2 Primary -> active-active', () => {
    expect(derivePattern([primaryA, primaryB])).toBe('active-active')
  })
  it('empty -> singleton', () => {
    expect(derivePattern([])).toBe('singleton')
  })
})

describe('validatePlacement capability gate (#3969 §7.3, DoD 5)', () => {
  it('primary+standby rejects a 2nd Primary with MultiPrimaryNotSupported', () => {
    const v = validatePlacement([primaryA, primaryB], 'primary+standby')
    expect(v?.reason).toBe(MULTI_PRIMARY_NOT_SUPPORTED)
  })
  it('multi-primary accepts 2 Primary', () => {
    expect(validatePlacement([primaryA, primaryB], 'multi-primary')).toBeNull()
  })
  it('Standby without a type is rejected', () => {
    const v = validatePlacement([primaryA, { ...hotB, standbyType: undefined }], 'primary+standby')
    expect(v?.reason).toBe('StandbyMissingType')
  })
  it('a valid hot-standby placement passes', () => {
    expect(validatePlacement([primaryA, hotB], 'primary+standby')).toBeNull()
  })
})

describe('canAddPrimary', () => {
  it('primary+standby blocks a 2nd Primary', () => {
    expect(canAddPrimary([primaryA], 'primary+standby')).toBe(false)
  })
  it('multi-primary allows it', () => {
    expect(canAddPrimary([primaryA], 'multi-primary')).toBe(true)
  })
})

describe('normalizeCapability', () => {
  it('defaults to primary+standby', () => {
    expect(normalizeCapability(undefined)).toBe('primary+standby')
    expect(normalizeCapability('')).toBe('primary+standby')
    expect(normalizeCapability('multi-primary')).toBe('multi-primary')
  })
})

describe('targetsFromLegacy projection', () => {
  it('active-hot-standby mode -> Primary + Hot standby', () => {
    const ts = targetsFromLegacy({ mode: 'active-hotstandby', regions: ['region-a', 'region-b'] })
    expect(ts[0].role).toBe('Primary')
    expect(ts[1].role).toBe('Standby')
    expect(ts[1].standbyType).toBe('Hot')
  })
  it('active-passive mode -> Cold standby', () => {
    const ts = targetsFromLegacy({ mode: 'active-passive', regions: ['region-a', 'region-b'] })
    expect(ts[1].standbyType).toBe('Cold')
  })
  it('status rollup with roles is preferred', () => {
    const ts = targetsFromLegacy({
      statusRegions: [
        { name: 'region-a', role: 'primary' },
        { name: 'region-b', role: 'standby' },
      ],
    })
    expect(ts[0].role).toBe('Primary')
    expect(ts[1].role).toBe('Standby')
  })
})
