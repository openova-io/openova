/**
 * rbac.api.test.ts — pure-function tests for the RBAC management
 * API module (EPIC-3 #1098 slice U1+U2+U3+U4).
 *
 * Network calls are tested where they live (per-page Playwright
 * E2Es). This file pins the validators + the canonical vocabularies
 * so a typo in the JS scope-key set surfaces immediately rather than
 * waiting for a Playwright failure.
 */

import { describe, expect, it } from 'vitest'

import {
  RBAC_SCOPE_KEYS,
  RBAC_TIERS,
  TIER_ACTION_SETS,
  TIER_AUTO_INJECTED_SCOPES,
  TIER_LEVEL,
  validateScopeKey,
  validateScopeValue,
} from './rbac.api'

describe('RBAC tier vocabulary', () => {
  it('enumerates the 5 catalog tiers in ascending precedence', () => {
    expect([...RBAC_TIERS]).toEqual(['viewer', 'developer', 'operator', 'admin', 'owner'])
  })

  it('declares one TIER_LEVEL entry per tier with strict 10-step precedence', () => {
    expect(TIER_LEVEL.viewer).toBe(10)
    expect(TIER_LEVEL.developer).toBe(20)
    expect(TIER_LEVEL.operator).toBe(30)
    expect(TIER_LEVEL.admin).toBe(40)
    expect(TIER_LEVEL.owner).toBe(50)
  })

  it('declares one TIER_ACTION_SETS entry per tier (no missing tiers)', () => {
    for (const t of RBAC_TIERS) {
      expect(TIER_ACTION_SETS[t]).toBeDefined()
      expect(TIER_ACTION_SETS[t].length).toBeGreaterThan(0)
    }
  })

  it('developer auto-injects env-type=dev (per design doc §6.2)', () => {
    expect(TIER_AUTO_INJECTED_SCOPES.developer).toEqual([
      { key: 'openova.io/env-type', value: 'dev' },
    ])
  })

  it('only developer has auto-injected scope (rest are undefined or empty)', () => {
    for (const t of RBAC_TIERS) {
      if (t === 'developer') continue
      const v = TIER_AUTO_INJECTED_SCOPES[t]
      expect(v == null || v.length === 0).toBe(true)
    }
  })
})

describe('validateScopeKey', () => {
  it('accepts every canonical key in NAMING-CONVENTION.md §6 vocab', () => {
    for (const k of RBAC_SCOPE_KEYS) {
      expect(validateScopeKey(k)).toBeNull()
    }
  })

  it('accepts whitespace-padded canonical keys', () => {
    expect(validateScopeKey('  openova.io/application  ')).toBeNull()
  })

  it('rejects empty keys', () => {
    expect(validateScopeKey('')).toBe('scope key is required')
    expect(validateScopeKey('   ')).toBe('scope key is required')
  })

  it('rejects non-canonical keys', () => {
    expect(validateScopeKey('app')).toMatch(/unknown scope key: app/)
    expect(validateScopeKey('openova.io/app')).toMatch(/unknown scope key/)
    expect(validateScopeKey('whatever')).toMatch(/unknown scope key/)
  })
})

describe('validateScopeValue', () => {
  it('accepts non-empty values', () => {
    expect(validateScopeValue('wordpress')).toBeNull()
    expect(validateScopeValue('  wordpress  ')).toBeNull()
  })

  it('rejects empty values', () => {
    expect(validateScopeValue('')).toBe('scope value is required')
    expect(validateScopeValue('   ')).toBe('scope value is required')
  })
})
