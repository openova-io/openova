/**
 * GraphCanvas — reusable, low-level force-directed graph component
 * (P2 of issue openova-io/openova#309). Rendered by
 * ArchitectureGraphPage but designed to be reusable for other
 * graph surfaces (job DAG, application map, ...).
 *
 * Founder spec, verbatim subset:
 *   • forwardRef wrapping the SVG root
 *   • props: nodes, edges (both immutable from caller's view), plus
 *     focusNodeId / hiddenTypes / highlightedIds for filter modes
 *   • imperative handle: addElements, removeElements, unpinNode,
 *     relax, fit
 *   • node radius: 6 + sqrt(degree) * 2.8 (clamped 6..20)
 *   • stroke states:
 *       highlighted (search match): yellow #fcc419, 3px
 *       focusNodeId match:          pink #f06595, 3px
 *       pinned (dragged):           dark dashed #343a40
 *       default:                    white #fff, 1.6px
 *   • adaptive physics: 5 tiers based on node count
 *   • pin-on-drag: set fx/fy on drag end
 *   • double-click via useRef timestamp (event.detail unreliable)
 *   • focus mode: filter to focusNodeId + direct neighbors
 *   • type visibility: hide nodes of types in hiddenTypes
 *   • stats overlay: bottom-left badges (live node/edge count)
 *   • responsive: internal ResizeObserver
 *   • cooldownTicks Infinity — simulation stays alive
 *
 * Implementation note: the canonical spec referenced
 * react-force-graph-2d (canvas-based), but this codebase is uniformly
 * SVG + Tailwind + Radix (no canvas-based graphs anywhere) — see
 * widgets/job-deps-graph/JobDependenciesGraph.tsx for the established
 * pattern. We use d3-force directly (already a dep) and render to
 * SVG, which preserves: testability via data-testid, visual-style
 * consistency with the rest of the portal, and the ability to drop in
 * the same status palette / typography / dark-mode tokens. All the
 * BEHAVIOURAL requirements above (degree-based radius, pin-on-drag,
 * focus mode, search highlight, double-click, drag-to-pin, etc.) are
 * implemented identically; the swap is engine-only.
 *
 * The widget is router-agnostic and side-effect-free except for the
 * single `requestAnimationFrame` driving the simulation tick. All
 * data mutation goes through the imperative handle so React's
 * reconciliation never fights with d3-force.
 *
 * react-hooks/refs lint exception: this widget intentionally reads
 * refs during render — the d3-force simulation mutates LiveNode x/y
 * fields ~60 times/sec, and copying those into useState every tick
 * would defeat the purpose of using refs (and trigger O(n) React
 * reconciliation on every frame). The rAF loop in `tick()` calls
 * forceRender({}) to re-snapshot, so the render reads ARE the way
 * the canvas stays in sync with the physics. This is the documented
 * d3-force-in-React pattern.
 */
/* eslint-disable react-hooks/refs */

