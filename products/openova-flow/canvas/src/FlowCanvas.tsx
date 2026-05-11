/**
 * @openova/flow-canvas — props-driven SVG canvas.
 *
 * Receives:
 *   • A FlowInstance + FlowNode[] + Relationship[] triple from the
 *     host (typically piped through `layout()` in @openova/flow-core).
 *   • A status palette, family + region descriptors, and event
 *     handlers from the host (typically supplied by the adapter).
 *
 * Renders:
 *   • One SVG <g> per visible node, positioned by a d3-force
 *     simulation anchored on per-depth columns + per-bucket Y rank.
 *   • One SVG edge per Relationship (excluding `contains`), styled
 *     by relType per the founder-locked table:
 *
 *       FS       — solid stroke, normal arrow, counted for depth
 *       SS       — solid stroke + "↓" tag at origin
 *       FF       — solid stroke + "↓" tag at terminus
 *       SF       — dashed stroke
 *       triggers — solid stroke + lightning glyph at midpoint
 *       on-failure — red dashed, low opacity until source 'failed'
 *
 *   • Three highlight rings:
 *       1. amber (`--flow-selection-ring`) — current selected node
 *       2. teal  (`--flow-host-ring`)      — page's host node
 *       3. status tone                     — default
 *
 * ZERO OpenOva imports: no `@/entities`, no Catalyst Job type, no
 * router. Everything is supplied by the caller via props.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, MouseEvent as ReactMouseEvent, ReactNode } from 'react'
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
  layout as computeLayout,
  type FlowInstance,
  type FlowNode,
  type Relationship,
  type RelationshipType,
  type StatusTone,
  type FamilyDescriptor,
  type RegionDescriptor,
  type LayoutHints,
  type LayoutOutput,
  type PositionedNode,
  type PositionedEdge,
} from '@openova/flow-core'

/* ─── Layout constants ────────────────────────────────────────────── */

const MIN_NODE_RADIUS = 16
const MAX_NODE_RADIUS = 40
const GROUP_RADIUS_DELTA = 8
const COLLIDE_PADDING = 12
const MIN_HOST_W = 1200
const MIN_HOST_H = 700
const MIN_PER_DEPTH_X = 110
const MAX_PER_DEPTH_X = 200
const FORCE_X_STRENGTH = 0.18
const FORCE_Y_STRENGTH = 0.22
const FORCE_LINK_STRENGTH = 0.18
const MAX_TICKS = 120

/* ─── Sim node type ───────────────────────────────────────────────── */

type SimNode = SimulationNodeDatum & {
  id: string
  depth: number
  depRank: number
  region: string
  family: string
  status: string
  isGroup: boolean
}

/* ─── Default status palette (used when adapter doesn't supply one) ── */

const DEFAULT_PALETTE: Record<string, StatusTone> = {
  pending: {
    fill: 'var(--flow-bubble-fill-pending)',
    ring: 'var(--flow-bubble-ring-pending)',
    glyph: 'var(--flow-bubble-glyph-pending)',
    glow: 'var(--flow-bubble-glow-pending)',
    edge: 'var(--flow-bubble-edge-pending)',
    arrow: '#94A3B8',
    label: 'Pending',
  },
  running: {
    fill: 'var(--flow-bubble-fill-running)',
    ring: 'var(--flow-bubble-ring-running)',
    glyph: 'var(--flow-bubble-glyph-running)',
    glow: 'var(--flow-bubble-glow-running)',
    edge: 'var(--flow-bubble-edge-running)',
    arrow: '#38BDF8',
    label: 'Running',
  },
  succeeded: {
    fill: 'var(--flow-bubble-fill-succeeded)',
    ring: 'var(--flow-bubble-ring-succeeded)',
    glyph: 'var(--flow-bubble-glyph-succeeded)',
    glow: 'var(--flow-bubble-glow-succeeded)',
    edge: 'var(--flow-bubble-edge-succeeded)',
    arrow: '#16A34A',
    label: 'Succeeded',
  },
  failed: {
    fill: 'var(--flow-bubble-fill-failed)',
    ring: 'var(--flow-bubble-ring-failed)',
    glyph: 'var(--flow-bubble-glyph-failed)',
    glow: 'var(--flow-bubble-glow-failed)',
    edge: 'var(--flow-bubble-edge-failed)',
    arrow: '#B91C1C',
    label: 'Failed',
  },
}

