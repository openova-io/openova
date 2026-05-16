/**
 * handoverStageOverride.test.ts — unit tests for the synthetic-stage
 * status override that resolves the Gap C JobsPage phantom-Pending
 * issue (session_2026_05_16_t117_dod_partial.md).
 *
 * The override module is the only piece of logic that re-derives
 * Apps / Handover / Cutover stage status from the deployment record.
 * If this drifts every JobsPage consumer's notion of "pending" drifts
 * with it, so every contract bullet gets a direct test.
 */

import { describe, it, expect } from 'vitest'
import type { Job } from '@/lib/jobs.types'
import {
  applyHandoverStageOverride,
  isHandoverFired,
  matchLifecycleStageSlug,
} from './handoverStageOverride'

const DEP = 'abc123def456'

function makeJob(id: string, status: Job['status'], overrides: Partial<Job> = {}): Job {
  return {
    id,
    jobName: id,
    type: 'group',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status,
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...overrides,
  }
}

describe('matchLifecycleStageSlug', () => {
  it('matches the three lifecycle slugs in their bare top-level form', () => {
    expect(matchLifecycleStageSlug(`${DEP}:apps`)).toBe('apps')
    expect(matchLifecycleStageSlug(`${DEP}:handover`)).toBe('handover')
    expect(matchLifecycleStageSlug(`${DEP}:cutover`)).toBe('cutover')
  })

  it('matches the per-region variant suffix', () => {
    expect(matchLifecycleStageSlug(`${DEP}:hel1-2:apps`)).toBe('apps')
    expect(matchLifecycleStageSlug(`${DEP}:nbg1-1:handover`)).toBe('handover')
    expect(matchLifecycleStageSlug(`${DEP}:sin-2:cutover`)).toBe('cutover')
  })

  it('does NOT match install-* leaf job ids (anti-regression)', () => {
    expect(matchLifecycleStageSlug(`${DEP}:install-cilium`)).toBeNull()
    expect(matchLifecycleStageSlug(`${DEP}:install-self-sovereign-cutover`)).toBeNull()
    expect(matchLifecycleStageSlug(`${DEP}:install-hel1-2:cilium`)).toBeNull()
  })

  it('does NOT match other group slugs (bootstrap-kit / provisioner)', () => {
    expect(matchLifecycleStageSlug(`${DEP}:bootstrap-kit`)).toBeNull()
    expect(matchLifecycleStageSlug(`${DEP}:provisioner`)).toBeNull()
    expect(matchLifecycleStageSlug(`${DEP}:hel1-2:bootstrap-kit`)).toBeNull()
  })

  it('returns null for empty / non-matching strings', () => {
    expect(matchLifecycleStageSlug('')).toBeNull()
    expect(matchLifecycleStageSlug('apps')).toBeNull() // no `:` separator
    expect(matchLifecycleStageSlug(`${DEP}`)).toBeNull()
  })
})

describe('isHandoverFired', () => {
  it('is false for null / undefined snapshot', () => {
    expect(isHandoverFired(null)).toBe(false)
    expect(isHandoverFired(undefined)).toBe(false)
  })

  it('is true when status="ready" alone', () => {
    expect(isHandoverFired({ status: 'ready' })).toBe(true)
  })

  it('is true when handoverFiredAt is non-empty (top-level)', () => {
    expect(isHandoverFired({ handoverFiredAt: '2026-05-16T09:55:06Z' })).toBe(true)
  })

  it('is true when handoverFiredAt is non-empty (nested under result)', () => {
    expect(
      isHandoverFired({ result: { handoverFiredAt: '2026-05-16T09:55:06Z' } }),
    ).toBe(true)
  })

  it('is false for a still-installing snapshot', () => {
    expect(isHandoverFired({ status: 'phase1-installing' })).toBe(false)
    expect(isHandoverFired({ status: 'phase0-running' })).toBe(false)
  })

  it('is false when handoverFiredAt is an empty string', () => {
    expect(isHandoverFired({ handoverFiredAt: '' })).toBe(false)
    expect(isHandoverFired({ result: { handoverFiredAt: '' } })).toBe(false)
  })
})

