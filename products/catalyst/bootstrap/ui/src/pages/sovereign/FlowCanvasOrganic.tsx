/**
 * FlowCanvasOrganic — organic canvas rendering for the Flow page.
 *
 * REPLACES FlowCanvasV4. Differences:
 *   • NO grid / NO column divisions / NO "STAGE n" labels.
 *   • NO precomputed positions — d3-force lays out from scratch.
 *   • forceX = depth × horizontalSpan (full canvas width usage).
 *   • forceY = region midpoint + per-node deterministic jitter so
 *     siblings scatter naturally and don't form a vertical column.
 *   • Edges drawn each tick from live positions; arrowheads with
 *     status-tinted markers.
 *   • Bubbles draggable (d3-drag); release lets physics resettle.
 *
 * Pure presentation: receives nodes/edges from flowLayoutOrganic +
 * region/family palettes and click handlers. No data fetching.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import type { JobStatus } from '@/lib/jobs.types'
import type {
  OrganicLayoutResult,
  OrganicNode,
  OrganicFamily,
  OrganicRegion,
} from '@/lib/flowLayoutOrganic'

/* ── Status palette ──────────────────────────────────────────── */

interface StatusTone {
  fill: string
  ring: string
  glyph: string
  glow: string
  edge: string
  arrow: string
  label: string
}
const STATUS_TONE: Record<JobStatus, StatusTone> = {
  succeeded: {
    fill: '#0F1F18',
    ring: 'rgba(74,222,128,0.78)',
    glyph: '#86EFAC',
    glow: 'rgba(74,222,128,0.20)',
    edge: 'rgba(74,222,128,0.55)',
    arrow: '#4ADE80',
    label: 'Succeeded',
  },
  running: {
    fill: '#0E1A33',
    ring: 'rgba(56,189,248,0.85)',
    glyph: '#BAE6FD',
    glow: 'rgba(56,189,248,0.30)',
    edge: 'rgba(56,189,248,0.65)',
    arrow: '#38BDF8',
    label: 'Running',
  },
  failed: {
    fill: '#23070A',
    ring: 'rgba(248,113,113,0.85)',
    glyph: '#FCA5A5',
    glow: 'rgba(248,113,113,0.30)',
    edge: 'rgba(248,113,113,0.65)',
    arrow: '#F87171',
    label: 'Failed',
  },
  pending: {
    fill: '#0D1726',
    ring: 'rgba(148,163,184,0.45)',
    glyph: 'rgba(148,163,184,0.65)',
    glow: 'transparent',
    edge: 'rgba(148,163,184,0.32)',
    arrow: 'rgba(148,163,184,0.60)',
    label: 'Pending',
  },
}

const NODE_RADIUS = 30 // px
const COLLIDE_PADDING = 6
// VIEW_H still used as a fallback for empty-region geometry.
const VIEW_H = 1100

/* Sim node shape. */
type SimNode = SimulationNodeDatum & {
  id: string
  depth: number
  regionId: string
  familyId: string
  status: JobStatus
}

export interface FlowCanvasOrganicProps {
  layout: OrganicLayoutResult
  openJobId: string | null
  highlightJobId: string | null
  embedded?: boolean
  onJobClick: (jobId: string, event: ReactMouseEvent<SVGGElement>) => void
  onJobDoubleClick: (jobId: string) => void
  onCanvasBackgroundClick: () => void
}