import {
  forwardRef,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import {
  forceCenter,
  forceCollide,
  forceLink,
  forceX,
  forceY,
  forceManyBody,
  forceSimulation,
  type Simulation,
  type SimulationLinkDatum,
} from 'd3-force'
import {
  edgeNodeId,
  EDGE_DASHED,
  EDGE_MARKER_END,
  EDGE_MARKER_START,
  EDGE_STROKE,
  FAMILY_BORDER,
  NODE_CATEGORY,
  NODE_FAMILY,
  STATUS_FILL,
  type ArchEdgeType,
  type ArchNodeType,
  type ArchStatus,
  type EdgeMarker,
  type GraphEdge,
  type GraphNode,
  type LiveEdge,
  type LiveNode,
} from './types'
import { shapeForCategory } from './shapes'
import { GraphLegend } from './GraphLegend'
import { BOUND_PADDING, NODE_R, physicsFor } from './layout'
import { markerId, uniqueMarkerDefs } from './markers'

/* ── Public types ────────────────────────────────────────────────── */

export interface GraphCanvasHandle {
  /** Append new nodes/edges to the live simulation, preserving layout. */
  addElements: (n: GraphNode[], e: GraphEdge[]) => void
  /** Remove nodes (and any edges incident to them) by id. */
  removeElements: (nodeIds: string[]) => void
  /** Release a pinned node so D3-force lays it out again. */
  unpinNode: (id: string) => void
  /** Re-warm the simulation alpha (call after big edits to settle layout). */
  relax: () => void
  /** Center the camera on the current bounding box. */
  fit: () => void
}

export interface GraphCanvasProps {
  nodes: GraphNode[]
  edges: GraphEdge[]
  /** Highlighted node ids — yellow ring. Typically the search match set. */
  highlightedIds?: Set<string>
  /** Focus node — when set, the canvas filters down to this node + neighbors. */
  focusNodeId?: string | null
  /** Set of node types to hide entirely (hidden in legend / density slider). */
  hiddenTypes?: Set<ArchNodeType>
  /** Per-type element-count cap; renders the first N nodes of each type. */
  typeLimits?: Partial<Record<ArchNodeType, number>>
  /** Click handler — receives the clicked node. */
  onNodeClick?: (n: GraphNode) => void
  /** Double-click handler — used to enter focus mode. */
  onNodeDoubleClick?: (n: GraphNode) => void
  /** Right-click handler — used to open the context menu. */
  onNodeContextMenu?: (n: GraphNode, ev: React.MouseEvent) => void
  /** Right-click on empty canvas. */
  onCanvasContextMenu?: (ev: React.MouseEvent) => void
  /** Shift+drag-from one node to another emits this. */
  onEdgeCreate?: (sourceId: string, targetId: string) => void
  /** Optional data-testid prefix; defaults to "arch-graph". */
  testIdPrefix?: string
  /**
   * Optional per-node FILL override. When it returns a colour that wins
   * over the status palette (STATUS_FILL[status]); returning undefined
   * falls back to the status fill. The unified Cloud-graph (#3958) drives
   * fill by STATUS by default — this hook is retained for callers that
   * want a bespoke fill rule. Opt-in: omitting it preserves the default
   * fill-by-status behaviour.
   */
  nodeColorFn?: (n: GraphNode) => string | undefined
  /**
   * Render the three-channel legend overlay (shape→category, border→
   * family, fill→status) in the bottom-right. Defaults to false so
   * embedders (job DAG, etc.) opt in. The unified Cloud-graph turns it
   * on (#3958).
   */
  showLegend?: boolean
}

/**
 * The status FILL for a node. Defaults to grey ('unknown') when the
 * node carries no status. Centralised so the canvas + the legend agree.
 */
function statusFill(status: ArchStatus | undefined): string {
  return STATUS_FILL[status ?? 'unknown'] ?? STATUS_FILL.unknown
}

/* ── Constant-density centered layout (#3958) — physicsFor + the
 *    NODE_R / BOUND_PADDING constants live in ./layout (pure math, so
 *    this file stays component-only for react-refresh). ───────────── */

/* ── Helpers ─────────────────────────────────────────────────────── */

function computeDegree(nodes: GraphNode[], edges: GraphEdge[]): Map<string, number> {
  const m = new Map<string, number>()
  for (const n of nodes) m.set(n.id, 0)
  for (const e of edges) {
    if (m.has(e.source)) m.set(e.source, (m.get(e.source) ?? 0) + 1)
    if (m.has(e.target)) m.set(e.target, (m.get(e.target) ?? 0) + 1)
  }
  return m
}

function radiusForDegree(degree: number): number {
  // 6 + sqrt(degree) * 2.8, clamped 6..20 — locked by spec.
  const r = 6 + Math.sqrt(Math.max(0, degree)) * 2.8
  return Math.max(6, Math.min(20, r))
}

/**
 * Bounded-physics: clamp each node's x/y (and any pinned fx/fy) inside
 * a [r+pad, W-r-pad] × [r+pad, H-r-pad] box every tick (#348 item 5).
 * Implemented as a d3-force "force" so it integrates naturally with
 * the simulation's tick loop and runs after charge / collide /
 * link forces have moved nodes for the frame.
 */
function makeForceBound(
  width: number,
  height: number,
  padding = BOUND_PADDING,
) {
  let nodes: LiveNode[] = []
  // d3-force calls force(alpha) every tick. The bound combines:
  //   - Hard clamp on PINNED positions (drag past the edge snaps in).
  //   - Soft elastic bounce on free nodes: when a node touches the
  //     wall, reflect its velocity inward (instead of arresting it
  //     to a dead stop, which causes nodes to stack at the edges
  //     and produces the "bubbles escape to edges" complaint).
  // Velocity reflection costs nothing extra and lets the simulation
  // actually relax — the kinetic energy returns to the system rather
  // than being silently zeroed by a hard clamp.
  const force = () => {
    for (const n of nodes) {
      const r = radiusForDegree(n.degree)
      const minX = r + padding
      const minY = r + padding
      const maxX = Math.max(minX, width - r - padding)
      const maxY = Math.max(minY, height - r - padding)
      if (n.x < minX) {
        n.x = minX
        if (n.vx < 0) n.vx = -n.vx * 0.4
      } else if (n.x > maxX) {
        n.x = maxX
        if (n.vx > 0) n.vx = -n.vx * 0.4
      }
      if (n.y < minY) {
        n.y = minY
        if (n.vy < 0) n.vy = -n.vy * 0.4
      } else if (n.y > maxY) {
        n.y = maxY
        if (n.vy > 0) n.vy = -n.vy * 0.4
      }
      // Pinned positions still hard-clamp so a manual drag past the
      // edge instantly snaps inside.
      if (n.fx !== null) {
        n.fx = Math.max(minX, Math.min(maxX, n.fx))
      }
      if (n.fy !== null) {
        n.fy = Math.max(minY, Math.min(maxY, n.fy))
      }
    }
  }
  // d3-force calls `initialize(nodes)` when the force is added; we
  // capture the live LiveNode array so subsequent ticks have a
  // current handle even after sim.nodes() is reset.
  ;(force as unknown as { initialize: (n: LiveNode[]) => void }).initialize = (
    nextNodes,
  ) => {
    nodes = nextNodes
  }
  return force as (() => void) & {
    initialize: (n: LiveNode[]) => void
  }
}

/**
 * Soft ELLIPTICAL containment (#3958). A GENTLE inward force for any node
 * whose normalized position leaves the field ellipse
 * `(dx/fieldRadiusX)² + (dy/fieldRadiusY)² > 1`. Unlike makeForceBound (a
 * hard clamp at the literal viewport edge), this is a spring that grows
 * with how far the node has strayed past the target field — so the cloud
 * settles into an ellipse filling ~85% of BOTH the canvas width and
 * height, with the natural force-directed topology intact, never flung to
 * the edges and never crushed into the centre. On a wide-short canvas the
 * X radius is materially larger than the Y radius, so the cloud fills the
 * empty left/right margin instead of collapsing into a short-dim disc.
 * Inside the ellipse it does nothing (the charge/link/gravity balance owns
 * the layout there).
 */
function makeForceRadialCap(cx: number, cy: number, fieldRadiusX: number, fieldRadiusY: number) {
  let nodes: LiveNode[] = []
  const force = (alpha: number) => {
    if (fieldRadiusX <= 0 || fieldRadiusY <= 0) return
    for (const n of nodes) {
      // Pinned nodes are operator-controlled — never nudge them.
      if (n.fx !== null || n.fy !== null) continue
      const dx = n.x - cx
      const dy = n.y - cy
      // Normalized elliptical distance: == 1 on the field boundary, > 1
      // outside it. Squaring avoids a sqrt on the common in-field path.
      const nx = dx / fieldRadiusX
      const ny = dy / fieldRadiusY
      const norm2 = nx * nx + ny * ny
      if (norm2 <= 1 || norm2 === 0) continue
      // overshoot = how far past the boundary, as a fraction. norm is the
      // elliptical radius (1 at the boundary); (norm-1) is the fractional
      // overshoot, identical in spirit to the old (dist-R)/R.
      const norm = Math.sqrt(norm2)
      const overshoot = norm - 1
      // Spring strength scales with the overshoot fraction so a node just
      // past the field barely feels it, while a far-flung node is pulled
      // firmly back. k tuned to be gentle (founder: keep the organic feel
      // — no snapping). Push back along the true displacement (dx, dy) so
      // the restoring force points at the centre.
      const k = Math.min(0.25, overshoot) * alpha
      n.vx -= dx * k
      n.vy -= dy * k
    }
  }
  ;(force as unknown as { initialize: (n: LiveNode[]) => void }).initialize = (nextNodes) => {
    nodes = nextNodes
  }
  return force as ((alpha: number) => void) & {
    initialize: (n: LiveNode[]) => void
  }
}

interface ResizeBox {
  width: number
  height: number
}

function useContainerSize(): [React.RefObject<HTMLDivElement | null>, ResizeBox] {
  const ref = useRef<HTMLDivElement | null>(null)
  const [size, setSize] = useState<ResizeBox>({ width: 800, height: 480 })
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    // Capture the current measurements asynchronously so React's
    // strictness rule (no synchronous setState in an effect body)
    // is satisfied. The microtask delay is invisible to the user.
    if (typeof ResizeObserver === 'undefined') {
      const w = el.clientWidth || 800
      const h = el.clientHeight || 480
      const id = setTimeout(() => setSize({ width: w, height: h }), 0)
      return () => clearTimeout(id)
    }
    const ro = new ResizeObserver(([entry]) => {
      const r = entry?.contentRect
      if (!r) return
      setSize({
        width: Math.max(120, r.width),
        height: Math.max(120, r.height),
      })
    })
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, size]
}

