/**
 * cronScheduleModel.adherence-6703.test.ts — scheduled-vs-actual + origin
 * (epic #6703 P3/P4b).
 *
 * The controller stamps `batch.kubernetes.io/cronjob-scheduled-timestamp` on
 * every child Job it creates; a manual run (Run now / kubectl create job
 * --from) carries `cronjob.kubernetes.io/instantiate: manual` and no slot.
 * The lag is startTime − slot. Measured on hw307 2026-11-05: the syft-grype
 * run for slot 2026-11-02T18:30Z started 2026-11-03T02:33Z — 8h03m late —
 * which is exactly the signal this derivation exists to surface.
 */

import { describe, expect, it } from 'vitest'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  childRunsOfCron,
  deriveCronRows,
  formatLag,
  scheduleAdherence,
  LATE_RUN_THRESHOLD_MS,
  type CronRun,
} from './cronScheduleModel'

function cron(name: string, ns: string, labels: Record<string, string> = {}, spec: Record<string, unknown> = {}): [string, K8sObject] {
  return [
    `cronjob:${ns}/${name}`,
    {
      apiVersion: 'batch/v1',
      kind: 'CronJob',
      metadata: { name, namespace: ns, uid: `uid-${name}`, labels },
      spec: { schedule: '30 2 * * *', ...spec },
      status: { lastSuccessfulTime: '2026-11-03T02:33:27Z' },
    },
  ]
}

function run(
  name: string,
  ns: string,
  cronName: string,
  opts: { scheduled?: string; start?: string; manual?: boolean; status?: Record<string, unknown> },
): [string, K8sObject] {
  const annotations: Record<string, string> = {}
  if (opts.scheduled) annotations['batch.kubernetes.io/cronjob-scheduled-timestamp'] = opts.scheduled
  if (opts.manual) annotations['cronjob.kubernetes.io/instantiate'] = 'manual'
  return [
    `job:${ns}/${name}`,
    {
      apiVersion: 'batch/v1',
      kind: 'Job',
      metadata: {
        name,
        namespace: ns,
        annotations,
        ownerReferences: [{ apiVersion: 'batch/v1', kind: 'CronJob', name: cronName }],
      },
      status: { startTime: opts.start, ...(opts.status ?? { succeeded: 1 }) },
    },
  ]
}

const REF = new Date(2026, 10, 5, 12, 0)

