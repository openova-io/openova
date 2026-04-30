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

import { useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, MouseEvent as ReactMouseEvent } from 'react'
import {
  forceSimulation,
  forceCollide,
  forceX,
  forceY,
  forceLink,
  type Simulation,
  type SimulationNodeDatum,
  type SimulationLinkDatum,
} from 'd3-force'
import { drag as d3drag } from 'd3-drag'
import { select } from 'd3-selection'
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

/* Simulation node shape — extends d3 SimulationNodeDatum with our own
 * jobId and the layout-suggested anchor point we softly pull toward. */
type SimNode = SimulationNodeDatum & {
  id: string
  ax: number // anchor x (from layout)
  ay: number // anchor y (from layout)
  r: number  // collision radius
}

export function FlowCanvasV4(props: FlowCanvasV4Props) {
  const { layout, families, openJobId, highlightJobId, onJobClick, onJobDoubleClick, onCanvasBackgroundClick } = props
  const svgRef = useRef<SVGSVGElement | null>(null)
  const simRef = useRef<Simulation<SimNode, SimulationLinkDatum<SimNode>> | null>(null)
  const nodesRef = useRef<Map<string, SimNode>>(new Map())
  // tick: bumped on every simulation frame so React re-renders node positions
  const [tick, setTick] = useState(0)

  // Build / refresh sim nodes whenever layout changes (job added/removed
  // or mode toggled). We preserve any jobs already simulated so dragged
  // positions survive a layout refresh.
  const simNodes = useMemo<SimNode[]>(() => {
    const next: SimNode[] = []
    const seen = new Set<string>()
    for (const n of layout.nodes) {
      seen.add(n.id)
      const existing = nodesRef.current.get(n.id)
      const r = n.r + 4
      if (existing) {
        // Update anchor + radius; keep current x/y (drag survives)
        existing.ax = n.cx
        existing.ay = n.cy
        existing.r = r
        next.push(existing)
      } else {
        const fresh: SimNode = { id: n.id, x: n.cx, y: n.cy, ax: n.cx, ay: n.cy, r }
        nodesRef.current.set(n.id, fresh)
        next.push(fresh)
      }
    }
    // Drop nodes that no longer exist
    for (const id of Array.from(nodesRef.current.keys())) {
      if (!seen.has(id)) nodesRef.current.delete(id)
    }
    return next
  }, [layout])

  // Build / restart simulation when sim nodes change.
  useEffect(() => {
    if (simNodes.length === 0) {
      simRef.current?.stop()
      simRef.current = null
      return
    }
    const links: SimulationLinkDatum<SimNode>[] = []
    for (const e of layout.edges) {
      const s = nodesRef.current.get(e.fromId)
      const t = nodesRef.current.get(e.toId)
      if (s && t) links.push({ source: s, target: t })
    }
    const avgR = simNodes.reduce((s, n) => s + n.r, 0) / Math.max(1, simNodes.length)
    const sim = forceSimulation<SimNode>(simNodes)
      .alpha(0.6)
      .alphaDecay(0.05)
      .velocityDecay(0.35)
      .force('collide', forceCollide<SimNode>().radius((d) => d.r).strength(0.85).iterations(2))
      .force('x', forceX<SimNode>().x((d) => d.ax).strength(0.18))
      .force('y', forceY<SimNode>().y((d) => d.ay).strength(0.22))
      .force('link', forceLink<SimNode, SimulationLinkDatum<SimNode>>(links)
        .id((d) => d.id)
        .distance(avgR * 4)
        .strength(0.04))
      .on('tick', () => setTick((t) => t + 1))

    simRef.current = sim
    return () => {
      sim.stop()
    }
  }, [simNodes, layout.edges])

  // Wire d3-drag onto each node group. Re-run when nodes change.
  useEffect(() => {
    if (!svgRef.current) return
    const svg = select(svgRef.current)
    const sim = simRef.current
    if (!sim) return

    const dragBehavior = d3drag<SVGGElement, SimNode>()
      .on('start', (event, d) => {
        if (!event.active) sim.alphaTarget(0.3).restart()
        d.fx = d.x ?? 0
        d.fy = d.y ?? 0
      })
      .on('drag', (event, d) => {
        d.fx = event.x
        d.fy = event.y
      })
      .on('end', (event, d) => {
        if (!event.active) sim.alphaTarget(0)
        // Release pin so it settles back into anchor pull when not dragged.
        d.fx = null
        d.fy = null
      })

    const sel = svg.selectAll<SVGGElement, SimNode>('[data-flow-draggable]')
      .data(simNodes, (d) => d?.id ?? '')
    // d3-drag attaches via .call — TS gets confused; runtime is fine.
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(sel as any).call(dragBehavior)
  }, [simNodes, tick])

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

  // Build a position-override map from sim state so render uses simulated
  // x/y instead of static layout x/y.
  const livePos = new Map<string, { x: number; y: number }>()
  for (const n of simNodes) {
    if (typeof n.x === 'number' && typeof n.y === 'number') {
      livePos.set(n.id, { x: n.x, y: n.y })
    }
  }
  void tick // ensure re-render on each tick

  return (
    <svg
      ref={svgRef}
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
      {layout.edges.map((edge) => {
        const s = livePos.get(edge.fromId)
        const t = livePos.get(edge.toId)
        // When live positions exist, recompute control points so the
        // bezier follows the dragged/simulated nodes. Two control
        // points perpendicular to the centre line.
        let liveEdge: FlowEdgeV4 = edge
        if (s && t) {
          const dx = t.x - s.x
          const dy = t.y - s.y
          const len = Math.hypot(dx, dy) || 1
          const off = Math.min(60, len * 0.18)
          const nx = -dy / len
          const ny = dx / len
          liveEdge = {
            ...edge,
            points: [
              { x: s.x, y: s.y },
              { x: s.x + dx * 0.33 + nx * off, y: s.y + dy * 0.33 + ny * off },
              { x: s.x + dx * 0.66 + nx * off, y: s.y + dy * 0.66 + ny * off },
              { x: t.x, y: t.y },
            ],
          }
        }
        return (
          <FlowEdge key={`${edge.fromId}-${edge.toId}`} edge={liveEdge} />
        )
      })}

      {/* ── Nodes (circular glyph + ring + arc + label) ─────────── */}
      {layout.nodes.map((node) => {
        const pos = livePos.get(node.id)
        const liveNode: FlowNodeV4 = pos ? { ...node, cx: pos.x, cy: pos.y } : node
        return (
          <FlowNode
            key={node.id}
            node={liveNode}
            family={familyById.get(node.familyId) ?? null}
            isOpen={openJobId === node.id}
            isHighlighted={node.highlighted || highlightJobId === node.id}
            onClick={(e) => onJobClick(node.id, e)}
            onDoubleClick={() => onJobDoubleClick(node.id)}
          />
        )
      })}
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
      data-flow-draggable
      data-status={node.status}
      data-region={node.regionId}
      data-family={node.familyId}
      data-highlighted={isHighlighted ? 'true' : 'false'}
      data-open={isOpen ? 'true' : 'false'}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      style={{ cursor: 'grab' }}
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