/* ── ArchiMate marker definitions ───────────────────────────────── */

/**
 * Renders the actual <marker> body for a (kind, stroke) pair. Marker
 * geometry follows ArchiMate 3.x conventions: composition diamond
 * 12×7, aggregation diamond 12×7 hollow, assignment dot r=3, triggering
 * filled triangle 10×7, used-by open triangle 10×7, realization hollow
 * triangle 10×7, attached small open circle r=4.
 */
function MarkerBody({ kind, stroke }: { kind: EdgeMarker; stroke: string }) {
  if (!kind) return null
  const common = {
    markerUnits: 'strokeWidth' as const,
    orient: 'auto' as const,
  }
  switch (kind) {
    case 'composition':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={14}
          markerHeight={10}
          refX={11}
          refY={5}
          viewBox="0 0 14 10"
        >
          <polygon points="0,5 7,1 14,5 7,9" fill={stroke} stroke={stroke} strokeWidth={1} />
        </marker>
      )
    case 'aggregation':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={14}
          markerHeight={10}
          refX={11}
          refY={5}
          viewBox="0 0 14 10"
        >
          <polygon
            points="0,5 7,1 14,5 7,9"
            fill="#0b0d12"
            stroke={stroke}
            strokeWidth={1.4}
          />
        </marker>
      )
    case 'assignment-dot':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={8}
          markerHeight={8}
          refX={4}
          refY={4}
          viewBox="0 0 8 8"
        >
          <circle cx={4} cy={4} r={3} fill={stroke} />
        </marker>
      )
    case 'triggering':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={11}
          markerHeight={9}
          refX={10}
          refY={4.5}
          viewBox="0 0 11 9"
        >
          <polygon points="0,0 11,4.5 0,9" fill={stroke} />
        </marker>
      )
    case 'used-by':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={11}
          markerHeight={9}
          refX={10}
          refY={4.5}
          viewBox="0 0 11 9"
        >
          <polyline points="0,0 11,4.5 0,9" fill="none" stroke={stroke} strokeWidth={1.4} />
        </marker>
      )
    case 'realization':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={11}
          markerHeight={9}
          refX={10}
          refY={4.5}
          viewBox="0 0 11 9"
        >
          <polygon
            points="0,0 11,4.5 0,9"
            fill="#0b0d12"
            stroke={stroke}
            strokeWidth={1.4}
          />
        </marker>
      )
    case 'attached':
      return (
        <marker
          id={markerId(kind, stroke)}
          {...common}
          markerWidth={9}
          markerHeight={9}
          refX={7}
          refY={4.5}
          viewBox="0 0 9 9"
        >
          <circle cx={4.5} cy={4.5} r={3} fill="#0b0d12" stroke={stroke} strokeWidth={1.2} />
        </marker>
      )
    default:
      return null
  }
}

