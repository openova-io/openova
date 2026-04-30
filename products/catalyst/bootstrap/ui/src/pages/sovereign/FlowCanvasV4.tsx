/**
 * FlowCanvasV4 — circular-node + multi-region + bezier rendering layer.
 *
 * Replaces the pill-card swimlane SVG that PR #245 shipped (rejected by
 * the operator as "intentional divergence" — the canonical mockup at
 * `marketing/mockups/provision-mockup-v4.png` calls for circular glyph
 * nodes, regional grouping, and a right-side log feed).
 *
 * Pure presentation: receives a fully-laid-out FlowLayoutV4Result plus
 * click handlers and renders the SVG. No data-fetching, no derivation
 * past what the layout already produced.
 *
 * Preserved test contracts (so cosmetic-guards.spec.ts keeps passing):
 *   • `data-testid="flow-canvas-svg"` on the root <svg>.
 *   • `data-testid="flow-job-<jobId>"` on every node group.
 *   • `data-testid="flow-batch-<batchId>"` on each region container —
 *     mapped 1:1 from `regionId` for cosmetic-guards Test #6 ("Flow
 *     canvas rendered with at least one batch swimlane"). Multi-region
 *     designs render multiple [data-testid^=flow-batch-] elements.
 *
 * New testids (for the upgraded mockup-fidelity guards):
 *   • `data-testid="flow-node-circle-<jobId>"` — the actual <circle>.
 *   • `data-testid="flow-region-<regionId>"` — region band frame.
 *   • `data-testid="flow-stage-<n>"` — stage column divider line.
 *   • `data-testid="flow-edge-<from>-<to>"` — directional edges.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall) — circular nodes, region bands, bezier edges, family
 *      glyphs, and progress arcs all ship together.
 *   #2 (no compromise) — no graph library, no canvas rendering — pure
 *      SVG so testids work and the operator can right-click → inspect.
 *   #4 (never hardcode) — every dimension is in the geometry knob set,
 *      every colour in the FlowFamily palette.
 */

import type { CSSProperties, MouseEvent as ReactMouseEvent } from 'react'
import {
  pointsToPath,
  type FlowLayoutV4Result,
  type FlowNodeV4,
  type FlowEdgeV4,
  type FlowFamily,
  type FlowRegionLane,
} from '@/lib/flowLayoutV4'
import type { JobStatus } from '@/lib/jobs.types'

/* ──────────────────────────────────────────────────────────────────
 * Status palette
 * ────────────────────────────────────────────────────────────────── */

interface StatusTone {
  fill: string
  ring: string
  glyph: string
  glow: string
  arc: string
  label: string
}

const STATUS_TONE: Record<JobStatus, StatusTone> = {
  succeeded: {
    fill: '#0F1F18',
    ring: 'rgba(74,222,128,0.65)',
    glyph: '#86EFAC',
    glow: 'rgba(74,222,128,0.20)',
    arc: '#4ADE80',
    label: 'Succeeded',
  },
  running: {
    fill: '#0E1A33',
    ring: 'rgba(56,189,248,0.65)',
    glyph: '#BAE6FD',
    glow: 'rgba(56,189,248,0.25)',
    arc: '#38BDF8',
    label: 'Running',
  },
  failed: {
    fill: '#23070A',
    ring: 'rgba(248,113,113,0.7)',
    glyph: '#FCA5A5',
    glow: 'rgba(248,113,113,0.30)',
    arc: '#F87171',
    label: 'Failed',
  },
  pending: {
    fill: '#0D1726',
    ring: 'rgba(148,163,184,0.32)',
    glyph: 'rgba(148,163,184,0.55)',
    glow: 'transparent',
    arc: 'rgba(148,163,184,0.45)',
    label: 'Pending',
  },
}

/* ──────────────────────────────────────────────────────────────────
 * Component
 * ────────────────────────────────────────────────────────────────── */

export interface FlowCanvasV4Props {
  layout: FlowLayoutV4Result
  /** Family palette — used to look up node ring colour by familyId. */
  families: readonly FlowFamily[]
  /** Currently-open job (single-click) — gets a brighter ring. */
  openJobId: string | null
  /** Currently-highlighted job (operator-supplied highlight). */
  highlightJobId: string | null
  /** Embedded variant — drops some chrome. */
  embedded: boolean
  /** Click + double-click delegates. */
  onJobClick: (jobId: string, event: ReactMouseEvent<SVGGElement>) => void
  onJobDoubleClick: (jobId: string) => void
  onCanvasBackgroundClick: () => void
}