function toneFor(palette: Record<string, StatusTone>, status: string): StatusTone {
  return palette[status] ?? DEFAULT_PALETTE[status] ?? DEFAULT_PALETTE.pending
}

/* ─── Props ───────────────────────────────────────────────────────── */

export interface FlowCanvasProps {
  flow: FlowInstance
  nodes: FlowNode[]
  relationships: Relationship[]
  folded: Set<string>
  selectedNodeId?: string | null
  hostNodeId?: string | null
  palette?: Record<string, StatusTone>
  families?: FamilyDescriptor[]
  regions?: RegionDescriptor[]
  /** Per-node hints (region / family / extraDepIds). */
  perNodeHints?: ReadonlyMap<string, { region?: string; family?: string; extraDepIds?: string[] }>
  onNodeOpen?: (nodeId: string) => void
  onNodeNavigate?: (nodeId: string) => void
  onFoldToggle?: (groupId: string) => void
  onBackgroundClick?: () => void
  renderDetail?: (nodeId: string) => ReactNode
  /** Test seam: pre-computed layout (skips the internal `computeLayout`
   *  call). Used in tests + adapters that already have a layout. */
  precomputedLayout?: LayoutOutput
}

/* ─── Component ───────────────────────────────────────────────────── */

export function FlowCanvas(props: FlowCanvasProps) {
  const {
    flow,
    nodes,
    relationships,
    folded,
    selectedNodeId = null,
    hostNodeId = null,
    palette = DEFAULT_PALETTE,
    families,
    regions,
    perNodeHints,
    onNodeOpen,
    onNodeNavigate,
    onFoldToggle,
    onBackgroundClick,
    precomputedLayout,
  } = props

  const hostRef = useRef<HTMLDivElement | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const simRef = useRef<Simulation<SimNode, SimulationLinkDatum<SimNode>> | null>(null)
  const nodesRef = useRef<Map<string, SimNode>>(new Map())
  const tickCountRef = useRef<number>(0)
  const [, setTick] = useState(0)
  const [hostSize, setHostSize] = useState<{ w: number; h: number }>({
    w: MIN_HOST_W,
    h: MIN_HOST_H,
  })

  /* ── Compute (or accept) the layout ───────────────────────────── */

  const layoutOut: LayoutOutput = useMemo(() => {
    if (precomputedLayout) return precomputedLayout
    const hints: LayoutHints = {
      perNode: perNodeHints,
      families,
      regions,
    }
    return computeLayout({
      flow,
      nodes,
      relationships,
      folded,
      hints,
    })
  }, [precomputedLayout, flow, nodes, relationships, folded, perNodeHints, families, regions])

  /* ── ResizeObserver — debounced + epsilon-gated ───────────────── */

  useEffect(() => {
    const el = hostRef.current
    if (!el) return
    let timer: ReturnType<typeof setTimeout> | null = null
    let pending: { w: number; h: number } | null = null
    const RESIZE_DEBOUNCE_MS = 60
    const RESIZE_EPSILON_PX = 4
    const ro = new ResizeObserver((entries) => {
      const e = entries[0]
      if (!e) return
      const rect = e.contentRect
      const w = Math.round(rect.width) || MIN_HOST_W
      const h = Math.round(rect.height) || MIN_HOST_H
      pending = { w, h }
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => {
        if (!pending) return
        const next = pending
        pending = null
        setHostSize((prev) => {
          if (
            Math.abs(prev.w - next.w) < RESIZE_EPSILON_PX &&
            Math.abs(prev.h - next.h) < RESIZE_EPSILON_PX
          ) {
            return prev
          }
          return next
        })
      }, RESIZE_DEBOUNCE_MS)
    })
    ro.observe(el)
    return () => {
      if (timer) clearTimeout(timer)
      ro.disconnect()
    }
  }, [])

  /* ── Layout metrics (R, per-depth slots) ─────────────────────── */

  const layoutMetrics = useMemo(() => {
    const buckets = new Map<number, number>()
    let maxBucket = 0
    let maxDepth = 0
    for (const n of layoutOut.positionedNodes) {
      const c = (buckets.get(n.depth) ?? 0) + 1
      buckets.set(n.depth, c)
      if (c > maxBucket) maxBucket = c
      if (n.depth > maxDepth) maxDepth = n.depth
    }
    const depthCount = maxDepth + 1
    const targetRowsFor = (count: number) =>
      Math.max(1, Math.round(Math.sqrt(Math.max(1, count) / 1.6)))
    let r = MAX_NODE_RADIUS
    if (maxBucket > 1) {
      const usableH = Math.max(60, hostSize.h - 2 * (MAX_NODE_RADIUS + COLLIDE_PADDING))
      const denseRows = targetRowsFor(maxBucket)
      const pitchAvail = usableH / denseRows
      const rFit = (pitchAvail - COLLIDE_PADDING) / 2
      r = Math.min(MAX_NODE_RADIUS, Math.max(MIN_NODE_RADIUS, Math.floor(rFit)))
    }
    r = Math.max(MIN_NODE_RADIUS, Math.min(MAX_NODE_RADIUS, Math.round(r / 4) * 4))
    const ROW_PITCH = r * 2 + COLLIDE_PADDING
    const Y_RANGE = Math.max(r * 2, hostSize.h - 2 * (r + COLLIDE_PADDING))
    const hardRowCap = Math.max(1, Math.floor(Y_RANGE / ROW_PITCH))
    const slotInfo = new Map<number, { cols: number; rows: number; width: number }>()
    for (const [d, count] of buckets) {
      const baseRows = targetRowsFor(count)
      const rows = Math.min(Math.max(1, baseRows), hardRowCap, count)
      const cols = Math.max(1, Math.ceil(count / rows))
      const naturalW = cols > 1
        ? (cols - 1) * (r * 2 + COLLIDE_PADDING) + r * 2
        : r * 2
      const width = Math.round(Math.max(naturalW, r * 2) / 8) * 8
      slotInfo.set(d, { cols, rows, width })
    }
    const gap = Math.max(MIN_PER_DEPTH_X, Math.min(MAX_PER_DEPTH_X, r * 4))
    const xByDepth = new Map<number, number>()
    let cursor = r + COLLIDE_PADDING
    for (let d = 0; d <= maxDepth; d++) {
      const w = slotInfo.get(d)?.width ?? r * 2
      xByDepth.set(d, cursor + w / 2)
      cursor += w + gap
    }
    const totalWidth = cursor - gap + r + COLLIDE_PADDING
    const linkDistance = gap * 0.625
    const gr = r + GROUP_RADIUS_DELTA
    return { r, gr, gap, slotInfo, xByDepth, totalWidth, linkDistance, depthCount }
  }, [layoutOut.positionedNodes, hostSize.w, hostSize.h])

  const R = layoutMetrics.r
  const GR = layoutMetrics.gr
  const PER_DEPTH_X = layoutMetrics.gap
  const LINK_DISTANCE = layoutMetrics.linkDistance

  const depthToX = useCallback(
    (depth: number) =>
      layoutMetrics.xByDepth.get(depth) ?? R + COLLIDE_PADDING + depth * (R * 4),
    [layoutMetrics, R],
  )

  /* ── Per-bucket Y rank ─────────────────────────────────────── */

  const Y_CENTER = hostSize.h / 2
  const PITCH = R * 2 + COLLIDE_PADDING
  const ZIGZAG_AMPLITUDE = Math.min(R * 3, hostSize.h * 0.12)

  const bucketRank = useMemo(() => {
    const rank = new Map<string, { idx: number; size: number }>()
    const seen = new Map<number, PositionedNode[]>()
    for (const n of layoutOut.positionedNodes) {
      let arr = seen.get(n.depth)
      if (!arr) {
        arr = []
        seen.set(n.depth, arr)
      }
      arr.push(n)
    }
    for (const arr of seen.values()) {
      arr.sort((a, b) => a.depRank - b.depRank)
      arr.forEach((n, i) => rank.set(n.id, { idx: i, size: arr.length }))
    }
    return rank
  }, [layoutOut.positionedNodes])

  const depthByNodeId = useMemo(() => {
    const m = new Map<string, number>()
    for (const n of layoutOut.positionedNodes) m.set(n.id, n.depth)
    return m
  }, [layoutOut.positionedNodes])

  const yForBucket = useCallback(
    (id: string) => {
      const b = bucketRank.get(id)
      if (!b) return Y_CENTER
      if (b.size === 1) {
        const d = depthByNodeId.get(id) ?? 0
        const sign = d % 2 === 0 ? -1 : 1
        return Y_CENTER + sign * ZIGZAG_AMPLITUDE
      }
      return Y_CENTER + (b.idx - (b.size - 1) / 2) * PITCH
    },
    [bucketRank, Y_CENTER, PITCH, ZIGZAG_AMPLITUDE, depthByNodeId],
  )

  const familyById = useMemo(() => {
    const m = new Map<string, FamilyDescriptor>()
    for (const f of layoutOut.families) m.set(f.id, f)
    return m
  }, [layoutOut.families])

  /* ── SimNodes ───────────────────────────────────────────────── */

  const simNodes = useMemo<SimNode[]>(() => {
    const next: SimNode[] = []
    const seen = new Set<string>()
    for (const n of layoutOut.positionedNodes) {
      seen.add(n.id)
      const existing = nodesRef.current.get(n.id)
      if (existing) {
        existing.depth = n.depth
        existing.depRank = n.depRank
        existing.region = n.region
        existing.family = n.family
        existing.status = n.status
        existing.isGroup = n.isGroup
        next.push(existing)
      } else {
        const seed = hashSeed(n.id)
        const initX = depthToX(n.depth) + (seed.fx - 0.5) * R * 1.5
        const initY = yForBucket(n.id) + (seed.fy - 0.5) * R * 0.6
        const fresh: SimNode = {
          id: n.id,
          depth: n.depth,
          depRank: n.depRank,
          region: n.region,
          family: n.family,
          status: n.status,
          isGroup: n.isGroup,
          x: initX,
          y: initY,
        }
        nodesRef.current.set(n.id, fresh)
        next.push(fresh)
      }
    }
    for (const id of Array.from(nodesRef.current.keys())) {
      if (!seen.has(id)) nodesRef.current.delete(id)
    }
    return next
  }, [layoutOut.positionedNodes, depthToX, yForBucket, R])

  /* ── d3-force simulation ─────────────────────────────────────── */

  useEffect(() => {
    if (simNodes.length === 0) {
      simRef.current?.stop()
      simRef.current = null
      return
    }
    const links: SimulationLinkDatum<SimNode>[] = []
    for (const e of layoutOut.edges) {
      const s = nodesRef.current.get(e.fromId)
      const t = nodesRef.current.get(e.toId)
      if (s && t) links.push({ source: s, target: t })
    }
    tickCountRef.current = 0
    for (const n of simNodes) {
      if (typeof n.fx === 'number' && typeof n.fy === 'number') continue
      const seed = hashSeed(n.id)
      n.x = depthToX(n.depth) + (seed.fx - 0.5) * R * 1.5
      n.y = yForBucket(n.id) + (seed.fy - 0.5) * R * 0.6
      n.vx = 0
      n.vy = 0
    }
    const sim = forceSimulation<SimNode>(simNodes)
      .alpha(0.9)
      .alphaDecay(0.06)
      .alphaMin(0.01)
      .velocityDecay(0.3)
      .force(
        'collide',
        forceCollide<SimNode>()
          .radius((d) => (d.isGroup ? GR : R) + COLLIDE_PADDING)
          .strength(0.95)
          .iterations(2),
      )
      .force('x', forceX<SimNode>().x((d) => depthToX(d.depth)).strength(FORCE_X_STRENGTH))
      .force('y', forceY<SimNode>().y((d) => yForBucket(d.id)).strength(FORCE_Y_STRENGTH))
      .force(
        'link',
        forceLink<SimNode, SimulationLinkDatum<SimNode>>(links)
          .id((d) => d.id)
          .distance(LINK_DISTANCE)
          .strength(FORCE_LINK_STRENGTH),
      )
      .on('tick', () => {
        for (const n of simNodes) {
          if (typeof n.fx === 'number' && typeof n.fy === 'number') continue
          const baseX = depthToX(n.depth)
          const xMin = Math.max(R, baseX - PER_DEPTH_X)
          const xMax = Math.min(layoutMetrics.totalWidth - R, baseX + PER_DEPTH_X)
          if (typeof n.x === 'number') {
            if (n.x < xMin) n.x = xMin
            else if (n.x > xMax) n.x = xMax
          }
          const targetY = yForBucket(n.id)
          const Y_HALF_BAND = R * 2 + COLLIDE_PADDING
          const yMin = Math.max(R, targetY - Y_HALF_BAND)
          const yMax = Math.min(hostSize.h - R, targetY + Y_HALF_BAND)
          if (typeof n.y === 'number') {
            if (n.y < yMin) n.y = yMin
            else if (n.y > yMax) n.y = yMax
          }
        }
        tickCountRef.current++
        if (tickCountRef.current >= MAX_TICKS) {
          sim.stop()
        }
        setTick((t) => t + 1)
      })
    simRef.current = sim
    return () => {
      sim.stop()
    }
  }, [simNodes, layoutOut.edges, depthToX, yForBucket, hostSize.h, R, GR, PER_DEPTH_X, LINK_DISTANCE, layoutMetrics.totalWidth])

  /* ── Drag wiring ────────────────────────────────────────────── */

  const nodeIdsKey = simNodes.map((n) => n.id).join(',')
  useEffect(() => {
    if (!svgRef.current) return
    const sim = simRef.current
    if (!sim) return
    const dragBehavior = d3drag<SVGGElement, unknown>()
      .on('start', function (event) {
        tickCountRef.current = 0
        if (!event.active) sim.alphaTarget(0.3).restart()
        const id = (this as SVGGElement).getAttribute('data-node-id')
        const d = id ? nodesRef.current.get(id) : null
        if (d) {
          d.fx = d.x ?? 0
          d.fy = d.y ?? 0
        }
      })
      .on('drag', function (event) {
        const id = (this as SVGGElement).getAttribute('data-node-id')
        const d = id ? nodesRef.current.get(id) : null
        if (d) {
          d.fx = event.x
          d.fy = event.y
        }
      })
      .on('end', function (event) {
        if (!event.active) sim.alphaTarget(0)
      })
    const sel = select(svgRef.current).selectAll<SVGGElement, unknown>('g[data-flow-draggable]')
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    ;(sel as any).call(dragBehavior)
  }, [nodeIdsKey])

  /* ── Click handlers (single vs double, 220ms debounce) ──────── */

  const clickTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelPendingClick = useCallback(() => {
    if (clickTimerRef.current) {
      clearTimeout(clickTimerRef.current)
      clickTimerRef.current = null
    }
  }, [])
  useEffect(() => () => cancelPendingClick(), [cancelPendingClick])

  const handleNodeClick = useCallback(
    (nodeId: string, _event: ReactMouseEvent<SVGGElement>) => {
      cancelPendingClick()
      clickTimerRef.current = setTimeout(() => {
        onNodeOpen?.(nodeId)
        clickTimerRef.current = null
      }, 220)
    },
    [cancelPendingClick, onNodeOpen],
  )

  const handleNodeDoubleClick = useCallback(
    (nodeId: string) => {
      cancelPendingClick()
      const positioned = layoutOut.positionedNodes.find((n) => n.id === nodeId)
      if (positioned?.isGroup) {
        onFoldToggle?.(nodeId)
        return
      }
      onNodeNavigate?.(nodeId)
    },
    [cancelPendingClick, layoutOut.positionedNodes, onFoldToggle, onNodeNavigate],
  )

  const handleBackgroundClick = useCallback(() => {
    cancelPendingClick()
    onBackgroundClick?.()
  }, [cancelPendingClick, onBackgroundClick])

  /* ── Render ────────────────────────────────────────────────── */

  if (layoutOut.positionedNodes.length === 0) {
    return (
      <div
        ref={hostRef}
        className="flow-canvas-host"
        data-testid="flow-canvas-empty"
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          padding: '2rem',
          fontSize: '0.875rem',
          color: 'var(--flow-bubble-sublabel)',
        }}
      >
        No nodes to render.
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
  if (selectedNodeId) {
    for (const e of layoutOut.edges) {
      if (e.fromId === selectedNodeId) neighborIds.add(e.toId)
      else if (e.toId === selectedNodeId) neighborIds.add(e.fromId)
    }
  }

  const vbW = Math.max(hostSize.w, layoutMetrics.totalWidth)
  const vbH = hostSize.h
  const CLAMP_INSET = R + 8
  const project = (p: { x: number; y: number }) => ({
    x: Math.min(vbW - CLAMP_INSET, Math.max(CLAMP_INSET, p.x)),
    y: Math.min(vbH - CLAMP_INSET, Math.max(CLAMP_INSET, p.y)),
  })
  const renderPos = new Map<string, { x: number; y: number }>()
  for (const [id, p] of livePos) renderPos.set(id, project(p))

  return (
    <div
      ref={hostRef}
      className="flow-canvas-host"
      data-testid="flow-canvas-host"
      style={{
        overflowX: layoutMetrics.totalWidth > hostSize.w ? 'auto' : 'hidden',
        overflowY: 'hidden',
      }}
    >
      <svg
        ref={svgRef}
        width={vbW}
        height="100%"
        viewBox={`0 0 ${vbW} ${vbH}`}
        preserveAspectRatio="xMinYMin meet"
        className="flow-canvas-svg"
        data-testid="flow-canvas-svg"
        role="img"
        aria-label={`Flow ${flow.id}`}
        style={{ display: 'block', minWidth: '100%', height: '100%' }}
        onClick={(e) => {
          if (e.target === e.currentTarget) handleBackgroundClick()
        }}
      >
        <FlowArrowDefs palette={palette} />
        {layoutOut.edges.map((e) => {
          const s = renderPos.get(e.fromId)
          const t = renderPos.get(e.toId)
          if (!s || !t) return null
          const onSelectionPath =
            selectedNodeId !== null && (e.fromId === selectedNodeId || e.toId === selectedNodeId)
          const onHostPath =
            hostNodeId !== null && !onSelectionPath && (e.fromId === hostNodeId || e.toId === hostNodeId)
          return (
            <FlowEdge
              key={`${e.fromId}-${e.toId}-${e.relType}`}
              edge={e}
              from={s}
              to={t}
              palette={palette}
              highlighted={onSelectionPath ? 'selection' : onHostPath ? 'host' : 'none'}
              r={R}
            />
          )
        })}
        {layoutOut.positionedNodes.map((node) => {
          const pos = renderPos.get(node.id)
          if (!pos) return null
          const family = familyById.get(node.family) ?? null
          const isNeighbor = neighborIds.has(node.id)
          const isOpen = selectedNodeId === node.id
          const isHost = hostNodeId === node.id
          return (
            <FlowNodeView
              key={node.id}
              node={node}
              x={pos.x}
              y={pos.y}
              family={family}
              palette={palette}
              isOpen={isOpen}
              isHost={isHost}
              isNeighbor={isNeighbor}
              isDimmed={selectedNodeId !== null && !isNeighbor && !isOpen && !isHost}
              onClick={(e) => handleNodeClick(node.id, e)}
              onDoubleClick={() => handleNodeDoubleClick(node.id)}
              r={R}
              gr={GR}
            />
          )
        })}
      </svg>
    </div>
  )
}

