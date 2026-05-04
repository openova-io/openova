/**
 * cutover.test.ts — exhaustive validation of the branded `CutoverState`
 * parser, the `CUTOVER_STEPS` canonical list, and the `parseCutoverStatus`
 * runtime parser.
 *
 * Mirrors the rigour of `deployment.test.ts`: every shape that could
 * silently flip the UI between `tethered` and `sovereign` must throw or
 * be defensively dropped here. The 8-step list is asserted by name +
 * order; adding/removing a step requires updating this test, which is
 * the desired behaviour.
 */

import { describe, it, expect } from 'vitest'
import {
  CUTOVER_STEPS,
  CUTOVER_STEP_COUNT,
  applyCutoverStepUpdate,
  buildInitialCutoverStatus,
  isCutoverState,
  isCutoverStepID,
  parseCutoverState,
  parseCutoverStatus,
  type CutoverState,
  type CutoverStatus,
} from './cutover'

/* ───────────────────────────── parseCutoverState ────────────────────── */

describe('parseCutoverState — accepts only `tethered` and `sovereign`', () => {
  it('round-trips `tethered`', () => {
    expect(parseCutoverState('tethered')).toBe('tethered')
  })
  it('round-trips `sovereign`', () => {
    expect(parseCutoverState('sovereign')).toBe('sovereign')
  })
  it('throws on the empty string', () => {
    expect(() => parseCutoverState('')).toThrow(/invalid CutoverState/)
  })
  it('throws on a typo (`soverign`)', () => {
    expect(() => parseCutoverState('soverign')).toThrow(/invalid CutoverState/)
  })
  it('throws on uppercase (server emits lowercase only)', () => {
    expect(() => parseCutoverState('Tethered')).toThrow(/invalid CutoverState/)
  })
  it('throws on `pending` / `in-progress` (intermediate, not legal here)', () => {
    expect(() => parseCutoverState('pending')).toThrow(/invalid CutoverState/)
    expect(() => parseCutoverState('in-progress')).toThrow(/invalid CutoverState/)
  })
  it('throws on null / undefined / numbers / objects', () => {
    expect(() => parseCutoverState(null)).toThrow(/invalid CutoverState/)
    expect(() => parseCutoverState(undefined)).toThrow(/invalid CutoverState/)
    expect(() => parseCutoverState(0)).toThrow(/invalid CutoverState/)
    expect(() => parseCutoverState({})).toThrow(/invalid CutoverState/)
  })
  it('truncates oversized previews in the error message', () => {
    const huge = 'x'.repeat(10_000)
    let caught: Error | null = null
    try {
      parseCutoverState(huge)
    } catch (err) {
      caught = err as Error
    }
    expect(caught).toBeTruthy()
    expect(caught!.message.length).toBeLessThan(80)
  })
})

describe('isCutoverState — type-guard variant', () => {
  it('returns true for legal values', () => {
    expect(isCutoverState('tethered')).toBe(true)
    expect(isCutoverState('sovereign')).toBe(true)
  })
  it('returns false for everything else', () => {
    expect(isCutoverState('')).toBe(false)
    expect(isCutoverState('SOVEREIGN')).toBe(false)
    expect(isCutoverState(null)).toBe(false)
    expect(isCutoverState(undefined)).toBe(false)
    expect(isCutoverState(42)).toBe(false)
  })
})

/* ────────────────────────── CUTOVER_STEPS — canonical 8 ─────────────── */

describe('CUTOVER_STEPS — canonical 8-step list', () => {
  it('has exactly 8 entries', () => {
    expect(CUTOVER_STEPS).toHaveLength(8)
    expect(CUTOVER_STEP_COUNT).toBe(8)
  })

  it('matches the catalyst-api Job sequence by id + order', () => {
    // This list mirrors openova-io/openova#793 (Step 2 progress card)
    // and #792 (POST /start orchestrates these in this exact order).
    expect(CUTOVER_STEPS.map((s) => s.id)).toEqual([
      'gitea-mirror',
      'harbor-projects',
      'harbor-prewarm',
      'registry-pivot',
      'flux-gitrepository-patch',
      'helmrepo-patches',
      'catalyst-api-env-patch',
      'egress-block-test',
    ])
  })

  it('every step has a non-empty label and description', () => {
    for (const s of CUTOVER_STEPS) {
      expect(s.label.length).toBeGreaterThan(0)
      expect(s.description.length).toBeGreaterThan(0)
    }
  })
})

