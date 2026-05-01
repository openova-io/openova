/**
 * FlowCanvasOrganic — recursive Job-tree canvas with d3-force layout.
 *
 * Two visual classes of node:
 *
 *   • Leaf install — circular bubble, family-coloured ring, status
 *     glyph, label below.
 *   • Group (parent) — same circular geometry but with a thicker
 *     family ring + a child-count badge ("12 jobs"). Folded groups
 *     show only the badge; unfolded groups still render alongside
 *     their children with a "parent-child" edge. Double-click on a
 *     group toggles its fold state via the consumer's onJobDoubleClick.
 *
 * Three highlight rings, in priority order (highest wins on the outer
 * stroke):
 *
 *   1. amber  `#FBBF24` — `openJobId` (the job whose log pane is open
 *                         right now)
 *   2. teal   `#14B8A6` — `hostJobId` (the page's *home* job — the
 *                         one in the URL; persistent across single-
 *                         click selections of other jobs)
 *   3. status — succeeded/running/failed/pending tone
 *
 * `openJobId` neighbours get a softer amber ring; everything else
 * fades to 35% opacity when any node is open. The host's teal ring
 * stays full opacity so the page's anchor is always findable.
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

const NODE_RADIUS = 22
const GROUP_RADIUS = 28
const COLLIDE_PADDING = 14
const MIN_VBOX_W = 1200
const MIN_VBOX_H = 700
const VIEW_H = 1100

/** Selection palette — distinct from any status colour AND distinct
 *  from each other so the host-vs-open semantic is unambiguous. */
const HOST_RING = '#14B8A6'        // teal — page owner
const SELECTION_RING = '#FBBF24'   // amber — currently-clicked-for-logs
const NEIGHBOR_RING = '#FCD34D'    // lighter amber — neighbour of selected

/* Sim node shape. */
type SimNode = SimulationNodeDatum & {
  id: string
  depth: number
  regionId: string
  familyId: string
  status: JobStatus
  isGroup: boolean
}

export interface FlowCanvasOrganicProps {
  layout: OrganicLayoutResult
  /** The job whose log pane is currently displayed (amber selection ring). */
  openJobId: string | null
  /** The page's "home" job — persistent teal ring across single-click selections. */
  hostJobId: string | null
  embedded?: boolean
  onJobClick: (jobId: string, event: ReactMouseEvent<SVGGElement>) => void
  onJobDoubleClick: (jobId: string) => void
  onCanvasBackgroundClick: () => void
}

