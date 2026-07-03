/**
 * statusColors.test.ts — lock-in for the #4704 semantic status → colour
 * contract (founder spec 2026-07-03): green=success, blue=in-progress,
 * amber=warning, red=failed, grey=pending. In-progress must be visibly
 * distinct (blue) — never grey or green.
 */

import { describe, it, expect } from 'vitest'
import {
  statusKindOf,
  STATUS_KIND_COLOR,
  STATUS_KIND_BADGE_CLASSES,
} from './statusColors'

describe('statusKindOf', () => {
  it('classifies success states green', () => {
    for (const s of ['succeeded', 'completed', 'ready', 'installed', 'converged', 'Ready ']) {
      expect(statusKindOf(s)).toBe('success')
    }
  })

  it('classifies in-progress states blue — never grey or green', () => {
    for (const s of [
      'running',
      'installing',
      'reconciling',
      'provisioning',
      'tofu-applying',
      'flux-bootstrapping',
      'phase1-watching',
    ]) {
      expect(statusKindOf(s)).toBe('in-progress')
    }
  })

  it('classifies warning states amber', () => {
    for (const s of ['degraded', 'warning', 'partial-failure', 'out-of-sync']) {
      expect(statusKindOf(s)).toBe('warning')
    }
  })

  it('classifies failure states red', () => {
    for (const s of ['failed', 'error', 'errored']) {
      expect(statusKindOf(s)).toBe('failed')
    }
  })

  it('classifies pending / unknown / empty as grey — never green', () => {
    for (const s of ['pending', 'suspended', 'not-started', '', undefined, null]) {
      expect(statusKindOf(s)).toBe('pending')
    }
  })
})

describe('colour tokens', () => {
  it('every kind maps to a distinct theme token (no raw hex)', () => {
    const values = Object.values(STATUS_KIND_COLOR)
    expect(new Set(values).size).toBe(values.length)
    for (const v of values) expect(v).toMatch(/^var\(--color-/)
  })

  it('in-progress badge is the blue accent, pending badge is grey', () => {
    expect(STATUS_KIND_BADGE_CLASSES['in-progress']).toContain('var(--color-accent)')
    expect(STATUS_KIND_BADGE_CLASSES.pending).toContain('var(--color-text-dim)')
    expect(STATUS_KIND_BADGE_CLASSES.pending).not.toContain('var(--color-warn)')
  })
})
