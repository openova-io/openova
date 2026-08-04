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
  cutoverResultToStatus,
  isCutoverState,
  isCutoverStepID,
  parseCutoverState,
  parseCutoverStatus,
  parseCutoverStatusConfigMap,
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

describe('CUTOVER_STEPS — canonical 11-step list', () => {
  it('has exactly 11 entries', () => {
    expect(CUTOVER_STEPS).toHaveLength(11)
    expect(CUTOVER_STEP_COUNT).toBe(11)
  })

  it('matches the chart cutover-order labels + the catalyst-api Job sequence', () => {
    // Ground truth: platform/self-sovereign-cutover/chart/templates/NN-*.yaml
    // `bp.openova.io/cutover-order` labels 01..11. The catalyst-api engine
    // discovers + runs them in this order. The slug is `helmrepository-patches`
    // (the prior `helmrepo-patches` never matched the chart).
    expect(CUTOVER_STEPS.map((s) => s.id)).toEqual([
      'gitea-mirror',
      'harbor-projects',
      'harbor-prewarm',
      'registry-pivot',
      'flux-gitrepository-patch',
      'helmrepository-patches',
      'catalyst-api-env-patch',
      'egress-block-test',
      'gitea-token-mint',
      'vcluster-registry-pivot',
      'crossplane-provider-pivot',
    ])
  })

  it('every step has a non-empty label and description', () => {
    for (const s of CUTOVER_STEPS) {
      expect(s.label.length).toBeGreaterThan(0)
      expect(s.description.length).toBeGreaterThan(0)
    }
  })
})

/* ─────────────────────── cutoverResultToStatus ──────────────────────── */