/* ─── Edge defs (SVG <marker> for arrowheads) ────────────────────── */

function FlowArrowDefs({ palette }: { palette: Record<string, StatusTone> }) {
  const statuses = Object.keys({ ...DEFAULT_PALETTE, ...palette })
  return (
    <defs>
      {statuses.map((s) => {
        const tone = toneFor(palette, s)
        return (
          <marker
            key={s}
            id={`flow-arrow-${cssId(s)}`}
            viewBox="0 0 10 10"
            refX="9"
            refY="5"
            markerWidth="6"
            markerHeight="6"
            orient="auto-start-reverse"
          >
            <path d="M0,1 L9,5 L0,9 Z" fill={tone.arrow} opacity="0.92" />
          </marker>
        )
      })}
      <marker
        id="flow-arrow-host"
        viewBox="0 0 10 10"
        refX="9"
        refY="5"
        markerWidth="7"
        markerHeight="7"
        orient="auto-start-reverse"
      >
        <path d="M0,1 L9,5 L0,9 Z" fill="var(--flow-host-ring, #14B8A6)" opacity="1" />
      </marker>
      <marker
        id="flow-arrow-selection"
        viewBox="0 0 10 10"
        refX="9"
        refY="5"
        markerWidth="7"
        markerHeight="7"
        orient="auto-start-reverse"
      >
        <path d="M0,1 L9,5 L0,9 Z" fill="var(--flow-selection-ring, #FBBF24)" opacity="1" />
      </marker>
    </defs>
  )
}

