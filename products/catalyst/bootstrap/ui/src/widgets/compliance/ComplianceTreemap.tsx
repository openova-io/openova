/**
 * ComplianceTreemap — squarified treemap surface for the SRE Lead +
 * Security Lead compliance dashboards (slices U1+U2, #1096).
 *
 * Built on Recharts <Treemap> per the brief's architecture rule.
 * Recharts ships a squarified tiler by default, which matches the
 * brief's "Recharts squarified treemap" ask.
 *
 * Data shape:
 *   {
 *     name: <organization>,
 *     children: [
 *       {
 *         name: <application>,
 *         total: <0..100 | null>,        // for color
 *         denominator: <int>,             // for area
 *         score: <Score>,                 // raw, for tooltip
 *         children: undefined,
 *       },
 *       ...
 *     ],
 *   }, ...
 *
 * Cells:
 *   • box AREA  ← `denominator` (sum of policy weights = the
 *                 application's compliance "size")
 *   • box COLOR ← `total` percentage via `scoreColor()` palette
 *
 * Click a leaf → `onLeafClick` fires (consumer routes to U4 drilldown).
 * Hover → tooltip with score + violation count.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the palette is
 * runtime-selected via the `palette` prop. Cell padding + min-label-size
 * thresholds are exported constants for tests.
 */

import { useMemo } from 'react'
import { ResponsiveContainer, Treemap } from 'recharts'
import {
  scoreColor,
  scoreLabel,
  type ColorPalette,
} from '@/pages/admin/compliance/compliance.api'
import type { ComplianceTreemapNode } from './ComplianceTreemapNode'
import {
  TREEMAP_HEIGHT_PX as HEIGHT_PX,
  LABEL_MIN_WIDTH_PX as MIN_W,
  LABEL_MIN_HEIGHT_PX as MIN_H,
} from './ComplianceTreemap.constants'

const TREEMAP_HEIGHT_PX = HEIGHT_PX
const LABEL_MIN_WIDTH_PX = MIN_W
const LABEL_MIN_HEIGHT_PX = MIN_H

export interface ComplianceTreemapProps {
  nodes: ComplianceTreemapNode[]
  palette: ColorPalette
  onLeafClick?: (node: ComplianceTreemapNode) => void
  /** Test seam — prevents the ResponsiveContainer probe under jsdom. */
  forceWidth?: number
  forceHeight?: number
}

/**
 * RechartsCellProps — shape recharts v3 passes to the content callback.
 *
 * In recharts v3 the Treemap `content` function is invoked with the
 * computed TreemapNode itself as the single argument
 * (`content(nodeProps)` in ContentItem) — not with a `{payload, ...}`
 * wrapper as recharts v2 did. The node carries `x/y/width/height/
 * depth/name/value/children` AT THE TOP LEVEL, plus every original
 * data field (size, total, score, …) spread back onto it via
 * `_objectSpread({}, node)`. There is no `payload` key.
 *
 * Pre-G86b this interface declared `payload?: ComplianceTreemapNode`
 * and the cell renderer destructured `payload` → it was always
 * undefined → the early `if (!payload) return null` short-circuit
 * fired for EVERY leaf → 48 empty `<g>` containers with no `<rect>`
 * inside → blank treemap surface despite valid wire data.
 */
interface RechartsCellProps {
  x?: number
  y?: number
  width?: number
  height?: number
  depth?: number
  name?: string
  children?: ComplianceTreemapNode[]
  total?: number | null
  size?: number
  score?: ComplianceTreemapNode['score']
  // Recharts adds its own bookkeeping fields too; we don't use them.
  root?: unknown
  index?: number
  value?: number
  tooltipIndex?: unknown
}

/**
 * renderTreemapCell — pure cell-rendering function. NOT a React
 * component (no hooks, no state) — recharts invokes it as a tree-walk
 * callback with the cell's geometry + payload-equivalent fields
 * spread onto the same argument object. Returns SVG nodes.
 *
 * The two callsite-specific values (palette + onLeafClick) flow in
 * through the closure created at component-render time; the returned
 * function captures them. Recharts re-walks every cell on each
 * render, so live palette / handler updates surface immediately.
 */
