/**
 * cronScheduleModel.ts — pure derivation of the consolidated CronJob
 * Schedule view's data from the live k8scache SSE snapshot (P3-frontend,
 * Refs #6703).
 *
 * The /jobs job-store rows carry a CronJob's LATEST-run status but not the
 * raw `.spec.schedule` nor the full child-Job run history the founder asked
 * for ("what fires at 12:00 + collisions" and "the complete history of a
 * cron"). Those live only on the raw batch/v1 CronJob + Job objects, which
 * the catalyst-api k8scache now watches (kinds `cronjob` + `job`). This
 * module reads them straight off the same `K8sSnapshot` the Cloud list
 * pages consume — no new fetch layer — and shapes them for the SVG timeline
 * + the run-history drawer.
 *
 * Everything here is a pure function of (snapshot, referenceDate) so the
 * correctness-critical fire-placement + child-Job matching is unit-tested
 * without a DOM or a live EventSource.
 */

import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  describeCron,
  fireMinutesOnDate,
  nextFireTime,
  tryParseCron,
  type ParsedCron,
} from '@/shared/lib/cronSchedule'

/** k8s Job terminal/active state, mapped to a small closed vocabulary. */
export type CronRunStatus = 'succeeded' | 'failed' | 'running' | 'unknown'

/** One historical run (child Job) of a CronJob. */
export interface CronRun {
  /** Child Job name. */
  name: string
  namespace: string
  status: CronRunStatus
  /** ISO start time (`.status.startTime`), when present. */
  startTime?: string
  /** ISO completion time (`.status.completionTime`), when present. */
  completionTime?: string
  /** Wall-clock run duration in ms, when both start + completion are known. */
  durationMs?: number
}

/** One CronJob row on the Schedule surface. */
export interface CronRow {
  /** Stable id — `namespace/name@cluster` (region-safe, #5571). */
  key: string
  name: string
  namespace: string
  /** k8scache region id, when the stream attributes one. */
  cluster?: string
  /** Raw 5-field schedule (`.spec.schedule`). */
  schedule: string
  /** Parsed schedule, or null when unparseable / @reboot. */
  parsed: ParsedCron | null
  /** Human-readable schedule (`describeCron`). */
  description: string
  /** `.spec.suspend === true` — a suspended CronJob does not fire. */
  suspended: boolean
  /** `.status.lastScheduleTime` — when the controller last scheduled a run. */
  lastScheduleTime?: string
  /** Minutes-of-day (0..1439) the cron fires on the reference date. */
  fireMinutes: number[]
  /** Next wall-clock fire at/after the reference date (local calendar). */
  nextFire: Date | null
  /** The most-recent child-Job run, when any exists in the snapshot. */
  latestRun: CronRun | null
  /** How many child-Job runs are visible in the snapshot. */
  runCount: number
}

/** Read a nested string from an unstructured object without `any`. */
function str(obj: Record<string, unknown> | undefined, key: string): string | undefined {
  const v = obj?.[key]
  return typeof v === 'string' ? v : undefined
}
function num(obj: Record<string, unknown> | undefined, key: string): number | undefined {
  const v = obj?.[key]
  return typeof v === 'number' ? v : undefined
}

/**
 * Classify a k8s Job object's run status. Prefers the explicit
 * `.status.conditions` (Complete / Failed), falling back to the count
 * fields (`succeeded` / `failed` / `active`).
 */
export function jobRunStatus(job: K8sObject): CronRunStatus {
  const status = (job.status as Record<string, unknown> | undefined) ?? {}
  const conds = status['conditions'] as Array<Record<string, unknown>> | undefined
  if (Array.isArray(conds)) {
    for (const c of conds) {
      if (c?.['status'] !== 'True') continue
      if (c?.['type'] === 'Complete') return 'succeeded'
      if (c?.['type'] === 'Failed') return 'failed'
    }
  }
  if ((num(status, 'succeeded') ?? 0) > 0) return 'succeeded'
  if ((num(status, 'failed') ?? 0) > 0) return 'failed'
  if ((num(status, 'active') ?? 0) > 0) return 'running'
  return 'unknown'
}

/** True when `job` is a child run of the CronJob `cronName`/`ns` (ownerRef). */
function isChildOf(job: K8sObject, cronName: string, ns: string): boolean {
  if ((job.metadata?.namespace ?? '') !== ns) return false
  const refs = job.metadata?.ownerReferences ?? []
  return refs.some((r) => r?.kind === 'CronJob' && r?.name === cronName)
}

function toRun(job: K8sObject): CronRun {
  const status = (job.status as Record<string, unknown> | undefined) ?? {}
  const startTime = str(status, 'startTime')
  const completionTime = str(status, 'completionTime')
  let durationMs: number | undefined
  if (startTime && completionTime) {
    const d = new Date(completionTime).getTime() - new Date(startTime).getTime()
    if (Number.isFinite(d) && d >= 0) durationMs = d
  }
  return {
    name: job.metadata?.name ?? '',
    namespace: job.metadata?.namespace ?? '',
    status: jobRunStatus(job),
    startTime,
    completionTime,
    durationMs,
  }
}