describe('applyHandoverStageOverride', () => {
  const HANDOVER_FIRED_SNAPSHOT = {
    status: 'ready',
    handoverFiredAt: '2026-05-16T09:55:06Z',
    handoverURL: 'https://console.example.com/auth/handover?token=eyJ',
  }
  const STILL_INSTALLING_SNAPSHOT = {
    status: 'phase1-installing',
  }

  it('coerces pending Apps / Handover / Cutover stages to succeeded when handover fired', () => {
    const jobs: Job[] = [
      makeJob(`${DEP}:apps`, 'pending'),
      makeJob(`${DEP}:handover`, 'pending'),
      makeJob(`${DEP}:cutover`, 'pending'),
    ]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out.map((j) => j.status)).toEqual(['succeeded', 'succeeded', 'succeeded'])
  })

  it('coerces per-region lifecycle stages to succeeded when handover fired', () => {
    const jobs: Job[] = [
      makeJob(`${DEP}:hel1-2:apps`, 'pending'),
      makeJob(`${DEP}:nbg1-1:handover`, 'pending'),
      makeJob(`${DEP}:sin-2:cutover`, 'pending'),
    ]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out.map((j) => j.status)).toEqual(['succeeded', 'succeeded', 'succeeded'])
  })

  it('coerces running lifecycle stages too (running is non-terminal)', () => {
    const jobs: Job[] = [makeJob(`${DEP}:apps`, 'running')]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out[0]!.status).toBe('succeeded')
  })

  it('does NOT touch lifecycle stages when handover has NOT fired', () => {
    const jobs: Job[] = [
      makeJob(`${DEP}:apps`, 'pending'),
      makeJob(`${DEP}:handover`, 'pending'),
    ]
    const out = applyHandoverStageOverride(jobs, STILL_INSTALLING_SNAPSHOT)
    expect(out.map((j) => j.status)).toEqual(['pending', 'pending'])
    // Same reference returned for no-op invocation.
    expect(out).toBe(jobs)
  })

  it('does NOT touch a terminal succeeded lifecycle stage (backend wins)', () => {
    const jobs: Job[] = [makeJob(`${DEP}:apps`, 'succeeded')]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out[0]!.status).toBe('succeeded')
    expect(out).toBe(jobs) // no-op → same reference
  })

  it('does NOT touch a terminal failed lifecycle stage (backend wins)', () => {
    const jobs: Job[] = [makeJob(`${DEP}:cutover`, 'failed')]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out[0]!.status).toBe('failed')
    expect(out).toBe(jobs)
  })

  it('passes non-lifecycle jobs through untouched even when handover fired', () => {
    const jobs: Job[] = [
      makeJob(`${DEP}:install-cilium`, 'pending', { type: 'install' }),
      makeJob(`${DEP}:bootstrap-kit`, 'pending'),
      makeJob(`${DEP}:provisioner`, 'running'),
    ]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out.map((j) => j.status)).toEqual(['pending', 'pending', 'running'])
    expect(out).toBe(jobs)
  })

  it('mixes overridden + pass-through correctly in a single call', () => {
    const jobs: Job[] = [
      makeJob(`${DEP}:install-cilium`, 'succeeded', { type: 'install' }), // pass-through
      makeJob(`${DEP}:apps`, 'pending'), // override
      makeJob(`${DEP}:bootstrap-kit`, 'succeeded'), // pass-through
      makeJob(`${DEP}:hel1-2:handover`, 'pending'), // override
      makeJob(`${DEP}:cutover`, 'failed'), // pass-through (terminal)
    ]
    const out = applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(out.map((j) => j.status)).toEqual([
      'succeeded',
      'succeeded',
      'succeeded',
      'succeeded',
      'failed',
    ])
    // Returns a new array (mutation happened) so memo-aware consumers
    // re-render.
    expect(out).not.toBe(jobs)
    // Pass-through items keep object identity.
    expect(out[0]).toBe(jobs[0])
    expect(out[2]).toBe(jobs[2])
    expect(out[4]).toBe(jobs[4])
  })

  it('does not mutate the input array or input job objects', () => {
    const jobs: Job[] = [makeJob(`${DEP}:apps`, 'pending')]
    const snapshotBefore = JSON.parse(JSON.stringify(jobs))
    applyHandoverStageOverride(jobs, HANDOVER_FIRED_SNAPSHOT)
    expect(jobs).toEqual(snapshotBefore)
  })
})
