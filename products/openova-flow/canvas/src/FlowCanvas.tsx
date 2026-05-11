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
  type LaneDescriptor,
  type NodeAction,
} from '@openova/flow-core'
import { NodeActionsMenu } from './NodeActionsMenu'

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
  /** Adapter-supplied custom actions for the right-click context menu.
   *  Each action's `enabled` predicate is consulted per node. */
  actions?: ReadonlyArray<NodeAction>
  /** Operator picked an action from the right-click menu. The canvas
   *  delegates the actual side-effect to the host (which typically
   *  calls `action.invoke(nodeId)`). */
  onNodeAction?: (nodeId: string, actionId: string) => void | Promise<void>
  /** Operator clicked a `triggeredBy` badge or a cross-flow edge tag.
   *  Hosts wire this to their router (e.g. `navigate({ to: '/sovereign/provision/$id' })`). */
  onNavigateFlow?: (flowId: string) => void
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
    actions,
    onNodeAction,
    onNavigateFlow,
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
  /** Right-click context menu state. `null` = closed. */
  const [actionMenu, setActionMenu] = useState<
    { nodeId: string; x: number; y: number } | null
  >(null)
  /** Hover dim state — node id currently hovered (null = none). When
   *  set, all non-neighbor nodes + non-incident edges dim to 35%. */
  const [hoveredNodeId, setHoveredNodeId] = useState<string | null>(null)

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

  /* ── Lane geometry — derived from layoutOut.lanes ──────────────────
   *
   * The canvas honours `meta.layout` on `contains`-parents by laying
   * out the parent as a rounded-rect "lane" with its label on top,
   * and packing its direct children left→right (horizontal) or
   * top→bottom (vertical) inside the box.
   *
   * Lanes nest: top-level lanes (`laneDepth = 0`) split the host
   * surface; nested lanes occupy a sub-region inside their parent
   * lane. We compute boxes outermost-first so nested lanes inherit
   * their parent's box bounds.
   *
   * If no lane is declared by the adapter, this Map is empty and the
   * existing organic d3-force layout runs unchanged. */
  type LaneBox = {
    lane: LaneDescriptor
    /** Box origin (top-left) in canvas pixel coords. */
    x: number
    y: number
    /** Box dimensions in canvas pixel coords. */
    w: number
    h: number
  }
  const laneGeometry = useMemo(() => {
    const lanes = layoutOut.lanes
    const byLane = new Map<string, LaneBox>()
    /** Per-leaf anchor — drives forceX/forceY for the simulation. */
    const anchor = new Map<string, { x: number; y: number }>()
    if (!lanes || lanes.length === 0) {
      return { byLane, anchor }
    }
    const PAD_X = 24
    const PAD_Y_TOP = 32 // room for lane label on top
    const PAD_Y_BOTTOM = 16
    const NESTED_GAP = 16
    const LANE_GAP = 18

    // Split lanes by parent: outer top-level lanes vs nested.
    const topLevel = lanes.filter((l) => l.parentLaneId === null)
    const childrenOfLane = new Map<string, LaneDescriptor[]>()
    for (const l of lanes) {
      if (l.parentLaneId === null) continue
      const arr = childrenOfLane.get(l.parentLaneId) ?? []
      arr.push(l)
      childrenOfLane.set(l.parentLaneId, arr)
    }
    // Sort children deterministically.
    for (const arr of childrenOfLane.values()) {
      arr.sort((a, b) => {
        if (a.sortKey !== b.sortKey) return a.sortKey - b.sortKey
        return a.id.localeCompare(b.id)
      })
    }

    // Outer layout: top-level lanes split the surface along the AXIS
    // of the OUTERMOST lane. Convention: when top-level axis is
    // 'vertical' the top-level lanes stack as vertical columns
    // (region = vertical swimlane = View 4 in the spec); when
    // 'horizontal' the top-level lanes stack as horizontal rows.
    // Default to vertical when mixed.
    const outerAxis: 'horizontal' | 'vertical' =
      topLevel.length > 0 && topLevel.every((l) => l.axis === 'horizontal')
        ? 'horizontal'
        : 'vertical'

    const surfaceW = Math.max(MIN_HOST_W, hostSize.w)
    const surfaceH = Math.max(MIN_HOST_H, hostSize.h)
    const innerW = Math.max(200, surfaceW - 2 * LANE_GAP)
    const innerH = Math.max(200, surfaceH - 2 * LANE_GAP)

    // Compute top-level lane boxes.
    if (outerAxis === 'vertical') {
      // Columns side-by-side, each occupying the full height.
      const perLaneW = Math.max(
        220,
        Math.floor((innerW - (topLevel.length - 1) * LANE_GAP) / Math.max(1, topLevel.length)),
      )
      let cursorX = LANE_GAP
      for (const lane of topLevel) {
        byLane.set(lane.id, {
          lane,
          x: cursorX,
          y: LANE_GAP,
          w: perLaneW,
          h: innerH,
        })
        cursorX += perLaneW + LANE_GAP
      }
    } else {
      // Rows stacked, each occupying the full width.
      const perLaneH = Math.max(
        180,
        Math.floor((innerH - (topLevel.length - 1) * LANE_GAP) / Math.max(1, topLevel.length)),
      )
      let cursorY = LANE_GAP
      for (const lane of topLevel) {
        byLane.set(lane.id, {
          lane,
          x: LANE_GAP,
          y: cursorY,
          w: innerW,
          h: perLaneH,
        })
        cursorY += perLaneH + LANE_GAP
      }
    }

    // Compute nested lane boxes — packed inside their parent's inner
    // rect along the parent's AXIS.
    const visited = new Set<string>()
    function placeNested(parent: LaneBox) {
      if (visited.has(parent.lane.id)) return
      visited.add(parent.lane.id)
      const kids = childrenOfLane.get(parent.lane.id) ?? []
      if (kids.length === 0) return
      const innerLaneX = parent.x + PAD_X
      const innerLaneY = parent.y + PAD_Y_TOP
      const innerLaneW = Math.max(0, parent.w - 2 * PAD_X)
      const innerLaneH = Math.max(0, parent.h - PAD_Y_TOP - PAD_Y_BOTTOM)
      if (parent.lane.axis === 'horizontal') {
        // Children packed L→R inside the parent's inner rect.
        const perKidW = Math.max(
          120,
          Math.floor((innerLaneW - (kids.length - 1) * NESTED_GAP) / Math.max(1, kids.length)),
        )
        let cx = innerLaneX
        for (const kid of kids) {
          byLane.set(kid.id, {
            lane: kid,
            x: cx,
            y: innerLaneY,
            w: perKidW,
            h: innerLaneH,
          })
          cx += perKidW + NESTED_GAP
        }
      } else {
        // Vertical lanes nest by stacking T→B inside the parent.
        const perKidH = Math.max(
          100,
          Math.floor((innerLaneH - (kids.length - 1) * NESTED_GAP) / Math.max(1, kids.length)),
        )
        let cy = innerLaneY
        for (const kid of kids) {
          byLane.set(kid.id, {
            lane: kid,
            x: innerLaneX,
            y: cy,
            w: innerLaneW,
            h: perKidH,
          })
          cy += perKidH + NESTED_GAP
        }
      }
      // Recurse into grandkids.
      for (const kid of kids) {
        const kidBox = byLane.get(kid.id)
        if (kidBox) placeNested(kidBox)
      }
    }
    for (const lane of topLevel) {
      const parentBox = byLane.get(lane.id)
      if (parentBox) placeNested(parentBox)
    }

    // Compute per-child-node anchors. For each leaf inside a lane,
    // its anchor is determined by:
    //   1. The deepest enclosing lane's box.
    //   2. The leaf's index inside that lane's `childIds`.
    //   3. The lane axis.
    // The deepest enclosing lane is the one whose `childIds` contains
    // the leaf. (Lanes only list direct children — leaves of the
    // deepest enclosing group are the direct children.)
    for (const lane of lanes) {
      const box = byLane.get(lane.id)
      if (!box) continue
      const innerX = box.x + PAD_X
      const innerY = box.y + PAD_Y_TOP
      const innerW = Math.max(0, box.w - 2 * PAD_X)
      const innerH = Math.max(0, box.h - PAD_Y_TOP - PAD_Y_BOTTOM)
      // Filter children that are NOT themselves lanes (those have
      // their own box). The remaining children are leaves placed
      // directly inside this lane.
      const leafKids = lane.childIds.filter((id) => !byLane.has(id))
      if (leafKids.length === 0) continue
      if (lane.axis === 'horizontal') {
        // Pack L→R. Use grid wrap if too many for one row.
        const maxPerRow = Math.max(1, Math.floor(innerW / 90))
        const rows = Math.max(1, Math.ceil(leafKids.length / maxPerRow))
        const cols = Math.min(maxPerRow, leafKids.length)
        const colW = cols > 0 ? innerW / cols : innerW
        const rowH = rows > 0 ? innerH / rows : innerH
        leafKids.forEach((kid, i) => {
          const col = i % cols
          const row = Math.floor(i / cols)
          anchor.set(kid, {
            x: innerX + colW * (col + 0.5),
            y: innerY + rowH * (row + 0.5),
          })
        })
      } else {
        // Vertical: pack T→B. Wrap to multiple columns if dense.
        const maxPerCol = Math.max(1, Math.floor(innerH / 90))
        const cols = Math.max(1, Math.ceil(leafKids.length / maxPerCol))
        const rows = Math.min(maxPerCol, leafKids.length)
        const colW = cols > 0 ? innerW / cols : innerW
        const rowH = rows > 0 ? innerH / rows : innerH
        leafKids.forEach((kid, i) => {
          const row = i % rows
          const col = Math.floor(i / rows)
          anchor.set(kid, {
            x: innerX + colW * (col + 0.5),
            y: innerY + rowH * (row + 0.5),
          })
        })
      }
    }

    // Lane GROUP nodes themselves (when present in positionedNodes,
    // e.g. when folded) anchor at the centre of their box so they
    // render as a single super-bubble inside the lane.
    for (const [laneId, box] of byLane) {
      anchor.set(laneId, {
        x: box.x + box.w / 2,
        y: box.y + box.h / 2,
      })
    }

    return { byLane, anchor }
  }, [layoutOut.lanes, hostSize.w, hostSize.h])

  const hasLanes = laneGeometry.byLane.size > 0

  const depthToX = useCallback(
    (depth: number) =>
      layoutMetrics.xByDepth.get(depth) ?? R + COLLIDE_PADDING + depth * (R * 4),
    [layoutMetrics, R],
  )

  /** When the canvas has lane geometry, leaves inside a lane use the
   *  lane anchor directly; everything else falls back to depthToX. */
  const xForNode = useCallback(
    (nodeId: string, depth: number) => {
      const a = laneGeometry.anchor.get(nodeId)
      if (a) return a.x
      return depthToX(depth)
    },
    [laneGeometry, depthToX],
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
      // Lane override: if the node is anchored by a lane, use that Y
      // — the lane has already decided the visual rank.
      const a = laneGeometry.anchor.get(id)
      if (a) return a.y
      const b = bucketRank.get(id)
      if (!b) return Y_CENTER
      if (b.size === 1) {
        const d = depthByNodeId.get(id) ?? 0
        const sign = d % 2 === 0 ? -1 : 1
        return Y_CENTER + sign * ZIGZAG_AMPLITUDE
      }
      return Y_CENTER + (b.idx - (b.size - 1) / 2) * PITCH
    },
    [bucketRank, Y_CENTER, PITCH, ZIGZAG_AMPLITUDE, depthByNodeId, laneGeometry],
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
        const initX = xForNode(n.id, n.depth) + (seed.fx - 0.5) * R * 1.5
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
  }, [layoutOut.positionedNodes, xForNode, yForBucket, R])

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
      n.x = xForNode(n.id, n.depth) + (seed.fx - 0.5) * R * 1.5
      n.y = yForBucket(n.id) + (seed.fy - 0.5) * R * 0.6
      n.vx = 0
      n.vy = 0
    }
    // Lane-anchored nodes pull harder toward their target — keeps
    // children inside the lane rectangle even with strong link
    // tensions across regions.
    const LANE_FORCE_STRENGTH = 0.55
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
      .force(
        'x',
        forceX<SimNode>()
          .x((d) => xForNode(d.id, d.depth))
          .strength((d) => (laneGeometry.anchor.has(d.id) ? LANE_FORCE_STRENGTH : FORCE_X_STRENGTH)),
      )
      .force(
        'y',
        forceY<SimNode>()
          .y((d) => yForBucket(d.id))
          .strength((d) => (laneGeometry.anchor.has(d.id) ? LANE_FORCE_STRENGTH : FORCE_Y_STRENGTH)),
      )
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
          const baseX = xForNode(n.id, n.depth)
          const inLane = laneGeometry.anchor.has(n.id)
          const xBand = inLane ? R * 1.5 : PER_DEPTH_X
          const xMin = Math.max(R, baseX - xBand)
          const xMax = Math.min(layoutMetrics.totalWidth - R, baseX + xBand)
          if (typeof n.x === 'number') {
            if (n.x < xMin) n.x = xMin
            else if (n.x > xMax) n.x = xMax
          }
          const targetY = yForBucket(n.id)
          const Y_HALF_BAND = inLane ? R * 1.5 : R * 2 + COLLIDE_PADDING
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
  }, [simNodes, layoutOut.edges, xForNode, yForBucket, hostSize.h, R, GR, PER_DEPTH_X, LINK_DISTANCE, layoutMetrics.totalWidth, laneGeometry])

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
    setActionMenu(null)
    onBackgroundClick?.()
  }, [cancelPendingClick, onBackgroundClick])

  /* ── Right-click → actions menu ─────────────────────────────── */

  const handleNodeContextMenu = useCallback(
    (nodeId: string, event: ReactMouseEvent<SVGGElement>) => {
      // Only open when the adapter actually supplied actions.
      if (!actions || actions.length === 0) return
      event.preventDefault()
      event.stopPropagation()
      const host = hostRef.current
      const rect = host?.getBoundingClientRect()
      const relX = rect ? event.clientX - rect.left : event.clientX
      const relY = rect ? event.clientY - rect.top : event.clientY
      setActionMenu({ nodeId, x: relX, y: relY })
    },
    [actions],
  )

  const handleActionSelect = useCallback(
    (actionId: string) => {
      const target = actionMenu
      setActionMenu(null)
      if (!target) return
      void onNodeAction?.(target.nodeId, actionId)
    },
    [actionMenu, onNodeAction],
  )

  /* ── Hover handlers — hover dim ─────────────────────────────── */

  const handleNodeMouseEnter = useCallback((nodeId: string) => {
    setHoveredNodeId(nodeId)
  }, [])
  const handleNodeMouseLeave = useCallback(() => {
    setHoveredNodeId(null)
  }, [])

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

  /* ── Hover-dim sets ──────────────────────────────────────────── */
  const hoverNeighborIds = new Set<string>()
  if (hoveredNodeId) {
    hoverNeighborIds.add(hoveredNodeId)
    for (const e of layoutOut.edges) {
      if (e.fromId === hoveredNodeId) hoverNeighborIds.add(e.toId)
      else if (e.toId === hoveredNodeId) hoverNeighborIds.add(e.fromId)
    }
  }

  /* ── Cross-flow edges — `Relationship.toFlowId !== flow.id` and
   *      target is NOT in the visible node set. Render as a "→" tag
   *      from the source node. ─────────────────────────────────── */
  const visibleIds = new Set(layoutOut.positionedNodes.map((n) => n.id))
  const crossFlowTags = new Map<string, string>() // sourceId → targetFlowId
  for (const r of relationships) {
    if (r.type === 'contains') continue
    if (!r.toFlowId) continue
    if (r.toFlowId === flow.id) continue
    if (visibleIds.has(r.toId)) continue
    // The source MUST be a visible node — only then can we render
    // an arrow off the bubble.
    if (visibleIds.has(r.fromId)) {
      crossFlowTags.set(r.fromId, r.toFlowId)
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

  /* ── triggeredBy banner — only when the flow declares one. ─── */
  const triggeredBy = flow.triggeredBy ?? []

  return (
    <div
      ref={hostRef}
      className="flow-canvas-host"
      data-testid="flow-canvas-host"
      style={{
        position: 'relative',
        overflowX: layoutMetrics.totalWidth > hostSize.w ? 'auto' : 'hidden',
        overflowY: 'hidden',
      }}
    >
      {triggeredBy.length > 0 ? (
        <div
          data-testid="flow-triggered-by-banner"
          style={{
            position: 'absolute',
            top: 10,
            left: 12,
            display: 'inline-flex',
            alignItems: 'center',
            gap: 8,
            padding: '5px 10px',
            background: 'var(--flow-banner-bg, rgba(15,23,42,0.85))',
            border: '1px solid var(--flow-banner-border, rgba(255,255,255,0.10))',
            borderRadius: 999,
            color: 'var(--flow-banner-text, #cbd5e1)',
            fontSize: 11,
            fontWeight: 600,
            letterSpacing: 0.02,
            zIndex: 30,
            pointerEvents: 'auto',
          }}
        >
          <span style={{ color: 'var(--flow-banner-label, #94a3b8)', textTransform: 'uppercase', fontSize: 10 }}>
            triggeredBy
          </span>
          {triggeredBy.map((t, i) => (
            <button
              key={`${t.flowId}-${i}`}
              type="button"
              data-testid={`flow-triggered-by-${t.flowId}`}
              data-flow-id={t.flowId}
              data-when={t.when}
              onClick={() => onNavigateFlow?.(t.flowId)}
              style={{
                background: 'transparent',
                border: 0,
                padding: 0,
                font: 'inherit',
                fontSize: 11,
                color: 'var(--flow-banner-link, #38BDF8)',
                cursor: 'pointer',
                textDecoration: 'underline dotted',
              }}
            >
              {`▣ ${t.flowId} (↗ open flow)`}
            </button>
          ))}
        </div>
      ) : null}
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
        {hasLanes ? <FlowLanes geometry={laneGeometry} onFoldToggle={onFoldToggle} /> : null}
        {layoutOut.edges.map((e) => {
          const s = renderPos.get(e.fromId)
          const t = renderPos.get(e.toId)
          if (!s || !t) return null
          const onSelectionPath =
            selectedNodeId !== null && (e.fromId === selectedNodeId || e.toId === selectedNodeId)
          const onHostPath =
            hostNodeId !== null && !onSelectionPath && (e.fromId === hostNodeId || e.toId === hostNodeId)
          // Hover-dim non-incident edges to 35%.
          const onHoverPath =
            hoveredNodeId !== null && (e.fromId === hoveredNodeId || e.toId === hoveredNodeId)
          const hoverDimmed = hoveredNodeId !== null && !onHoverPath
          return (
            <FlowEdge
              key={`${e.fromId}-${e.toId}-${e.relType}`}
              edge={e}
              from={s}
              to={t}
              palette={palette}
              highlighted={onSelectionPath ? 'selection' : onHostPath ? 'host' : 'none'}
              hoverDimmed={hoverDimmed}
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
          // Selection-dim still takes precedence over hover-dim.
          const selectionDimmed =
            selectedNodeId !== null && !isNeighbor && !isOpen && !isHost
          const hoverDimmed =
            hoveredNodeId !== null && hoveredNodeId !== node.id && !hoverNeighborIds.has(node.id)
          // Selection ring takes precedence per the docstring; hover
          // never dims an already-selected/host/neighbor node.
          const isDimmed =
            selectionDimmed ||
            (hoverDimmed && !isOpen && !isHost && !isNeighbor)
          const crossFlowTargetFlow = crossFlowTags.get(node.id) ?? null
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
              isDimmed={isDimmed}
              hasActions={Boolean(actions && actions.length > 0)}
              crossFlowTargetFlow={crossFlowTargetFlow}
              onCrossFlowClick={() => {
                if (crossFlowTargetFlow) onNavigateFlow?.(crossFlowTargetFlow)
              }}
              onClick={(e) => handleNodeClick(node.id, e)}
              onDoubleClick={() => handleNodeDoubleClick(node.id)}
              onContextMenu={(e) => handleNodeContextMenu(node.id, e)}
              onMouseEnter={() => handleNodeMouseEnter(node.id)}
              onMouseLeave={handleNodeMouseLeave}
              r={R}
              gr={GR}
            />
          )
        })}
      </svg>
      {actionMenu && actions ? (
        <NodeActionsMenu
          nodeId={actionMenu.nodeId}
          actions={actions.filter((a) => !a.enabled || a.enabled(actionMenu.nodeId))}
          x={actionMenu.x}
          y={actionMenu.y}
          onSelect={handleActionSelect}
          onDismiss={() => setActionMenu(null)}
        />
      ) : null}
    </div>
  )
}

/* ─── FlowLanes — rounded-rect backgrounds for `meta.layout` groups ── */

interface FlowLanesProps {
  geometry: { byLane: Map<string, { lane: LaneDescriptor; x: number; y: number; w: number; h: number }> }
  onFoldToggle?: (groupId: string) => void
}

function FlowLanes({ geometry, onFoldToggle }: FlowLanesProps) {
  const boxes = Array.from(geometry.byLane.values()).sort(
    (a, b) => a.lane.laneDepth - b.lane.laneDepth,
  )
  return (
    <g data-testid="flow-lanes" pointerEvents="none">
      {boxes.map((box) => {
        const isNested = box.lane.laneDepth > 0
        const fill = isNested
          ? 'rgba(56,189,248,0.04)'
          : 'rgba(148,163,184,0.04)'
        const stroke = isNested
          ? 'rgba(56,189,248,0.18)'
          : 'rgba(148,163,184,0.22)'
        return (
          <g
            key={box.lane.id}
            data-testid={`flow-lane-${box.lane.id}`}
            data-lane-id={box.lane.id}
            data-lane-axis={box.lane.axis}
            data-lane-depth={box.lane.laneDepth}
          >
            <rect
              x={box.x}
              y={box.y}
              width={box.w}
              height={box.h}
              rx={14}
              ry={14}
              fill={fill}
              stroke={stroke}
              strokeWidth={1}
            />
            <text
              x={box.x + 14}
              y={box.y + 20}
              fontSize={11}
              fontWeight={700}
              fill="var(--flow-lane-label, #cbd5e1)"
              fontFamily="ui-sans-serif, system-ui, sans-serif"
              style={{ pointerEvents: onFoldToggle ? 'auto' : 'none', cursor: onFoldToggle ? 'pointer' : 'default' }}
              onDoubleClick={() => onFoldToggle?.(box.lane.id)}
            >
              {box.lane.label}
            </text>
          </g>
        )
      })}
    </g>
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
  /** When true, the edge dims to 35% opacity (hover-dim). Overridden
   *  by `highlighted !== 'none'`. */
  hoverDimmed: boolean
  r: number
}

function FlowEdge({ edge, from, to, palette, highlighted, hoverDimmed, r }: FlowEdgeProps) {
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
  const baseOpacity =
    highlighted !== 'none'
      ? 1
      : isFailure
        ? sourceFailed
          ? 0.85
          : 0.25
        : 0.7
  const opacity = highlighted === 'none' && hoverDimmed ? 0.35 : baseOpacity
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
    <g
      data-flow-edge=""
      data-rel-type={edge.relType}
      data-condition={edge.condition}
      data-hover-dimmed={hoverDimmed ? 'true' : 'false'}
    >
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
  /** True when the adapter supplied actions — used to set the cursor /
   *  ARIA attrs so the right-click affordance is discoverable. */
  hasActions: boolean
  /** Target flow id for a cross-flow tag — when non-null the bubble
   *  renders a "→ <flowId>" tag pointing at the other flow. */
  crossFlowTargetFlow: string | null
  onCrossFlowClick: () => void
  onClick: (e: ReactMouseEvent<SVGGElement>) => void
  onDoubleClick: () => void
  onContextMenu: (e: ReactMouseEvent<SVGGElement>) => void
  onMouseEnter: () => void
  onMouseLeave: () => void
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
  hasActions,
  crossFlowTargetFlow,
  onCrossFlowClick,
  onClick,
  onDoubleClick,
  onContextMenu,
  onMouseEnter,
  onMouseLeave,
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
      data-has-actions={hasActions ? 'true' : 'false'}
      data-cross-flow={crossFlowTargetFlow ?? ''}
      onClick={onClick}
      onDoubleClick={onDoubleClick}
      onContextMenu={onContextMenu}
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
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
      {/* Child-count badge `[N]` top-right — only on foldable parents
       *   (groups with descendants). Independent of fold state per
       *   founder lock (View 4 still shows `[43]` on the region even
       *   when expanded to phases). */}
      {node.isGroup && node.descendantCount > 0 ? (
        <g
          data-testid={`flow-node-badge-${node.id}`}
          data-descendant-count={node.descendantCount}
          pointerEvents="none"
        >
          <rect
            x={radius - 2}
            y={-radius - 12}
            width={node.descendantCount > 99 ? 28 : 22}
            height={14}
            rx={7}
            ry={7}
            fill="var(--flow-badge-bg, rgba(15,23,42,0.92))"
            stroke="var(--flow-badge-border, rgba(56,189,248,0.45))"
            strokeWidth={1}
          />
          <text
            x={radius - 2 + (node.descendantCount > 99 ? 14 : 11)}
            y={-radius - 1}
            textAnchor="middle"
            fontSize={10}
            fontWeight={700}
            fontFamily="ui-monospace, monospace"
            fill="var(--flow-badge-text, #38BDF8)"
          >
            {`[${node.descendantCount}]`}
          </text>
        </g>
      ) : null}
      {/* Cross-flow tag — when the node has a Relationship targeting a
       *   node in another flow, render a "↗" tag pointing right. Click
       *   navigates the host's router. */}
      {crossFlowTargetFlow ? (
        <g
          data-testid={`flow-node-crossflow-${node.id}`}
          data-cross-flow-target={crossFlowTargetFlow}
          onClick={(e) => {
            e.stopPropagation()
            onCrossFlowClick()
          }}
          style={{ cursor: 'pointer' }}
        >
          <rect
            x={radius + 6}
            y={-7}
            width={36}
            height={14}
            rx={4}
            ry={4}
            fill="var(--flow-crossflow-bg, rgba(15,23,42,0.92))"
            stroke="var(--flow-crossflow-border, rgba(56,189,248,0.5))"
            strokeWidth={1}
          />
          <text
            x={radius + 24}
            y={4}
            textAnchor="middle"
            fontSize={10}
            fontWeight={700}
            fontFamily="ui-monospace, monospace"
            fill="var(--flow-crossflow-text, #38BDF8)"
          >
            → flow
          </text>
        </g>
      ) : null}
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
