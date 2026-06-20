/**
 * useCloudLens.test.tsx — the lens-seeding contract (#3996 follow-up).
 *
 * The ConvergenceWizard reconciliation deep-link arrives at the Cloud
 * surface with `?lens=reconciliation`; CloudPage threads it into
 * CloudLensProvider as `initialLensId`. These tests pin that the initial
 * lens is honoured, that an absent/unknown seed falls back to the default
 * Cloud lens, and that operator interaction (selectLens) still wins
 * thereafter.
 */
import { describe, it, expect } from 'vitest'
import { act, renderHook } from '@testing-library/react'
import { useCloudLensState } from './useCloudLens'
import { DEFAULT_LENS_ID, LENSES, lensChips, setsEqual } from './presets'

describe('useCloudLensState — initial lens seeding (#3996)', () => {
  it('opens on the reconciliation lens when seeded with it', () => {
    const { result } = renderHook(() => useCloudLensState('reconciliation'))
    expect(result.current.lensId).toBe('reconciliation')
    expect(
      setsEqual(result.current.activeTypes, lensChips(LENSES.reconciliation)),
    ).toBe(true)
    expect(result.current.isCustom).toBe(false)
  })

  it('falls back to the default Cloud lens when seed is undefined', () => {
    const { result } = renderHook(() => useCloudLensState(undefined))
    expect(result.current.lensId).toBe(DEFAULT_LENS_ID)
  })

  it('falls back to the default Cloud lens when seed is an unknown id', () => {
    // Cast through unknown — validateSearch already closed-set-guards the
    // wire value, but the hook must be defensive if a stray id arrives.
    const { result } = renderHook(() =>
      useCloudLensState('not-a-lens' as never),
    )
    expect(result.current.lensId).toBe(DEFAULT_LENS_ID)
  })

  it('lets operator selectLens override the seeded lens', () => {
    const { result } = renderHook(() => useCloudLensState('reconciliation'))
    act(() => result.current.selectLens('networking'))
    expect(result.current.lensId).toBe('networking')
    expect(
      setsEqual(result.current.activeTypes, lensChips(LENSES.networking)),
    ).toBe(true)
  })
})