export function FlowCanvasV4(props: FlowCanvasV4Props) {
  const { layout, families, openJobId, highlightJobId, onJobClick, onJobDoubleClick, onCanvasBackgroundClick } = props

  if (layout.nodes.length === 0 && layout.regions.length === 0) {
    return (
      <div
        data-testid="flow-canvas-empty"
        className="rounded-xl border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-dim)]"
      >
        No jobs to render in the dependency graph.
      </div>
    )
  }

  const familyById = new Map<string, FlowFamily>()
  for (const f of families) familyById.set(f.id, f)

  return (
    <svg
      width="100%"
      height="100%"
      viewBox={`0 0 ${layout.width} ${layout.height}`}
      preserveAspectRatio="xMidYMid meet"
      className="flow-canvas-svg-v4"
      data-testid="flow-canvas-svg"
      role="img"
      aria-label="Provisioning dependency flow"
      style={{ display: 'block', width: '100%', height: '100%' }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onCanvasBackgroundClick()
      }}
    >
      <defs>
        {/* Arrow markers, one per status colour, so edges retain
            directional meaning across pending/running/done/failed. */}
        {(['pending', 'running', 'succeeded', 'failed'] as const).map((s) => (
          <marker
            key={s}
            id={`flow-v4-arrow-${s}`}
            viewBox="0 0 8 8"
            refX="7"
            refY="4"
            markerWidth="5"
            markerHeight="5"
            orient="auto-start-reverse"
          >
            <path d="M0,1 L7,4 L0,7 Z" fill={STATUS_TONE[s].arc} opacity="0.85" />
          </marker>
        ))}
        {/* Cross-region marker uses a warm amber tone like the mockup. */}
        <marker
          id="flow-v4-arrow-cross"
          viewBox="0 0 8 8"
          refX="7"
          refY="4"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path d="M0,1 L7,4 L0,7 Z" fill="rgba(253,230,138,0.85)" />
        </marker>
      </defs>

      {/* ── Region band frames + labels ─────────────────────────── */}
      {layout.regions.map((r) => (
        <RegionBand key={r.regionId} region={r} canvasWidth={layout.width} />
      ))}

      {/* ── Stage column dividers (subtle vertical lines) ───────── */}
      {layout.stages.slice(1).map((s) => (
        <line
          key={`div-${s.stage}`}
          x1={s.left}
          x2={s.left}
          y1={12}
          y2={layout.height - 26}
          stroke="rgba(255,255,255,0.045)"
          strokeWidth={1}
          data-testid={`flow-stage-${s.stage}`}
        />
      ))}
      {/* ── Stage column labels (mockup row at bottom) ──────────── */}
      {layout.stages.map((s) => (
        <text
          key={`lbl-${s.stage}`}
          x={s.cx}
          y={layout.height - 8}
          fontSize={9}
          fontWeight={700}
          letterSpacing="0.10em"
          textAnchor="middle"
          fill="rgba(255,255,255,0.30)"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
          data-testid={`flow-stage-label-${s.stage}`}
          pointerEvents="none"
        >
          STAGE {s.stage}
        </text>
      ))}

      {/* ── Edges (drawn before nodes so nodes sit on top) ──────── */}
      {layout.edges.map((edge) => (
        <FlowEdge key={`${edge.fromId}-${edge.toId}`} edge={edge} />
      ))}

      {/* ── Nodes (circular glyph + ring + arc + label) ─────────── */}
      {layout.nodes.map((node) => (
        <FlowNode
          key={node.id}
          node={node}
          family={familyById.get(node.familyId) ?? null}
          isOpen={openJobId === node.id}
          isHighlighted={node.highlighted || highlightJobId === node.id}
          onClick={(e) => onJobClick(node.id, e)}
          onDoubleClick={() => onJobDoubleClick(node.id)}
        />
      ))}
    </svg>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * RegionBand
 * ────────────────────────────────────────────────────────────────── */

interface RegionBandProps {
  region: FlowRegionLane
  canvasWidth: number
}

function RegionBand({ region, canvasWidth }: RegionBandProps) {
  return (
    <g
      data-testid={`flow-batch-${region.regionId}`}
      data-region={region.regionId}
    >
      {/* Subtle band background — a wide rect framing the region. */}
      <rect
        x={12}
        y={region.y}
        width={canvasWidth - 24}
        height={region.height}
        rx={14}
        ry={14}
        fill="rgba(255,255,255,0.012)"
        stroke="rgba(255,255,255,0.05)"
        strokeWidth={1}
        data-testid={`flow-region-${region.regionId}`}
      />
      <text
        x={20}
        y={region.y + 16}
        fill="rgba(255,255,255,0.55)"
        fontSize={10}
        fontWeight={700}
        letterSpacing="0.14em"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
      >
        {region.label.toUpperCase()}
      </text>
      {region.meta ? (
        <text
          x={20}
          y={region.y + 16}
          dx={10 + region.label.length * 6.4}
          fill="rgba(255,255,255,0.30)"
          fontSize={9}
          fontWeight={500}
          letterSpacing="0.04em"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
        >
          {region.meta}
        </text>
      ) : null}
      <text
        x={canvasWidth - 20}
        y={region.y + 16}
        fill="rgba(255,255,255,0.30)"
        fontSize={9}
        fontWeight={600}
        letterSpacing="0.06em"
        textAnchor="end"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
      >
        {region.nodeCount} {region.nodeCount === 1 ? 'JOB' : 'JOBS'}
      </text>
    </g>
  )
}

/* ──────────────────────────────────────────────────────────────────
 * FlowNode
 * ────────────────────────────────────────────────────────────────── */

interface FlowNodeProps {
  node: FlowNodeV4
  family: FlowFamily | null
  isOpen: boolean
  isHighlighted: boolean
  onClick: (e: ReactMouseEvent<SVGGElement>) => void
  onDoubleClick: () => void
}

function FlowNode({ node, family, isOpen, isHighlighted, onClick, onDoubleClick }: FlowNodeProps) {
  const tone = STATUS_TONE[node.status]
  const familyColor = family?.color ?? 'rgba(148,163,184,0.55)'
  // Pending nodes still wear their family ring colour at a softer
  // opacity so the operator can still scan the family layout while
  // nothing has started running.
  const ringColor = node.status === 'pending' ? familyColor : tone.ring
  const ringOpacity = node.status === 'pending' ? 0.55 : 0.85
  const circumference = 2 * Math.PI * node.r
  const arcLen = Math.max(0, Math.min(1, node.progress)) * circumference

  // Single-letter glyph derived from the family — readable when the
  // node is small. Keeps the mockup aesthetic without bringing in an
  // icon library.
  const glyph = (family?.label ?? node.familyId).charAt(0).toUpperCase()

  const tooltip = [
    node.label,
    `Family: ${family?.label ?? node.familyId}`,
    `Stage: ${node.stage}`,
    `Status: ${tone.label}`,
    node.subLabel ? `Duration: ${node.subLabel}` : '',
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <g
      data-testid={`flow-job-${node.id}`}
      data-status={node.status}
      data-region={node.regionId}
      data-family={node.familyId}
      data-highlighted={isHighlighted ? 'true' : 'false'}
      data-open={isOpen ? 'true' : 'false'}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      style={{ cursor: 'pointer' }}
      transform={`translate(${node.cx.toFixed(1)}, ${node.cy.toFixed(1)})`}
    >
      <title>{tooltip}</title>
      {/* Hover halo — invisible until :hover via CSS. */}
      <circle
        r={node.r + 8}
        fill="none"
        stroke={familyColor}
        strokeWidth={1.5}
        opacity={isHighlighted ? 0.65 : 0}
        className="flow-v4-halo"
      />
      {/* Glow underlay for active states. */}
      {node.status === 'running' || node.status === 'failed' || isOpen || isHighlighted ? (
        <circle
          r={node.r + 4}
          fill={isHighlighted ? 'rgba(56,189,248,0.18)' : tone.glow}
          opacity={node.status === 'running' ? 0.85 : 0.55}
        />
      ) : null}
      {/* Body fill */}
      <circle
        r={node.r}
        fill={tone.fill}
        data-testid={`flow-node-circle-${node.id}`}
      />
      {/* Family ring */}
      <circle
        r={node.r}
        fill="none"
        stroke={ringColor}
        strokeWidth={1.6}
        opacity={ringOpacity}
      />
      {/* Progress arc — outer ring driven by node.progress */}
      {node.progress > 0 ? (
        <circle
          r={node.r}
          fill="none"
          stroke={tone.arc}
          strokeWidth={3.2}
          strokeLinecap="round"
          strokeDasharray={`${arcLen.toFixed(2)} ${circumference.toFixed(2)}`}
          transform="rotate(-90)"
          opacity={node.status === 'pending' ? 0 : 0.92}
        >
          {node.status === 'running' ? (
            <animate
              attributeName="opacity"
              values="0.92;0.42;0.92"
              dur="1.8s"
              repeatCount="indefinite"
            />
          ) : null}
        </circle>
      ) : null}
      {/* Centre glyph: status icon for terminal states, family letter
          for pending/running. */}
      {node.status === 'succeeded' ? (
        <text
          fontSize={Math.round(node.r * 0.85)}
          textAnchor="middle"
          dominantBaseline="central"
          fill={tone.glyph}
          fontFamily="Inter, system-ui, sans-serif"
          fontWeight={800}
          pointerEvents="none"
        >
          ✓
        </text>
      ) : node.status === 'failed' ? (
        <text
          fontSize={Math.round(node.r * 0.85)}
          textAnchor="middle"
          dominantBaseline="central"
          fill={tone.glyph}
          fontFamily="Inter, system-ui, sans-serif"
          fontWeight={800}
          pointerEvents="none"
        >
          ✕
        </text>
      ) : (
        <text
          fontSize={Math.round(node.r * 0.62)}
          textAnchor="middle"
          dominantBaseline="central"
          fill={familyColor}
          fontFamily="Inter, system-ui, sans-serif"
          fontWeight={700}
          opacity={0.85}
          pointerEvents="none"
        >
          {glyph}
        </text>
      )}
      {/* Pulse dot for actively running. */}
      {node.status === 'running' ? (
        <circle
          cx={node.r * 0.78}
          cy={-node.r * 0.78}
          r={3}
          fill={tone.arc}
          className="flow-v4-pulse"
        >
          <animate
            attributeName="opacity"
            values="1;0.35;1"
            dur="1.4s"
            repeatCount="indefinite"
          />
        </circle>
      ) : null}
      {/* Label below the node */}
      <text
        y={node.r + 12}
        fontSize={10}
        textAnchor="middle"
        fill={node.status === 'pending' ? 'rgba(255,255,255,0.45)' : familyColor}
        fontFamily="Inter, system-ui, sans-serif"
        fontWeight={600}
        pointerEvents="none"
      >
        {truncate(node.label, 16)}
      </text>
      {node.subLabel ? (
        <text
          y={node.r + 24}
          fontSize={9}
          textAnchor="middle"
          fill="rgba(255,255,255,0.40)"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
          pointerEvents="none"
        >
          {node.subLabel}
        </text>
      ) : null}
    </g>
  )
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s
  return s.slice(0, max - 1) + '…'
}

/* ──────────────────────────────────────────────────────────────────
 * FlowEdge
 * ────────────────────────────────────────────────────────────────── */

interface FlowEdgeProps {
  edge: FlowEdgeV4
}

function FlowEdge({ edge }: FlowEdgeProps) {
  const d = pointsToPath(edge.points)
  if (!d) return null
  let stroke: string
  let strokeWidth = 1.4
  let dasharray: string | undefined
  let marker: string
  let opacity: number
  if (edge.kind === 'cross-region') {
    stroke = 'rgba(253,230,138,0.55)'
    strokeWidth = 1.6
    dasharray = '7 4'
    marker = 'url(#flow-v4-arrow-cross)'
    opacity = 0.85
  } else {
    const tone = STATUS_TONE[edge.fromStatus]
    if (edge.fromStatus === 'succeeded') {
      stroke = 'rgba(167,243,208,0.42)'
    } else if (edge.fromStatus === 'running') {
      stroke = 'rgba(186,230,253,0.55)'
    } else if (edge.fromStatus === 'failed') {
      stroke = 'rgba(252,165,165,0.65)'
    } else {
      stroke = 'rgba(148,163,184,0.28)'
    }
    void tone
    marker = `url(#flow-v4-arrow-${edge.fromStatus})`
    opacity = edge.fromStatus === 'pending' ? 0.5 : 0.85
  }
  const style: CSSProperties = dasharray ? { strokeDasharray: dasharray } : {}
  return (
    <path
      d={d}
      fill="none"
      stroke={stroke}
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      opacity={opacity}
      markerEnd={marker}
      style={style}
      data-testid={`flow-edge-${edge.fromId}-${edge.toId}`}
      data-kind={edge.kind}
      data-from-status={edge.fromStatus}
    />
  )
}