function cssId(s: string): string {
  return s.replace(/[^a-zA-Z0-9_-]/g, '-')
}

/* ─── FlowEdge ────────────────────────────────────────────────────── */

interface FlowEdgeProps {
  edge: PositionedEdge
  from: { x: number; y: number }
  to: { x: number; y: number }
  palette: Record<string, StatusTone>
  highlighted: 'none' | 'selection' | 'host'
  r: number
}

function FlowEdge({ edge, from, to, palette, highlighted, r }: FlowEdgeProps) {
  const tone = toneFor(palette, edge.fromStatus)
  const dx = to.x - from.x
  const dy = to.y - from.y
  const len = Math.hypot(dx, dy) || 1
  const trim = r + 6
  const fx = from.x + (dx / len) * r
  const fy = from.y + (dy / len) * r
  const tx = to.x - (dx / len) * trim
  const ty = to.y - (dy / len) * trim

  /* Style table from the spec:
   *   FS       — solid, normal arrow
   *   SS       — solid, "↓" inline tag near origin (handled as glyph below)
   *   FF       — solid, "↓" inline tag near terminus
   *   SF       — dashed
   *   triggers — solid + lightning glyph at midpoint
   *   on-failure — red dashed, low opacity until source is 'failed'
   */
  const isFailure = edge.condition === 'on-failure'
  const sourceFailed = edge.fromStatus === 'failed'
  const baseStroke =
    highlighted === 'selection'
      ? 'var(--flow-selection-ring, #FBBF24)'
      : highlighted === 'host'
        ? 'var(--flow-host-ring, #14B8A6)'
        : isFailure
          ? 'var(--flow-bubble-edge-failed, #F87171)'
          : tone.edge
  const opacity =
    highlighted !== 'none'
      ? 1
      : isFailure
        ? sourceFailed
          ? 0.85
          : 0.25
        : 0.7
  const width = highlighted !== 'none' ? 2.6 : 1.4
  const marker =
    highlighted === 'selection'
      ? 'flow-arrow-selection'
      : highlighted === 'host'
        ? 'flow-arrow-host'
        : `flow-arrow-${cssId(edge.fromStatus)}`
  const dashArray =
    isFailure || edge.relType === 'start-to-finish' ? '4 3' : undefined

  // Midpoint annotations for SS/FF/triggers.
  const midX = (fx + tx) / 2
  const midY = (fy + ty) / 2
  const annotation = (() => {
    if (highlighted !== 'none') return null
    if (edge.relType === 'start-to-start') {
      // Near origin.
      const ox = fx + (tx - fx) * 0.18
      const oy = fy + (ty - fy) * 0.18
      return (
        <text
          x={ox}
          y={oy}
          fontSize={10}
          fill={baseStroke}
          pointerEvents="none"
          textAnchor="middle"
          fontFamily="ui-monospace, monospace"
        >
          SS
        </text>
      )
    }
    if (edge.relType === 'finish-to-finish') {
      // Near terminus.
      const ox = fx + (tx - fx) * 0.82
      const oy = fy + (ty - fy) * 0.82
      return (
        <text
          x={ox}
          y={oy}
          fontSize={10}
          fill={baseStroke}
          pointerEvents="none"
          textAnchor="middle"
          fontFamily="ui-monospace, monospace"
        >
          FF
        </text>
      )
    }
    if (edge.relType === 'triggers') {
      return (
        <text
          x={midX}
          y={midY - 4}
          fontSize={12}
          fill={baseStroke}
          pointerEvents="none"
          textAnchor="middle"
        >
          ⚡
        </text>
      )
    }
    if (edge.relType === 'start-to-finish') {
      return (
        <text
          x={midX}
          y={midY - 4}
          fontSize={10}
          fill={baseStroke}
          pointerEvents="none"
          textAnchor="middle"
          fontFamily="ui-monospace, monospace"
        >
          SF
        </text>
      )
    }
    return null
  })()

  return (
    <g data-flow-edge="" data-rel-type={edge.relType} data-condition={edge.condition}>
      <line
        x1={fx.toFixed(1)}
        y1={fy.toFixed(1)}
        x2={tx.toFixed(1)}
        y2={ty.toFixed(1)}
        stroke={baseStroke}
        strokeWidth={width}
        strokeDasharray={dashArray}
        markerEnd={`url(#${marker})`}
        opacity={opacity}
      />
      {annotation}
    </g>
  )
}

