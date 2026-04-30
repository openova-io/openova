/**
 * BatchProgress — per-batch progress visualization. Two render modes:
 *
 *   1) Strip mode  — `<BatchProgress batches={Batch[]} />`
 *      Renders one compact row per batch. Originally mounted above the
 *      JobsTable for the at-a-glance batch rollups.
 *
 *   2) Single-batch detail mode — `<BatchProgress singleBatch={Batch} />`
 *      Renders ONE large card with a prominent progress bar + the four
 *      bucket counts. Used by the BatchDetail page (epic #204 item 4):
 *      the founder asked that the progress bar appear only on a batch
 *      detail page, not on the global JobsPage.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label
 * comes from the {@link Batch} input — the component never inlines a
 * batchId, count, or status string.
 */

import type { Batch } from '@/lib/jobs.types'

interface BatchProgressProps {
  /** Strip mode: zero-or-more batch rollup rows. */
  batches?: readonly Batch[]
  /** Single-batch detail mode: one large card with prominent progress bar. */
  singleBatch?: Batch
}

export function BatchProgress({ batches, singleBatch }: BatchProgressProps) {
  if (singleBatch) {
    return (
      <div className="batch-progress batch-progress-single" data-testid="batch-progress-single">
        <style>{BATCH_PROGRESS_CSS}</style>
        <BatchCard batch={singleBatch} />
      </div>
    )
  }
  if (!batches || batches.length === 0) {
    return null
  }
  return (
    <div className="batch-progress" data-testid="batch-progress">
      <style>{BATCH_PROGRESS_CSS}</style>
      {batches.map((b) => (
        <BatchRow key={b.batchId} batch={b} />
      ))}
    </div>
  )
}

interface BatchRowProps {
  batch: Batch
}

/**
 * Compute the ('done' | 'running' | 'failed' | 'pending') tone for a
 * batch — extracted so both render modes use the same logic.
 */
function batchTone(batch: Batch): 'done' | 'running' | 'failed' | 'pending' {
  if (batch.failed > 0) return 'failed'
  if (batch.running > 0) return 'running'
  if (batch.total > 0 && batch.finished === batch.total) return 'done'
  return 'pending'
}

function batchPct(batch: Batch): number {
  return batch.total > 0 ? Math.round((batch.finished / batch.total) * 100) : 0
}

function BatchRow({ batch }: BatchRowProps) {
  const pct = batchPct(batch)
  const tone = batchTone(batch)

  return (
    <div className="batch-row" data-testid={`batch-row-${batch.batchId}`} data-tone={tone}>
      <div className="batch-meta">
        <span className="batch-label" data-testid={`batch-label-${batch.batchId}`}>
          {batch.batchId}
        </span>
        <span className="batch-count" data-testid={`batch-count-${batch.batchId}`}>
          {batch.finished}/{batch.total}
        </span>
      </div>
      <div className="batch-bar" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
        <div className={`batch-bar-fill batch-bar-fill-${tone}`} style={{ width: `${pct}%` }} />
      </div>
      <div className="batch-chips">
        {batch.running > 0 ? (
          <span className="chip chip-running" data-testid={`batch-chip-running-${batch.batchId}`}>
            <span className="chip-dot" /> {batch.running} running
          </span>
        ) : null}
        {batch.pending > 0 ? (
          <span className="chip chip-pending" data-testid={`batch-chip-pending-${batch.batchId}`}>
            {batch.pending} pending
          </span>
        ) : null}
        {batch.failed > 0 ? (
          <span className="chip chip-failed" data-testid={`batch-chip-failed-${batch.batchId}`}>
            {batch.failed} failed
          </span>
        ) : null}
        {batch.finished - batch.failed > 0 ? (
          <span className="chip chip-succeeded" data-testid={`batch-chip-succeeded-${batch.batchId}`}>
            {batch.finished - batch.failed} succeeded
          </span>
        ) : null}
      </div>
    </div>
  )
}

interface BatchCardProps {
  batch: Batch
}

/**
 * Single-batch detail card — full-width, vertical layout with the
 * progress bar dominating the visual. Used on the BatchDetail page
 * (founder spec, epic #204 item 4).
 */
function BatchCard({ batch }: BatchCardProps) {
  const pct = batchPct(batch)
  const tone = batchTone(batch)
  const succeeded = batch.finished - batch.failed

  return (
    <div className="batch-card" data-testid={`batch-card-${batch.batchId}`} data-tone={tone}>
      <div className="batch-card-header">
        <div>
          <div className="batch-card-label" data-testid={`batch-card-label-${batch.batchId}`}>
            {batch.batchId}
          </div>
          <div className="batch-card-sub" data-testid={`batch-card-sub-${batch.batchId}`}>
            {batch.finished} of {batch.total} jobs finished
          </div>
        </div>
        <div className="batch-card-pct" data-testid={`batch-card-pct-${batch.batchId}`}>
          {pct}%
        </div>
      </div>
      <div
        className="batch-bar batch-bar-large"
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        data-testid={`batch-card-bar-${batch.batchId}`}
      >
        <div className={`batch-bar-fill batch-bar-fill-${tone}`} style={{ width: `${pct}%` }} />
      </div>
      <div className="batch-card-stats">
        <Stat label="Running" value={batch.running} kind="running" testid={`batch-card-stat-running-${batch.batchId}`} />
        <Stat label="Pending" value={batch.pending} kind="pending" testid={`batch-card-stat-pending-${batch.batchId}`} />
        <Stat label="Succeeded" value={succeeded} kind="succeeded" testid={`batch-card-stat-succeeded-${batch.batchId}`} />
        <Stat label="Failed" value={batch.failed} kind="failed" testid={`batch-card-stat-failed-${batch.batchId}`} />
        <Stat label="Total" value={batch.total} kind="total" testid={`batch-card-stat-total-${batch.batchId}`} />
      </div>
    </div>
  )
}