export function FlowCanvasOrganic(props: FlowCanvasOrganicProps) {
  const {
    layout,
    openJobId,
    hostJobId,
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

  const REGION_BAND_H = NODE_RADIUS * 8
  const regionYMid = useMemo(() => {
    const map = new Map<string, number>()
    const regions = layout.regions
    if (regions.length === 0) return map
    regions.forEach((r, i) => {
      map.set(r.id, i * REGION_BAND_H + REGION_BAND_H / 2)
    })
    return map
  }, [layout.regions, REGION_BAND_H])

  const PER_DEPTH_X = NODE_RADIUS * 5
  const depthToX = useCallback(
    (depth: number) => depth * PER_DEPTH_X,
    [PER_DEPTH_X],
  )

  const familyById = useMemo(() => {
    const m = new Map<string, OrganicFamily>()
    for (const f of layout.families) m.set(f.id, f)
    return m
  }, [layout.families])

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
        existing.isGroup = n.isGroup
        next.push(existing)
      } else {
        const baseX = depthToX(n.depth)
        const baseY = regionYMid.get(n.regionId) ?? VIEW_H / 2
        const seed = hashSeed(n.id)
        const fresh: SimNode = {
          id: n.id,
          depth: n.depth,
          regionId: n.regionId,
          familyId: n.familyId,
          status: n.status,
          isGroup: n.isGroup,
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
          .radius((d) => (d.isGroup ? GROUP_RADIUS : NODE_RADIUS) + COLLIDE_PADDING)
          .strength(0.95)
          .iterations(2),
      )
      .force(
        'x',
        forceX<SimNode>()
          .x((d) => depthToX(d.depth))
          .strength(0.55),
      )
      .force(
        'y',
        forceY<SimNode>()
          .y((d) => {
            const base = regionYMid.get(d.regionId) ?? VIEW_H / 2
            const seed = hashSeed(d.id)
            return base + (seed.fy - 0.5) * 360
          })
          .strength(0.05),
      )
      .force(
        'link',
        forceLink<SimNode, SimulationLinkDatum<SimNode>>(links)
          .id((d) => d.id)
          .distance(NODE_RADIUS * 4)
          .strength(0.08),
      )
      .on('tick', () => setTick((t) => t + 1))

    simRef.current = sim
    return () => {
      sim.stop()
    }
  }, [simNodes, layout.edges, depthToX, regionYMid])

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
        // Pin where dropped — operator wants drag to stick.
      })

    const sel = select(svgRef.current).selectAll<SVGGElement, unknown>(
      'g[data-flow-draggable]',
    )
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(sel as any).call(dragBehavior)
  }, [nodeIdsKey])

  void tick

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

  const livePos = new Map<string, { x: number; y: number }>()
  for (const n of simNodes) {
    if (typeof n.x === 'number' && typeof n.y === 'number') {
      livePos.set(n.id, { x: n.x, y: n.y })
    }
  }

  const neighborIds = new Set<string>()
  if (openJobId) {
    for (const e of layout.edges) {
      if (e.fromId === openJobId) neighborIds.add(e.toId)
      else if (e.toId === openJobId) neighborIds.add(e.fromId)
    }
  }

  let bbMinX = Infinity, bbMinY = Infinity, bbMaxX = -Infinity, bbMaxY = -Infinity
  for (const p of livePos.values()) {
    if (p.x < bbMinX) bbMinX = p.x
    if (p.y < bbMinY) bbMinY = p.y
    if (p.x > bbMaxX) bbMaxX = p.x
    if (p.y > bbMaxY) bbMaxY = p.y
  }
  const PAD_X = NODE_RADIUS + 30
  const PAD_Y_TOP = NODE_RADIUS + 12
  const PAD_Y_BOTTOM = NODE_RADIUS + 40
  const naturalW = (bbMaxX - bbMinX) + PAD_X * 2
  const naturalH = (bbMaxY - bbMinY) + PAD_Y_TOP + PAD_Y_BOTTOM
  const vbW = Math.max(MIN_VBOX_W, naturalW)
  const vbH = Math.max(MIN_VBOX_H, naturalH)
  const cx = Number.isFinite(bbMinX) ? (bbMinX + bbMaxX) / 2 : vbW / 2
  const cy = Number.isFinite(bbMinY) ? (bbMinY + bbMaxY) / 2 : vbH / 2
  const vbX = cx - vbW / 2
  const vbY = cy - vbH / 2

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
        <marker
          id="flow-org-arrow-highlight"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M0,1 L9,5 L0,9 Z" fill={SELECTION_RING} opacity="1" />
        </marker>
        <marker
          id="flow-org-arrow-host"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M0,1 L9,5 L0,9 Z" fill={HOST_RING} opacity="1" />
        </marker>
      </defs>

      {layout.edges.map((e) => {
        const s = livePos.get(e.fromId)
        const t = livePos.get(e.toId)
        if (!s || !t) return null
        const onSelectionPath =
          openJobId !== null && (e.fromId === openJobId || e.toId === openJobId)
        const onHostPath =
          hostJobId !== null && !onSelectionPath && (e.fromId === hostJobId || e.toId === hostJobId)
        return (
          <FlowEdge
            key={`${e.fromId}-${e.toId}-${e.kind}`}
            from={s}
            to={t}
            status={e.fromStatus}
            kind={e.kind}
            highlighted={onSelectionPath ? 'selection' : onHostPath ? 'host' : 'none'}
          />
        )
      })}

      {layout.nodes.map((node) => {
        const pos = livePos.get(node.id)
        if (!pos) return null
        const family = familyById.get(node.familyId) ?? null
        const isNeighbor = neighborIds.has(node.id)
        const isOpen = openJobId === node.id
        const isHost = hostJobId === node.id
        return (
          <FlowNode
            key={node.id}
            node={node}
            x={pos.x}
            y={pos.y}
            family={family}
            isOpen={isOpen}
            isHost={isHost}
            isNeighbor={isNeighbor}
            isDimmed={openJobId !== null && !isNeighbor && !isOpen && !isHost}
            onClick={(e) => onJobClick(node.id, e)}
            onDoubleClick={() => onJobDoubleClick(node.id)}
          />
        )
      })}
    </svg>
  )
}

/* ── FlowEdge — straight line, rim-to-rim, with arrowhead ──────── */

interface FlowEdgeProps {
  from: { x: number; y: number }
  to: { x: number; y: number }
  status: JobStatus
  kind: 'depends-on' | 'parent-child'
  highlighted: 'none' | 'selection' | 'host'
}

