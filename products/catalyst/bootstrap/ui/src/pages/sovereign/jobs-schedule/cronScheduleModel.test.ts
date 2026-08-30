/**
 * cronScheduleModel.test.ts — pins the pure derivation behind the Schedule
 * surface (P3-frontend, Refs #6703): CronJob rows + fire placement, child-Job
 * run-history matching, and the collision set — all read off a k8scache
 * snapshot exactly as the live SSE stream delivers it.
 */

import { describe, expect, it } from 'vitest'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  childRunsOfCron,
  collisionMinutes,
  deriveCronRows,
  formatDuration,
  jobRunStatus,
} from './cronScheduleModel'

function cronjob(
  name: string,
  ns: string,
  schedule: string,
  extra: Partial<{ suspend: boolean; lastScheduleTime: string; cluster: string }> = {},
): [string, K8sObject] {
  const cluster = extra.cluster ?? ''
  const key = `cronjob:${ns}/${name}${cluster ? `@${cluster}` : ''}`
  const obj: K8sObject = {
    apiVersion: 'batch/v1',
    kind: 'CronJob',
    metadata: { name, namespace: ns },
    spec: { schedule, ...(extra.suspend ? { suspend: true } : {}) },
    status: extra.lastScheduleTime ? { lastScheduleTime: extra.lastScheduleTime } : {},
    ...(cluster ? { clusterId: cluster } : {}),
  }
  return [key, obj]
}

function childJob(
  name: string,
  ns: string,
  cronName: string,
  status: Record<string, unknown>,
  cluster = '',
): [string, K8sObject] {
  const key = `job:${ns}/${name}${cluster ? `@${cluster}` : ''}`
  const obj: K8sObject = {
    apiVersion: 'batch/v1',
    kind: 'Job',
    metadata: {
      name,
      namespace: ns,
      ownerReferences: [{ apiVersion: 'batch/v1', kind: 'CronJob', name: cronName }],
    },
    status,
    ...(cluster ? { clusterId: cluster } : {}),
  }
  return [key, obj]
}

const REF = new Date(2026, 7, 24, 6, 0) // Mon 2026-08-24 06:00 local

describe('jobRunStatus', () => {
  it('reads Complete/Failed conditions first', () => {
    expect(jobRunStatus({ status: { conditions: [{ type: 'Complete', status: 'True' }] } })).toBe('succeeded')
    expect(jobRunStatus({ status: { conditions: [{ type: 'Failed', status: 'True' }] } })).toBe('failed')
  })
  it('falls back to count fields', () => {
    expect(jobRunStatus({ status: { succeeded: 1 } })).toBe('succeeded')
    expect(jobRunStatus({ status: { failed: 2 } })).toBe('failed')
    expect(jobRunStatus({ status: { active: 1 } })).toBe('running')
    expect(jobRunStatus({ status: {} })).toBe('unknown')
  })
})

describe('deriveCronRows', () => {
  it('shapes each CronJob with schedule, description, fires + next fire', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('nightly-backup', 'db', '0 0 * * *'),
      cronjob('noon-report', 'analytics', '0 12 * * *'),
    ])
    const rows = deriveCronRows(snap, REF)
    expect(rows).toHaveLength(2)
    // Sorted by namespace: analytics before db.
    expect(rows[0].name).toBe('noon-report')
    expect(rows[0].description).toBe('Every day at 12:00')
    expect(rows[0].fireMinutes).toEqual([720])
    // Next fire today at 12:00 (REF is 06:00).
    expect(rows[0].nextFire).toEqual(new Date(2026, 7, 24, 12, 0))

    expect(rows[1].name).toBe('nightly-backup')
    expect(rows[1].fireMinutes).toEqual([0])
    // Midnight already passed on REF day → next fire tomorrow 00:00.
    expect(rows[1].nextFire).toEqual(new Date(2026, 7, 25, 0, 0))
  })

  it('draws no marks for a suspended CronJob', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('paused', 'db', '0 12 * * *', { suspend: true }),
    ])
    const rows = deriveCronRows(snap, REF)
    expect(rows[0].suspended).toBe(true)
    expect(rows[0].fireMinutes).toEqual([])
    expect(rows[0].nextFire).toBeNull()
  })

  it('attaches the latest child run + run count', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('scan', 'sec', '0 * * * *'),
      childJob('scan-1', 'sec', 'scan', {
        startTime: '2026-08-24T04:00:00Z',
        completionTime: '2026-08-24T04:00:30Z',
        succeeded: 1,
      }),
      childJob('scan-2', 'sec', 'scan', {
        startTime: '2026-08-24T05:00:00Z',
        completionTime: '2026-08-24T05:00:20Z',
        succeeded: 1,
      }),
    ])
    const rows = deriveCronRows(snap, REF)
    expect(rows[0].runCount).toBe(2)
    // Newest first → scan-2 is the latest run.
    expect(rows[0].latestRun?.name).toBe('scan-2')
    expect(rows[0].latestRun?.status).toBe('succeeded')
  })

  it('leaves an unparseable schedule as a null parse with a raw description', () => {
    const snap: K8sSnapshot = new Map([cronjob('broken', 'x', 'not-a-cron')])
    const rows = deriveCronRows(snap, REF)
    expect(rows[0].parsed).toBeNull()
    expect(rows[0].fireMinutes).toEqual([])
  })
})