interface StatProps {
  label: string
  value: number
  kind: 'running' | 'pending' | 'succeeded' | 'failed' | 'total'
  testid: string
}

function Stat({ label, value, kind, testid }: StatProps) {
  return (
    <div className={`batch-stat batch-stat-${kind}`} data-testid={testid}>
      <div className="batch-stat-value">{value}</div>
      <div className="batch-stat-label">{label}</div>
    </div>
  )
}

const BATCH_PROGRESS_CSS = `
.batch-progress {
  display: flex;
  flex-direction: column;
  gap: 0.6rem;
  margin-bottom: 1rem;
}
.batch-row {
  display: grid;
  grid-template-columns: minmax(140px, 0.8fr) 2.5fr minmax(220px, 1fr);
  align-items: center;
  gap: 0.9rem;
  padding: 0.6rem 0.9rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 10px;
}
.batch-meta {
  display: flex;
  flex-direction: column;
  gap: 0.15rem;
  min-width: 0;
}
.batch-label {
  font-size: 0.78rem;
  font-weight: 600;
  color: var(--color-text-strong);
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
}
.batch-count {
  font-size: 0.7rem;
  color: var(--color-text-dim);
}
.batch-bar {
  position: relative;
  height: 8px;
  border-radius: 999px;
  background: var(--color-border);
  overflow: hidden;
}
.batch-bar-large {
  height: 14px;
}
.batch-bar-fill {
  height: 100%;
  border-radius: 999px;
  transition: width 0.3s ease-out;
}
.batch-bar-fill-done    { background: #4ADE80; }
.batch-bar-fill-running { background: #38BDF8; }
.batch-bar-fill-failed  { background: #F87171; }
.batch-bar-fill-pending { background: #94A3B8; }
.batch-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 0.35rem;
  justify-content: flex-end;
}
.chip {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding: 0.12rem 0.5rem;
  border-radius: 999px;
  font-size: 0.66rem;
  font-weight: 600;
  letter-spacing: 0.04em;
  text-transform: uppercase;
  white-space: nowrap;
}
.chip-running   { background: rgba(56,189,248,0.10);  color: #38BDF8; border: 1px solid rgba(56,189,248,0.35); }
.chip-pending   { background: rgba(148,163,184,0.10); color: var(--color-text-dim); border: 1px solid rgba(148,163,184,0.30); }
.chip-failed    { background: rgba(248,113,113,0.10); color: #F87171; border: 1px solid rgba(248,113,113,0.35); }
.chip-succeeded { background: rgba(74,222,128,0.10);  color: #4ADE80; border: 1px solid rgba(74,222,128,0.35); }
.chip-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: currentColor;
  animation: sov-pulse 1.6s ease-in-out infinite;
}

/* Single-batch detail card — full width, prominent progress bar. */
.batch-progress-single {
  margin-bottom: 1.2rem;
}
.batch-card {
  display: flex;
  flex-direction: column;
  gap: 0.9rem;
  padding: 1.1rem 1.2rem;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 12px;
}
.batch-card-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 1rem;
}
.batch-card-label {
  font-size: 1rem;
  font-weight: 700;
  color: var(--color-text-strong);
  font-family: var(--font-mono, ui-monospace, monospace);
  letter-spacing: 0.02em;
}
.batch-card-sub {
  margin-top: 0.2rem;
  font-size: 0.82rem;
  color: var(--color-text-dim);
}
.batch-card-pct {
  font-size: 1.5rem;
  font-weight: 700;
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
}
.batch-card-stats {
  display: grid;
  grid-template-columns: repeat(5, minmax(0, 1fr));
  gap: 0.6rem;
}
.batch-stat {
  padding: 0.55rem 0.7rem;
  background: color-mix(in srgb, var(--color-border) 30%, transparent);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  text-align: center;
}
.batch-stat-value {
  font-size: 1.2rem;
  font-weight: 700;
  color: var(--color-text-strong);
  font-variant-numeric: tabular-nums;
}
.batch-stat-label {
  margin-top: 0.15rem;
  font-size: 0.66rem;
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.batch-stat-running .batch-stat-value   { color: #38BDF8; }
.batch-stat-pending .batch-stat-value   { color: var(--color-text-dim); }
.batch-stat-succeeded .batch-stat-value { color: #4ADE80; }
.batch-stat-failed .batch-stat-value    { color: #F87171; }
@media (max-width: 720px) {
  .batch-card-stats { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
`
