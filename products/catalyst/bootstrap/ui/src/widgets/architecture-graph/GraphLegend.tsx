/**
 * GraphLegend — the three-channel legend overlay for the unified
 * Cloud-graph (#3958). Decodes the canvas's three INDEPENDENT visual
 * channels:
 *   • SHAPE  = coarse category (6 polygons)
 *   • BORDER = family (operator / API group)
 *   • FILL   = status (5 buckets)
 *
 * Collapsible (starts collapsed) so it never blocks the force layout.
 * Pure presentational — no data, no side effects beyond its own
 * open/closed state.
 */

import { useState } from 'react'
import {
  ALL_CATEGORIES,
  ALL_FAMILIES,
  CATEGORY_LABEL,
  FAMILY_BORDER,
  FAMILY_LABEL,
  STATUS_FILL,
  type ArchStatus,
} from './types'
import { shapeForCategory } from './shapes'

const STATUS_LEGEND: { status: ArchStatus; label: string }[] = [
  { status: 'healthy', label: 'Reconciled / Healthy' },
  { status: 'reconciling', label: 'Reconciling / Progressing' },
  { status: 'drifted', label: 'Drifted / Warning' },
  { status: 'failed', label: 'Degraded / Failed' },
  { status: 'unknown', label: 'Suspended / Unknown' },
]

/** A 22×22 swatch that draws a category shape (neutral fill + border). */
function ShapeSwatch({ category }: { category: (typeof ALL_CATEGORIES)[number] }) {
  const r = 8
  const geom = shapeForCategory(category, r)
  return (
    <svg width={20} height={20} viewBox="-11 -11 22 22" aria-hidden>
      {geom.el === 'circle' ? (
        <circle r={geom.r} fill="var(--color-text-dim)" stroke="var(--color-text)" strokeWidth={1.4} />
      ) : (
        <polygon
          points={geom.points}
          fill="var(--color-text-dim)"
          stroke="var(--color-text)"
          strokeWidth={1.4}
          strokeLinejoin="round"
        />
      )}
    </svg>
  )
}

export function GraphLegend({ testIdPrefix = 'arch-graph' }: { testIdPrefix?: string }) {
  const [open, setOpen] = useState(false)

  return (
    <div
      data-testid={`${testIdPrefix}-legend`}
      className="absolute bottom-2 right-2 z-10 max-w-[18rem] text-[11px]"
    >
      <button
        type="button"
        data-testid={`${testIdPrefix}-legend-toggle`}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="ml-auto flex items-center gap-1 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)]/90 px-2 py-1 text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
      >
        <span aria-hidden>{open ? '▾' : '▸'}</span>
        <span>Legend</span>
      </button>
      {open && (
        <div
          data-testid={`${testIdPrefix}-legend-panel`}
          className="mt-1 flex max-h-[60vh] flex-col gap-3 overflow-y-auto rounded-md border border-[var(--color-border)] bg-[var(--color-bg)]/95 p-3 shadow-xl"
        >
          {/* SHAPE = category */}
          <section>
            <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-dim)]">
              Shape · category
            </h4>
            <ul className="grid grid-cols-1 gap-0.5">
              {ALL_CATEGORIES.map((c) => (
                <li
                  key={c}
                  data-testid={`${testIdPrefix}-legend-category-${c}`}
                  className="flex items-center gap-2 text-[var(--color-text)]"
                >
                  <ShapeSwatch category={c} />
                  <span>{CATEGORY_LABEL[c]}</span>
                </li>
              ))}
            </ul>
          </section>

          {/* BORDER = family */}
          <section>
            <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-dim)]">
              Border · family
            </h4>
            <ul className="grid grid-cols-2 gap-x-2 gap-y-0.5">
              {ALL_FAMILIES.map((f) => (
                <li
                  key={f}
                  data-testid={`${testIdPrefix}-legend-family-${f}`}
                  className="flex items-center gap-1.5 text-[var(--color-text)]"
                >
                  <span
                    aria-hidden
                    className="inline-block h-2.5 w-2.5 rounded-full border-2"
                    style={{ borderColor: FAMILY_BORDER[f], background: 'transparent' }}
                  />
                  <span>{FAMILY_LABEL[f]}</span>
                </li>
              ))}
            </ul>
          </section>

          {/* FILL = status */}
          <section>
            <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-dim)]">
              Fill · status
            </h4>
            <ul className="grid grid-cols-1 gap-0.5">
              {STATUS_LEGEND.map(({ status, label }) => (
                <li
                  key={status}
                  data-testid={`${testIdPrefix}-legend-status-${status}`}
                  className="flex items-center gap-2 text-[var(--color-text)]"
                >
                  <span
                    aria-hidden
                    className="inline-block h-3 w-3 rounded-full"
                    style={{ background: STATUS_FILL[status] }}
                  />
                  <span>{label}</span>
                </li>
              ))}
            </ul>
          </section>
        </div>
      )}
    </div>
  )
}
