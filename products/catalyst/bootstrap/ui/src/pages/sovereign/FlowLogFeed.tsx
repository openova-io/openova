/**
 * FlowLogFeed — right-side persistent log stream for the Flow canvas.
 *
 * Mirrors the right panel in `marketing/mockups/provision-mockup-v4.png`
 * + the static mockup at `marketing/mockups/provision-mockup.html`:
 *
 *   • A fixed-width column (default 280px) that lives outside the SVG.
 *   • Header: "LIVE LOG" label + active-job chip + status hint.
 *   • Body: monospace stream of recent steps, colour-coded
 *     (green = done, cyan = active, grey = waiting, red = failed).
 *
 * Composition contract:
 *   • The active job is supplied by the parent (FlowPage) — usually
 *     `selectedJob ?? mostRecentRunningJob ?? mostRecentJob`.
 *   • Steps are derived from the FlowPage's data adapter — this
 *     component is purely presentational.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (no compromise) — single
 * presentation file; no pagination, no collapsible header. The stream
 * scrolls.
 */

import type { Job, JobStatus } from '@/lib/jobs.types'

export interface FlowLogStreamLine {
  /** Stable line id — opaque, used as React key. */
  id: string
  /** Optional ISO timestamp — shown left-of-message in HH:MM:SS form. */
  timestamp: string | null
  /** The line's status — drives colour. */
  status: JobStatus
  /** The line's plain-text content. */
  message: string
}

export interface FlowLogFeedProps {
  /** The job whose stream is currently displayed; null = none selected. */
  job: Job | null
  /** The lines to render — newest at the bottom (the panel auto-scrolls). */
  lines: readonly FlowLogStreamLine[]
  /** True when the active job is still streaming (cursor blink etc.). */
  live: boolean
  /** True when the panel is collapsed (rendered as a 0-width strip). */
  collapsed?: boolean
}

const STATUS_COLOR: Record<JobStatus, string> = {
  succeeded: '#86EFAC',
  running: '#BAE6FD',
  failed: '#FCA5A5',
  pending: 'rgba(255,255,255,0.40)',
}

const STATUS_LABEL: Record<JobStatus, string> = {
  succeeded: 'done',
  running: 'running',
  failed: 'failed',
  pending: 'waiting',
}

export function FlowLogFeed({ job, lines, live, collapsed }: FlowLogFeedProps) {
  if (collapsed) {
    return (
      <aside
        className="flow-log-feed-collapsed"
        data-testid="flow-log-feed"
        data-collapsed="true"
        aria-label="Live log (collapsed)"
        style={{ width: 0, overflow: 'hidden' }}
      />
    )
  }
  return (
    <aside
      className="flow-log-feed"
      data-testid="flow-log-feed"
      data-collapsed="false"
      aria-label="Live log"
    >
      <div className="flow-log-feed-header">
        <span className="flow-log-feed-label">Live Log</span>
        <span
          className="flow-log-feed-chip"
          data-testid="flow-log-feed-chip"
        >
          {job?.jobName ?? '—'}
        </span>
        <span className="flow-log-feed-status" data-testid="flow-log-feed-status">
          {job ? STATUS_LABEL[job.status] : '—'}
        </span>
      </div>
      <div
        className="flow-log-feed-stream"
        data-testid="flow-log-feed-stream"
      >
        {lines.length === 0 ? (
          <div className="flow-log-feed-empty">
            {job
              ? `No log lines yet for ${job.jobName}.`
              : 'Click any job in the canvas to stream its log lines here.'}
          </div>
        ) : (
          lines.map((line, idx) => (
            <div
              key={line.id}
              className="flow-log-feed-line"
              data-testid="flow-log-feed-line"
            >
              <span className="flow-log-feed-ts">{formatTime(line.timestamp)}</span>
              <span
                className="flow-log-feed-msg"
                style={{ color: STATUS_COLOR[line.status] }}
              >
                {line.message}
                {live && idx === lines.length - 1 ? (
                  <span className="flow-log-feed-cursor" />
                ) : null}
              </span>
            </div>
          ))
        )}
      </div>
    </aside>
  )
}

function formatTime(ts: string | null): string {
  if (!ts) return '--:--:--'
  const t = new Date(ts).getTime()
  if (!Number.isFinite(t) || t <= 0) return '--:--:--'
  const d = new Date(t)
  const pad = (n: number) => n.toString().padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

/* The companion CSS lives in FlowPage.tsx FLOW_PAGE_CSS_V4 since the
 * Flow surface ships its CSS inline (per the PR #245 pattern — keeps
 * the canvas + log + tree in lockstep). */
