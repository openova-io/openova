/**
 * GraphLegend — the legend overlay for the unified Cloud-graph (#3958).
 * Decodes the canvas's three INDEPENDENT visual channels PLUS the
 * ArchiMate relation set (#3980 fix 3 — the standalone "ArchiMate
 * connections (N)" bottom button is gone; its relations list now lives
 * INSIDE this collapsible Legend panel as a fourth section):
 *   • SHAPE  = coarse category (6 polygons)
 *   • BORDER = family (operator / API group)
 *   • FILL   = status (5 buckets)
 *   • RELATIONS = ArchiMate edge types (markers + per-type live count)
 *
 * Collapsible (starts collapsed) so it never blocks the force layout.
 * Pure presentational — no data side effects beyond its own open/closed
 * state. The relation counts are supplied by the embedder (the canvas's
 * live edge set) via `edgeTypeCounts`.
 */

import { useState } from 'react'
import {
  ALL_CATEGORIES,
  ALL_EDGE_TYPES,
  ALL_FAMILIES,
  CATEGORY_LABEL,
  EDGE_DASHED,
  EDGE_MARKER_END,
  EDGE_MARKER_START,
  EDGE_STROKE,
  FAMILY_BORDER,
  FAMILY_LABEL,
  STATUS_FILL,
  type ArchEdgeType,
  type ArchStatus,
  type EdgeMarker,
} from './types'
import { markerId } from './markers'
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

/* ── ArchiMate relation thumbnail (merged from the retired bottom
 *    EdgeLegendPopover, #3980 fix 3) ───────────────────────────────── */

/** Standalone marker bodies for the legend SVGs. Distinct id suffix
 *  (`-legend`) so a per-line marker reference never collides with the
 *  canvas's main <defs>. */
function LegendMarker({ kind, stroke }: { kind: NonNullable<EdgeMarker>; stroke: string }) {
  const id = `${markerId(kind, stroke)}-legend`
  const common = {
    markerUnits: 'strokeWidth' as const,
    orient: 'auto' as const,
  }
  switch (kind) {
    case 'composition':
      return (
        <marker id={id} {...common} markerWidth={14} markerHeight={10} refX={11} refY={5} viewBox="0 0 14 10">
          <polygon points="0,5 7,1 14,5 7,9" fill={stroke} stroke={stroke} strokeWidth={1} />
        </marker>
      )
    case 'aggregation':
      return (
        <marker id={id} {...common} markerWidth={14} markerHeight={10} refX={11} refY={5} viewBox="0 0 14 10">
          <polygon points="0,5 7,1 14,5 7,9" fill="#0b0d12" stroke={stroke} strokeWidth={1.4} />
        </marker>
      )
    case 'assignment-dot':
      return (
        <marker id={id} {...common} markerWidth={8} markerHeight={8} refX={4} refY={4} viewBox="0 0 8 8">
          <circle cx={4} cy={4} r={3} fill={stroke} />
        </marker>
      )
    case 'triggering':
      return (
        <marker id={id} {...common} markerWidth={11} markerHeight={9} refX={10} refY={4.5} viewBox="0 0 11 9">
          <polygon points="0,0 11,4.5 0,9" fill={stroke} />
        </marker>
      )
    case 'used-by':
      return (
        <marker id={id} {...common} markerWidth={11} markerHeight={9} refX={10} refY={4.5} viewBox="0 0 11 9">
          <polyline points="0,0 11,4.5 0,9" fill="none" stroke={stroke} strokeWidth={1.4} />
        </marker>
      )
    case 'realization':
      return (
        <marker id={id} {...common} markerWidth={11} markerHeight={9} refX={10} refY={4.5} viewBox="0 0 11 9">
          <polygon points="0,0 11,4.5 0,9" fill="#0b0d12" stroke={stroke} strokeWidth={1.4} />
        </marker>
      )
    case 'attached':
      return (
        <marker id={id} {...common} markerWidth={9} markerHeight={9} refX={7} refY={4.5} viewBox="0 0 9 9">
          <circle cx={4.5} cy={4.5} r={3} fill="#0b0d12" stroke={stroke} strokeWidth={1.2} />
        </marker>
      )
    default:
      return null
  }
}

/** Inline 44×14 thumbnail that renders the same marker shapes the canvas
 *  uses for an edge of the given relation type. */
function EdgeLegendThumb({ type }: { type: ArchEdgeType }) {
  const stroke = EDGE_STROKE[type]
  const dashed = EDGE_DASHED[type]
  const startKind = EDGE_MARKER_START[type]
  const endKind = EDGE_MARKER_END[type]
  const W = 44
  const H = 14
  return (
    <svg width={W} height={H} aria-hidden="true">
      <defs>
        {startKind && <LegendMarker kind={startKind} stroke={stroke} />}
        {endKind && <LegendMarker kind={endKind} stroke={stroke} />}
      </defs>
      <line
        x1={6}
        y1={H / 2}
        x2={W - 6}
        y2={H / 2}
        stroke={stroke}
        strokeWidth={1.5}
        strokeDasharray={dashed ? '5,3' : undefined}
        markerStart={startKind ? `url(#${markerId(startKind, stroke)}-legend)` : undefined}
        markerEnd={endKind ? `url(#${markerId(endKind, stroke)}-legend)` : undefined}
      />
    </svg>
  )
}

export interface GraphLegendProps {
  testIdPrefix?: string
  /** Live per-relation edge counts from the canvas. When provided the
   *  Legend renders a fourth "Relations" section (the merged ArchiMate
   *  connections list, #3980 fix 3). Omitting it hides that section so
   *  non-cloud embedders (job DAG, etc.) keep the 3-channel legend. */
  edgeTypeCounts?: Map<ArchEdgeType, number>
}

export function GraphLegend({ testIdPrefix = 'arch-graph', edgeTypeCounts }: GraphLegendProps) {
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

          {/* RELATIONS = ArchiMate edge types (merged from the retired
              bottom EdgeLegendPopover, #3980 fix 3). Only rendered when the
              embedder supplies live edge counts. */}
          {edgeTypeCounts && (
            <section data-testid={`${testIdPrefix}-legend-relations`}>
              <h4 className="mb-1 text-[10px] font-semibold uppercase tracking-wider text-[var(--color-text-dim)]">
                ArchiMate connections ({ALL_EDGE_TYPES.length})
              </h4>
              <ul className="grid grid-cols-1 gap-0.5">
                {ALL_EDGE_TYPES.map((t) => {
                  const count = edgeTypeCounts.get(t) ?? 0
                  return (
                    <li
                      key={t}
                      data-testid={`${testIdPrefix}-legend-relation-${t}`}
                      className="flex items-center gap-1.5 text-[var(--color-text)]"
                      aria-label={`${t} relation: ${count} edges`}
                    >
                      <EdgeLegendThumb type={t} />
                      <span>{t}</span>
                      <span className="text-[var(--color-text-dim)]">({count})</span>
                    </li>
                  )
                })}
              </ul>
            </section>
          )}
        </div>
      )}
    </div>
  )
}
