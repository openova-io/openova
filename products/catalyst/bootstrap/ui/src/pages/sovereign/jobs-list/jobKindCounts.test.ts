/**
 * jobKindCounts.test.ts — P1b (Refs #6703).
 *
 * `deriveJobKindCounts` is the production derivation behind the /jobs
 * chip badges. These guards pin:
 *   • the tally is per `jobKind(j)` — the backend-stamped kind wins, and a
 *     legacy row with no `kind` derives from its jobName prefix;
 *   • `group` rows are EXCLUDED (a synthesised parent is not an engine);
 *   • a kind with no rows reports 0 (not undefined) so the chip can apply
 *     its "hide 0-count non-active" rule on a real number.
 */

import { describe, it, expect } from 'vitest'
import type { Job, JobKind, JobType } from '@/lib/jobs.types'
import { deriveJobKindCounts, nullJobKindCounts } from './jobKindCounts'

function job(id: string, opts: Partial<Job> = {}): Job {
  return {
    id,
    jobName: opts.jobName ?? id,
    type: (opts.type ?? 'install') as JobType,
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: 'succeeded',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...opts,
  }
}

describe('deriveJobKindCounts', () => {
  it('tallies each job under its backend-stamped kind', () => {
    const counts = deriveJobKindCounts([
      job('a', { kind: 'install' }),
      job('b', { kind: 'install' }),
      job('c', { kind: 'cron' }),
      job('d', { kind: 'mutation' }),
      job('e', { kind: 'step' }),
    ])
    expect(counts.install).toBe(2)
    expect(counts.cron).toBe(1)
    expect(counts.mutation).toBe(1)
    expect(counts.step).toBe(1)
  })

  it('reports 0 (not undefined) for a kind with no rows', () => {
    const counts = deriveJobKindCounts([job('a', { kind: 'install' })])
    expect(counts.reconciler).toBe(0)
    expect(counts.task).toBe(0)
    expect(counts.lifecycle).toBe(0)
  })

  it('EXCLUDES group rows — a synthesised parent is not a filterable engine', () => {
    const counts = deriveJobKindCounts([
      job('grp', { type: 'group', kind: 'group', childIds: ['a'] }),
      job('a', { kind: 'install', parentId: 'grp' }),
    ])
    // The group must NOT be counted anywhere; only the install leaf is.
    expect(counts.group).toBe(0)
    expect(counts.install).toBe(1)
    const total = (Object.keys(counts) as JobKind[]).reduce((s, k) => s + counts[k], 0)
    expect(total).toBe(1)
  })

  it('derives a legacy row with no kind from its jobName prefix (jobKind, not the raw name)', () => {
    const counts = deriveJobKindCounts([
      job('x', { jobName: 'install-openbao' }), // no kind → jobKind → install
      job('y', { jobName: 'cron-trivy-scan' }), // no kind → jobKind → cron
      job('z', { jobName: 'anything-else' }), // no prefix match → lifecycle
    ])
    expect(counts.install).toBe(1)
    expect(counts.cron).toBe(1)
    expect(counts.lifecycle).toBe(1)
  })

  it('empty input yields an all-zero map with every JobKind key present', () => {
    const counts = deriveJobKindCounts([])
    for (const k of [
      'install', 'reconcile', 'step', 'mutation',
      'cron', 'task', 'reconciler', 'lifecycle', 'group',
    ] as JobKind[]) {
      expect(counts[k]).toBe(0)
    }
  })
})

describe('nullJobKindCounts (loading state)', () => {
  const ALL: JobKind[] = [
    'install', 'reconcile', 'step', 'mutation',
    'cron', 'task', 'reconciler', 'lifecycle', 'group',
  ]

  it('maps every JobKind to null (renders "—", not "(0)")', () => {
    const counts = nullJobKindCounts()
    for (const k of ALL) expect(counts[k]).toBeNull()
  })

  it('is DISTINCT from the all-zero map — null ("loading") is not 0 ("none")', () => {
    const loading = nullJobKindCounts()
    const empty = deriveJobKindCounts([])
    // the whole point: while the backend list loads we must NOT show 0,
    // which reads as "there are none", nor a reducer tally.
    expect(loading.install).toBeNull()
    expect(empty.install).toBe(0)
  })
})