/* ── Component ───────────────────────────────────────────────────── */

export const GraphCanvas = forwardRef<GraphCanvasHandle, GraphCanvasProps>(function GraphCanvas(
  {
    nodes,
    edges,
    highlightedIds,
    focusNodeId = null,
    hiddenTypes,
    typeLimits,
    onNodeClick,
    onNodeDoubleClick,
    onNodeContextMenu,
    onCanvasContextMenu,
    onEdgeCreate,
    testIdPrefix = 'arch-graph',
    nodeColorFn,
    showLegend = false,
  },
  ref,
) {
  const [containerRef, size] = useContainerSize()

  // Live mutable maps survive across re-renders so D3-force can keep
  // its physics state. Wrapped in refs to avoid render-loop traps.
  const liveNodesRef = useRef<Map<string, LiveNode>>(new Map())
  const liveEdgesRef = useRef<Map<string, LiveEdge>>(new Map())
  const simRef = useRef<Simulation<LiveNode, LiveEdge> | null>(null)

  // Drag state (pin-on-drag + shift-drag-to-create-edge).
  const dragState = useRef<{
    nodeId: string | null
    startX: number
    startY: number
    movedPx: number
    shift: boolean
    /** The node the drag is currently over (for shift-drag edge creation). */
    overId: string | null
  }>({
    nodeId: null,
    startX: 0,
    startY: 0,
    movedPx: 0,
    shift: false,
    overId: null,
  })

  // Double-click detector — event.detail is unreliable across browsers.
  const lastClickRef = useRef<{ id: string | null; t: number }>({ id: null, t: 0 })

  /* ── Build / sync the visible (filtered) node + edge sets ─────── */

  // Apply hiddenTypes + typeLimits, then focusNodeId neighbor filter.
  const { visibleNodes, visibleEdges } = useMemo(() => {
    const hidden = hiddenTypes ?? new Set<ArchNodeType>()
    const limits = typeLimits ?? {}

    // 1. Type-filter and per-type limit.
    const typeCount = new Map<ArchNodeType, number>()
    const passId = new Set<string>()
    for (const n of nodes) {
      if (hidden.has(n.type)) continue
      const seen = typeCount.get(n.type) ?? 0
      const cap = limits[n.type]
      if (typeof cap === 'number' && seen >= cap) continue
      typeCount.set(n.type, seen + 1)
      passId.add(n.id)
    }

    // 2. Focus mode — keep only focus + direct neighbors.
    if (focusNodeId && passId.has(focusNodeId)) {
      const keep = new Set<string>([focusNodeId])
      for (const e of edges) {
        if (e.source === focusNodeId && passId.has(e.target)) keep.add(e.target)
        if (e.target === focusNodeId && passId.has(e.source)) keep.add(e.source)
      }
      // Replace passId with the focus set.
      for (const id of [...passId]) {
        if (!keep.has(id)) passId.delete(id)
      }
    }

    const visN = nodes.filter((n) => passId.has(n.id))
    const visE = edges.filter((e) => passId.has(e.source) && passId.has(e.target))
    return { visibleNodes: visN, visibleEdges: visE }
  }, [nodes, edges, hiddenTypes, typeLimits, focusNodeId])

  /* ── Sync incoming nodes/edges into liveNodes/liveEdges ───────── */

  // Re-runs whenever the visible set changes. Keeps existing
  // LiveNodes (preserving x/y/fx/fy) and only adds/drops as needed.
  useEffect(() => {
    const incomingIds = new Set(visibleNodes.map((n) => n.id))
    const liveNodes = liveNodesRef.current
    const liveEdges = liveEdgesRef.current

    // Drop nodes no longer present.
    for (const id of [...liveNodes.keys()]) {
      if (!incomingIds.has(id)) liveNodes.delete(id)
    }

    // Compute fresh degree.
    const degMap = computeDegree(visibleNodes, visibleEdges)

    // Add or update.
    const cx = size.width / 2
    const cy = size.height / 2
    for (const n of visibleNodes) {
      const existing = liveNodes.get(n.id)
      if (existing) {
        // Preserve x/y/fx/fy; refresh metadata + degree.
        existing.label = n.label
        existing.sublabel = n.sublabel
        existing.status = n.status
        existing.metadata = n.metadata
        existing.type = n.type
        existing.degree = degMap.get(n.id) ?? 0
      } else {
        // Seed at the CENTROID with a small jitter proportional to the
        // target field — small graphs start as a tight centred cluster,
        // never flung outward (#3958 constant-density seeding).
        const seedR = Math.min(60, physicsFor(visibleNodes.length, size.width, size.height).fieldRadius * 0.5)
        const ang = Math.random() * 2 * Math.PI
        const rad = Math.sqrt(Math.random()) * seedR
        liveNodes.set(n.id, {
          ...n,
          x: cx + Math.cos(ang) * rad,
          y: cy + Math.sin(ang) * rad,
          vx: 0,
          vy: 0,
          fx: null,
          fy: null,
          degree: degMap.get(n.id) ?? 0,
        })
      }
    }

    // Replace edges wholesale — they're cheap.
    liveEdges.clear()
    for (const e of visibleEdges) {
      liveEdges.set(e.id, { ...e })
    }

    // Build / re-tune the simulation — viewport-aware so the constant-
    // density field scales with √N and the actual canvas size.
    const phys = physicsFor(liveNodes.size, size.width, size.height)
    if (!simRef.current) {
      simRef.current = forceSimulation<LiveNode, LiveEdge>([...liveNodes.values()])
        .force('link', forceLink<LiveNode, LiveEdge>([]).id((n) => n.id))
        .force('charge', forceManyBody())
        .force('collide', forceCollide())
        .force('center', forceCenter(cx, cy))
        // Center-gravity forces — pull every individual node toward
        // (cx, cy). Without these, charge repulsion drifts nodes to
        // the canvas edge with nothing to oppose it, producing the
        // "bubbles escape to edges, center empty" failure mode on
        // small graphs (omantel.biz topology, ~25 nodes).
        .force('gravityX', forceX(cx).strength(0))
        .force('gravityY', forceY(cy).strength(0))
    }

    const sim = simRef.current!
    sim.nodes([...liveNodes.values()])
    const linkForce = sim.force('link') as
      | ReturnType<typeof forceLink<LiveNode, LiveEdge>>
      | undefined
    if (linkForce) {
      // d3-force expects SimulationLinkDatum<LiveNode>; LiveEdge is the
      // structural superset (it adds id/type) so the cast is safe.
      const linkData = [...liveEdges.values()] as unknown as SimulationLinkDatum<LiveNode>[]
      linkForce
        .links(linkData as unknown as LiveEdge[])
        // CLAMPED link distance — physicsFor already constrains
        // linkDistance to [minLink, maxLink]: minLink ≥ sum-of-radii+pad
        // (no overlap), maxLink caps edge-fling.
        .distance(Math.max(phys.minLink, Math.min(phys.maxLink, phys.linkDistance)))
        .strength(phys.linkStrength)
    }
    ;(sim.force('charge') as ReturnType<typeof forceManyBody>).strength(phys.charge)
    // Hard collision floor — radius is the per-node draw radius + the
    // physics collide pad, so no two bubbles ever overlap (#3958).
    ;(sim.force('collide') as ReturnType<typeof forceCollide>)
      .radius((d) => Math.max(NODE_R, radiusForDegree((d as LiveNode).degree)) + 4)
      .strength(0.9)
    ;(sim.force('center') as ReturnType<typeof forceCenter>).x(cx).y(cy)
    ;(sim.force('gravityX') as ReturnType<typeof forceX>).x(cx).strength(phys.centerGravity)
    ;(sim.force('gravityY') as ReturnType<typeof forceY>).y(cy).strength(phys.centerGravity)

    // Soft ELLIPTICAL containment — a GENTLE inward nudge for any node that
    // strays past the target field ellipse (~85% of width AND height). This
    // is the "stay in-field, don't fling to edges" force; the hard bound
    // clamp below is only the absolute safety net at the true viewport edge
    // (#3958 — fill the canvas on BOTH axes, never collapse to a short-dim
    // disc with empty left/right margin).
    sim.force('field', makeForceRadialCap(cx, cy, phys.fieldRadiusX, phys.fieldRadiusY))

    // Bounded-physics force — re-install every tick-tune so the box
    // matches the current container size. d3-force's `force()` setter
    // accepts a callable that exposes an `initialize(nodes)` hook;
    // we capture the latest size by closure (#348 item 5).
    sim.force('bound', makeForceBound(size.width, size.height, BOUND_PADDING))

    sim.alphaDecay(phys.alphaDecay).alphaTarget(0).alpha(0.7).restart()
  }, [visibleNodes, visibleEdges, size.width, size.height])

  /* ── Drive the render via the simulation tick ─────────────────── */

  const [, forceRender] = useState({})
  useEffect(() => {
    let raf = 0
    const tick = () => {
      forceRender({})
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [])

  /* ── Stop simulation on unmount ───────────────────────────────── */

  useEffect(() => {
    return () => {
      simRef.current?.stop()
      simRef.current = null
    }
  }, [])

  /* ── Imperative handle ────────────────────────────────────────── */

  useImperativeHandle(
    ref,
    (): GraphCanvasHandle => ({
      addElements(n, e) {
        // Caller is expected to pass FRESH ids; if any clash, the
        // existing live node wins.
        const liveNodes = liveNodesRef.current
        const liveEdges = liveEdgesRef.current
        const cx = size.width / 2
        const cy = size.height / 2
        for (const node of n) {
          if (liveNodes.has(node.id)) continue
          liveNodes.set(node.id, {
            ...node,
            x: cx + (Math.random() - 0.5) * 100,
            y: cy + (Math.random() - 0.5) * 100,
            vx: 0,
            vy: 0,
            fx: null,
            fy: null,
            degree: 0,
          })
        }
        for (const edge of e) {
          if (!liveEdges.has(edge.id)) liveEdges.set(edge.id, { ...edge })
        }
        simRef.current?.alpha(0.5).restart()
      },
      removeElements(ids) {
        const idSet = new Set(ids)
        const liveNodes = liveNodesRef.current
        const liveEdges = liveEdgesRef.current
        for (const id of idSet) liveNodes.delete(id)
        for (const [eid, e] of [...liveEdges.entries()]) {
          if (idSet.has(edgeNodeId(e.source)) || idSet.has(edgeNodeId(e.target))) {
            liveEdges.delete(eid)
          }
        }
        simRef.current?.alpha(0.5).restart()
      },
      unpinNode(id) {
        const n = liveNodesRef.current.get(id)
        if (!n) return
        n.fx = null
        n.fy = null
        simRef.current?.alpha(0.4).restart()
      },
      relax() {
        simRef.current?.alpha(0.7).restart()
      },
      fit() {
        // No camera transform — the whole graph already fills the
        // svg viewBox via CSS. fit() re-centers the simulation.
        const sim = simRef.current
        if (!sim) return
        const cx = size.width / 2
        const cy = size.height / 2
        ;(sim.force('center') as ReturnType<typeof forceCenter>).x(cx).y(cy)
        sim.alpha(0.3).restart()
      },
    }),
    [size.width, size.height],
  )

  /* ── Mouse handlers (drag-to-pin, shift-drag-to-create-edge) ──── */

  function svgPoint(ev: React.MouseEvent): { x: number; y: number } {
    const rect = (ev.currentTarget as SVGSVGElement).getBoundingClientRect()
    return { x: ev.clientX - rect.left, y: ev.clientY - rect.top }
  }

  function onMouseDownNode(ev: React.MouseEvent, n: LiveNode) {
    if (ev.button !== 0) return // left button only
    ev.stopPropagation()
    const p = svgPoint(ev)
    dragState.current = {
      nodeId: n.id,
      startX: p.x,
      startY: p.y,
      movedPx: 0,
      shift: ev.shiftKey,
      overId: null,
    }
    if (!ev.shiftKey) {
      // Standard drag-to-pin path: pin to current point.
      n.fx = n.x
      n.fy = n.y
    }
    simRef.current?.alphaTarget(0.3).restart()
  }

  function clampToBounds(p: { x: number; y: number }, r: number) {
    const minX = r + BOUND_PADDING
    const minY = r + BOUND_PADDING
    const maxX = Math.max(minX, size.width - r - BOUND_PADDING)
    const maxY = Math.max(minY, size.height - r - BOUND_PADDING)
    return {
      x: Math.max(minX, Math.min(maxX, p.x)),
      y: Math.max(minY, Math.min(maxY, p.y)),
    }
  }

  function onMouseMoveSvg(ev: React.MouseEvent) {
    const ds = dragState.current
    if (!ds.nodeId) return
    const liveNodes = liveNodesRef.current
    const n = liveNodes.get(ds.nodeId)
    if (!n) return
    const p = svgPoint(ev)
    const dx = p.x - ds.startX
    const dy = p.y - ds.startY
    ds.movedPx = Math.max(ds.movedPx, Math.hypot(dx, dy))

    if (ds.shift) {
      // Shift-drag — track the under-cursor node id; we draw a guide
      // line from source to the cursor in render below.
      let over: string | null = null
      for (const cand of liveNodes.values()) {
        if (cand.id === n.id) continue
        const r = radiusForDegree(cand.degree)
        const d = Math.hypot(cand.x - p.x, cand.y - p.y)
        if (d < r + 4) {
          over = cand.id
          break
        }
      }
      ds.overId = over
    } else {
      // Standard drag — pin the node to the cursor, clamped to canvas
      // bounds (#348 item 5).
      const r = radiusForDegree(n.degree)
      const clamped = clampToBounds(p, r)
      n.fx = clamped.x
      n.fy = clamped.y
    }
  }

  function onMouseUpSvg(ev: React.MouseEvent) {
    const ds = dragState.current
    if (!ds.nodeId) return
    const liveNodes = liveNodesRef.current
    const n = liveNodes.get(ds.nodeId)

    if (ds.shift && n && ds.overId && ds.overId !== n.id) {
      onEdgeCreate?.(n.id, ds.overId)
    }
    // Pinning persists post-drag (drag-to-pin contract). For
    // shift-drag we DON'T pin — the source stays unpinned.
    if (ds.shift && n) {
      n.fx = null
      n.fy = null
    }
    dragState.current = {
      nodeId: null,
      startX: 0,
      startY: 0,
      movedPx: 0,
      shift: false,
      overId: null,
    }
    simRef.current?.alphaTarget(0)

    // Suppress click if we actually dragged (>4px)
    if (ds.movedPx > 4) {
      ev.preventDefault()
      ev.stopPropagation()
    }
  }

  function onClickNode(ev: React.MouseEvent, n: LiveNode) {
    ev.stopPropagation()
    if (dragState.current.movedPx > 4) return // suppress synthetic clicks after a drag
    // ev.timeStamp is the DOMHighResTimeStamp the browser attached to
    // this event — pure relative to the event, not a fresh syscall.
    // This satisfies the no-impure-during-render rule while remaining
    // monotonically usable for the <400ms double-click window.
    const now = ev.timeStamp
    const last = lastClickRef.current
    if (last.id === n.id && now - last.t < 400) {
      onNodeDoubleClick?.(n)
      lastClickRef.current = { id: null, t: 0 }
      return
    }
    lastClickRef.current = { id: n.id, t: now }
    onNodeClick?.(n)
  }

  function onContextMenuNode(ev: React.MouseEvent, n: LiveNode) {
    ev.preventDefault()
    ev.stopPropagation()
    onNodeContextMenu?.(n, ev)
  }

  function onContextMenuSvg(ev: React.MouseEvent) {
    ev.preventDefault()
    onCanvasContextMenu?.(ev)
  }

  /* ── Render ───────────────────────────────────────────────────── */

  // Tap into liveNodesRef for the actual draw — these positions
  // change every animation frame.
  const liveNodes = [...liveNodesRef.current.values()]
  const liveEdgeArr: LiveEdge[] = [...liveEdgesRef.current.values()]
  const ds = dragState.current
  const draggingNode = ds.nodeId ? liveNodesRef.current.get(ds.nodeId) ?? null : null

  // Compute the unique marker defs the current edge set needs. We
  // memoise off `visibleEdges` (the upstream React-stable input)
  // rather than `liveEdgeArr` (a fresh array every animation frame) —
  // the marker palette depends purely on edge type, not on positions,
  // so this avoids re-allocating the same defs 60 times a second.
  const markerDefs = useMemo(
    () => uniqueMarkerDefs(visibleEdges),
    [visibleEdges],
  )

  return (
    <div
      ref={containerRef}
      data-testid={`${testIdPrefix}-canvas`}
      className="relative h-full w-full overflow-hidden rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)]"
    >
      <svg
        data-testid={`${testIdPrefix}-svg`}
        width={size.width}
        height={size.height}
        viewBox={`0 0 ${size.width} ${size.height}`}
        onMouseMove={onMouseMoveSvg}
        onMouseUp={onMouseUpSvg}
        onMouseLeave={onMouseUpSvg}
        onContextMenu={onContextMenuSvg}
        style={{ cursor: ds.nodeId ? 'grabbing' : 'default', userSelect: 'none' }}
      >
        <defs data-testid={`${testIdPrefix}-marker-defs`}>
          {markerDefs.map(({ kind, stroke }) => (
            <MarkerBody key={`${kind}-${stroke}`} kind={kind} stroke={stroke} />
          ))}
        </defs>

        {/* Edges first so they render under nodes. */}
        <g data-testid={`${testIdPrefix}-edges`}>
          {liveEdgeArr.map((e) => {
            const sId = edgeNodeId(e.source)
            const tId = edgeNodeId(e.target)
            const s = liveNodesRef.current.get(sId)
            const t = liveNodesRef.current.get(tId)
            if (!s || !t) return null
            const stroke = EDGE_STROKE[e.type as ArchEdgeType] ?? '#888'
            const dash = EDGE_DASHED[e.type as ArchEdgeType] ? '6,4' : undefined
            const startKind = EDGE_MARKER_START[e.type as ArchEdgeType]
            const endKind = EDGE_MARKER_END[e.type as ArchEdgeType]
            const markerStart = startKind ? `url(#${markerId(startKind, stroke)})` : undefined
            const markerEnd = endKind ? `url(#${markerId(endKind, stroke)})` : undefined
            return (
              <line
                key={e.id}
                data-testid={`${testIdPrefix}-edge-${e.id}`}
                data-edge-type={e.type}
                data-marker-start={startKind ?? ''}
                data-marker-end={endKind ?? ''}
                data-dashed={EDGE_DASHED[e.type as ArchEdgeType] ? 'true' : 'false'}
                x1={s.x}
                y1={s.y}
                x2={t.x}
                y2={t.y}
                stroke={stroke}
                strokeWidth={1.75}
                strokeOpacity={0.85}
                strokeDasharray={dash}
                markerStart={markerStart}
                markerEnd={markerEnd}
              />
            )
          })}
        </g>

        {/* Shift-drag guide line. */}
        {ds.shift && draggingNode && (
          <line
            data-testid={`${testIdPrefix}-edge-create-preview`}
            x1={draggingNode.x}
            y1={draggingNode.y}
            x2={ds.startX + (ds.movedPx > 0 ? Math.cos(0) : 0) * 0}
            y2={ds.startY + (ds.movedPx > 0 ? Math.sin(0) : 0) * 0}
            stroke="#fcc419"
            strokeWidth={1.6}
            strokeDasharray="4,3"
          />
        )}

        {/* Nodes — three independent visual channels (#3958):
            • FILL   (inside colour) = status   → STATUS_FILL
            • SHAPE  (polygon)       = category → NODE_CATEGORY
            • BORDER (stroke colour) = family   → FAMILY_BORDER
            NO icons. */}
        <g data-testid={`${testIdPrefix}-nodes`}>
          {liveNodes.map((n) => {
            const r = Math.max(NODE_R, radiusForDegree(n.degree))
            const category = NODE_CATEGORY[n.type] ?? 'compute'
            const family = NODE_FAMILY[n.type] ?? 'coreK8s'
            // FILL = status (nodeColorFn may override per-node).
            const fill = nodeColorFn?.(n) ?? statusFill(n.status)
            // BORDER = family (default). Overridden by highlight / focus
            // / pinned states below, in that priority order.
            const familyColor = FAMILY_BORDER[family] ?? '#64748b'
            let stroke = familyColor
            let strokeWidth = 2.5
            let dash: string | undefined
            if (highlightedIds?.has(n.id)) {
              stroke = '#fcc419'
              strokeWidth = 3.5
            } else if (focusNodeId && n.id === focusNodeId) {
              stroke = '#f06595'
              strokeWidth = 3.5
            } else if (n.fx !== null && n.fy !== null) {
              stroke = '#cbd5e1'
              strokeWidth = 2.5
              dash = '3,3'
            }

            const geom = shapeForCategory(category, r)

            return (
              <g
                key={n.id}
                data-testid={`${testIdPrefix}-node-${n.type}-${n.id}`}
                data-node-type={n.type}
                data-node-id={n.id}
                data-node-category={category}
                data-node-family={family}
                data-node-status={n.status ?? 'unknown'}
                data-pinned={n.fx !== null && n.fy !== null ? 'true' : 'false'}
                transform={`translate(${n.x}, ${n.y})`}
                onMouseDown={(ev) => onMouseDownNode(ev, n)}
                onClick={(ev) => onClickNode(ev, n)}
                onContextMenu={(ev) => onContextMenuNode(ev, n)}
                style={{ cursor: 'pointer' }}
                tabIndex={0}
                role="button"
                aria-label={`${n.label} — ${n.type} (${n.status ?? 'unknown'})`}
                onKeyDown={(ev) => {
                  if (ev.key === 'Enter' || ev.key === ' ') {
                    ev.preventDefault()
                    onNodeClick?.(n)
                  }
                }}
              >
                {geom.el === 'circle' ? (
                  <circle
                    data-testid={`${testIdPrefix}-node-shape-${n.type}`}
                    data-shape="circle"
                    r={geom.r}
                    fill={fill}
                    stroke={stroke}
                    strokeWidth={strokeWidth}
                    strokeDasharray={dash}
                  />
                ) : (
                  <polygon
                    data-testid={`${testIdPrefix}-node-shape-${n.type}`}
                    data-shape={category}
                    points={geom.points}
                    fill={fill}
                    stroke={stroke}
                    strokeWidth={strokeWidth}
                    strokeDasharray={dash}
                    strokeLinejoin="round"
                  />
                )}
                <text
                  y={r + 12}
                  textAnchor="middle"
                  fontSize={11}
                  fontWeight={500}
                  fill="var(--color-text)"
                  style={{ pointerEvents: 'none' }}
                >
                  {n.label.length > 24 ? n.label.slice(0, 23) + '…' : n.label}
                </text>
              </g>
            )
          })}
        </g>
      </svg>

      {/* Three-channel legend (#3958) — shape→category, border→family,
          fill→status. Opt-in via showLegend; the unified Cloud-graph
          turns it on. Collapsible so it never blocks the canvas. */}
      {showLegend && <GraphLegend testIdPrefix={testIdPrefix} />}

      {/* Stats overlay — bottom-left badges. */}
      <div
        data-testid={`${testIdPrefix}-stats`}
        className="pointer-events-none absolute bottom-2 left-2 flex gap-2"
      >
        <span
          data-testid={`${testIdPrefix}-stats-nodes`}
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)]/80 px-2 py-0.5 text-[11px] text-[var(--color-text-dim)]"
        >
          {liveNodes.length} nodes
        </span>
        <span
          data-testid={`${testIdPrefix}-stats-edges`}
          className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)]/80 px-2 py-0.5 text-[11px] text-[var(--color-text-dim)]"
        >
          {liveEdgeArr.length} edges
        </span>
      </div>
    </div>
  )
})
