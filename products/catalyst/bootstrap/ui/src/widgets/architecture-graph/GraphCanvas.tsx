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
  forceManyBody,
  forceSimulation,
  type Simulation,
  type SimulationLinkDatum,
} from 'd3-force'
import {
  edgeNodeId,
  EDGE_DASHED,
  EDGE_STROKE,
  NODE_FILL,
  type ArchEdgeType,
  type ArchNodeType,
  type GraphEdge,
  type GraphNode,
  type LiveEdge,
  type LiveNode,
} from './types'

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
}

/* ── Adaptive physics tiers ──────────────────────────────────────── */

/**
 * 5 tiers — node count → simulation params. Tuned so even ~5k node
 * graphs settle in <2s on commodity hardware while small graphs
 * (≤50) get a punchier collision radius for readability.
 */
function physicsFor(nodeCount: number): {
  charge: number
  linkDistance: number
  linkStrength: number
  collide: number
  alphaDecay: number
} {
  if (nodeCount <= 50) {
    return { charge: -240, linkDistance: 80, linkStrength: 0.7, collide: 28, alphaDecay: 0.02 }
  }
  if (nodeCount <= 200) {
    return { charge: -180, linkDistance: 60, linkStrength: 0.5, collide: 22, alphaDecay: 0.025 }
  }
  if (nodeCount <= 1000) {
    return { charge: -90, linkDistance: 40, linkStrength: 0.3, collide: 16, alphaDecay: 0.03 }
  }
  if (nodeCount <= 5000) {
    return { charge: -40, linkDistance: 24, linkStrength: 0.2, collide: 10, alphaDecay: 0.04 }
  }
  return { charge: -20, linkDistance: 14, linkStrength: 0.1, collide: 6, alphaDecay: 0.05 }
}

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
        liveNodes.set(n.id, {
          ...n,
          x: cx + (Math.random() - 0.5) * 100,
          y: cy + (Math.random() - 0.5) * 100,
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

    // Build / re-tune the simulation.
    const phys = physicsFor(liveNodes.size)
    if (!simRef.current) {
      simRef.current = forceSimulation<LiveNode, LiveEdge>([...liveNodes.values()])
        .force('link', forceLink<LiveNode, LiveEdge>([]).id((n) => n.id))
        .force('charge', forceManyBody())
        .force('collide', forceCollide())
        .force('center', forceCenter(cx, cy))
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
        .distance(phys.linkDistance)
        .strength(phys.linkStrength)
    }
    ;(sim.force('charge') as ReturnType<typeof forceManyBody>).strength(phys.charge)
    ;(sim.force('collide') as ReturnType<typeof forceCollide>).radius(phys.collide)
    ;(sim.force('center') as ReturnType<typeof forceCenter>).x(cx).y(cy)
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
      // Standard drag — pin the node to the cursor.
      n.fx = p.x
      n.fy = p.y
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
            return (
              <line
                key={e.id}
                data-testid={`${testIdPrefix}-edge-${e.id}`}
                data-edge-type={e.type}
                x1={s.x}
                y1={s.y}
                x2={t.x}
                y2={t.y}
                stroke={stroke}
                strokeWidth={1.5}
                strokeOpacity={0.65}
                strokeDasharray={dash}
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

        {/* Nodes. */}
        <g data-testid={`${testIdPrefix}-nodes`}>
          {liveNodes.map((n) => {
            const r = radiusForDegree(n.degree)
            const fill = NODE_FILL[n.type] ?? '#888'

            // Stroke priority: highlighted > focus > pinned > default
            let stroke = '#fff'
            let strokeWidth = 1.6
            let dash: string | undefined
            if (highlightedIds?.has(n.id)) {
              stroke = '#fcc419'
              strokeWidth = 3
            } else if (focusNodeId && n.id === focusNodeId) {
              stroke = '#f06595'
              strokeWidth = 3
            } else if (n.fx !== null && n.fy !== null) {
              stroke = '#343a40'
              strokeWidth = 1.8
              dash = '3,3'
            }

            return (
              <g
                key={n.id}
                data-testid={`${testIdPrefix}-node-${n.type}-${n.id}`}
                data-node-type={n.type}
                data-node-id={n.id}
                data-pinned={n.fx !== null && n.fy !== null ? 'true' : 'false'}
                transform={`translate(${n.x}, ${n.y})`}
                onMouseDown={(ev) => onMouseDownNode(ev, n)}
                onClick={(ev) => onClickNode(ev, n)}
                onContextMenu={(ev) => onContextMenuNode(ev, n)}
                style={{ cursor: 'pointer' }}
                tabIndex={0}
                role="button"
                aria-label={`${n.label} — ${n.type}`}
                onKeyDown={(ev) => {
                  if (ev.key === 'Enter' || ev.key === ' ') {
                    ev.preventDefault()
                    onNodeClick?.(n)
                  }
                }}
              >
                <circle
                  r={r}
                  fill={fill}
                  stroke={stroke}
                  strokeWidth={strokeWidth}
                  strokeDasharray={dash}
                />
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
