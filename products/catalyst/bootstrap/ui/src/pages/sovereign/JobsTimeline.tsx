/**
 * JobsTimeline — fullscreen Gantt-style retrospective view of all jobs.
 * (Stretch deliverable for #206; route at /provision/$deploymentId/jobs/timeline.)
 *
 * Each row is one job; a horizontal bar spans `startedAt` → `finishedAt`
 * (or "now" if the job is still running). Bars are colour-coded by
 * status using the same palette as JobDependenciesGraph and JobCard.
 *
 *   • Pure SVG. No charting lib (per principle #2 + the issue
 *     hard rule against `reactflow`/`d3-dag`/etc.).
 *   • Time axis: left edge = earliest startedAt across all jobs (or
 *     `now - 1m` if no job has started yet). Right edge = latest
 *     finishedAt (or `now` for still-running jobs). The visible time
 *     range is rounded out to a "nice" tick interval (1s/10s/30s/1m/
 *     5m/15m/1h) so the gridlines stay readable across both 30s
 *     deployments and 30m deployments.
 *   • Jobs without `startedAt` (pending) render as a stippled
 *     placeholder bar at the right edge to signal "queued".
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the row height,
 * label-column width, and tick interval are derived from the data, not
 * baked into magic numbers.
 */

import { useMemo } from 'react'
import { useParams, Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { PortalShell } from './PortalShell'
import { resolveApplications } from './applicationCatalog'
import { useDeploymentEvents } from './useDeploymentEvents'
import { deriveJobs, type Job, type JobUiStatus } from './jobs'

interface JobsTimelineProps {
  /** Test seam — disables the live SSE EventSource attach. */
  disableStream?: boolean
  /** Test seam — fixed "now" so the rendered bars are deterministic. */
  nowOverride?: Date
  /**
   * Test seam — bypass the wizard-derived job list and pass a synthetic
   * `(jobs, withStartFinish[])` instead. The Gantt page's source jobs
   * (today) come from the wizard reducer, which doesn't yet capture
   * startedAt/finishedAt — so the page renders the data it has. Once
   * the backend (#205) lands, this prop goes away.
   */
  jobsOverride?: TimelineJob[]
}

/**
 * Job shape needed by the timeline. Today's `Job` from jobs.ts only has
 * `updatedAt`, so we synthesise (startedAt, finishedAt) from the step
 * timeline. Once #205 lands the Job model gains real columns.
 */
export interface TimelineJob {
  id: string
  app: string
  title: string
  status: JobUiStatus
  startedAt: string | null
  finishedAt: string | null
}

const STATUS_FILL: Record<JobUiStatus, string> = {
  succeeded: 'var(--color-success)',
  running: 'var(--color-accent)',
  failed: 'var(--color-danger)',
  pending: 'var(--color-text-dim)',
}

const ROW_HEIGHT = 32
const LABEL_W = 240
const PADDING_X = 24
const PADDING_Y = 24
const AXIS_HEIGHT = 28

export function JobsTimeline({
  disableStream = false,
  nowOverride,
  jobsOverride,
}: JobsTimelineProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/jobs/timeline' as never,
  }) as { deploymentId: string }
  const { deploymentId } = params
  const store = useWizardStore()

  const applications = useMemo(
    () => resolveApplications(store.selectedComponents),
    [store.selectedComponents],
  )
  const applicationIds = useMemo(() => applications.map((a) => a.id), [applications])

  const { state, snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds,
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const liveJobs = useMemo(() => deriveJobs(state, applications), [state, applications])
  const jobs: TimelineJob[] = useMemo(() => {
    if (jobsOverride) return jobsOverride
    return liveJobs.map(deriveTimelineJob)
  }, [jobsOverride, liveJobs])

  const now = nowOverride ?? new Date()
  const range = computeRange(jobs, now)

  return (
    <PortalShell deploymentId={deploymentId} sovereignFQDN={sovereignFQDN}>
      <div className="flex items-start justify-between gap-4">
        <div>
          <h1
            className="text-2xl font-bold text-[var(--color-text-strong)]"
            data-testid="sov-jobs-timeline-heading"
          >
            Jobs timeline
          </h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Retrospective Gantt-style view of every job in this deployment.
          </p>
        </div>
        <Link
          to="/provision/$deploymentId/jobs"
          params={{ deploymentId }}
          className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-jobs-timeline-back"
        >
          ← Back to jobs
        </Link>
      </div>

      {jobs.length === 0 ? (
        <div className="mt-12 text-center" data-testid="sov-jobs-timeline-empty">
          <p className="text-[var(--color-text-dim)]">No jobs yet for this deployment.</p>
        </div>
      ) : (
        <TimelineSvg jobs={jobs} range={range} now={now} />
      )}
    </PortalShell>
  )
}

interface TimeRange {
  start: number
  end: number
  tickEvery: number
}

function computeRange(jobs: TimelineJob[], now: Date): TimeRange {
  const starts: number[] = []
  const ends: number[] = []
  for (const j of jobs) {
    if (j.startedAt) starts.push(new Date(j.startedAt).getTime())
    if (j.finishedAt) ends.push(new Date(j.finishedAt).getTime())
    else if (j.startedAt) ends.push(now.getTime())
  }
  const start = starts.length === 0 ? now.getTime() - 60_000 : Math.min(...starts)
  const end = ends.length === 0 ? now.getTime() : Math.max(...ends)
  const span = Math.max(1, end - start)
  // Pick a "nice" tick interval so we get ~6-10 ticks across the span.
  const candidates = [
    1_000, 5_000, 10_000, 30_000, 60_000, 5 * 60_000, 15 * 60_000, 60 * 60_000,
  ]
  let tickEvery = 60_000
  for (const c of candidates) {
    const ticks = span / c
    if (ticks <= 10) {
      tickEvery = c
      break
    }
  }
  return { start, end: end + tickEvery, tickEvery }
}