/* ─── FlowNodeView ────────────────────────────────────────────────── */

interface FlowNodeViewProps {
  node: PositionedNode
  x: number
  y: number
  family: FamilyDescriptor | null
  palette: Record<string, StatusTone>
  isOpen: boolean
  isHost: boolean
  isNeighbor: boolean
  isDimmed: boolean
  onClick: (e: ReactMouseEvent<SVGGElement>) => void
  onDoubleClick: () => void
  r: number
  gr: number
}

function FlowNodeView({
  node,
  x,
  y,
  family,
  palette,
  isOpen,
  isHost,
  isNeighbor,
  isDimmed,
  onClick,
  onDoubleClick,
  r,
  gr,
}: FlowNodeViewProps) {
  const tone = toneFor(palette, node.status)
  const innerRing = isOpen
    ? 'var(--flow-selection-ring, #FBBF24)'
    : isNeighbor
      ? 'var(--flow-neighbor-ring, #FCD34D)'
      : isHost
        ? 'var(--flow-host-ring, #14B8A6)'
        : tone.ring
  const familyColor = family?.color ?? 'rgba(148,163,184,0.55)'
  const radius = node.isGroup ? gr : r
  const grpStyle: CSSProperties = { cursor: 'grab' }
  const groupOpacity = isDimmed ? 0.35 : 1
  const innerWidth = isOpen ? 4 : isNeighbor ? 3 : isHost ? 3.5 : 2

  return (
    <g
      data-testid={`flow-node-${node.id}`}
      data-flow-draggable=""
      data-node-id={node.id}
      data-status={node.status}
      data-region={node.region}
      data-family={node.family}
      data-flow={node.flowId}
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
        {`${node.label} — ${tone.label}${isHost ? ' · host' : ''}`}
      </title>
      {isHost ? (
        <circle r={radius + 12} fill="rgba(20,184,166,0.30)" />
      ) : isOpen ? (
        <circle r={radius + 10} fill="rgba(251,191,36,0.30)" />
      ) : isNeighbor ? (
        <circle r={radius + 8} fill="rgba(252,211,77,0.18)" />
      ) : tone.glow !== 'transparent' ? (
        <circle r={radius + 8} fill={tone.glow} />
      ) : null}
      {isHost ? (
        <circle
          r={radius + 6}
          fill="none"
          stroke="var(--flow-host-ring, #14B8A6)"
          strokeWidth={3.5}
          opacity={0.95}
        />
      ) : null}
      <circle
        r={radius + 2}
        fill="none"
        stroke={familyColor}
        strokeWidth={node.isGroup ? 2.5 : 1}
        opacity={0.55}
      />
      <circle r={radius} fill={tone.fill} stroke={innerRing} strokeWidth={innerWidth} />
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
      <text
        x={0}
        y={radius + 14}
        textAnchor="middle"
        fontSize={10}
        fill="var(--flow-bubble-label)"
        fontFamily="var(--font-mono, ui-monospace, monospace)"
        pointerEvents="none"
      >
        {node.label.length > 18 ? node.label.slice(0, 17) + '…' : node.label}
      </text>
    </g>
  )
}

function glyphFor(status: string): string {
  if (status === 'succeeded') return '✓'
  if (status === 'failed') return '✗'
  if (status === 'running') return '◐'
  return '○'
}

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

/* Re-export the RelationshipType for callers that need to switch on
 * the rel type without importing flow-core directly. */
export type { RelationshipType }