function renderTreemapCell(
  palette: ColorPalette,
  onLeafClick: ((n: ComplianceTreemapNode) => void) | undefined,
): (props: RechartsCellProps) => React.ReactNode {
  return function cellRenderer(props: RechartsCellProps): React.ReactNode {
    const { x = 0, y = 0, width = 0, height = 0, depth = 0 } = props
    if (width <= 0 || height <= 0) return null

    // Depth 0 is the synthetic root recharts wraps everything in. We
    // never render it.
    if (depth === 0) return null

    // Recharts v3 contract: every original data field is spread back
    // onto the node argument, so `name / children / total / score` are
    // read DIRECTLY off props — not via a `payload` wrapper.
    const children = props.children as ComplianceTreemapNode[] | undefined
    const isLeaf = !children || children.length === 0
    const total = props.total ?? null
    const name = props.name ?? '—'
    const fill = isLeaf
      ? scoreColor(total, palette)
      : 'rgba(255, 255, 255, 0.04)'
    const stroke = depth === 1 ? 'rgba(255, 255, 255, 0.18)' : 'rgba(255, 255, 255, 0.10)'
    const strokeWidth = depth === 1 ? 1 : 0.5

    const showLabel = width >= LABEL_MIN_WIDTH_PX && height >= LABEL_MIN_HEIGHT_PX
    const cursor = isLeaf && onLeafClick ? 'pointer' : 'default'

    // Reconstruct a ComplianceTreemapNode for the click handler so the
    // existing onLeafClick signature is preserved (no API change for
    // consumers). The synthesised node carries the same fields the
    // upstream mapper produced.
    const nodeForClick: ComplianceTreemapNode = {
      name,
      total,
      size: props.size ?? 0,
      score: props.score,
    }

    return (
      <g
        data-testid={`compliance-treemap-cell-${name}`}
        style={{ cursor }}
        onClick={() => {
          if (isLeaf && onLeafClick) onLeafClick(nodeForClick)
        }}
      >
      <rect
        x={x}
        y={y}
        width={width}
        height={height}
        style={{ fill, stroke, strokeWidth }}
      />
      {showLabel ? (
        <>
          <text
            x={x + 8}
            y={y + 16}
            fill="rgba(255, 255, 255, 0.95)"
            fontSize={11}
            fontWeight={600}
            style={{ pointerEvents: 'none' }}
          >
            {truncateLabel(name, width)}
          </text>
          {isLeaf ? (
            <text
              x={x + 8}
              y={y + 30}
              fill="rgba(255, 255, 255, 0.7)"
              fontSize={10}
              style={{ pointerEvents: 'none' }}
            >
              {scoreLabel(total)}
              {total !== null ? '%' : ''}
            </text>
          ) : null}
        </>
      ) : null}
    </g>
    )
  }
}

function truncateLabel(name: string, width: number): string {
  const maxChars = Math.max(3, Math.floor((width - 12) / 6.5))
  if (name.length <= maxChars) return name
  return name.slice(0, Math.max(1, maxChars - 1)) + '…'
}

export function ComplianceTreemap({
  nodes,
  palette,
  onLeafClick,
  forceWidth,
  forceHeight,
}: ComplianceTreemapProps) {
  // renderCell is a CALL-BACK render fn (NOT a component) — recharts
  // accepts content as either a ReactNode or a function. Passing a fn
  // sidesteps the "components-during-render" lint rule and gives us
  // live closure capture of palette + onLeafClick.
  const renderCell = useMemo(
    () => renderTreemapCell(palette, onLeafClick),
    [palette, onLeafClick],
  )

  // Recharts requires a top-level array with a single synthetic root
  // OR the array directly — using the array directly produces an
  // implicit root that wraps everything.
  const data = useMemo(() => nodes, [nodes])

  if (nodes.length === 0) {
    return (
      <div
        data-testid="compliance-treemap-empty"
        className="flex h-[600px] items-center justify-center text-sm text-[var(--color-text-dim)]"
        style={{ height: forceHeight ?? TREEMAP_HEIGHT_PX }}
      >
        No compliance data yet. Once Kyverno reports come in, applications will appear here.
      </div>
    )
  }

  const useResponsive = forceWidth === undefined || forceHeight === undefined
  if (!useResponsive && forceWidth !== undefined && forceHeight !== undefined) {
    return (
      <div data-testid="compliance-treemap-surface" style={{ width: forceWidth, height: forceHeight }}>
        <Treemap
          width={forceWidth}
          height={forceHeight}
          data={data}
          dataKey="size"
          stroke="rgba(255, 255, 255, 0.18)"
          aspectRatio={1}
          content={renderCell as never}
          isAnimationActive={false}
        />
      </div>
    )
  }

  return (
    <div
      data-testid="compliance-treemap-surface"
      style={{ width: '100%', height: TREEMAP_HEIGHT_PX }}
    >
      <ResponsiveContainer width="100%" height={TREEMAP_HEIGHT_PX}>
        <Treemap
          data={data}
          dataKey="size"
          stroke="rgba(255, 255, 255, 0.18)"
          aspectRatio={1}
          content={renderCell as never}
          isAnimationActive={false}
        />
      </ResponsiveContainer>
    </div>
  )
}