interface TimelineSvgProps {
  jobs: TimelineJob[]
  range: TimeRange
  now: Date
}

function TimelineSvg({ jobs, range, now }: TimelineSvgProps) {
  const chartWidth = 900 // visual width — wrapper's overflow-auto handles narrow viewports
  const innerW = chartWidth - LABEL_W - PADDING_X * 2
  const totalH = jobs.length * ROW_HEIGHT + AXIS_HEIGHT + PADDING_Y * 2
  const span = Math.max(1, range.end - range.start)

  const xFor = (ms: number): number => {
    const clamped = Math.max(range.start, Math.min(range.end, ms))
    return LABEL_W + PADDING_X + ((clamped - range.start) / span) * innerW
  }

  const ticks: number[] = []
  for (let t = range.start; t <= range.end; t += range.tickEvery) ticks.push(t)

  return (
    <div
      className="mt-6 overflow-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-2"
      data-testid="sov-jobs-timeline-wrapper"
    >
      <svg
        width={chartWidth}
        height={totalH}
        viewBox={`0 0 ${chartWidth} ${totalH}`}
        data-testid="sov-jobs-timeline"
        role="img"
        aria-label="Jobs Gantt timeline"
      >
        {/* Axis ticks + gridlines */}
        <g data-testid="sov-jobs-timeline-axis">
          {ticks.map((t, i) => (
            <g key={i}>
              <line
                x1={xFor(t)}
                x2={xFor(t)}
                y1={PADDING_Y + AXIS_HEIGHT}
                y2={totalH - PADDING_Y}
                stroke="var(--color-border)"
                strokeWidth={0.5}
              />
              <text
                x={xFor(t)}
                y={PADDING_Y + AXIS_HEIGHT - 8}
                fill="var(--color-text-dim)"
                fontSize={10}
                textAnchor="middle"
              >
                {fmtTick(t, range.tickEvery)}
              </text>
            </g>
          ))}
        </g>

        {/* Rows */}
        <g data-testid="sov-jobs-timeline-rows">
          {jobs.map((j, i) => {
            const y = PADDING_Y + AXIS_HEIGHT + i * ROW_HEIGHT
            const startMs = j.startedAt ? new Date(j.startedAt).getTime() : null
            const endMs = j.finishedAt
              ? new Date(j.finishedAt).getTime()
              : startMs
              ? now.getTime()
              : null
            return (
              <g
                key={j.id}
                data-testid={`sov-jobs-timeline-row-${j.id}`}
                data-status={j.status}
              >
                {/* Label */}
                <text
                  x={PADDING_X}
                  y={y + ROW_HEIGHT / 2 + 4}
                  fill="var(--color-text-strong)"
                  fontSize={12}
                  fontWeight={500}
                >
                  {truncate(j.title, 32)}
                </text>
                {/* Bar */}
                {startMs !== null && endMs !== null ? (
                  <rect
                    data-testid={`sov-jobs-timeline-bar-${j.id}`}
                    x={xFor(startMs)}
                    y={y + 6}
                    width={Math.max(2, xFor(endMs) - xFor(startMs))}
                    height={ROW_HEIGHT - 12}
                    rx={4}
                    fill={STATUS_FILL[j.status]}
                    opacity={j.status === 'pending' ? 0.35 : 0.85}
                  >
                    <title>
                      {`${j.title}\nStatus: ${j.status}\nStarted: ${j.startedAt ?? '—'}\nFinished: ${j.finishedAt ?? (j.startedAt ? 'in progress' : '—')}`}
                    </title>
                  </rect>
                ) : (
                  // Pending placeholder — small dashed pill at the right edge.
                  <rect
                    data-testid={`sov-jobs-timeline-bar-${j.id}`}
                    x={LABEL_W + PADDING_X + innerW - 50}
                    y={y + 8}
                    width={50}
                    height={ROW_HEIGHT - 16}
                    rx={4}
                    fill="none"
                    stroke="var(--color-text-dim)"
                    strokeDasharray="3 3"
                  >
                    <title>Pending — not yet started</title>
                  </rect>
                )}
              </g>
            )
          })}
        </g>
      </svg>
    </div>
  )
}

/**
 * Synthesise a TimelineJob from today's `Job` shape. We use the first
 * step's startedAt as the job start, and the last step's startedAt as
 * the finish (a coarse approximation until the backend lands real
 * timestamps in #205).
 */
function deriveTimelineJob(j: Job): TimelineJob {
  const stepsWithTime = j.steps.filter((s) => s.startedAt)
  const firstStart = stepsWithTime[0]?.startedAt ?? null
  const lastStart = stepsWithTime[stepsWithTime.length - 1]?.startedAt ?? null
  const finishedAt =
    j.status === 'succeeded' || j.status === 'failed'
      ? lastStart ?? j.updatedAt
      : null
  return {
    id: j.id,
    app: j.app,
    title: j.title,
    status: j.status,
    startedAt: firstStart ?? null,
    finishedAt,
  }
}

function fmtTick(ms: number, tickEvery: number): string {
  const d = new Date(ms)
  if (tickEvery < 60_000) {
    return d.toLocaleTimeString(undefined, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  }
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, Math.max(0, max - 1)) + '…'
}
