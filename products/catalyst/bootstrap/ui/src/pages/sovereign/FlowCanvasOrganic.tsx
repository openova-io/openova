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

/* ── Status palette ──────────────────────────────────────────────────
 *
 * Colours are read from CSS variables defined in globals.css so the
 * whole flow canvas reskins on `[data-theme="light"]` without touching
 * this file. Each status has a six-token surface (fill / ring / glyph
 * / glow / edge / arrow). Tokens are looked up via `var(--bubble-*)`
 * inline so hot-reload + theme flips work without re-mounting.
 */

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
    fill: 'var(--bubble-fill-succeeded)',
    ring: 'var(--bubble-ring-succeeded)',
    glyph: 'var(--bubble-glyph-succeeded)',
    glow: 'var(--bubble-glow-succeeded)',
    edge: 'var(--bubble-edge-succeeded)',
    arrow: 'var(--bubble-arrow-succeeded)',
    label: 'Succeeded',
  },
  running: {
    fill: 'var(--bubble-fill-running)',
    ring: 'var(--bubble-ring-running)',
    glyph: 'var(--bubble-glyph-running)',
    glow: 'var(--bubble-glow-running)',
    edge: 'var(--bubble-edge-running)',
    arrow: 'var(--bubble-arrow-running)',
    label: 'Running',
  },
  failed: {
    fill: 'var(--bubble-fill-failed)',
    ring: 'var(--bubble-ring-failed)',
    glyph: 'var(--bubble-glyph-failed)',
    glow: 'var(--bubble-glow-failed)',
    edge: 'var(--bubble-edge-failed)',
    arrow: 'var(--bubble-arrow-failed)',
    label: 'Failed',
  },
  pending: {
    fill: 'var(--bubble-fill-pending)',
    ring: 'var(--bubble-ring-pending)',
    glyph: 'var(--bubble-glyph-pending)',
    glow: 'var(--bubble-glow-pending)',
    edge: 'var(--bubble-edge-pending)',
    arrow: 'var(--bubble-arrow-pending)',
    label: 'Pending',
  },
}

/** SVG `<marker>` elements cannot read CSS variables directly inside
 *  attributes that the browser resolves at definition time (Chrome's
 *  marker resolution does not propagate `currentColor` through `<marker>`
 *  consistently). Concrete fallback colours used only inside the
 *  arrow-marker `<defs>` — bubble + edge strokes themselves use the
 *  CSS-variable tokens above. The marker fill is a small visual
 *  arrowhead and these fallbacks read acceptable on both themes. */
const ARROW_FALLBACK: Record<JobStatus, string> = {
  succeeded: '#16A34A',
  running:   '#38BDF8',
  failed:    '#B91C1C',
  pending:   '#94A3B8',
}

/** Issue #669 — bubble sizing decoupled from canvas size.
 *
 *  The previous design coupled bubble radius to the SVG viewBox: viewBox
 *  was capped at MAX_VBOX 1200×700 and `preserveAspectRatio="xMidYMid
 *  meet"` upscaled the entire content to fit the host. On full-screen,
 *  that upscale made bubbles bigger instead of giving the dependency
 *  chain more horizontal room.
 *
 *  Fix: viewBox = host pixel dimensions (driven by ResizeObserver), so
 *  NODE_RADIUS=40 means literally 40 CSS px on screen, regardless of
 *  host size. Extra horizontal canvas space becomes layout space along
 *  the dep chain.
 *
 *  No-overlap rule: the previous projection-scale fallback (`xScale`)
 *  compressed positions but not radii — guaranteed overlap on wide
 *  clusters. Removed entirely. forceCollide (radius NODE_RADIUS +
 *  COLLIDE_PADDING, strength 0.95, iterations 2) is now the single
 *  source of pairwise spacing, applied to the rendered positions
 *  directly. Wide clusters that don't fit get clamped at the viewBox
 *  edge by the per-tick clamp; forceCollide then resolves any
 *  edge-induced overlaps within the visible window.
 */
