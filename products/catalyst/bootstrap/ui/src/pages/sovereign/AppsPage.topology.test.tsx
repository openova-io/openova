/**
 * AppsPage.topology.test.tsx — #4897.
 *
 * The Apps-grid topology badge (`sov-app-topology-<id>`) is driven by the
 * `topology` value the BFF projects from the Application's `spec.placement`.
 * That placement is DUAL-FORM: a legacy posture STRING, or the object
 * `{ mode, regions }` a New-instance active-hot-standby create writes.
 *
 * `topologyLabel` normalises BOTH shapes so an active-hot-standby instance
 * created via Catalog→New-instance renders its badge ("active-hot-standby"),
 * exactly like a string-form spine AHS app — without regressing the singleton
 * / string path or emitting a spurious badge for a missing placement.
 */

import { describe, it, expect } from 'vitest'
import { topologyLabel } from './AppsPage'

describe('topologyLabel — #4897 dual-shape placement resilience', () => {
  it('object-form active-hot-standby → reads .mode', () => {
    expect(
      topologyLabel({
        mode: 'active-hot-standby',
        regions: ['me-east-215-a', 'me-east-215-b'],
      }),
    ).toBe('active-hot-standby')
  })

  it('string-form posture → used as-is', () => {
    expect(topologyLabel('active-hot-standby')).toBe('active-hot-standby')
    expect(topologyLabel('singleton')).toBe('singleton')
    expect(topologyLabel('active-active')).toBe('active-active')
  })

  it('object without a string mode → empty (no badge)', () => {
    expect(topologyLabel({ regions: ['fsn'] })).toBe('')
    expect(topologyLabel({ mode: 123 as unknown })).toBe('')
    expect(topologyLabel({})).toBe('')
  })

  it('missing / non-object placement → empty (no badge)', () => {
    expect(topologyLabel(undefined)).toBe('')
    expect(topologyLabel(null)).toBe('')
    expect(topologyLabel('')).toBe('')
  })
})
