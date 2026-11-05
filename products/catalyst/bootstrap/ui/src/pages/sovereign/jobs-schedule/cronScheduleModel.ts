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

/**
 * The annotation the CronJob controller stamps on every child Job with the
 * schedule slot it was created for (batch/v1, Kubernetes ≥1.26). This is the
 * "scheduled" half of scheduled-vs-actual: `.status.startTime` is the
 * "actual" half, and the gap between them is the lag.
 */
export const CRONJOB_SCHEDULED_TIMESTAMP_ANNOTATION =
  'batch.kubernetes.io/cronjob-scheduled-timestamp'

/** `kubectl create job --from=cronjob` (and the console's Run now) stamp
 *  this; such a run has no schedule slot to compare against. */
export const CRONJOB_INSTANTIATE_ANNOTATION = 'cronjob.kubernetes.io/instantiate'

/** The console's own Run-now marker (catalyst-api createJobFromCronJob). */
export const MANUAL_RUN_LABEL = 'catalyst.openova.io/manual-run'

/** Labels the catalyst-api stamps on an ad-hoc (console-created) schedule. */
export const SCHEDULE_ORIGIN_LABEL = 'catalyst.openova.io/schedule-origin'
export const SCHEDULE_SOURCE_LABEL = 'catalyst.openova.io/schedule-source'
export const BLUEPRINT_LABEL = 'catalyst.openova.io/blueprint'

/** Where a CronJob came from: a Blueprint chart (changes in Git) or the
 *  console's "New schedule" (editable + deletable here). */
export type CronOrigin = 'chart' | 'console'

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
  /** ISO schedule slot the controller created this run for
   *  (`batch.kubernetes.io/cronjob-scheduled-timestamp`), when present. */
  scheduledTime?: string
  /** True for a manual (Run now / kubectl create job --from) run — no slot. */
  manual: boolean
  /** startTime − scheduledTime in ms (scheduled-vs-actual), when both known. */
  lagMs?: number
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
  /** `.metadata.uid` — identity for the edit/delete actions. */
  uid?: string
  /** `.spec.timeZone` (IANA) when set; otherwise the controller's clock. */
  timeZone?: string
  /** chart (declared by a Blueprint) or console (ad-hoc, "New schedule"). */
  origin: CronOrigin
  /** For a console schedule: the CronJob whose jobTemplate it clones. */
  sourceName?: string
  /** The Blueprint the CronJob belongs to (`catalyst.openova.io/blueprint`). */
  blueprint?: string
  /** `.status.lastSuccessfulTime` — the controller's own last-success stamp. */
  lastSuccessfulTime?: string
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
  const annotations = job.metadata?.annotations ?? {}
  const labels = job.metadata?.labels ?? {}
  const manual =
    annotations[CRONJOB_INSTANTIATE_ANNOTATION] === 'manual' ||
    labels[MANUAL_RUN_LABEL] === 'true'
  const scheduledTime = manual ? undefined : annotations[CRONJOB_SCHEDULED_TIMESTAMP_ANNOTATION]
  let lagMs: number | undefined
  if (scheduledTime && startTime) {
    const d = new Date(startTime).getTime() - new Date(scheduledTime).getTime()
    if (Number.isFinite(d)) lagMs = d
  }
  return {
    name: job.metadata?.name ?? '',
    namespace: job.metadata?.namespace ?? '',
    status: jobRunStatus(job),
    startTime,
    completionTime,
    durationMs,
    scheduledTime,
    manual,
    lagMs,
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
    const labels = obj.metadata?.labels ?? {}
    const origin: CronOrigin = labels[SCHEDULE_ORIGIN_LABEL] === 'console' ? 'console' : 'chart'

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
      uid: obj.metadata?.uid,
      timeZone: str(spec, 'timeZone'),
      origin,
      sourceName: origin === 'console' ? labels[SCHEDULE_SOURCE_LABEL] : undefined,
      blueprint: labels[BLUEPRINT_LABEL],
      lastSuccessfulTime: str(status, 'lastSuccessfulTime'),
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

/* ── Scheduled vs actual ───────────────────────────────────────────── */

/** A run is "late" when it started more than this after its schedule slot.
 *  The CronJob controller's own tolerance for a missed start is
 *  startingDeadlineSeconds (default: unlimited); one minute is the slot
 *  width, so anything beyond it was not the fire the schedule promised. */
export const LATE_RUN_THRESHOLD_MS = 60_000

export interface ScheduleAdherence {
  /** Runs that carry a schedule slot (manual runs excluded). */
  scheduledRuns: number
  /** Runs started more than LATE_RUN_THRESHOLD_MS after their slot. */
  lateRuns: number
  /** Manual (Run now / kubectl) runs — no slot to compare against. */
  manualRuns: number
  /** The largest lag seen, in ms, or null when no run carries a slot. */
  worstLagMs: number | null
  /** The run with the worst lag, when any. */
  worstRun: CronRun | null
}

/**
 * Summarise scheduled-vs-actual over a CronJob's runs: how many fired late,
 * how late the worst one was, how many were manual. Pure.
 */
export function scheduleAdherence(runs: readonly CronRun[]): ScheduleAdherence {
  let scheduledRuns = 0
  let lateRuns = 0
  let manualRuns = 0
  let worstLagMs: number | null = null
  let worstRun: CronRun | null = null
  for (const run of runs) {
    if (run.manual) {
      manualRuns++
      continue
    }
    if (run.lagMs == null) continue
    scheduledRuns++
    if (run.lagMs > LATE_RUN_THRESHOLD_MS) lateRuns++
    if (worstLagMs == null || run.lagMs > worstLagMs) {
      worstLagMs = run.lagMs
      worstRun = run
    }
  }
  return { scheduledRuns, lateRuns, manualRuns, worstLagMs, worstRun }
}

/** Signed compact lag (`+4s`, `+8h 03m`, `−2s`, `on time`). */
export function formatLag(ms: number | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return '—'
  if (Math.abs(ms) < 1000) return 'on time'
  const sign = ms < 0 ? '−' : '+'
  return `${sign}${formatDuration(Math.abs(ms))}`
}