/** Sort key for a run — startTime (newest first), then creationTimestamp. */
function runSortValue(job: K8sObject): number {
  const status = (job.status as Record<string, unknown> | undefined) ?? {}
  const start = str(status, 'startTime') ?? job.metadata?.creationTimestamp
  const t = start ? new Date(start).getTime() : 0
  return Number.isFinite(t) ? t : 0
}

/**
 * All child-Job runs of a CronJob row, newest first. Reads raw `job:` keys
 * from the snapshot and matches by ownerReference (kind=CronJob) + namespace,
 * and (when both carry it) region — a 2-region Sovereign runs the same
 * CronJob in both regions, so an unqualified name match would cross-list.
 */
export function childRunsOfCron(snapshot: K8sSnapshot, row: CronRow): CronRun[] {
  const jobs: K8sObject[] = []
  for (const [key, obj] of snapshot.entries()) {
    if (!key.startsWith('job:')) continue
    if (!isChildOf(obj, row.name, row.namespace)) continue
    // Region-scope when both sides carry a cluster id (#5571).
    if (row.cluster && obj.clusterId && obj.clusterId !== row.cluster) continue
    jobs.push(obj)
  }
  jobs.sort((a, b) => runSortValue(b) - runSortValue(a))
  return jobs.map(toRun)
}

/**
 * Derive the CronJob rows from the snapshot for a reference date. The date
 * gates `fireMinutes` (day-of-month / month / day-of-week) and seeds
 * next-fire; the timeline x-axis itself is the cron's wall-clock minute.
 */
export function deriveCronRows(snapshot: K8sSnapshot, referenceDate: Date): CronRow[] {
  const rows: CronRow[] = []
  for (const [key, obj] of snapshot.entries()) {
    if (!key.startsWith('cronjob:')) continue
    const name = obj.metadata?.name ?? ''
    const namespace = obj.metadata?.namespace ?? ''
    const cluster = obj.clusterId
    const spec = (obj.spec as Record<string, unknown> | undefined) ?? {}
    const status = (obj.status as Record<string, unknown> | undefined) ?? {}
    const schedule = str(spec, 'schedule') ?? ''
    const parsed = schedule ? tryParseCron(schedule) : null
    const suspended = spec['suspend'] === true

    const runs = childRunsOfCron(
      snapshot,
      { key, name, namespace, cluster } as CronRow,
    )

    rows.push({
      key,
      name,
      namespace,
      cluster,
      schedule,
      parsed,
      description: schedule ? describeCron(parsed ?? schedule) : '—',
      suspended,
      lastScheduleTime: str(status, 'lastScheduleTime'),
      // A suspended CronJob is drawn with no marks — it is not firing.
      fireMinutes: parsed && !suspended ? fireMinutesOnDate(parsed, referenceDate) : [],
      nextFire: parsed && !suspended ? nextFireTime(parsed, referenceDate) : null,
      latestRun: runs[0] ?? null,
      runCount: runs.length,
    })
  }
  rows.sort((a, b) => {
    if (a.namespace !== b.namespace) return a.namespace.localeCompare(b.namespace)
    if (a.name !== b.name) return a.name.localeCompare(b.name)
    return (a.cluster ?? '').localeCompare(b.cluster ?? '')
  })
  return rows
}

/**
 * Minutes-of-day where ≥2 DISTINCT CronJobs fire simultaneously — the
 * "collision" set the operator wants to spot (e.g. three backups all at
 * midnight). Returns minute → count of distinct crons (only entries ≥2).
 */
export function collisionMinutes(rows: readonly CronRow[]): Map<number, number> {
  const perMinute = new Map<number, Set<string>>()
  for (const row of rows) {
    for (const minute of new Set(row.fireMinutes)) {
      let set = perMinute.get(minute)
      if (!set) {
        set = new Set<string>()
        perMinute.set(minute, set)
      }
      set.add(row.key)
    }
  }
  const out = new Map<number, number>()
  for (const [minute, set] of perMinute.entries()) {
    if (set.size >= 2) out.set(minute, set.size)
  }
  return out
}

/** Format a minute-of-day (0..1439) as `HH:MM`. */
export function formatMinuteOfDay(minuteOfDay: number): string {
  const h = Math.floor(minuteOfDay / 60)
  const m = minuteOfDay % 60
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
}

/** Compact human duration (`820ms` / `3.2s` / `4m 05s` / `1h 02m`). */
export function formatDuration(ms: number | undefined): string {
  if (ms == null || !Number.isFinite(ms) || ms < 0) return '—'
  if (ms < 1000) return `${ms}ms`
  const totalSec = Math.round(ms / 1000)
  if (totalSec < 60) return `${(ms / 1000).toFixed(1)}s`
  const min = Math.floor(totalSec / 60)
  const sec = totalSec % 60
  if (min < 60) return `${min}m ${String(sec).padStart(2, '0')}s`
  const hr = Math.floor(min / 60)
  const remMin = min % 60
  return `${hr}h ${String(remMin).padStart(2, '0')}m`
}
