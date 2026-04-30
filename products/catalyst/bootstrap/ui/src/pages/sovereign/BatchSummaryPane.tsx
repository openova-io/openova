/**
 * BatchSummaryPane — right-side floating pane shown when a batch
 * bubble is single-clicked in batch mode.
 *
 * Per operator spec (2026-04-30):
 *   • Show start time
 *   • Show finish time (if all child jobs done) OR estimated finish
 *     extrapolated from completed-vs-total + running-rate
 *   • Show counts: succeeded / running / pending / failed
 *   • Show overall duration (or elapsed if still running)
 */

import { useMemo } from 'react'
import type { Job } from '@/lib/jobs.types'

export interface BatchSummaryPaneProps {
  batchId: string
  jobs: readonly Job[]
  onClose: () => void
}

export function BatchSummaryPane({ batchId, jobs, onClose }: BatchSummaryPaneProps) {
  const summary = useMemo(() => buildSummary(jobs), [jobs])

  return (
    <div
      data-testid="batch-summary-pane"
      role="dialog"
      aria-label={`Batch ${batchId}`}
      className="batch-summary-pane"
    >
      <style>{BATCH_SUMMARY_CSS}</style>
      <header className="batch-summary-pane-header">
        <div className="batch-summary-pane-title">
          <span className="batch-summary-pane-eyebrow">BATCH</span>
          <span className="batch-summary-pane-name" data-testid="batch-summary-pane-name">
            {batchId}
          </span>
        </div>
        <button
          type="button"
          className="batch-summary-pane-close"
          aria-label="Close batch summary"
          onClick={onClose}
          data-testid="batch-summary-pane-close"
        >
          ×
        </button>
      </header>
      <dl className="batch-summary-pane-grid">
        <dt>Started</dt>
        <dd data-testid="batch-summary-started">{fmtTime(summary.startedAt) || '—'}</dd>

        <dt>{summary.allDone ? 'Finished' : 'ETA'}</dt>
        <dd data-testid="batch-summary-finished">
          {summary.allDone
            ? fmtTime(summary.finishedAt) || '—'
            : summary.etaIso
              ? fmtTime(summary.etaIso)
              : 'unknown'}
        </dd>

        <dt>{summary.allDone ? 'Duration' : 'Elapsed'}</dt>
        <dd data-testid="batch-summary-duration">{fmtDuration(summary.durationMs)}</dd>

        <dt>Progress</dt>
        <dd data-testid="batch-summary-progress">
          {summary.finished} / {summary.total}
        </dd>
      </dl>
      <ul className="batch-summary-pane-counts">
        <li data-testid="batch-summary-count-succeeded">
          <span className="dot dot-succeeded" /> Succeeded {summary.succeeded}
        </li>
        <li data-testid="batch-summary-count-running">
          <span className="dot dot-running" /> Running {summary.running}
        </li>
        <li data-testid="batch-summary-count-pending">
          <span className="dot dot-pending" /> Pending {summary.pending}
        </li>
        <li data-testid="batch-summary-count-failed">
          <span className="dot dot-failed" /> Failed {summary.failed}
        </li>
      </ul>
      <p className="batch-summary-pane-hint">
        Double-click the bubble to drill into this batch's jobs.
      </p>
    </div>
  )
}

interface Summary {
  startedAt: string | null
  finishedAt: string | null
  allDone: boolean
  durationMs: number
  total: number
  finished: number
  succeeded: number
  running: number
  pending: number
  failed: number
  etaIso: string | null
}