describe('isCutoverStepID — guards forward-compat', () => {
  it('accepts every canonical id', () => {
    for (const s of CUTOVER_STEPS) expect(isCutoverStepID(s.id)).toBe(true)
  })
  it('rejects unknown ids (a future api adding step 9 must NOT crash a stale UI)', () => {
    expect(isCutoverStepID('chaos-monkey')).toBe(false)
    expect(isCutoverStepID('')).toBe(false)
    expect(isCutoverStepID(null)).toBe(false)
    expect(isCutoverStepID(123)).toBe(false)
  })
})

/* ────────────────────────── parseCutoverStatus ──────────────────────── */

describe('parseCutoverStatus — happy paths', () => {
  it('parses a fresh tethered snapshot with no steps', () => {
    const out = parseCutoverStatus({ state: 'tethered', steps: [] })
    expect(out.state).toBe('tethered')
    expect(out.cutoverComplete).toBe(false)
    expect(out.steps).toEqual([])
  })

  it('parses a terminal sovereign snapshot with summary fields', () => {
    const out = parseCutoverStatus({
      state: 'sovereign',
      cutoverComplete: true,
      steps: CUTOVER_STEPS.map((s) => ({
        step: s.id,
        status: 'done',
        finishedAt: '2026-05-04T10:00:00Z',
      })),
      mirroredCommitSHA: 'abc123def4567890',
      harborProjectCount: 7,
      egressTestPassed: true,
      finishedAt: '2026-05-04T10:11:00Z',
    })
    expect(out.state).toBe('sovereign')
    expect(out.cutoverComplete).toBe(true)
    expect(out.steps).toHaveLength(8)
    expect(out.mirroredCommitSHA).toBe('abc123def4567890')
    expect(out.harborProjectCount).toBe(7)
    expect(out.egressTestPassed).toBe(true)
  })

  it('drops steps with unknown ids (forward-compat)', () => {
    const out = parseCutoverStatus({
      state: 'tethered',
      steps: [
        { step: 'gitea-mirror', status: 'done' },
        { step: 'NEW-FUTURE-STEP', status: 'pending' },
        { step: 'harbor-projects', status: 'running' },
      ],
    })
    expect(out.steps).toHaveLength(2)
    expect(out.steps.map((s) => s.step)).toEqual([
      'gitea-mirror',
      'harbor-projects',
    ])
  })

  it('drops steps with malformed status values', () => {
    const out = parseCutoverStatus({
      state: 'tethered',
      steps: [
        { step: 'gitea-mirror', status: 'sleepy' },
        { step: 'harbor-projects', status: 'running' },
      ],
    })
    expect(out.steps).toHaveLength(1)
    expect(out.steps[0]?.step).toBe('harbor-projects')
  })

  it('infers cutoverComplete from state when wire field is missing', () => {
    const out = parseCutoverStatus({ state: 'sovereign', steps: [] })
    expect(out.cutoverComplete).toBe(true)
  })
})

describe('parseCutoverStatus — defensive boundaries', () => {
  it('throws when state is missing or unknown', () => {
    expect(() => parseCutoverStatus({})).toThrow(/invalid CutoverState/)
    expect(() => parseCutoverStatus({ state: 'pending', steps: [] })).toThrow(
      /invalid CutoverState/,
    )
  })

  it('throws on non-objects', () => {
    expect(() => parseCutoverStatus(null)).toThrow(/invalid CutoverStatus/)
    expect(() => parseCutoverStatus('tethered')).toThrow(/invalid CutoverStatus/)
    expect(() => parseCutoverStatus(42)).toThrow(/invalid CutoverStatus/)
  })

  it('treats non-array steps as empty rather than crashing', () => {
    const out = parseCutoverStatus({ state: 'tethered', steps: 'oops' })
    expect(out.steps).toEqual([])
  })

  it('drops non-object step entries (e.g. a stray string)', () => {
    const out = parseCutoverStatus({
      state: 'tethered',
      steps: [
        'should-be-an-object',
        null,
        { step: 'gitea-mirror', status: 'done' },
      ],
    })
    expect(out.steps).toHaveLength(1)
  })
})

