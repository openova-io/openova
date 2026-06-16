/**
 * jobs.types.test.ts — the §4b "read kind, not prefix" contract (issue
 * #3646): jobKind prefers the backend-stamped kind and falls back to a
 * JobName-prefix derivation only for a legacy row; isJobRetryable +
 * isHealthAxisStatus gate the Retry affordance + the health badge.
 */
import { describe, it, expect } from 'vitest'
import { jobKind, isJobRetryable, isHealthAxisStatus } from './jobs.types'
import type { Job } from './jobs.types'

function leaf(partial: Partial<Job>): Job {
  return {
    id: 'd:' + (partial.jobName ?? 'x'),
    jobName: 'x',
    type: 'install',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...partial,
  }
}

describe('jobKind — read the typed kind, not the prefix', () => {
  it('prefers the backend-stamped kind', () => {
    expect(jobKind(leaf({ kind: 'cron', jobName: 'install-openbao' }))).toBe('cron')
  })

  it('falls back to JobName-prefix derivation for a legacy row (no kind)', () => {
    expect(jobKind(leaf({ jobName: 'install-openbao' }))).toBe('install')
    expect(jobKind(leaf({ jobName: 'reconcile-flux-system' }))).toBe('reconcile')
    expect(jobKind(leaf({ jobName: 'cron-openbao-snapshot-save' }))).toBe('cron')
    expect(jobKind(leaf({ jobName: 'task-cnpg-pair-3-join' }))).toBe('task')
    expect(jobKind(leaf({ jobName: 'reconciler-sso-bridge' }))).toBe('reconciler')
    expect(jobKind(leaf({ jobName: 'cutover-step-harbor-prewarm' }))).toBe('step')
    expect(jobKind(leaf({ jobName: 'mutation-create-cluster' }))).toBe('mutation')
    expect(jobKind(leaf({ type: 'group', jobName: 'reconcilers' }))).toBe('group')
    expect(jobKind(leaf({ jobName: 'tofu-init' }))).toBe('lifecycle')
  })
})

describe('isJobRetryable — only failed/degraded/failing rows offer Retry', () => {
  it('returns true for failed + health-unhealthy', () => {
    expect(isJobRetryable('failed')).toBe(true)
    expect(isJobRetryable('degraded')).toBe(true)
    expect(isJobRetryable('failing')).toBe(true)
  })
  it('returns false for healthy/running/succeeded/pending', () => {
    expect(isJobRetryable('healthy')).toBe(false)
    expect(isJobRetryable('running')).toBe(false)
    expect(isJobRetryable('succeeded')).toBe(false)
    expect(isJobRetryable('pending')).toBe(false)
  })
})

describe('isHealthAxisStatus', () => {
  it('recognises the three health-axis values', () => {
    expect(isHealthAxisStatus('healthy')).toBe(true)
    expect(isHealthAxisStatus('degraded')).toBe(true)
    expect(isHealthAxisStatus('failing')).toBe(true)
    expect(isHealthAxisStatus('succeeded')).toBe(false)
    expect(isHealthAxisStatus('failed')).toBe(false)
  })
})
