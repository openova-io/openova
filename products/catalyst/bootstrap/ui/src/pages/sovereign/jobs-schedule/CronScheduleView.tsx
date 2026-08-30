/**
 * CronScheduleView — the consolidated CronJob Schedule surface (P3-frontend,
 * Refs #6703).
 *
 * Answers the founder's two questions directly:
 *   1. "what is scheduled for 12pm / where do crons collide?" → a read-only
 *      00:00→24:00 time-of-day timeline (pure SVG, no charting lib per
 *      INVIOLABLE-PRINCIPLES #2 — same house style as JobsTimeline.tsx) with
 *      one row per CronJob, a mark at every wall-clock time it fires, hour
 *      gridlines (noon emphasised), and a collision overlay where ≥2 crons
 *      fire on the same minute.
 *   2. "how do I get the complete history of a cron?" → clicking a row opens
 *      the run-history drawer: that CronJob's child Jobs (ownerRef match),
 *      newest first, each with a status badge + start + duration.
 *
 * Data path (verified — no new fetch layer): the raw batch/v1 CronJob +
 * child Job objects come from the catalyst-api k8scache SSE stream via
 * `useK8sCacheStream(deploymentId, { kinds: ['cronjob','job'] })` — the exact
 * hook + `/api/v1/sovereigns/{id}/k8s/stream` endpoint the Cloud list pages
 * use. `deriveCronRows` / `childRunsOfCron` (cronScheduleModel.ts) do the
 * pure shaping; `cronSchedule.ts` parses `.spec.schedule`.
 *
 * READ-ONLY: no create/edit ("+ New schedule" is a later phase). Nothing here
 * mutates cluster state.
 */

