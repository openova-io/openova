/**
 * placement-vocabulary.drift.test.tsx — #3375 DoD-1 drift guard.
 *
 * Pins the ONE canonical placement/topology vocabulary across every
 * FRONTEND surface so a future edit that re-introduces the legacy
 * editor dialect (`single-region` / `active-hotstandby`) fails CI here
 * instead of silently drifting from the catalog placementSchema, the
 * Application CR, and the application-controller's resolver.
 *
 * The single source of truth is the Go placement package
 * (core/controllers/internal/placement.CanonicalModes); this test pins
 * the FE emitters (topology/modes.ALL_MODES, the InstallPage <select>,
 * fleet.api.TopologyMode) to the SAME set + asserts the FE canonicaliser
 * folds every legacy spelling exactly as the Go Canonicalize does.
 */
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { ALL_MODES, canonicalizeMode, describeMode } from './modes'
import { canonicalizeTopologyMode } from '@/lib/fleet.api'

// THE canonical vocabulary — must mirror Go placement.CanonicalModes()
// and the catalyst-api canonicalizeTopology output. Keep this literal in
// sync across Go + both FE codebases (the console mirrors it in
// src/lib/api/types.ts BcpTopology).
const CANONICAL_MODES = ['singleton', 'active-active', 'active-hot-standby', 'active-passive'] as const

// Legacy spellings that MUST fold onto the canonical token but MUST NOT
// appear as a primary emitted value anywhere.
const LEGACY_SPELLINGS = ['single-region', 'active-hotstandby'] as const

describe('#3375 DoD-1 — one placement vocabulary, every FE surface', () => {
  it('topology/modes.ALL_MODES is exactly the canonical set (no legacy spelling)', () => {
    expect([...ALL_MODES]).toEqual([...CANONICAL_MODES])
    for (const legacy of LEGACY_SPELLINGS) {
      expect(ALL_MODES as readonly string[]).not.toContain(legacy)
    }
  })

  it('the post-create editor describes every canonical class (no blank helper text)', () => {
    for (const m of CANONICAL_MODES) {
      expect(describeMode(m).length).toBeGreaterThan(0)
    }
  })

  it('canonicalizeMode folds every legacy + canonical spelling onto the canonical token', () => {
    expect(canonicalizeMode('single-region')).toBe('singleton')
    expect(canonicalizeMode('singleton')).toBe('singleton')
    expect(canonicalizeMode('active-hotstandby')).toBe('active-hot-standby')
    expect(canonicalizeMode('active-hot-standby')).toBe('active-hot-standby')
    expect(canonicalizeMode('active-passive')).toBe('active-passive')
    expect(canonicalizeMode('active-active')).toBe('active-active')
    // case / whitespace robustness (mirrors Go Canonicalize)
    expect(canonicalizeMode('  Active-HotStandby ')).toBe('active-hot-standby')
  })

  it('fleet.api canonicalizeTopologyMode agrees with the editor canonicaliser', () => {
    for (const spelling of [...CANONICAL_MODES, ...LEGACY_SPELLINGS]) {
      expect(canonicalizeTopologyMode(spelling)).toBe(canonicalizeMode(spelling))
    }
  })

  it('the InstallPage placement <select> hard-codes exactly the canonical options (no legacy spelling)', () => {
    // The render-level assertion lives in InstallPage.placement.test.tsx
    // (it mocks the page hooks). Here we statically pin the <option>
    // value literals so a hand-edit that re-introduces single-region /
    // active-hotstandby in the install picker fails CI without needing the
    // full page mount.
    // vitest runs with cwd = the package root (products/catalyst/bootstrap/ui).
    const src = readFileSync(
      resolve(process.cwd(), 'src/pages/sovereign/InstallPage.tsx'),
      'utf8',
    )
    const optionValues = [...src.matchAll(/<option value="([^"]+)">/g)].map((m) => m[1])
    expect(optionValues).toEqual([...CANONICAL_MODES])
    for (const legacy of LEGACY_SPELLINGS) {
      expect(optionValues).not.toContain(legacy)
    }
  })
})