export function FlowCanvasOrganic(props: FlowCanvasOrganicProps) {
  const {
    layout,
    openJobId,
    highlightJobId,
    onJobClick,
    onJobDoubleClick,
    onCanvasBackgroundClick,
  } = props

  const svgRef = useRef<SVGSVGElement | null>(null)
  const simRef = useRef<Simulation<SimNode, SimulationLinkDatum<SimNode>> | null>(
    null,
  )
  const nodesRef = useRef<Map<string, SimNode>>(new Map())
  const [tick, setTick] = useState(0)

  // Region midpoints — divide vertical band by region count.
  // Constants in pixels of the simulation coord system; viewBox below
  // auto-fits so these absolute numbers don't matter for canvas usage.
  const REGION_BAND_H = NODE_RADIUS * 8 // 240px per region
  const regionYMid = useMemo(() => {
    const map = new Map<string, number>()
    const regions = layout.regions
    if (regions.length === 0) return map
    regions.forEach((r, i) => {
      map.set(r.id, i * REGION_BAND_H + REGION_BAND_H / 2)
    })
    return map
  }, [layout.regions, REGION_BAND_H])

  // Depth-to-x mapping: each unit of depth advances by a fixed
  // PER_DEPTH_X step. The auto-fit viewBox below scales the WHOLE
  // canvas to whatever total span the simulation produced — so even
  // 2 nodes at depth 0/1 won't fly to opposite edges (small bbox →
  // tight zoom) and 35 nodes at depth 0..6 will fill (big bbox →
  // wide zoom). This is the user's "smart fill" requirement.
  const PER_DEPTH_X = NODE_RADIUS * 5 // 150px per depth step
  const depthToX = useCallback(
    (depth: number) => depth * PER_DEPTH_X,
    [PER_DEPTH_X],
  )

  // Family palette lookup.
  const familyById = useMemo(() => {
    const m = new Map<string, OrganicFamily>()
    for (const f of layout.families) m.set(f.id, f)
    return m
  }, [layout.families])

  // Build / refresh sim nodes whenever layout changes. Preserve existing
  // positions so a layout refresh (e.g. status change) doesn't snap.
  const simNodes = useMemo<SimNode[]>(() => {
    const next: SimNode[] = []
    const seen = new Set<string>()
    for (const n of layout.nodes) {
      seen.add(n.id)
      const existing = nodesRef.current.get(n.id)
      if (existing) {
        existing.depth = n.depth
        existing.regionId = n.regionId
        existing.familyId = n.familyId
        existing.status = n.status
        next.push(existing)
      } else {
        // Seed with deterministic-but-spread initial position
        // x = depth-mapped + small jitter, y = region midpoint + per-node jitter
        const baseX = depthToX(n.depth)
        const baseY = regionYMid.get(n.regionId) ?? VIEW_H / 2
        const seed = hashSeed(n.id)
        const fresh: SimNode = {
          id: n.id,
          depth: n.depth,
          regionId: n.regionId,
          familyId: n.familyId,
          status: n.status,
          x: baseX + (seed.fx - 0.5) * 80,
          y: baseY + (seed.fy - 0.5) * 280,
        }
        nodesRef.current.set(n.id, fresh)
        next.push(fresh)
      }
    }
    for (const id of Array.from(nodesRef.current.keys())) {
      if (!seen.has(id)) nodesRef.current.delete(id)
    }
    return next
  }, [layout.nodes, depthToX, regionYMid])

  // Build / restart simulation when nodes or edges change.
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
    const sim = forceSimulation<SimNode>(simNodes)
      .alpha(0.9)
      .alphaDecay(0.025)
      .velocityDecay(0.3)
      .force(
        'collide',
        forceCollide<SimNode>()
          .radius(NODE_RADIUS + COLLIDE_PADDING)
          .strength(0.95)
          .iterations(2),
      )
      .force(
        'x',
        forceX<SimNode>()
          .x((d) => depthToX(d.depth))
          .strength(0.32),
      )
      .force(
        'y',
        forceY<SimNode>()
          .y((d) => {
            const base = regionYMid.get(d.regionId) ?? VIEW_H / 2
            // Add a per-node deterministic vertical offset so siblings
            // don't all converge on the region midline.
            const seed = hashSeed(d.id)
            return base + (seed.fy - 0.5) * 360
          })
          .strength(0.05),
      )
      .force(
        'link',
        forceLink<SimNode, SimulationLinkDatum<SimNode>>(links)
          .id((d) => d.id)
          // Each link prefers ~120px (= NODE_RADIUS*4). Hard caps on x-attractor
          // depthToX(maxDepth=N) keep total horizontal span bounded by node count.
          .distance(NODE_RADIUS * 4)
          .strength(0.08),
      )
      .on('tick', () => setTick((t) => t + 1))

    simRef.current = sim
    return () => {
      sim.stop()
    }
  }, [simNodes, layout.edges, depthToX, regionYMid])

  // d3-drag binding — re-run only when the SET of node ids changes.
  const nodeIdsKey = simNodes.map((n) => n.id).join(',')
  useEffect(() => {
    if (!svgRef.current) return
    const sim = simRef.current
    if (!sim) return

    const dragBehavior = d3drag<SVGGElement, unknown>()
      .on('start', function (event) {
        if (!event.active) sim.alphaTarget(0.3).restart()
        const id = (this as SVGGElement).getAttribute('data-job-id')
        const d = id ? nodesRef.current.get(id) : null
        if (d) {
          d.fx = d.x ?? 0
          d.fy = d.y ?? 0
        }
      })
      .on('drag', function (event) {
        const id = (this as SVGGElement).getAttribute('data-job-id')
        const d = id ? nodesRef.current.get(id) : null
        if (d) {
          d.fx = event.x
          d.fy = event.y
        }
      })
      .on('end', function (event) {
        if (!event.active) sim.alphaTarget(0)
        // PIN where the user dropped — operator wants drag to stick.
        // event.x/y are the final cursor position (already applied to fx/fy
        // during the last 'drag' event). Leaving fx/fy non-null pins the
        // node permanently against the simulation's anchor pull.
        // Operator can re-drag any time.
      })

    const sel = select(svgRef.current).selectAll<SVGGElement, unknown>(
      'g[data-flow-draggable]',
    )
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(sel as any).call(dragBehavior)
  }, [nodeIdsKey])

  void tick // ensure re-render each frame

  if (layout.nodes.length === 0) {
    return (
      <div
        data-testid="flow-canvas-empty"
        className="rounded-xl border border-dashed border-[var(--color-border)] p-8 text-center text-sm text-[var(--color-text-dim)]"
      >
        No jobs to render.
      </div>
    )
  }

  // Live position lookup.
  const livePos = new Map<string, { x: number; y: number }>()
  for (const n of simNodes) {
    if (typeof n.x === 'number' && typeof n.y === 'number') {
      livePos.set(n.id, { x: n.x, y: n.y })
    }
  }

  // Auto-fit viewBox: compute the bounding box of all simulated nodes
  // each render, add padding for labels, use that as the SVG viewBox.
  // This is the operator's "smart fill" requirement — 2 bubbles tightly
  // zoomed, 35 bubbles wider zoomed, always ~85-95% canvas usage.
  let bbMinX = Infinity, bbMinY = Infinity, bbMaxX = -Infinity, bbMaxY = -Infinity
  for (const p of livePos.values()) {
    if (p.x < bbMinX) bbMinX = p.x
    if (p.y < bbMinY) bbMinY = p.y
    if (p.x > bbMaxX) bbMaxX = p.x
    if (p.y > bbMaxY) bbMaxY = p.y
  }
  // Padding accounts for: bubble radius + label below + a bit of breathing room.
  const PAD_X = NODE_RADIUS + 30
  const PAD_Y_TOP = NODE_RADIUS + 12
  const PAD_Y_BOTTOM = NODE_RADIUS + 40 // extra for the text label below
  const vbX = (Number.isFinite(bbMinX) ? bbMinX : 0) - PAD_X
  const vbY = (Number.isFinite(bbMinY) ? bbMinY : 0) - PAD_Y_TOP
  const vbW = Math.max(NODE_RADIUS * 4, (bbMaxX - bbMinX) + PAD_X * 2)
  const vbH = Math.max(NODE_RADIUS * 4, (bbMaxY - bbMinY) + PAD_Y_TOP + PAD_Y_BOTTOM)

  return (
    <svg
      ref={svgRef}
      width="100%"
      height="100%"
      viewBox={`${vbX.toFixed(1)} ${vbY.toFixed(1)} ${vbW.toFixed(1)} ${vbH.toFixed(1)}`}
      preserveAspectRatio="xMidYMid meet"
      className="flow-canvas-svg-organic"
      data-testid="flow-canvas-svg"
      role="img"
      aria-label="Provisioning dependency flow"
      style={{ display: 'block', width: '100%', height: '100%' }}
      onClick={(e) => {
        if (e.target === e.currentTarget) onCanvasBackgroundClick()
      }}
    >
      <defs>
        {(['pending', 'running', 'succeeded', 'failed'] as const).map((s) => (
          <marker
            key={s}
            id={`flow-org-arrow-${s}`}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M0,1 L9,5 L0,9 Z" fill={STATUS_TONE[s].arrow} opacity="0.92" />
          </marker>
        ))}
      </defs>

      {/* Edges first so nodes sit on top */}
      {layout.edges.map((e) => {
        const s = livePos.get(e.fromId)
        const t = livePos.get(e.toId)
        if (!s || !t) return null
        return (
          <FlowEdge
            key={`${e.fromId}-${e.toId}`}
            from={s}
            to={t}
            status={e.fromStatus}
          />
        )
      })}

      {/* Nodes */}
      {layout.nodes.map((node) => {
        const pos = livePos.get(node.id)
        if (!pos) return null
        const family = familyById.get(node.familyId) ?? null
        return (
          <FlowNode
            key={node.id}
            node={node}
            x={pos.x}
            y={pos.y}
            family={family}
            isOpen={openJobId === node.id}
            isHighlighted={highlightJobId === node.id}
            onClick={(e) => onJobClick(node.id, e)}
            onDoubleClick={() => onJobDoubleClick(node.id)}
          />
        )
      })}
    </svg>
  )
}