describe('scheduled vs actual (#6703 P3)', () => {
  it('reads the slot annotation and derives the lag — the hw307 syft run is 8h03m late', () => {
    const snap: K8sSnapshot = new Map([
      cron('syft-grype-bp-syft-grype', 'syft-grype'),
      run('syft-grype-bp-syft-grype-29894070', 'syft-grype', 'syft-grype-bp-syft-grype', {
        scheduled: '2026-11-02T18:30:00Z',
        start: '2026-11-03T02:33:04Z',
      }),
      run('syft-grype-bp-syft-grype-29895510', 'syft-grype', 'syft-grype-bp-syft-grype', {
        scheduled: '2026-11-03T18:30:00Z',
        start: '2026-11-03T18:30:06Z',
      }),
    ])
    const [row] = deriveCronRows(snap, REF)
    const runs = childRunsOfCron(snap, row)
    expect(runs.map((r) => r.name)).toEqual([
      'syft-grype-bp-syft-grype-29895510', // newest first
      'syft-grype-bp-syft-grype-29894070',
    ])
    const late = runs[1]
    expect(late.scheduledTime).toBe('2026-11-02T18:30:00Z')
    expect(late.lagMs).toBe((8 * 60 + 3) * 60_000 + 4_000)
    expect(late.manual).toBe(false)
    expect(formatLag(late.lagMs)).toBe('+8h 03m')
    // Six seconds is inside the slot — on time.
    expect(runs[0].lagMs).toBe(6_000)
    expect(formatLag(runs[0].lagMs)).toBe('+6.0s')

    const adherence = scheduleAdherence(runs)
    expect(adherence.scheduledRuns).toBe(2)
    expect(adherence.lateRuns).toBe(1)
    expect(adherence.worstLagMs).toBe(late.lagMs)
    expect(adherence.worstRun?.name).toBe('syft-grype-bp-syft-grype-29894070')
    expect(adherence.manualRuns).toBe(0)
    // The table flags the LATEST run only when it was late; here the latest
    // run was on time, so the row carries no late flag.
    expect(row.latestRun?.lagMs).toBe(6_000)
    expect((row.latestRun?.lagMs ?? 0) > LATE_RUN_THRESHOLD_MS).toBe(false)
  })

  it('a manual run has no slot and no lag, and is counted apart', () => {
    const snap: K8sSnapshot = new Map([
      cron('openbao-snapshot-save', 'openbao'),
      run('openbao-snapshot-save-manual-1762358400', 'openbao', 'openbao-snapshot-save', {
        manual: true,
        start: '2026-11-05T16:00:00Z',
        // A manual run can carry the slot annotation if someone copies it —
        // the manual marker still wins.
        scheduled: '2026-11-05T15:00:00Z',
      }),
      run('openbao-snapshot-save-29898290', 'openbao', 'openbao-snapshot-save', {
        scheduled: '2026-11-05T16:50:00Z',
        start: '2026-11-05T16:50:42Z',
        status: { failed: 1 },
      }),
    ])
    const [row] = deriveCronRows(snap, REF)
    const runs = childRunsOfCron(snap, row)
    const manual = runs.find((r) => r.manual)
    expect(manual).toBeTruthy()
    expect(manual?.scheduledTime).toBeUndefined()
    expect(manual?.lagMs).toBeUndefined()
    const adherence = scheduleAdherence(runs)
    expect(adherence.manualRuns).toBe(1)
    expect(adherence.scheduledRuns).toBe(1)
    expect(adherence.lateRuns).toBe(0)
  })

  it('a run with no slot annotation (pre-1.26 controller) is neither scheduled nor manual', () => {
    const runs: CronRun[] = [
      { name: 'x', namespace: 'ns', status: 'succeeded', manual: false, startTime: '2026-11-05T16:00:00Z' },
    ]
    const adherence = scheduleAdherence(runs)
    expect(adherence).toEqual({ scheduledRuns: 0, lateRuns: 0, manualRuns: 0, worstLagMs: null, worstRun: null })
  })

  it('formatLag is signed and compact', () => {
    expect(formatLag(undefined)).toBe('—')
    expect(formatLag(0)).toBe('on time')
    expect(formatLag(999)).toBe('on time')
    expect(formatLag(-4_000)).toBe('−4.0s')
    expect(formatLag(90_000)).toBe('+1m 30s')
  })
})

describe('origin + time zone (#6703 P4b)', () => {
  it('a chart CronJob is origin=chart with its Blueprint; a console schedule is origin=console with its source', () => {
    const snap: K8sSnapshot = new Map([
      cron('openbao-snapshot-save', 'openbao', { 'catalyst.openova.io/blueprint': 'bp-openbao' }),
      cron(
        'openbao-snapshot-noon',
        'openbao',
        {
          'catalyst.openova.io/blueprint': 'bp-openbao',
          'catalyst.openova.io/schedule-origin': 'console',
          'catalyst.openova.io/schedule-source': 'openbao-snapshot-save',
          'app.kubernetes.io/managed-by': 'catalyst-api',
        },
        { schedule: '0 12 * * 1-5', timeZone: 'Asia/Muscat' },
      ),
    ])
    const rows = deriveCronRows(snap, REF)
    const chart = rows.find((r) => r.name === 'openbao-snapshot-save')
    const console_ = rows.find((r) => r.name === 'openbao-snapshot-noon')
    expect(chart?.origin).toBe('chart')
    expect(chart?.blueprint).toBe('bp-openbao')
    expect(chart?.sourceName).toBeUndefined()
    expect(chart?.timeZone).toBeUndefined()
    expect(chart?.uid).toBe('uid-openbao-snapshot-save')
    expect(chart?.lastSuccessfulTime).toBe('2026-11-03T02:33:27Z')
    expect(console_?.origin).toBe('console')
    expect(console_?.sourceName).toBe('openbao-snapshot-save')
    expect(console_?.timeZone).toBe('Asia/Muscat')
    expect(console_?.description).toBe('Monday–Friday at 12:00')
  })
})