function FlowEdge({ from, to, status, kind, highlighted }: FlowEdgeProps) {
  const tone = STATUS_TONE[status]
  const dx = to.x - from.x
  const dy = to.y - from.y
  const len = Math.hypot(dx, dy) || 1
  const trim = NODE_RADIUS + 6
  const fx = from.x + (dx / len) * NODE_RADIUS
  const fy = from.y + (dy / len) * NODE_RADIUS
  const tx = to.x - (dx / len) * trim
  const ty = to.y - (dy / len) * trim

  const stroke =
    highlighted === 'selection' ? SELECTION_RING : highlighted === 'host' ? HOST_RING : tone.edge
  const opacity = highlighted !== 'none' ? 1 : kind === 'parent-child' ? 0.4 : 0.7
  const width = highlighted !== 'none' ? 2.6 : kind === 'parent-child' ? 1.0 : 1.4
  const marker =
    highlighted === 'selection'
      ? 'flow-org-arrow-highlight'
      : highlighted === 'host'
        ? 'flow-org-arrow-host'
        : `flow-org-arrow-${status}`
  const dashArray = kind === 'parent-child' && highlighted === 'none' ? '4 3' : undefined

  return (
    <line
      x1={fx.toFixed(1)}
      y1={fy.toFixed(1)}
      x2={tx.toFixed(1)}
      y2={ty.toFixed(1)}
      stroke={stroke}
      strokeWidth={width}
      strokeDasharray={dashArray}
      markerEnd={`url(#${marker})`}
      opacity={opacity}
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
  isHost: boolean
  isNeighbor: boolean
  isDimmed: boolean
  onClick: (e: ReactMouseEvent<SVGGElement>) => void
  onDoubleClick: () => void
}

function FlowNode({
  node,
  x,
  y,
  family,
  isOpen,
  isHost,
  isNeighbor,
  isDimmed,
  onClick,
  onDoubleClick,
}: FlowNodeProps) {
  const tone = STATUS_TONE[node.status]
  // Outer ring priority: selection (amber) > host (teal) > neighbour > status tone.
  const outerRing = isOpen
    ? SELECTION_RING
    : isHost
      ? HOST_RING
      : isNeighbor
        ? NEIGHBOR_RING
        : tone.ring
  const familyColor = family?.color ?? 'rgba(148,163,184,0.55)'
  const radius = node.isGroup ? GROUP_RADIUS : NODE_RADIUS
  const grpStyle: CSSProperties = { cursor: 'grab' }
  const groupOpacity = isDimmed ? 0.35 : 1
  const ringWidth = isOpen ? 4 : isHost ? 3.5 : isNeighbor ? 3 : 2

  return (
    <g
      data-testid={`flow-job-${node.id}`}
      data-flow-draggable=""
      data-job-id={node.id}
      data-status={node.status}
      data-region={node.regionId}
      data-family={node.familyId}
      data-kind={node.isGroup ? 'group' : 'leaf'}
      data-folded={node.isFolded ? 'true' : 'false'}
      data-open={isOpen ? 'true' : 'false'}
      data-host={isHost ? 'true' : 'false'}
      data-neighbor={isNeighbor ? 'true' : 'false'}
      data-dimmed={isDimmed ? 'true' : 'false'}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      style={grpStyle}
      transform={`translate(${x.toFixed(1)}, ${y.toFixed(1)})`}
      opacity={groupOpacity}
    >
      <title>
        {`${node.label} — ${tone.label}${node.subLabel ? ` · ${node.subLabel}` : ''}`}
      </title>

      {/* Glow underlay — strongest on selection, then host. */}
      {isOpen ? (
        <circle r={radius + 10} fill="rgba(251,191,36,0.30)" />
      ) : isHost ? (
        <circle r={radius + 10} fill="rgba(20,184,166,0.30)" />
      ) : isNeighbor ? (
        <circle r={radius + 8} fill="rgba(252,211,77,0.18)" />
      ) : node.status === 'running' || node.status === 'failed' ? (
        <circle r={radius + 8} fill={tone.glow} />
      ) : null}

      {/* Family-coloured ring (thin) */}
      <circle
        r={radius + 2}
        fill="none"
        stroke={familyColor}
        strokeWidth={node.isGroup ? 2.5 : 1}
        opacity={0.55}
      />

      {/* Status fill + selection/host/neighbor ring overlay */}
      <circle
        r={radius}
        fill={tone.fill}
        stroke={outerRing}
        strokeWidth={ringWidth}
      />

      {/* Status glyph or child-count badge */}
      {node.isFolded ? (
        <text
          x={0}
          y={6}
          textAnchor="middle"
          fontSize={node.childCount > 99 ? 14 : 18}
          fontWeight={700}
          fill={tone.glyph}
          fontFamily="ui-sans-serif, system-ui, sans-serif"
          pointerEvents="none"
        >
          {node.childCount}
        </text>
      ) : (
        <text
          x={0}
          y={6}
          textAnchor="middle"
          fontSize={node.isGroup ? 16 : 18}
          fontWeight={700}
          fill={tone.glyph}
          fontFamily="ui-sans-serif, system-ui, sans-serif"
          pointerEvents="none"
        >
          {node.isGroup ? '◇' : glyphFor(node.status)}
        </text>
      )}

      {/* Label below bubble */}
      <text
        x={0}
        y={radius + 14}
        textAnchor="middle"
        fontSize={10}
        fill="rgba(255,255,255,0.85)"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
        pointerEvents="none"
      >
        {node.label.length > 18 ? node.label.slice(0, 17) + '…' : node.label}
      </text>

      {/* Sub-label (duration / "n jobs" for folded groups) */}
      {node.subLabel ? (
        <text
          x={0}
          y={radius + 26}
          textAnchor="middle"
          fontSize={8}
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