const NODE_RADIUS = 40
const GROUP_RADIUS = 48
const COLLIDE_PADDING = 12
/** Initial host dimensions used until ResizeObserver fires.
 *
 *  ResizeObserver emits asynchronously after mount, so on first paint
 *  we lay out against a sensible default rather than 0×0 (which would
 *  collapse every node onto the origin and break forceCollide's
 *  pairwise spacing). 1200×700 mirrors the previous MAX_VBOX seed —
 *  the first frame renders against that, then the observer's first
 *  callback corrects to the real host pixel rect within ~16ms. */
const MIN_HOST_W = 1200
const MIN_HOST_H = 700
/** Per-depth column width — wider than NODE_RADIUS*4 so adjacent-depth
 *  bubbles never visually touch. */
const PER_DEPTH_X = NODE_RADIUS * 4
/** Link distance — connected siblings settle ~100px apart, total
 *  on-canvas edge length stays <140px even with arrowhead trim. */
const LINK_DISTANCE = NODE_RADIUS * 2.5
/** Force strengths re-tuned post-#483. Gentle X-anchor lets the link
 *  force pull connected nodes together without the X-force fighting
 *  back and producing the oscillation that read as "infinite stretch". */
const FORCE_X_STRENGTH = 0.12
const FORCE_Y_STRENGTH = 0.10
const FORCE_LINK_STRENGTH = 0.18

/** Selection palette — distinct from any status colour AND distinct
 *  from each other so the host-vs-open semantic is unambiguous. */
const HOST_RING = '#14B8A6'        // teal — page owner
const SELECTION_RING = '#FBBF24'   // amber — currently-clicked-for-logs
const NEIGHBOR_RING = '#FCD34D'    // lighter amber — neighbour of selected