/* ── FlowEdge — straight line, rim-to-rim, with arrowhead ──────── */

function FlowEdge({
  from,
  to,
  status,
}: {
  from: { x: number; y: number }
  to: { x: number; y: number }
  status: JobStatus
}) {
  const tone = STATUS_TONE[status]
  const dx = to.x - from.x
  const dy = to.y - from.y
  const len = Math.hypot(dx, dy) || 1
  const trim = NODE_RADIUS + 6 // arrow-head clearance
  const fx = from.x + (dx / len) * NODE_RADIUS
  const fy = from.y + (dy / len) * NODE_RADIUS
  const tx = to.x - (dx / len) * trim
  const ty = to.y - (dy / len) * trim
  return (
    <line
      x1={fx.toFixed(1)}
      y1={fy.toFixed(1)}
      x2={tx.toFixed(1)}
      y2={ty.toFixed(1)}
      stroke={tone.edge}
      strokeWidth={1.6}
      markerEnd={`url(#flow-org-arrow-${status})`}
      opacity={0.85}
    />
  )
}

/* ── FlowNode ──────────────────────────────────────────────────── */

interface FlowNodeProps {
  node: OrganicNode
  x: number
  y: number
  family: OrganicFamily | null
  isOpen: boolean
  isHighlighted: boolean
  onClick: (e: ReactMouseEvent<SVGGElement>) => void
  onDoubleClick: () => void
}

