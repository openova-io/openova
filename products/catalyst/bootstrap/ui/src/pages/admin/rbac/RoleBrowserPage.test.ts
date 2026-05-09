/**
 * RoleBrowserPage.test.ts — pure-function tests for the helpers
 * exported by the role browser (EPIC-3 #1098 slice U4).
 */

import { describe, expect, it } from 'vitest'

import { readTierLevel, sortRolesByTierLevel } from './roleHelpers'
import type { KCRole } from './rbac.api'

const role = (name: string, level?: string): KCRole => ({
  name,
  attributes: level ? { 'tier-level': [level] } : undefined,
})

describe('readTierLevel', () => {
  it('returns the integer when tier-level attribute is set', () => {
    expect(readTierLevel(role('catalyst-viewer', '10'))).toBe(10)
    expect(readTierLevel(role('catalyst-owner', '50'))).toBe(50)
  })

  it('returns null when tier-level is unset', () => {
    expect(readTierLevel(role('custom-role'))).toBeNull()
  })

  it('returns null when tier-level is non-numeric', () => {
    expect(readTierLevel(role('weird', 'abc'))).toBeNull()
  })
})

describe('sortRolesByTierLevel', () => {
  it('sorts ascending by tier-level, then alphabetical', () => {
    const roles: KCRole[] = [
      role('catalyst-admin', '40'),
      role('catalyst-viewer', '10'),
      role('zz-untiered'),
      role('aa-untiered'),
      role('catalyst-developer', '20'),
    ]
    const sorted = sortRolesByTierLevel(roles).map((r) => r.name)
    expect(sorted).toEqual([
      'catalyst-viewer',
      'catalyst-developer',
      'catalyst-admin',
      'aa-untiered',
      'zz-untiered',
    ])
  })

  it('does not mutate the input array', () => {
    const input: KCRole[] = [role('z', '50'), role('a', '10')]
    const inputCopy = [...input]
    sortRolesByTierLevel(input)
    expect(input).toEqual(inputCopy)
  })
})