function buildSummary(jobs: readonly Job[]): Summary {
  const total = jobs.length
  let succeeded = 0
  let running = 0
  let pending = 0
  let failed = 0
  let earliestStart: number | null = null
  let earliestStartIso: string | null = null
  let latestFinish: number | null = null
  let latestFinishIso: string | null = null
  for (const j of jobs) {
    if (j.status === 'succeeded') succeeded++
    else if (j.status === 'running') running++
    else if (j.status === 'pending') pending++
    else if (j.status === 'failed') failed++
    if (j.startedAt) {
      const t = Date.parse(j.startedAt)
      if (Number.isFinite(t) && (earliestStart === null || t < earliestStart)) {
        earliestStart = t
        earliestStartIso = j.startedAt
      }
    }
    if (j.finishedAt) {
      const t = Date.parse(j.finishedAt)
      if (Number.isFinite(t) && (latestFinish === null || t > latestFinish)) {
        latestFinish = t
        latestFinishIso = j.finishedAt
      }
    }
  }
  const finished = succeeded + failed
  const allDone = total > 0 && finished === total

  let durationMs = 0
  if (allDone && earliestStart !== null && latestFinish !== null) {
    durationMs = Math.max(0, latestFinish - earliestStart)
  } else if (earliestStart !== null) {
    durationMs = Math.max(0, Date.now() - earliestStart)
  }

  let etaIso: string | null = null
  if (!allDone && earliestStart !== null && finished > 0 && finished < total) {
    const elapsed = Date.now() - earliestStart
    const ratePerJob = elapsed / finished
    const remaining = total - finished
    const etaMs = Date.now() + ratePerJob * remaining
    etaIso = new Date(etaMs).toISOString()
  }

  return {
    startedAt: earliestStartIso,
    finishedAt: latestFinishIso,
    allDone,
    durationMs,
    total,
    finished,
    succeeded,
    running,
    pending,
    failed,
    etaIso,
  }
}

function fmtTime(iso: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}

function fmtDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '—'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  if (m < 60) return `${m}m ${rs}s`
  const h = Math.floor(m / 60)
  const rm = m % 60
  return `${h}h ${rm}m`
}

const BATCH_SUMMARY_CSS = `
.batch-summary-pane {
  position: fixed;
  top: 90px;
  right: 24px;
  width: 320px;
  z-index: 60;
  border-radius: 12px;
  border: 1px solid rgba(255,255,255,0.08);
  background: rgba(7,10,18,0.96);
  backdrop-filter: blur(8px);
  color: rgba(255,255,255,0.92);
  font-family: 'Inter', system-ui, sans-serif;
  box-shadow: 0 12px 40px rgba(0,0,0,0.5);
  overflow: hidden;
}
.batch-summary-pane-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 12px 14px 8px;
  border-bottom: 1px solid rgba(255,255,255,0.06);
}
.batch-summary-pane-title {
  display: flex; flex-direction: column; gap: 2px; min-width: 0;
}
.batch-summary-pane-eyebrow {
  font-size: 9px; font-weight: 700;
  letter-spacing: 0.14em; text-transform: uppercase;
  color: rgba(255,255,255,0.4);
}
.batch-summary-pane-name {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 12px; font-weight: 600;
  color: var(--color-accent, #38BDF8);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.batch-summary-pane-close {
  background: transparent; border: 0; cursor: pointer;
  font-size: 22px; line-height: 1;
  color: rgba(255,255,255,0.4); padding: 2px 6px;
}
.batch-summary-pane-close:hover { color: rgba(255,255,255,0.85); }

.batch-summary-pane-grid {
  display: grid;
  grid-template-columns: 90px 1fr;
  gap: 6px 12px;
  padding: 12px 14px;
  margin: 0;
}
.batch-summary-pane-grid dt {
  font-size: 10px; letter-spacing: 0.06em; text-transform: uppercase;
  color: rgba(255,255,255,0.4); align-self: center;
}
.batch-summary-pane-grid dd {
  font-family: var(--font-mono, ui-monospace, monospace);
  font-size: 12px; color: rgba(255,255,255,0.92);
  margin: 0; align-self: center;
}

.batch-summary-pane-counts {
  list-style: none; margin: 0;
  padding: 6px 14px 12px;
  display: grid; grid-template-columns: 1fr 1fr; gap: 4px 10px;
  border-top: 1px solid rgba(255,255,255,0.06);
}
.batch-summary-pane-counts li {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px; color: rgba(255,255,255,0.78);
}
.batch-summary-pane-counts .dot {
  width: 7px; height: 7px; border-radius: 50%;
}
.batch-summary-pane-counts .dot-succeeded { background: #4ADE80; }
.batch-summary-pane-counts .dot-running   { background: #38BDF8; box-shadow: 0 0 6px rgba(56,189,248,0.6); }
.batch-summary-pane-counts .dot-pending   { background: rgba(148,163,184,0.5); }
.batch-summary-pane-counts .dot-failed    { background: #F87171; }

.batch-summary-pane-hint {
  margin: 0;
  padding: 8px 14px 12px;
  border-top: 1px solid rgba(255,255,255,0.04);
  font-size: 10px; color: rgba(255,255,255,0.36);
  font-style: italic;
}
`