function FlowNode({
  node,
  x,
  y,
  family,
  isOpen,
  isHighlighted,
  onClick,
  onDoubleClick,
}: FlowNodeProps) {
  const tone = STATUS_TONE[node.status]
  const ringColor = tone.ring
  const familyColor = family?.color ?? 'rgba(148,163,184,0.55)'
  const grpStyle: CSSProperties = { cursor: 'grab' }

  return (
    <g
      data-testid={`flow-job-${node.id}`}
      data-flow-draggable=""
      data-job-id={node.id}
      data-status={node.status}
      data-region={node.regionId}
      data-family={node.familyId}
      data-open={isOpen ? 'true' : 'false'}
      data-highlighted={isHighlighted ? 'true' : 'false'}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      style={grpStyle}
      transform={`translate(${x.toFixed(1)}, ${y.toFixed(1)})`}
    >
      <title>{`${node.label} — ${tone.label}${node.subLabel ? ` · ${node.subLabel}` : ''}`}</title>
      {/* Glow underlay for active states */}
      {(node.status === 'running' || node.status === 'failed' || isOpen || isHighlighted) ? (
        <circle r={NODE_RADIUS + 8} fill={tone.glow} />
      ) : null}
      {/* Family-coloured outer ring (thin) */}
      <circle
        r={NODE_RADIUS + 2}
        fill="none"
        stroke={familyColor}
        strokeWidth={isHighlighted ? 2.5 : 1.2}
        opacity={0.55}
      />
      {/* Status ring */}
      <circle
        r={NODE_RADIUS}
        fill={tone.fill}
        stroke={ringColor}
        strokeWidth={isOpen ? 3 : 2}
      />
      {/* Status glyph */}
      <text
        x={0}
        y={6}
        textAnchor="middle"
        fontSize={22}
        fontWeight={700}
        fill={tone.glyph}
        fontFamily="ui-sans-serif, system-ui, sans-serif"
        pointerEvents="none"
      >
        {glyphFor(node.status)}
      </text>
      {/* Label below bubble */}
      <text
        x={0}
        y={NODE_RADIUS + 18}
        textAnchor="middle"
        fontSize={11}
        fill="rgba(255,255,255,0.85)"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
        pointerEvents="none"
      >
        {node.label.length > 18 ? node.label.slice(0, 17) + '…' : node.label}
      </text>
      {/* Sub-label (duration) */}
      {node.subLabel ? (
        <text
          x={0}
          y={NODE_RADIUS + 32}
          textAnchor="middle"
          fontSize={9}
          fill="rgba(255,255,255,0.45)"
          fontFamily="var(--font-mono, ui-monospace, monospace)"
          pointerEvents="none"
        >
          {node.subLabel}
        </text>
      ) : null}
    </g>
  )
}

function glyphFor(status: JobStatus): string {
  if (status === 'succeeded') return '✓'
  if (status === 'failed') return '✗'
  if (status === 'running') return '◐'
  return '○'
}

/* Deterministic per-id float in [0,1] (FNV-1a hash → mantissa). */
function hashSeed(id: string): { fx: number; fy: number } {
  let h = 2166136261
  for (let i = 0; i < id.length; i++) {
    h ^= id.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  // Two independent floats from the hash
  const fx = ((h >>> 0) % 1000) / 1000
  let h2 = h
  h2 = Math.imul(h2 ^ (h2 >>> 13), 2654435761)
  const fy = ((h2 >>> 0) % 1000) / 1000
  return { fx, fy }
}

/* ── Region count for tests ──────────────────────────────────── */
export function _regionCountFor(layout: { regions: readonly OrganicRegion[] }) {
  return layout.regions.length
}