describe('cutoverResultToStatus — engine result → UI status', () => {
  it('maps success + skipped → done', () => {
    expect(cutoverResultToStatus('success')).toBe('done')
    expect(cutoverResultToStatus('skipped')).toBe('done')
  })
  it('maps running → running, failed → failed', () => {
    expect(cutoverResultToStatus('running')).toBe('running')
    expect(cutoverResultToStatus('failed')).toBe('failed')
  })
  it('maps empty / absent → pending', () => {
    expect(cutoverResultToStatus('')).toBe('pending')
    expect(cutoverResultToStatus(undefined)).toBe('pending')
    expect(cutoverResultToStatus(null)).toBe('pending')
  })
  it('returns null for an unrecognised value (forward-compat)', () => {
    expect(cutoverResultToStatus('explodey')).toBeNull()
    expect(cutoverResultToStatus(42)).toBeNull()
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
    expect(out.steps).toHaveLength(11)
    expect(out.mirroredCommitSHA).toBe('abc123def4567890')
    expect(out.harborProjectCount).toBe(7)
    expect(out.egressTestPassed).toBe(true)
  })

  it('parses the LIVE catalyst-api wire shape (steps[].name + .result)', () => {
    // This is the shape api/internal/handler/cutover.go actually emits
    // from GET /sovereign/cutover/status — `name`/`result`, NOT
    // `step`/`status`. Before the adapter every one of these records was
    // dropped, so the progress card stayed blank on a completed cutover.
    const out = parseCutoverStatus({
      state: 'sovereign',
      cutoverComplete: true,
      cutoverStartedAt: '2026-06-18T10:00:00Z',
      cutoverFinishedAt: '2026-06-18T10:11:00Z',
      steps: [
        {
          name: 'gitea-mirror',
          result: 'success',
          startedAt: '2026-06-18T10:00:00Z',
          finishedAt: '2026-06-18T10:01:00Z',
          jobName: 'cutover-gitea-mirror-1',
        },
        { name: 'harbor-projects', result: 'success' },
        { name: 'egress-block-test', result: 'success' },
      ],
    })
    expect(out.cutoverComplete).toBe(true)
    // success → done
    expect(out.steps.find((s) => s.step === 'gitea-mirror')?.status).toBe(
      'done',
    )
    expect(out.steps.find((s) => s.step === 'gitea-mirror')?.startedAt).toBe(
      '2026-06-18T10:00:00Z',
    )
    // Live timestamps come off cutoverStartedAt/cutoverFinishedAt.
    expect(out.startedAt).toBe('2026-06-18T10:00:00Z')
    expect(out.finishedAt).toBe('2026-06-18T10:11:00Z')
    // egressTestPassed derived from the egress-block-test step result.
    expect(out.egressTestPassed).toBe(true)
  })

  it('derives egressTestPassed=false + folds lastError onto the failed step', () => {
    const out = parseCutoverStatus({
      cutoverComplete: false,
      failedStep: 'egress-block-test',
      lastError: 'reconcile bp-kyverno went NotReady during the deny-egress hold',
      steps: [
        { name: 'gitea-mirror', result: 'success' },
        { name: 'egress-block-test', result: 'failed' },
      ],
    })
    expect(out.state).toBe('tethered')
    const egress = out.steps.find((s) => s.step === 'egress-block-test')
    expect(egress?.status).toBe('failed')
    expect(egress?.message).toMatch(/deny-egress hold/)
    expect(out.egressTestPassed).toBe(false)
  })

  it('maps a running step from the live result vocabulary', () => {
    const out = parseCutoverStatus({
      cutoverComplete: false,
      steps: [
        { name: 'gitea-mirror', result: 'success' },
        { name: 'harbor-projects', result: 'running' },
      ],
    })
    expect(out.steps.find((s) => s.step === 'harbor-projects')?.status).toBe(
      'running',
    )
  })

  it('reads harborProjectCount from the nested raw string-map', () => {
    const out = parseCutoverStatus({
      cutoverComplete: true,
      steps: [],
      raw: { harborProjectCount: '7', mirroredCommitSHA: 'deadbeef0000' },
    })
    expect(out.harborProjectCount).toBe(7)
    expect(out.mirroredCommitSHA).toBe('deadbeef0000')
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

  it('defaults state to `tethered` when the wire field is missing (otech113 #933)', () => {
    // The dashboard rendered `invalid CutoverState: <undefined>` because
    // the catalyst-api response did not include a `state` field on
    // freshly-handed-over Sovereigns. Defensive parser must derive the
    // state from `cutoverComplete` rather than throwing.
    const out = parseCutoverStatus({ cutoverComplete: false, steps: [] })
    expect(out.state).toBe('tethered')
    expect(out.cutoverComplete).toBe(false)
  })

  it('defaults state to `sovereign` when wire field is missing but cutoverComplete=true', () => {
    const out = parseCutoverStatus({ cutoverComplete: true, steps: [] })
    expect(out.state).toBe('sovereign')
    expect(out.cutoverComplete).toBe(true)
  })

  it('defaults to `tethered` for an empty wire object (older catalyst-api Pod)', () => {
    const out = parseCutoverStatus({})
    expect(out.state).toBe('tethered')
    expect(out.cutoverComplete).toBe(false)
  })

  it('defaults to `tethered` when state is explicitly null', () => {
    const out = parseCutoverStatus({ state: null, cutoverComplete: false })
    expect(out.state).toBe('tethered')
  })
})

describe('parseCutoverStatus — defensive boundaries', () => {
  it('throws when state is an unknown string (typo / hostile input)', () => {
    expect(() => parseCutoverStatus({ state: 'pending', steps: [] })).toThrow(
      /invalid CutoverState/,
    )
    expect(() => parseCutoverStatus({ state: 'TETHERED', steps: [] })).toThrow(
      /invalid CutoverState/,
    )
    expect(() => parseCutoverStatus({ state: '', steps: [] })).toThrow(
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
  it('seeds 11 pending steps in canonical order', () => {
    const init = buildInitialCutoverStatus()
    expect(init.state).toBe('tethered')
    expect(init.cutoverComplete).toBe(false)
    expect(init.steps).toHaveLength(11)
    expect(init.steps.map((s) => s.step)).toEqual(
      CUTOVER_STEPS.map((s) => s.id),
    )
    expect(init.steps.every((s) => s.status === 'pending')).toBe(true)
  })
})

/* ─────────────────── parseCutoverStatusConfigMap (SSE) ──────────────── */

describe('parseCutoverStatusConfigMap — flat status ConfigMap (SSE snapshot)', () => {
  it('reconstructs steps from step.<name>.<field> keys', () => {
    // This is the flat string-map HandleCutoverEvents JSON-encodes into the
    // `cutover-status` SSE frame's message (readCutoverStatus output).
    const out = parseCutoverStatusConfigMap({
      cutoverComplete: 'true',
      cutoverStartedAt: '2026-06-18T10:00:00Z',
      cutoverFinishedAt: '2026-06-18T10:11:00Z',
      'step.gitea-mirror.result': 'success',
      'step.gitea-mirror.startedAt': '2026-06-18T10:00:00Z',
      'step.gitea-mirror.finishedAt': '2026-06-18T10:01:00Z',
      'step.egress-block-test.result': 'success',
    })
    expect(out).not.toBeNull()
    expect(out!.state).toBe('sovereign')
    expect(out!.cutoverComplete).toBe(true)
    expect(out!.steps.find((s) => s.step === 'gitea-mirror')?.status).toBe(
      'done',
    )
    expect(out!.egressTestPassed).toBe(true)
  })

  it('derives tethered from cutoverComplete:"false" with an in-flight step', () => {
    const out = parseCutoverStatusConfigMap({
      cutoverComplete: 'false',
      'step.gitea-mirror.result': 'success',
      'step.harbor-projects.result': 'running',
    })
    expect(out).not.toBeNull()
    expect(out!.state).toBe('tethered')
    expect(out!.steps.find((s) => s.step === 'harbor-projects')?.status).toBe(
      'running',
    )
  })

  it('returns null for a non-map input (frame ignored, stream survives)', () => {
    expect(parseCutoverStatusConfigMap(null)).toBeNull()
    expect(parseCutoverStatusConfigMap('nope')).toBeNull()
    expect(parseCutoverStatusConfigMap([1, 2, 3])).toBeNull()
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

/* ────────────── #5391 settled-roll override audit record ────────────── */

describe('parseCutoverStatus — #5391 settledRollOverrides audit record', () => {
  it('parses the typed top-level field (the /status first-class field)', () => {
    const out = parseCutoverStatus({
      cutoverComplete: false,
      settledRollOverrides:
        'delta-corp/bp-keycloak=quota-wedged plan-quota #5393',
      steps: [],
    })
    expect(out.settledRollOverrides).toBe(
      'delta-corp/bp-keycloak=quota-wedged plan-quota #5393',
    )
  })

  it('falls back to the raw status-ConfigMap key (older typed field absent)', () => {
    const out = parseCutoverStatus({
      cutoverComplete: true,
      steps: [],
      raw: {
        settledRollOverrides:
          'delta-corp/bp-keycloak=quota-wedged plan-quota #5393\ndelta-corp/bp-wordpress-tenant=dependency of bp-keycloak #5393',
      },
    })
    expect(out.settledRollOverrides).toMatch(/bp-keycloak=/)
    expect(out.settledRollOverrides).toMatch(/bp-wordpress-tenant=/)
  })

  it('is undefined when no override was used or a clean pass cleared it', () => {
    expect(
      parseCutoverStatus({ cutoverComplete: true, steps: [] })
        .settledRollOverrides,
    ).toBeUndefined()
    // A clean pass writes the empty string to CLEAR a previous record —
    // the empty string must not surface as a phantom entry.
    expect(
      parseCutoverStatus({
        cutoverComplete: true,
        settledRollOverrides: '',
        steps: [],
      }).settledRollOverrides,
    ).toBeUndefined()
  })

  it('survives the flat SSE status-ConfigMap shape', () => {
    const out = parseCutoverStatusConfigMap({
      cutoverComplete: 'false',
      settledRollOverrides:
        'delta-corp/bp-keycloak=quota-wedged plan-quota #5393',
      'step.harbor-prewarm.result': 'success',
    })
    expect(out?.settledRollOverrides).toBe(
      'delta-corp/bp-keycloak=quota-wedged plan-quota #5393',
    )
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
