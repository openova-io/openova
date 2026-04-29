/**
 * BatchProgress — strip rendered ABOVE the JobsTable (item #4 in the
 * issue #204 founder spec). One row per batch, each row shows:
 *
 *   • the batchId label
 *   • a progress bar (`finished / total` proportion)
 *   • a chip row for the four bucket counts (running / pending /
 *     succeeded / failed) so the operator can read the current state
 *     of the batch at a glance without expanding rows.
 *
 * The component is intentionally self-styled with the same CSS-variable
 * tokens the rest of the Sovereign Admin surface uses (`--color-*`,
 * `--wiz-*`) so it slots into JobsPage without a separate visual rework.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every label
 * comes from the {@link Batch} input — the component never inlines a
 * batchId, count, or status string.
 */

import type { Batch } from '@/lib/jobs.types'

interface BatchProgressProps {
  batches: readonly Batch[]
}

export function BatchProgress({ batches }: BatchProgressProps) {
  if (batches.length === 0) {
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

function BatchRow({ batch }: BatchRowProps) {
  // Avoid divide-by-zero when a batch has no jobs (degenerate, but the
  // backend may emit an empty rollup mid-rollout).
  const pct = batch.total > 0 ? Math.round((batch.finished / batch.total) * 100) : 0
  // Failed > 0 colours the bar in danger tone (the founder cares about
  // surfacing failure prominently — item #1: "current level of details
  // is very poor, we are almost blind").
  const tone = batch.failed > 0 ? 'failed' : batch.running > 0 ? 'running' : batch.finished === batch.total ? 'done' : 'pending'

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
`