/* Sim node shape. */
type SimNode = SimulationNodeDatum & {
  id: string
  depth: number
  /** Issue #532 — global topological rank in [0, N-1] used as the Y
   *  target. Lower depRank = earlier in the dep chain (top of the
   *  canvas); higher = deeper (bottom of the canvas). */
  depRank: number
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
  const hostRef = useRef<HTMLDivElement | null>(null)
  const simRef = useRef<Simulation<SimNode, SimulationLinkDatum<SimNode>> | null>(
    null,
  )
  const nodesRef = useRef<Map<string, SimNode>>(new Map())
  const [tick, setTick] = useState(0)
  /* Issue #669 — track the canvas-host pixel dimensions so the
   * viewBox can equal them 1:1. Bubble radius (CSS px) then stays
   * constant regardless of host size; full-screening the canvas
   * gives the dep chain more horizontal room instead of magnifying
   * each bubble. */
  const [hostSize, setHostSize] = useState<{ w: number; h: number }>({
    w: MIN_HOST_W,
    h: MIN_HOST_H,
  })

  useEffect(() => {
    const el = hostRef.current
    if (!el) return
    // ResizeObserver may fire synchronously during layout — use rAF to
    // batch state updates so we never trigger a re-render mid-tick.
    let raf = 0
    const ro = new ResizeObserver((entries) => {
      const e = entries[0]
      if (!e) return
      const rect = e.contentRect
      // Use the actual measured rect — not a floor. The MIN_HOST_*
      // constants only apply when the rect is degenerate (0×0 during
      // first paint). Forcing the viewBox to MIN_HOST_W when the
      // host is narrower (e.g. LogPane reserves 30vw) causes the
      // SVG to render 1200 viewBox-units into 686 CSS px (0.57×
      // downscale), shrinking bubbles AND collapsing pairwise
      // distances below the no-overlap threshold.
      const w = Math.round(rect.width) || MIN_HOST_W
      const h = Math.round(rect.height) || MIN_HOST_H
      cancelAnimationFrame(raf)
      raf = requestAnimationFrame(() => {
        setHostSize((prev) => (prev.w === w && prev.h === h ? prev : { w, h }))
      })
    })
    ro.observe(el)
    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
    }
  }, [])
  // Issue #532 — mutable tick counter shared between the sim's tick
  // callback and the drag-handler effect. dragstart resets tickCount to
  // 0 so each drag gets a fresh MAX_TICKS budget; without this, after
  // the initial 2s freeze the sim is dead and a drag can't re-flow
  // neighbours out of the way. Stored on a ref so the drag handler can
  // mutate the value the sim's tick callback reads each frame.
  const tickCountRef = useRef<number>(0)

  // Issue #532 — regionYMid removed. The Y axis is now driven by
  // depRank (global topological-sort rank), not region centroids.
  // Regions still drive family/region badges in FlowNode but no longer
  // partition the canvas vertically — the dependency order does.

  /* Issue #669 — anchor depth 0 at a left margin instead of x=0 so the
   * grid-target sub-columns (centred on baseX with span ±SUB_COL_SPAN)
   * don't extend into negative X. With viewBox = host px starting at
   * 0,0 (preserveAspectRatio "xMinYMin meet"), negative coordinates
   * render off-canvas to the left. */
  const X_LEFT_MARGIN = NODE_RADIUS + PER_DEPTH_X / 2
  const depthToX = useCallback(
    (depth: number) => X_LEFT_MARGIN + depth * PER_DEPTH_X,
    [X_LEFT_MARGIN],
  )

  /* Issue #532 — resolve a per-node depRank.
   *
   * Real flowLayoutOrganic() output sets `depRank` on every node (a
   * dense topological-sort rank in [0, N-1]). Test fixtures that build
   * OrganicNode literals directly may omit it; when missing, we derive
   * a rank by sorting layout.nodes by (depth, original-index) so the
   * canvas still spreads them homogeneously by dep-order on Y. */
  const resolvedDepRank = useMemo(() => {
    const map = new Map<string, number>()
    const indexOf = new Map<string, number>()
    layout.nodes.forEach((n, i) => indexOf.set(n.id, i))
    const sorted = layout.nodes.slice().sort((a, b) => {
      if (a.depth !== b.depth) return a.depth - b.depth
      return (indexOf.get(a.id) ?? 0) - (indexOf.get(b.id) ?? 0)
    })
    sorted.forEach((n, i) => {
      // Prefer the layout-supplied depRank when present; fall back to
      // the derived rank otherwise.
      map.set(n.id, typeof n.depRank === 'number' ? n.depRank : i)
    })
    return map
  }, [layout.nodes])

  /* Issue #532 — homogeneous Y-spread by dependency rank.
   *
   * Founder verbatim 2026-05-02:
   *   "following the dependency order in the y axis they must
   *    homogenously spread considering the edge cases such as max
   *    bubble size max wire length etc."
   *
   * Map every node's `depRank` ∈ [0, N-1] to a Y target in
   *   [Y_MARGIN, MAX_VBOX_H - Y_MARGIN]
   * so the visible cluster fills the Y axis evenly regardless of node
   * count. With Y_MARGIN = NODE_RADIUS + COLLIDE_PADDING the bubble's
   * full diameter stays inside the viewBox. The minimum spacing
   * NODE_RADIUS*2 + COLLIDE_PADDING (= 92px) caps the maximum number
   * of fully-spread nodes at MAX_VBOX_H / 92 ≈ 7; beyond that, the
   * forceCollide guarantees pairwise spacing while siblings pack into
   * the available vertical band naturally. */
  const Y_MARGIN = NODE_RADIUS + COLLIDE_PADDING
  /* Issue #669 — Y-range driven by hostSize.h, not a hardcoded MAX_VBOX
   * ceiling. ResizeObserver on the canvas-host re-runs this whenever
   * the host pixel height changes (e.g. fullscreen, log-pane closed). */
  const Y_RANGE = Math.max(NODE_RADIUS * 2, hostSize.h - Y_MARGIN * 2)
  const totalNodes = layout.nodes.length
  const yForDepRank = useCallback(
    (depRank: number) => {
      if (totalNodes <= 1) return Y_MARGIN + Y_RANGE / 2
      const t = depRank / (totalNodes - 1)
      return Y_MARGIN + t * Y_RANGE
    },
    [totalNodes, Y_RANGE, Y_MARGIN],
  )

  const familyById = useMemo(() => {
    const m = new Map<string, OrganicFamily>()
    for (const f of layout.families) m.set(f.id, f)
    return m
  }, [layout.families])

  /* ── Issue #493 + #532 — depth-bucket homogeneous-spread pre-pass ──
   *
   * The real OpenOva provisioning graph has one parent ("Applications")
   * with 50+ blueprint-install children at the same depth. Issue #532
   * (founder verbatim 2026-05-02) requires those siblings to spread
   * homogeneously on the Y axis — i.e. fill the full vertical range,
   * never stack into a 92px-pitch column that overflows the viewBox.
   *
   * For each depth bucket whose sibling count exceeds the single-column
   * capacity, distribute the siblings evenly across the full Y range
   * [Y_MARGIN, MAX_VBOX_H - Y_MARGIN] using a per-sibling fraction:
   *
   *   ty(i) = Y_MARGIN + (i / (count - 1)) * Y_RANGE
   *
   * Add a small alternating X jitter (±SUB_COL_SPAN/2) so consecutive
   * siblings don't sit on the exact same X — the link force then has
   * room to settle without producing the overlap pattern that the
   * naive grid layout caused. forceCollide takes over the final
   * pairwise spacing.
   */
  const gridTargets = useMemo(() => {
    type GridCell = {
      tx: number // absolute target X in layout coordinates
      ty: number // absolute target Y in layout coordinates
      totalCols: number
      totalRows: number
    }
    const ROW_PITCH = NODE_RADIUS * 2 + COLLIDE_PADDING
    const Y_MARGIN_LOCAL = NODE_RADIUS + COLLIDE_PADDING
    const Y_RANGE_LOCAL = Math.max(NODE_RADIUS * 2, hostSize.h - Y_MARGIN_LOCAL * 2)
    // How many bubbles fit vertically with the no-overlap collision
    // pitch (NODE_RADIUS*2 + COLLIDE_PADDING = 92px on 700px viewBox →
    // 7 rows). Beyond that, we MUST add sub-columns or bubbles overlap.
    const COL_CAPACITY = Math.max(1, Math.floor(Y_RANGE_LOCAL / ROW_PITCH))
    const buckets = new Map<number, OrganicNode[]>()
    for (const n of layout.nodes) {
      let bucket = buckets.get(n.depth)
      if (!bucket) {
        bucket = []
        buckets.set(n.depth, bucket)
      }
      bucket.push(n)
    }
    const cells = new Map<string, GridCell>()
    for (const [depth, bucket] of buckets) {
      // Only apply grid layout when sibling count exceeds the single-
      // column vertical capacity. Sparse depths keep the original
      // force-anchor behaviour (depthX + depRank-based Y).
      if (bucket.length <= COL_CAPACITY) continue
      // Issue #669 — anchor at depthToX (NODE_RADIUS + PER_DEPTH_X/2 +
      // depth * PER_DEPTH_X) so sub-columns centred on baseX never
      // extend into negative X (which would render off-canvas under
      // the new viewBox = host-px convention).
      const baseX = X_LEFT_MARGIN + depth * PER_DEPTH_X
      // Issue #532: with N siblings in a depth bucket and Y-range
      // budget that fits COL_CAPACITY rows at the no-overlap pitch,
      // we need ceil(N / COL_CAPACITY) sub-columns. Each sub-column
      // contains COL_CAPACITY rows distributed homogeneously across
      // the full Y range. This guarantees no overlap by construction
      // (forceCollide is then a safety net for boundary effects).
      const totalCols = Math.max(1, Math.ceil(bucket.length / COL_CAPACITY))
      const totalRows = Math.ceil(bucket.length / totalCols)
      // Sub-column span — each sub-column gets a slice of the depth
      // column's natural width. Cap at PER_DEPTH_X so adjacent depth
      // columns never visually merge.
      const SUB_COL_SPAN = PER_DEPTH_X * 0.8
      const colStep = totalCols > 1 ? SUB_COL_SPAN / (totalCols - 1) : 0
      // Issue #532 founder verbatim: "homogenously spread". Distribute
      // rows evenly across the full Y range (not packed at the top).
      const rowStep = totalRows > 1
        ? Y_RANGE_LOCAL / (totalRows - 1)
        : 0
      bucket.forEach((n, idx) => {
        // Column-major fill: idx 0..(totalRows-1) fill column 0 top→
        // bottom, then idx totalRows..(2*totalRows-1) fill column 1,
        // etc. This keeps consecutive siblings (often related in the
        // upstream order) clustered together rather than scattered.
        const subCol = Math.floor(idx / totalRows)
        const subRow = idx % totalRows
        const colOffset = totalCols > 1
          ? (subCol - (totalCols - 1) / 2) * colStep
          : 0
        const ty = totalRows > 1
          ? Y_MARGIN_LOCAL + subRow * rowStep
          : Y_MARGIN_LOCAL + Y_RANGE_LOCAL / 2
        cells.set(n.id, {
          tx: baseX + colOffset,
          ty,
          totalCols,
          totalRows,
        })
      })
    }
    return cells
  }, [layout.nodes, hostSize.h, X_LEFT_MARGIN])

  const simNodes = useMemo<SimNode[]>(() => {
    const next: SimNode[] = []
    const seen = new Set<string>()
    for (const n of layout.nodes) {
      seen.add(n.id)
      const rank = resolvedDepRank.get(n.id) ?? 0
      const existing = nodesRef.current.get(n.id)
      if (existing) {
        existing.depth = n.depth
        existing.depRank = rank
        existing.regionId = n.regionId
        existing.familyId = n.familyId
        existing.status = n.status
        existing.isGroup = n.isGroup
        next.push(existing)
      } else {
        const baseX = depthToX(n.depth)
        // Issue #532 — initial Y comes from either the high-fan-out
        // grid pre-pass (cell.ty is now absolute, homogeneous-spread
        // Y target for the depth bucket) or the depRank ladder for
        // sparse depths.
        const seed = hashSeed(n.id)
        const cell = gridTargets.get(n.id)
        const initX = cell ? cell.tx : baseX + (seed.fx - 0.5) * NODE_RADIUS * 1.5
        // For sparse-depth nodes, small Y jitter so two nodes with
        // identical depRank don't start at literally the same pixel —
        // the collision force then separates them deterministically.
        const initY = cell
          ? cell.ty
          : yForDepRank(rank) + (seed.fy - 0.5) * NODE_RADIUS * 0.6
        const fresh: SimNode = {
          id: n.id,
          depth: n.depth,
          depRank: rank,
          regionId: n.regionId,
          familyId: n.familyId,
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
  }, [layout.nodes, depthToX, yForDepRank, gridTargets, resolvedDepRank])

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
    // Issue #481 round 3 (2026-05-02): the simulation must converge in ≤2s
    // after page load and STOP dynamically rebuilding. With the default
    // alphaDecay=0.025 the sim runs ~300 ticks to alphaMin=0.001 (≈5s of
    // visible motion). alphaDecay=0.06 + alphaMin=0.01 brings it to ≈60
    // ticks (~1s at 60fps), and a hard MAX_TICKS guard freezes positions
    // even on slow devices that can't hit 60fps.
    //
    // Issue #532 — tickCount is stored on tickCountRef so the drag
    // handler effect can reset it to 0 on dragstart. After a drag,
    // each MAX_TICKS budget begins fresh and the sim has time to
    // re-flow neighbours away from the pinned drop position before
    // freezing again.
    const MAX_TICKS = 120
    tickCountRef.current = 0
    const sim = forceSimulation<SimNode>(simNodes)
      .alpha(0.9)
      .alphaDecay(0.06)
      .alphaMin(0.01)
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
          .x((d) => {
            // Issue #493 — high-fan-out depth buckets have a sub-column
            // X target. Sparse depths fall through to the depth anchor.
            const cell = gridTargets.get(d.id)
            return cell ? cell.tx : depthToX(d.depth)
          })
          .strength(FORCE_X_STRENGTH),
      )
      .force(
        'y',
        forceY<SimNode>()
          .y((d) => {
            // Issue #532 — Y target. High-fan-out depth buckets get
            // a homogeneous-spread Y from the gridTargets pre-pass
            // (cell.ty is absolute). Sparse depths fall through to
            // the depRank ladder so depth-of-dep order reads as
            // top → bottom.
            const cell = gridTargets.get(d.id)
            if (cell) return cell.ty
            return yForDepRank(d.depRank)
          })
          .strength(FORCE_Y_STRENGTH),
      )
      .force(
        'link',
        forceLink<SimNode, SimulationLinkDatum<SimNode>>(links)
          .id((d) => d.id)
          // Bug #481 follow-up — gentle link force (0.18) settles
          // connected siblings around LINK_DISTANCE (100px) without
          // fighting the X-anchor force (0.12) that keeps each node in
          // its depth column. Edges stay <140px at steady state.
          .distance(LINK_DISTANCE)
          .strength(FORCE_LINK_STRENGTH),
      )
      .on('tick', () => {
        // Bug #481 — post-tick bounding box clamp.
        // Issue #493 — when a node has a grid cell, clamp around the
        // (cell.tx, baseY+cell.ty) target instead of (depthX, regionYMid)
        // so high-fan-out siblings stay in their assigned sub-row.
        // Issue #532 — pinned nodes (fx/fy set by drag) MUST NOT be
        // clamped: the operator dragged them to a specific position
        // and that position has to be respected, even if it falls
        // outside the depth-anchor window.
        for (const n of simNodes) {
          // Skip clamping for pinned (dragged) nodes — d3-force already
          // snaps n.x/n.y to n.fx/n.fy each tick, but we must not let
          // our depth-window clamp override the operator's chosen pin.
          if (typeof n.fx === 'number' && typeof n.fy === 'number') continue
          const cell = gridTargets.get(n.id)
          if (cell) {
            // Issue #532 — sub-grid clamping. Each sibling has an
            // absolute (tx, ty) target inside the depth column's
            // sub-grid. The clamp window is half the sub-column span
            // wide and half the row step tall so adjacent siblings
            // can settle without invading each other's slots, but
            // the slot itself is large enough that forceCollide can
            // resolve any tiny overlaps inside the slot without
            // pushing the node outside.
            const SUB_COL_SPAN = PER_DEPTH_X * 0.8
            const colSlot = cell.totalCols > 1
              ? SUB_COL_SPAN / (cell.totalCols - 1)
              : PER_DEPTH_X
            const Y_MARGIN_LOCAL = NODE_RADIUS + COLLIDE_PADDING
            const Y_RANGE_LOCAL = Math.max(NODE_RADIUS * 2, hostSize.h - Y_MARGIN_LOCAL * 2)
            const rowSlot = cell.totalRows > 1
              ? Y_RANGE_LOCAL / (cell.totalRows - 1)
              : Y_RANGE_LOCAL
            const xMin = cell.tx - colSlot * 0.5
            const xMax = cell.tx + colSlot * 0.5
            const yMin = cell.ty - rowSlot * 0.5
            const yMax = cell.ty + rowSlot * 0.5
            if (typeof n.x === 'number') {
              if (n.x < xMin) n.x = xMin
              else if (n.x > xMax) n.x = xMax
            }
            if (typeof n.y === 'number') {
              if (n.y < yMin) n.y = yMin
              else if (n.y > yMax) n.y = yMax
            }
            continue
          }
          const baseX = depthToX(n.depth)
          /* Sparse-depth fallback — clamp X around the depth column,
           * Y around the dep-rank slot ± half the collision pitch so
           * siblings of identical depRank can still separate via the
           * forceCollide pass without escaping the rank-ordered band.
           *
           * Issue #669 — also clamp X+Y inside hostSize so no node
           * settles outside the viewBox. forceCollide is the no-
           * overlap guarantee, but it only resolves overlaps inside
           * the simulated coordinate space; if sim positions are
           * outside the viewBox, post-render clamping cannot recover
           * pairwise spacing. Keep the sim inside the visible window
           * end-to-end. */
          const xMin = Math.max(NODE_RADIUS, baseX - PER_DEPTH_X)
          const xMax = Math.min(hostSize.w - NODE_RADIUS, baseX + PER_DEPTH_X)
          if (typeof n.x === 'number') {
            if (n.x < xMin) n.x = xMin
            else if (n.x > xMax) n.x = xMax
          }
          const targetY = yForDepRank(n.depRank)
          const Y_HALF_BAND = (NODE_RADIUS * 2 + COLLIDE_PADDING)
          const yMin = Math.max(NODE_RADIUS, targetY - Y_HALF_BAND)
          const yMax = Math.min(hostSize.h - NODE_RADIUS, targetY + Y_HALF_BAND)
          if (typeof n.y === 'number') {
            if (n.y < yMin) n.y = yMin
            else if (n.y > yMax) n.y = yMax
          }
        }
        // Issue #481 round 3 + #532: hard MAX_TICKS cap. Even if
        // alpha/velocity decay schedules don't drive alpha below alphaMin
        // in time (slow device, many nodes), this stops the sim
        // deterministically at ~2s and freezes positions. The counter
        // resets on dragstart so each drag gets a fresh 2s budget to
        // re-flow neighbours out of the way of the pinned bubble.
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
  }, [simNodes, layout.edges, depthToX, yForDepRank, gridTargets, hostSize.h, hostSize.w])

  const nodeIdsKey = simNodes.map((n) => n.id).join(',')
  useEffect(() => {
    if (!svgRef.current) return
    const sim = simRef.current
    if (!sim) return

    const dragBehavior = d3drag<SVGGElement, unknown>()
      .on('start', function (event) {
        // Issue #532 — d81effc2 stops the sim permanently after
        // MAX_TICKS=120 (~2s). Without resetting the tick counter
        // here, the sim is dead by the time the operator drags and
        // neighbours can't move out of the way. Reset the counter to
        // 0 so the post-drag MAX_TICKS budget restarts from scratch,
        // then re-heat with alphaTarget(0.3) so the simulation
        // actually runs.
        tickCountRef.current = 0
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
        // Issue #532 — DO NOT clear d.fx/d.fy here. Pinned nodes stay
        // pinned forever (until the next drag) so the operator's
        // chosen position is respected. forceCollide guarantees the
        // pinned bubble never overlaps a free-moving neighbour;
        // free-moving neighbours yield around the pin during the
        // post-drag re-heat phase.
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
        ref={hostRef}
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

  /* Issue #669 — viewBox = host pixel dimensions (1:1).
   *
   * Bubble radius (NODE_RADIUS=40 in viewBox units) renders at exactly
   * 40 CSS px on screen because the viewBox now equals the host's
   * pixel rect — there is no preserveAspectRatio scale. Full-screening
   * the canvas grows hostSize.w / hostSize.h, which gives the
   * dependency chain more layout room along x without changing bubble
   * size. The preserveAspectRatio default is "xMidYMid meet", which is
   * a no-op when viewBox dims = SVG dims.
   *
   * The previous bbox-then-scale-then-clamp pipeline actively caused
   * overlap on wide clusters because position scaling did not also
   * scale the rendered radius. forceCollide's pairwise distance
   * guarantee then no longer held in screen space. Removed.
   *
   * For wide clusters that don't fit in hostSize.w (deep dep chains),
   * the per-tick clamp at line 540-606 already pulls nodes back inside
   * the viewBox window, and forceCollide resolves resulting clusters
   * within the visible area. We do NOT compress positions; we let the
   * sim clamp positions, which preserves the no-overlap guarantee.
   */
  const vbX = 0
  const vbY = 0
  const vbW = hostSize.w
  const vbH = hostSize.h

  /* Hard-clamp positions to viewBox. The per-tick clamp inside the sim
   * also clamps, but ResizeObserver may shrink hostSize between sim
   * ticks (drag of the LogPane); applying the clamp here ensures
   * rendering never paints a bubble outside the visible area, even
   * one frame. forceCollide already guarantees pairwise spacing of
   * ≥ NODE_RADIUS*2 + COLLIDE_PADDING (= 92 px), and clamping to a
   * single edge cannot reduce two clamped nodes' distance below that
   * threshold (the collide pass owns it pre-render). */
  const CLAMP_INSET = NODE_RADIUS + 8
  const project = (p: { x: number; y: number }) => {
    const x = Math.min(vbW - CLAMP_INSET, Math.max(CLAMP_INSET, p.x))
    const y = Math.min(vbH - CLAMP_INSET, Math.max(CLAMP_INSET, p.y))
    return { x, y }
  }
  const renderPos = new Map<string, { x: number; y: number }>()
  for (const [id, p] of livePos) {
    renderPos.set(id, project(p))
  }

  return (
    <div
      ref={hostRef}
      data-testid="flow-canvas-svg-host"
      style={{
        position: 'relative',
        width: '100%',
        height: '100%',
        minHeight: 0,
        minWidth: 0,
        overflow: 'hidden',
      }}
    >
    <svg
      ref={svgRef}
      width="100%"
      height="100%"
      viewBox={`${vbX} ${vbY} ${vbW} ${vbH}`}
      preserveAspectRatio="xMinYMin meet"
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
            <path d="M0,1 L9,5 L0,9 Z" fill={ARROW_FALLBACK[s]} opacity="0.92" />
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
        // Bug #481 — use clamped renderPos, not raw livePos. Edges
        // between clamped endpoints are bounded by the viewBox
        // diagonal so "kilometers of edges" is structurally impossible.
        const s = renderPos.get(e.fromId)
        const t = renderPos.get(e.toId)
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
        // Bug #481 — render at clamped position so no bubble ever sits
        // outside the viewBox.
        const pos = renderPos.get(node.id)
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
    </div>
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
  // Inner ring priority — drawn on the bubble itself:
  //   selection (amber) > neighbour (lighter amber) > status tone
  // The host's teal ring is rendered SEPARATELY as a thicker outer
  // halo so the host stays distinguishable even when it's also the
  // currently-selected job (the original bug: amber selection
  // overrode the teal host ring on the page's home job).
  const innerRing = isOpen
    ? SELECTION_RING
    : isNeighbor
      ? NEIGHBOR_RING
      : isHost
        ? HOST_RING
        : tone.ring
  const familyColor = family?.color ?? 'rgba(148,163,184,0.55)'
  const radius = node.isGroup ? GROUP_RADIUS : NODE_RADIUS
  const grpStyle: CSSProperties = { cursor: 'grab' }
  const groupOpacity = isDimmed ? 0.35 : 1
  const innerWidth = isOpen ? 4 : isNeighbor ? 3 : isHost ? 3.5 : 2

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
        {`${node.label} — ${tone.label}${isHost ? ' · home' : ''}${node.subLabel ? ` · ${node.subLabel}` : ''}`}
      </title>

      {/* Glow underlay — host wins (teal) when also selected so the
          home node always reads as the page anchor. Otherwise:
          selection > neighbour > status. */}
      {isHost ? (
        <circle r={radius + 12} fill="rgba(20,184,166,0.30)" />
      ) : isOpen ? (
        <circle r={radius + 10} fill="rgba(251,191,36,0.30)" />
      ) : isNeighbor ? (
        <circle r={radius + 8} fill="rgba(252,211,77,0.18)" />
      ) : node.status === 'running' || node.status === 'failed' ? (
        <circle r={radius + 8} fill={tone.glow} />
      ) : null}

      {/* HOST halo — always rendered on the page's home job, sits
          OUTSIDE the inner status/selection ring so it survives the
          amber selection ring without being overdrawn. Extra-thick
          stroke so it reads as a halo, not a regular ring. */}
      {isHost ? (
        <circle
          r={radius + 6}
          fill="none"
          stroke={HOST_RING}
          strokeWidth={3.5}
          opacity={0.95}
        />
      ) : null}

      {/* Family-coloured ring (thin) */}
      <circle
        r={radius + 2}
        fill="none"
        stroke={familyColor}
        strokeWidth={node.isGroup ? 2.5 : 1}
        opacity={0.55}
      />

      {/* Status fill + selection / neighbour / status ring overlay
          (the host's distinguishing teal ring is the halo above —
          this inner ring keeps the operator informed about the
          job's runtime status + currently-clicked state). */}
      <circle
        r={radius}
        fill={tone.fill}
        stroke={innerRing}
        strokeWidth={innerWidth}
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

      {/* Label below bubble — routed through CSS var so it flips on
          [data-theme="light"] (issue #669). */}
      <text
        x={0}
        y={radius + 14}
        textAnchor="middle"
        fontSize={10}
        fill="var(--bubble-label)"
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
          fill="var(--bubble-sublabel)"
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