/* ────────────────────────── buildInitialCutoverStatus ───────────────── */

describe('buildInitialCutoverStatus — first-paint placeholder', () => {
  it('seeds 8 pending steps in canonical order', () => {
    const init = buildInitialCutoverStatus()
    expect(init.state).toBe('tethered')
    expect(init.cutoverComplete).toBe(false)
    expect(init.steps).toHaveLength(8)
    expect(init.steps.map((s) => s.step)).toEqual(
      CUTOVER_STEPS.map((s) => s.id),
    )
    expect(init.steps.every((s) => s.status === 'pending')).toBe(true)
  })
})

/* ────────────────────────── applyCutoverStepUpdate ──────────────────── */

describe('applyCutoverStepUpdate — reducer for SSE step frames', () => {
  const seed: CutoverStatus = buildInitialCutoverStatus()

  it('updates the targeted step only, leaves others untouched', () => {
    const next = applyCutoverStepUpdate(seed, {
      step: 'gitea-mirror',
      status: 'running',
      startedAt: '2026-05-04T10:00:00Z',
    })
    const updated = next.steps.find((s) => s.step === 'gitea-mirror')
    expect(updated?.status).toBe('running')
    expect(updated?.startedAt).toBe('2026-05-04T10:00:00Z')
    // Every other step still pending.
    for (const s of next.steps) {
      if (s.step === 'gitea-mirror') continue
      expect(s.status).toBe('pending')
    }
  })

  it('returns the same object when the update is a no-op (referential equality)', () => {
    // Apply once …
    const a = applyCutoverStepUpdate(seed, {
      step: 'gitea-mirror',
      status: 'pending',
    })
    // … and again with the identical payload.
    const b = applyCutoverStepUpdate(a, {
      step: 'gitea-mirror',
      status: 'pending',
    })
    expect(b).toBe(a)
  })

  it('returns the same object when given an unknown step id (forward-compat)', () => {
    const next = applyCutoverStepUpdate(seed, {
      // @ts-expect-error -- forcing through the parser layer
      step: 'NEW-FUTURE-STEP',
      status: 'done',
    })
    expect(next).toBe(seed)
  })

  it('appends a record for a step that is not yet present (POST /start may seed only the current step)', () => {
    const sparse: CutoverStatus = {
      state: 'tethered' as const as CutoverStatus['state'],
      cutoverComplete: false,
      steps: [{ step: 'gitea-mirror', status: 'running' }],
    }
    const next = applyCutoverStepUpdate(sparse, {
      step: 'harbor-projects',
      status: 'running',
      startedAt: '2026-05-04T10:01:00Z',
    })
    expect(next.steps).toHaveLength(2)
    expect(next.steps[1]?.step).toBe('harbor-projects')
    expect(next.steps[1]?.status).toBe('running')
  })

  it('progresses a step from pending → running → done sequentially', () => {
    let s = seed
    s = applyCutoverStepUpdate(s, { step: 'gitea-mirror', status: 'running' })
    s = applyCutoverStepUpdate(s, {
      step: 'gitea-mirror',
      status: 'done',
      finishedAt: '2026-05-04T10:01:00Z',
    })
    const found = s.steps.find((x) => x.step === 'gitea-mirror')
    expect(found?.status).toBe('done')
    expect(found?.finishedAt).toBe('2026-05-04T10:01:00Z')
  })
})

/* ─────────────────── compile-time brand assertion (smoke) ───────────── */

describe('CutoverState — branded compile-time guarantee', () => {
  it('a raw string cannot be assigned without parseCutoverState', () => {
    // The test cannot directly assert compile-time errors; this exists
    // as a runtime smoke-test pinning the parser is required to land a
    // value that the type system accepts.
    const valid: CutoverState = parseCutoverState('tethered')
    expect(valid).toBe('tethered')
  })
})