describe('childRunsOfCron', () => {
  it('matches by ownerReference + namespace, newest first', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('scan', 'sec', '0 * * * *'),
      childJob('scan-old', 'sec', 'scan', { startTime: '2026-08-24T01:00:00Z', succeeded: 1 }),
      childJob('scan-new', 'sec', 'scan', { startTime: '2026-08-24T03:00:00Z', failed: 1 }),
      // Different owner — must NOT be listed.
      childJob('other-1', 'sec', 'other-cron', { startTime: '2026-08-24T02:00:00Z', succeeded: 1 }),
      // Same name, different namespace — must NOT be listed.
      childJob('scan-x', 'db', 'scan', { startTime: '2026-08-24T02:30:00Z', succeeded: 1 }),
    ])
    const row = deriveCronRows(snap, REF).find((r) => r.name === 'scan')!
    const runs = childRunsOfCron(snap, row)
    expect(runs.map((r) => r.name)).toEqual(['scan-new', 'scan-old'])
    expect(runs[0].status).toBe('failed')
  })

  it('region-scopes child jobs when both carry a cluster id (#5571)', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('scan', 'sec', '0 * * * *', { cluster: 'region-a' }),
      childJob('scan-a', 'sec', 'scan', { startTime: '2026-08-24T01:00:00Z', succeeded: 1 }, 'region-a'),
      childJob('scan-b', 'sec', 'scan', { startTime: '2026-08-24T01:00:00Z', succeeded: 1 }, 'region-b'),
    ])
    const row = deriveCronRows(snap, REF).find((r) => r.cluster === 'region-a')!
    const runs = childRunsOfCron(snap, row)
    expect(runs.map((r) => r.name)).toEqual(['scan-a'])
  })

  it('computes run duration from start + completion', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('scan', 'sec', '0 * * * *'),
      childJob('scan-1', 'sec', 'scan', {
        startTime: '2026-08-24T04:00:00Z',
        completionTime: '2026-08-24T04:02:05Z',
        succeeded: 1,
      }),
    ])
    const row = deriveCronRows(snap, REF)[0]
    const runs = childRunsOfCron(snap, row)
    expect(runs[0].durationMs).toBe(125_000)
  })
})

describe('collisionMinutes', () => {
  it('flags a minute where two distinct CronJobs fire together', () => {
    const snap: K8sSnapshot = new Map([
      cronjob('backup-a', 'db', '0 0 * * *'), // midnight
      cronjob('backup-b', 'cache', '0 0 * * *'), // midnight — collides
      cronjob('noon', 'x', '0 12 * * *'), // 12:00 — alone
    ])
    const rows = deriveCronRows(snap, REF)
    const collisions = collisionMinutes(rows)
    expect(collisions.get(0)).toBe(2) // 2 crons at 00:00
    expect(collisions.has(720)).toBe(false) // noon is alone
  })
})

describe('formatDuration', () => {
  it('formats ms / s / m / h buckets', () => {
    expect(formatDuration(820)).toBe('820ms')
    expect(formatDuration(3200)).toBe('3.2s')
    expect(formatDuration(125_000)).toBe('2m 05s')
    expect(formatDuration(3_720_000)).toBe('1h 02m')
    expect(formatDuration(undefined)).toBe('—')
  })
})