import { useMemo, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { sovereignPathOrDeployments } from '@/shared/lib/sovereignPaths'
import { Badge, type BadgeProps } from '@/shared/ui/badge'
import type { K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import { useK8sCacheStream } from '@/widgets/architecture-graph/useK8sCacheStream'
import { PortalShell } from '../PortalShell'
import {
  childRunsOfCron,
  collisionMinutes,
  deriveCronRows,
  formatDuration,
  formatMinuteOfDay,
  type CronRow,
  type CronRun,
  type CronRunStatus,
} from './cronScheduleModel'

/** The k8scache registry kinds this surface watches (batch/v1). Kept
 *  module-local (not exported) so this component file stays fast-refresh
 *  clean — react-refresh/only-export-components. */
const CRON_SCHEDULE_KINDS = ['cronjob', 'job'] as const

interface CronScheduleViewProps {
  /** Test seam — a fixed "now" so fire placement + next-fire are deterministic. */
  nowOverride?: Date
  /** Test seam — inject a k8scache snapshot instead of opening a live SSE. */
  snapshotOverride?: K8sSnapshot
  /** Test seam — preselect a CronJob row's history drawer by its key. */
  initialSelectedKey?: string
}

/* ── SVG geometry (derived, not magic — INVIOLABLE-PRINCIPLES #4) ─────── */
const CHART_W = 960
const LABEL_W = 220
const ROW_H = 30
const PAD_X = 20
const PAD_Y = 16
const AXIS_H = 26
const MINUTES_PER_DAY = 1440

/** Status → Badge variant + label for a run / last-run. */
const RUN_TONE: Record<CronRunStatus, { variant: BadgeProps['variant']; label: string }> = {
  succeeded: { variant: 'success', label: 'Succeeded' },
  failed: { variant: 'error', label: 'Failed' },
  running: { variant: 'info', label: 'Running' },
  unknown: { variant: 'default', label: 'Unknown' },
}

/** Sanitise a snapshot key (`cronjob:ns/name@cluster`) into a testid-safe id. */
function safeId(key: string): string {
  return key.replace(/[^a-zA-Z0-9_-]+/g, '-')
}

function RunStatusBadge({ status }: { status: CronRunStatus }) {
  const tone = RUN_TONE[status]
  return <Badge variant={tone.variant}>{tone.label}</Badge>
}

export function CronScheduleView({
  nowOverride,
  snapshotOverride,
  initialSelectedKey,
}: CronScheduleViewProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = resolvedId ?? ''

  // Live k8scache stream — same hook + endpoint as the Cloud list pages.
  // Disabled entirely when a test injects a snapshot.
  const live = useK8sCacheStream(deploymentId, {
    kinds: CRON_SCHEDULE_KINDS,
    enabled: !snapshotOverride,
  })
  const snapshot = snapshotOverride ?? live.snapshot
  const status = snapshotOverride ? 'open' : live.status

  const now = useMemo(() => nowOverride ?? new Date(), [nowOverride])
  const rows = useMemo(
    () => deriveCronRows(snapshot, now),
    // live.revision bumps on every applied SSE delta (the snapshot Map ref is
    // stable in-place, so it alone would never retrigger — mirror K8sListPage).
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [snapshot, live.revision, now],
  )
  const collisions = useMemo(() => collisionMinutes(rows), [rows])

  const [selectedKey, setSelectedKey] = useState<string | null>(
    initialSelectedKey ?? null,
  )
  const selectedRow = rows.find((r) => r.key === selectedKey) ?? null
  const selectedRuns = useMemo(
    () => (selectedRow ? childRunsOfCron(snapshot, selectedRow) : []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [selectedRow, snapshot, live.revision],
  )

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={null}
      pageTitle="CronJob schedule"
      headerSlotLeft={
        <Link
          to={sovereignPathOrDeployments('jobs', { deploymentId }) as never}
          search={{ view: 'list', kind: 'cron' } as never}
          className="text-[11px] text-[var(--color-text-dim)] hover:text-[var(--color-text)] no-underline"
          data-testid="sov-cron-schedule-back"
        >
          ← Back to jobs
        </Link>
      }
    >
      <style>{CRON_SCHEDULE_CSS}</style>
      <div data-testid="sov-cron-schedule">
        <p className="mt-4 text-sm text-[var(--color-text-dim)]">
          Every CronJob on the Sovereign, placed on a 24-hour clock by its
          schedule — read down a time to see what fires then, and where runs
          collide. Times are the CronJob&rsquo;s own wall-clock schedule.
        </p>

        {rows.length === 0 ? (
          <div
            className="mt-6 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
            data-testid="sov-cron-schedule-empty"
          >
            {status === 'connecting'
              ? 'Connecting to the live cluster stream…'
              : 'No CronJobs scheduled on this Sovereign.'}
          </div>
        ) : (
          <>
            {collisions.size > 0 ? (
              <CollisionSummary collisions={collisions} />
            ) : null}
            <ScheduleTimeline
              rows={rows}
              collisions={collisions}
              onSelect={setSelectedKey}
              selectedKey={selectedKey}
            />
            <ScheduleTable
              rows={rows}
              onSelect={setSelectedKey}
              selectedKey={selectedKey}
            />
          </>
        )}
      </div>

      {selectedRow ? (
        <RunHistoryDrawer
          row={selectedRow}
          runs={selectedRuns}
          onClose={() => setSelectedKey(null)}
        />
      ) : null}
    </PortalShell>
  )
}

/* ── Collision summary banner ─────────────────────────────────────────── */

function CollisionSummary({ collisions }: { collisions: Map<number, number> }) {
  const entries = [...collisions.entries()].sort((a, b) => a[0] - b[0])
  const preview = entries
    .slice(0, 6)
    .map(([minute, count]) => `${formatMinuteOfDay(minute)} (${count})`)
    .join(', ')
  return (
    <div
      className="mt-5 rounded-lg border border-[var(--color-warning)]/35 bg-[var(--color-warning)]/10 p-3 text-xs text-[var(--color-text)]"
      data-testid="sov-cron-collision-summary"
    >
      <span className="font-semibold text-[var(--color-warning)]">
        {entries.length} collision {entries.length === 1 ? 'time' : 'times'}
      </span>{' '}
      — {entries.length === 1 ? 'a minute where' : 'minutes where'} 2+ CronJobs
      fire together: {preview}
      {entries.length > 6 ? ', …' : ''}
    </div>
  )
}

/* ── The 24h SVG time-of-day timeline ─────────────────────────────────── */

interface ScheduleTimelineProps {
  rows: CronRow[]
  collisions: Map<number, number>
  selectedKey: string | null
  onSelect: (key: string) => void
}

function ScheduleTimeline({ rows, collisions, selectedKey, onSelect }: ScheduleTimelineProps) {
  const innerW = CHART_W - LABEL_W - PAD_X * 2
  const rowsTop = PAD_Y + AXIS_H
  const totalH = rows.length * ROW_H + AXIS_H + PAD_Y * 2

  const xForMinute = (minuteOfDay: number): number =>
    LABEL_W + PAD_X + (minuteOfDay / MINUTES_PER_DAY) * innerW

  // Hour gridlines every 2 hours (00,02,…,24).
  const hourTicks: number[] = []
  for (let h = 0; h <= 24; h += 2) hourTicks.push(h)

  return (
    <div
      className="mt-4 overflow-auto rounded-xl border border-[var(--color-border)] bg-[var(--color-surface)] p-2"
      data-testid="sov-cron-schedule-timeline-wrap"
    >
      <svg
        width={CHART_W}
        height={totalH}
        viewBox={`0 0 ${CHART_W} ${totalH}`}
        data-testid="sov-cron-schedule-timeline"
        role="img"
        aria-label="CronJob schedule — 24 hour time-of-day timeline"
      >
        {/* Hour gridlines + labels */}
        <g data-testid="sov-cron-schedule-axis">
          {hourTicks.map((h) => {
            const x = xForMinute(h * 60)
            const noon = h === 12
            return (
              <g key={h}>
                <line
                  x1={x}
                  x2={x}
                  y1={rowsTop}
                  y2={totalH - PAD_Y}
                  stroke={noon ? 'var(--color-accent)' : 'var(--color-border)'}
                  strokeWidth={noon ? 1 : 0.5}
                  strokeDasharray={noon ? '4 3' : undefined}
                  data-testid={`sov-cron-gridline-${h}`}
                />
                <text
                  x={x}
                  y={PAD_Y + AXIS_H - 9}
                  fill={noon ? 'var(--color-accent)' : 'var(--color-text-dim)'}
                  fontSize={10}
                  fontWeight={noon ? 600 : 400}
                  textAnchor="middle"
                >
                  {String(h).padStart(2, '0')}:00
                </text>
              </g>
            )
          })}
        </g>

        {/* Collision overlay — a full-height accent line at each colliding
            minute so "3 crons at midnight" is visible at a glance. */}
        <g data-testid="sov-cron-schedule-collisions">
          {[...collisions.entries()].map(([minute, count]) => (
            <line
              key={minute}
              x1={xForMinute(minute)}
              x2={xForMinute(minute)}
              y1={rowsTop}
              y2={totalH - PAD_Y}
              stroke="var(--color-warning)"
              strokeWidth={1.5}
              opacity={0.5}
              data-testid={`sov-cron-collision-${minute}`}
              data-count={count}
            >
              <title>{`${count} CronJobs fire at ${formatMinuteOfDay(minute)}`}</title>
            </line>
          ))}
        </g>

        {/* Rows */}
        <g data-testid="sov-cron-schedule-rows">
          {rows.map((row, i) => {
            const y = rowsTop + i * ROW_H
            const cy = y + ROW_H / 2
            const sid = safeId(row.key)
            const selected = row.key === selectedKey
            return (
              <g
                key={row.key}
                data-testid={`sov-cron-row-${sid}`}
                data-suspended={row.suspended ? 'true' : 'false'}
              >
                {/* Clickable row background (opens the run-history drawer). */}
                <rect
                  x={PAD_X}
                  y={y}
                  width={CHART_W - PAD_X * 2}
                  height={ROW_H}
                  fill={selected ? 'var(--color-accent)' : 'transparent'}
                  opacity={selected ? 0.08 : 0}
                  className="sov-cron-rowbg"
                  data-testid={`sov-cron-rowbg-${sid}`}
                  onClick={() => onSelect(row.key)}
                >
                  <title>{`${row.namespace}/${row.name} — ${row.description}`}</title>
                </rect>
                {/* Label */}
                <text
                  x={PAD_X + 4}
                  y={cy + 4}
                  fill="var(--color-text-strong)"
                  fontSize={12}
                  fontWeight={500}
                  className="sov-cron-rowlabel"
                  onClick={() => onSelect(row.key)}
                >
                  {truncate(row.name, 30)}
                </text>
                {/* Fire marks */}
                {row.fireMinutes.map((minute) => (
                  <circle
                    key={minute}
                    cx={xForMinute(minute)}
                    cy={cy}
                    r={2.6}
                    fill={row.suspended ? 'var(--color-text-dim)' : 'var(--color-accent)'}
                    opacity={row.suspended ? 0.4 : 0.9}
                    data-testid={`sov-cron-mark-${sid}-${minute}`}
                    data-minute={minute}
                    data-x={xForMinute(minute)}
                  >
                    <title>{`${row.name} fires at ${formatMinuteOfDay(minute)}`}</title>
                  </circle>
                ))}
                {/* Suspended rows fire nothing — show a hint pill. */}
                {row.suspended ? (
                  <text
                    x={LABEL_W + PAD_X + 6}
                    y={cy + 4}
                    fill="var(--color-text-dim)"
                    fontSize={10}
                    fontStyle="italic"
                  >
                    suspended
                  </text>
                ) : null}
              </g>
            )
          })}
        </g>
      </svg>
    </div>
  )
}

/* ── Per-cron detail table (the interactive list) ─────────────────────── */

interface ScheduleTableProps {
  rows: CronRow[]
  selectedKey: string | null
  onSelect: (key: string) => void
}

function ScheduleTable({ rows, selectedKey, onSelect }: ScheduleTableProps) {
  return (
    <div className="mt-4 overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)]">
      <table
        className="w-full border-collapse text-sm"
        data-testid="sov-cron-schedule-table"
      >
        <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
          <tr className="border-b border-[var(--color-border)]">
            <th className="px-3 py-2 text-left font-medium">Namespace</th>
            <th className="px-3 py-2 text-left font-medium">CronJob</th>
            <th className="px-3 py-2 text-left font-medium">Schedule</th>
            <th className="px-3 py-2 text-left font-medium">Next fire</th>
            <th className="px-3 py-2 text-left font-medium">Last run</th>
            <th className="px-3 py-2 text-left font-medium">Runs</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const sid = safeId(row.key)
            const selected = row.key === selectedKey
            return (
              <tr
                key={row.key}
                data-testid={`sov-cron-table-row-${sid}`}
                className={
                  'cursor-pointer border-b border-[var(--color-border)] last:border-0 hover:bg-[var(--color-bg-3)] ' +
                  (selected ? 'bg-[var(--color-bg-3)]' : '')
                }
                onClick={() => onSelect(row.key)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    onSelect(row.key)
                  }
                }}
                role="button"
                tabIndex={0}
                aria-label={`Run history for ${row.name}`}
              >
                <td className="px-3 py-2 text-[var(--color-text-dim)]">{row.namespace || '—'}</td>
                <td className="px-3 py-2 font-medium text-[var(--color-text-strong)]">
                  {row.name}
                  {row.suspended ? (
                    <span className="ml-2 rounded bg-[var(--color-bg-3)] px-1.5 py-0.5 text-[10px] uppercase text-[var(--color-text-dim)]">
                      suspended
                    </span>
                  ) : null}
                </td>
                <td className="px-3 py-2">
                  <span className="font-mono text-xs text-[var(--color-text-dim)]">{row.schedule || '—'}</span>
                  <span className="ml-2 text-[var(--color-text)]">{row.description}</span>
                </td>
                <td className="px-3 py-2 text-[var(--color-text)]">
                  {row.nextFire ? formatDateTime(row.nextFire) : '—'}
                </td>
                <td className="px-3 py-2">
                  {row.latestRun ? (
                    <span className="inline-flex items-center gap-2">
                      <RunStatusBadge status={row.latestRun.status} />
                      <span className="text-xs text-[var(--color-text-dim)]">
                        {row.latestRun.startTime ? formatDateTime(new Date(row.latestRun.startTime)) : ''}
                      </span>
                    </span>
                  ) : row.lastScheduleTime ? (
                    <span className="text-xs text-[var(--color-text-dim)]">
                      last scheduled {formatDateTime(new Date(row.lastScheduleTime))}
                    </span>
                  ) : (
                    <span className="text-[var(--color-text-dim)]">—</span>
                  )}
                </td>
                <td className="px-3 py-2 text-[var(--color-text-dim)] tabular-nums">
                  {row.runCount}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

/* ── Run-history drawer ───────────────────────────────────────────────── */

interface RunHistoryDrawerProps {
  row: CronRow
  runs: CronRun[]
  onClose: () => void
}

function RunHistoryDrawer({ row, runs, onClose }: RunHistoryDrawerProps) {
  return (
    <div className="sov-cron-drawer-scrim" data-testid="sov-cron-history-scrim" onClick={onClose}>
      <aside
        className="sov-cron-drawer"
        data-testid="sov-cron-history-drawer"
        role="dialog"
        aria-label={`Run history for ${row.name}`}
        onClick={(e) => e.stopPropagation()}
      >
        <div className="sov-cron-drawer-head">
          <div>
            <div className="text-sm font-semibold text-[var(--color-text-strong)]">{row.name}</div>
            <div className="text-xs text-[var(--color-text-dim)]">
              {row.namespace} · <span className="font-mono">{row.schedule}</span> · {row.description}
            </div>
          </div>
          <button
            type="button"
            className="sov-cron-drawer-close"
            data-testid="sov-cron-history-close"
            aria-label="Close run history"
            onClick={onClose}
          >
            ✕
          </button>
        </div>

        <div className="sov-cron-drawer-body">
          {runs.length === 0 ? (
            <div
              className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 text-sm text-[var(--color-text-dim)]"
              data-testid="sov-cron-history-empty"
            >
              No runs recorded yet for this CronJob.
              {row.lastScheduleTime
                ? ` Last scheduled ${formatDateTime(new Date(row.lastScheduleTime))}.`
                : ''}
            </div>
          ) : (
            <table className="w-full border-collapse text-sm" data-testid="sov-cron-history-table">
              <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
                <tr className="border-b border-[var(--color-border)]">
                  <th className="px-2 py-2 text-left font-medium">Run</th>
                  <th className="px-2 py-2 text-left font-medium">Status</th>
                  <th className="px-2 py-2 text-left font-medium">Started</th>
                  <th className="px-2 py-2 text-left font-medium">Duration</th>
                </tr>
              </thead>
              <tbody>
                {runs.map((run) => (
                  <tr
                    key={run.name}
                    data-testid={`sov-cron-run-${safeId(run.name)}`}
                    className="border-b border-[var(--color-border)] last:border-0"
                  >
                    <td className="px-2 py-2 font-mono text-xs text-[var(--color-text)]">{run.name}</td>
                    <td className="px-2 py-2">
                      <RunStatusBadge status={run.status} />
                    </td>
                    <td className="px-2 py-2 text-xs text-[var(--color-text-dim)]">
                      {run.startTime ? formatDateTime(new Date(run.startTime)) : '—'}
                    </td>
                    <td className="px-2 py-2 text-xs text-[var(--color-text-dim)] tabular-nums">
                      {run.status === 'running' && run.startTime && !run.completionTime
                        ? 'in progress'
                        : formatDuration(run.durationMs)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </aside>
    </div>
  )
}

/* ── Small formatters ─────────────────────────────────────────────────── */

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, Math.max(0, max - 1)) + '…'
}

/** `MMM D, HH:MM` in the viewer's locale — compact, unambiguous. */
function formatDateTime(d: Date): string {
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

const CRON_SCHEDULE_CSS = `
.sov-cron-rowbg { cursor: pointer; }
.sov-cron-rowlabel { cursor: pointer; }
.sov-cron-schedule-rows g:hover .sov-cron-rowbg { opacity: 0.06; fill: var(--color-accent); }

.sov-cron-drawer-scrim {
  position: fixed;
  inset: 0;
  z-index: 3000;
  background: rgba(0, 0, 0, 0.35);
  display: flex;
  justify-content: flex-end;
}
.sov-cron-drawer {
  width: min(560px, 92vw);
  height: 100%;
  background: var(--color-surface);
  border-left: 1px solid var(--color-border);
  box-shadow: -12px 0 32px rgba(0, 0, 0, 0.35);
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.sov-cron-drawer-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 1rem 1.1rem;
  border-bottom: 1px solid var(--color-border);
}
.sov-cron-drawer-close {
  border: 1px solid var(--color-border);
  background: var(--color-bg-2);
  color: var(--color-text-dim);
  border-radius: 6px;
  width: 28px;
  height: 28px;
  cursor: pointer;
  line-height: 1;
  flex-shrink: 0;
}
.sov-cron-drawer-close:hover { color: var(--color-text); }
.sov-cron-drawer-body {
  padding: 0.9rem 1.1rem;
  overflow-y: auto;
}
`
