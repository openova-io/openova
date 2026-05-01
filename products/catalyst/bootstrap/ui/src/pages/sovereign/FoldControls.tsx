/**
 * FoldControls — toolbar replacing the legacy jobs/batches mode toggle.
 *
 * Three controls:
 *
 *   • Collapse all  — folds every group Job (`Job.type === 'group'`).
 *   • Expand all    — unfolds every group Job.
 *   • Depth select  — sets a global "fold groups deeper than N" rule;
 *                     1 = top-level groups folded, 2 = top-level
 *                     unfolded + their children visible, "all" = no
 *                     depth-based fold.
 *
 * Per-node folds (a chevron click on a single group bubble) are
 * overlaid on the depth result by the parent component. The toolbar
 * only emits the global decision; the consumer composes.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the depth
 * options live in {@link DEPTH_OPTIONS}; consumers binding to
 * `?depth=` in the URL MUST go through this list.
 */

import type { Job } from '@/lib/jobs.types'

export type FoldDepth = 1 | 2 | 3 | 'all'

/** Depth options surfaced by the toolbar — single source of truth. */
export const DEPTH_OPTIONS: readonly { value: FoldDepth; label: string }[] = [
  { value: 1, label: '1' },
  { value: 2, label: '2' },
  { value: 3, label: '3' },
  { value: 'all', label: 'All' },
]

/** Parse a free-form `?depth=` query value into a typed FoldDepth. */
export function resolveDepth(raw: unknown): FoldDepth {
  if (raw === 1 || raw === '1') return 1
  if (raw === 3 || raw === '3') return 3
  if (raw === 'all' || raw === '*' || raw === 'inf' || raw === 'infinity') return 'all'
  return 2 // default — top-level groups + their direct children visible
}

interface FoldControlsProps {
  /** Current depth selection (drives the radio highlight). */
  depth: FoldDepth
  /** Called when the operator picks a depth from the toolbar. */
  onDepthChange: (next: FoldDepth) => void
  /** Called when the operator hits Collapse all. */
  onCollapseAll: () => void
  /** Called when the operator hits Expand all. */
  onExpandAll: () => void
  /** Hide the controls when there are no group nodes to fold. */
  hasGroups: boolean
}

export function FoldControls({
  depth,
  onDepthChange,
  onCollapseAll,
  onExpandAll,
  hasGroups,
}: FoldControlsProps) {
  if (!hasGroups) return null
  return (
    <div
      className="fold-controls"
      data-testid="fold-controls"
      role="toolbar"
      aria-label="Job tree fold controls"
    >
      <style>{FOLD_CONTROLS_CSS}</style>
      <button
        type="button"
        className="fold-btn"
        data-testid="fold-collapse-all"
        onClick={onCollapseAll}
        title="Collapse every group"
      >
        Collapse all
      </button>
      <button
        type="button"
        className="fold-btn"
        data-testid="fold-expand-all"
        onClick={onExpandAll}
        title="Unfold every group"
      >
        Expand all
      </button>
      <div className="fold-divider" aria-hidden />
      <span className="fold-caption">Depth</span>
      <div
        className="fold-depth"
        role="radiogroup"
        aria-label="Default fold depth"
        data-testid="fold-depth"
      >
        {DEPTH_OPTIONS.map((opt) => {
          const active = depth === opt.value
          return (
            <button
              key={String(opt.value)}
              type="button"
              role="radio"
              aria-checked={active}
              className={`fold-depth-btn${active ? ' active' : ''}`}
              data-testid={`fold-depth-${opt.value}`}
              onClick={() => {
                if (!active) onDepthChange(opt.value)
              }}
            >
              {opt.label}
            </button>
          )
        })}
      </div>
    </div>
  )
}

/** Convenience selector — returns true if any visible group is in the
 *  layout. The toolbar uses this to suppress itself on leaf-only
 *  views. */
export function hasGroupJobs(jobs: readonly Job[]): boolean {
  for (const j of jobs) {
    if (j.type === 'group') return true
  }
  return false
}

const FOLD_CONTROLS_CSS = `
.fold-controls {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.3rem 0.55rem;
  border-radius: 999px;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  font-size: 0.72rem;
}
.fold-btn {
  appearance: none;
  background: transparent;
  border: 0;
  padding: 0.22rem 0.6rem;
  border-radius: 6px;
  color: var(--color-text-dim);
  font-size: 0.72rem;
  font-weight: 600;
  letter-spacing: 0.02em;
  cursor: pointer;
  transition: color 0.12s ease, background-color 0.12s ease;
}
.fold-btn:hover {
  color: var(--color-text-strong);
  background: rgba(148, 163, 184, 0.10);
}
.fold-divider {
  width: 1px;
  height: 14px;
  background: var(--color-border);
}
.fold-caption {
  color: var(--color-text-dim);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  font-size: 0.62rem;
}
.fold-depth {
  display: inline-flex;
  align-items: center;
  background: rgba(148, 163, 184, 0.05);
  border-radius: 6px;
  padding: 1px;
}
.fold-depth-btn {
  appearance: none;
  background: transparent;
  border: 0;
  padding: 0.18rem 0.45rem;
  border-radius: 5px;
  color: var(--color-text-dim);
  font-size: 0.72rem;
  font-weight: 600;
  cursor: pointer;
  min-width: 24px;
  font-variant-numeric: tabular-nums;
  transition: background-color 0.12s ease, color 0.12s ease;
}
.fold-depth-btn:hover { color: var(--color-text); }
.fold-depth-btn.active {
  background: var(--color-accent);
  color: var(--color-bg);
}
`
