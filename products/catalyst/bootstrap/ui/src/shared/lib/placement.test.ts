import { describe, it, expect } from 'vitest'

import {
  MULTI_PRIMARY_NOT_SUPPORTED,
  PATTERN_NOT_REPORTED,
  type PlacementTarget,
  canAddPrimary,
  derivePattern,
  describePattern,
  normalizeCapability,
  patternLabel,
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
})

/**
 * #5515 — derivePattern must NOT fail open into `singleton`.
 *
 * The pre-fix body was:
 *     if (primaries >= 2) return 'active-active'
 *     if (standbys === 0) return 'singleton'    // fires when targets is EMPTY
 * so `targets: []` produced the one pattern that MEANS "no failover, and
 * that's fine". Live on hw291 this rendered `Pattern: singleton` next to the
 * same panel's "No placement targets reported yet."
 *
 * These assertions are deliberately TWO-SIDED: a `derivePattern` that returns
 * `not-reported` unconditionally fails the positive cases below just as hard as
 * the old fail-open version fails the negative ones.
 */
describe('#5515 derivePattern never fails open into singleton', () => {
  it('an EMPTY target list is not-reported — and specifically NOT singleton', () => {
    expect(derivePattern([])).toBe(PATTERN_NOT_REPORTED)
    // The exact regression: the confident pattern must not be claimed.
    expect(derivePattern([])).not.toBe('singleton')
  })

  it('a target list with NO Primary (roles unrecognised) is not-reported, not singleton', () => {
    // Same counters as the empty list (primaries 0 / standbys 0) — the other
    // input that reached the fail-open branch.
    const garbage = [{ region: 'region-a', cluster: 'c', vcluster: 'mgmt', role: 'Replica' }] as unknown as PlacementTarget[]
    expect(derivePattern(garbage)).toBe(PATTERN_NOT_REPORTED)
    expect(derivePattern(garbage)).not.toBe('singleton')
  })

  it('a Standby-only list is not-reported — no "active-*" claim without an active', () => {
    expect(derivePattern([hotB])).toBe(PATTERN_NOT_REPORTED)
    expect(derivePattern([hotB])).not.toBe('active-hot-standby')
    // Consistent with the validator's own verdict on the same input.
    expect(validatePlacement([hotB], 'primary+standby')?.reason).toBe('NoPrimary')
  })

  /* ── The POSITIVE control side (rule 3: one-sided tests are rejected) ────
     A derivePattern hard-wired to return not-reported must fail here. Every
     real pattern still derives, so the guard cannot be satisfied by a
     function that simply stopped deriving. */
  it('CONTROL — a real single Primary still derives singleton (the guard did not swallow real data)', () => {
    expect(derivePattern([primaryA])).toBe('singleton')
    expect(derivePattern([primaryA])).not.toBe(PATTERN_NOT_REPORTED)
  })

  it('CONTROL — every real pattern still derives its own name', () => {
    expect(derivePattern([primaryA, hotB])).toBe('active-hot-standby')
    expect(derivePattern([primaryA, coldB])).toBe('active-passive')
    expect(derivePattern([primaryA, primaryB])).toBe('active-active')
    for (const ts of [[primaryA], [primaryA, hotB], [primaryA, coldB], [primaryA, primaryB]]) {
      expect(derivePattern(ts)).not.toBe(PATTERN_NOT_REPORTED)
    }
  })

  it('describePattern covers not-reported with prose, and never returns undefined', () => {
    // The tsc exhaustiveness gate (TS2366) is the compile-time half; this is
    // the runtime half — a missing case would return undefined here.
    for (const p of ['singleton', 'active-passive', 'active-hot-standby', 'active-active', PATTERN_NOT_REPORTED] as const) {
      expect(typeof describePattern(p)).toBe('string')
      expect(describePattern(p).length).toBeGreaterThan(0)
    }
    expect(describePattern(PATTERN_NOT_REPORTED)).toContain('cannot be derived')
    // The un-derivable description must NOT reassure the reader with the
    // singleton wording ("no cross-region failover").
    expect(describePattern(PATTERN_NOT_REPORTED)).not.toContain('no cross-region failover')
  })

  it('patternLabel renders the un-derivable case as prose, real patterns verbatim', () => {
    expect(patternLabel(PATTERN_NOT_REPORTED)).toBe('not reported')
    expect(patternLabel(PATTERN_NOT_REPORTED)).not.toBe('singleton')
    // CONTROL — real patterns pass through unchanged.
    expect(patternLabel('singleton')).toBe('singleton')
    expect(patternLabel('active-hot-standby')).toBe('active-hot-standby')
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
