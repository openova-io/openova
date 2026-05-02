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

/** Bug #481 follow-up — the previous fix (#483) over-corrected.
 *  Symptoms after #483: bubbles invisible at default zoom, edges
 *  appeared to stretch infinitely. Root causes:
 *    (1) NODE_RADIUS was still 22 → diameter 44 → at MAX_VBOX 1600
 *        scale 0.4-0.5 in a 600-800px canvas-host (LogPane covers half
 *        the screen), bubbles rendered ~16-22px wide. Effectively
 *        invisible to the operator.
 *    (2) MIN_VBOX_W/H floors at 1200×700 forced sparse graphs (4-6
 *        nodes spread across 200×100 of layout space) into a viewBox
 *        6× bigger than the cluster — bubbles shrank to specks.
 *    (3) FORCE_X_STRENGTH=0.55 + FORCE_LINK_STRENGTH=0.45 fought hard
 *        on graphs with depth-disparate dependencies (depth 0 root
 *        wired to depth-5 leaf), causing oscillation that read as
 *        "infinitely stretching" in mid-tick frames.
 *
 *  The fix:
 *    • NODE_RADIUS 22 → 40 (diameter 80px — meets acceptance: bubble
 *      ≥80px wide on 1440×900 viewport).
 *    • MIN_VBOX dropped to a small floor (400×280) so sparse graphs
 *      render at native scale without the SVG zooming out to fit a
 *      pretend 1200×700 canvas.
 *    • MAX_VBOX 1600×900 → 1200×700 so even on full-screen canvas the
 *      effective render scale stays close to 1:1.
 *    • Force strengths balanced: X=0.12, Y=0.10, link=0.18 — gentle
 *      enough not to oscillate, strong enough to converge.
 *    • LINK_DISTANCE = NODE_RADIUS * 2.5 = 100px → connected siblings
 *      stay <140px apart at steady state, well under the 300px
 *      acceptance ceiling.
 *    • Per-tick X clamp tightened to ±PER_DEPTH_X (was ±1.5×) so depth
 *      anchoring stays disciplined.
 */
const NODE_RADIUS = 40
const GROUP_RADIUS = 48
const COLLIDE_PADDING = 12
/** Floor for very-sparse graphs — enough room for 1-3 nodes without
 *  the SVG collapsing to a single point. Anything denser uses the
 *  measured cluster bbox + padding. */
const MIN_VBOX_W = 400
const MIN_VBOX_H = 280
const VIEW_H = 800
/** MAX_VBOX matters because preserveAspectRatio "meet" scales the
 *  viewBox to fit the canvas-host. With MAX 1200×700, on a 1200px-wide
 *  host scale=1.0 (bubble = 80px). On a 600px host scale=0.5 (bubble =
 *  40px — still readable, vs the previous 22px). */
const MAX_VBOX_W = 1200
const MAX_VBOX_H = 700
/** Per-depth column width — wider than NODE_RADIUS*4 so adjacent-depth
 *  bubbles never visually touch. */
const PER_DEPTH_X = NODE_RADIUS * 4
/** Vertical scatter on first paint and inside the soft `forceY`.
 *  Tightened with the larger NODE_RADIUS so siblings stack cleanly
 *  instead of drifting beyond the visible cluster. */
const Y_SCATTER_PX = 80
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

  const depthToX = useCallback(
    (depth: number) => depth * PER_DEPTH_X,
    [],
  )

  const familyById = useMemo(() => {
    const m = new Map<string, OrganicFamily>()
    for (const f of layout.families) m.set(f.id, f)
    return m
  }, [layout.families])

  /* ── Issue #493 — depth-bucket grid pre-pass ─────────────────────
   *
   * The real OpenOva provisioning graph has one parent ("Applications")
   * with 50+ blueprint-install children at the same depth. Pre-fix the
   * simulation anchored every sibling to a single depthX coordinate
   * with Y clamp ±Y_SCATTER_PX*2 (±160). With 50 nodes × 92px collision
   * pitch the cluster wanted to grow 4600px tall, but viewBox MAX_VBOX_H
   * was clamped to 700; only ~15% of node centroids landed in view.
   * Live screenshot evidence: .playwright-mcp/otech9-cluster-bootstrap-2026-05-01.png
   *
   * Fix: when a depth bucket exceeds the vertical capacity of the
   * viewBox (~7 nodes at MAX_VBOX_H=700), lay siblings out in a
   * sub-column grid (multiple sub-columns within the depth column).
   * The simulation then anchors each node to its (subColX, subRowY)
   * grid target instead of the shared depthX/regionYMid pair.
   */
  const gridTargets = useMemo(() => {
    type GridCell = {
      tx: number // target X in layout coordinates
      ty: number // target Y RELATIVE to regionYMid
      totalCols: number
      totalRows: number
    }
    const ROW_PITCH = NODE_RADIUS * 2 + COLLIDE_PADDING
    const Y_BUDGET = MAX_VBOX_H - (NODE_RADIUS * 2 + 60)
    const COL_CAPACITY = Math.max(1, Math.floor(Y_BUDGET / ROW_PITCH))
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
      // Only apply grid layout when sibling count exceeds a single-
      // column capacity. Sparse depths keep the original force-anchor
      // behaviour (depthX + jittered Y inside region centroid).
      if (bucket.length <= COL_CAPACITY) continue
      const totalCols = Math.max(1, Math.ceil(bucket.length / COL_CAPACITY))
      const totalRows = Math.ceil(bucket.length / totalCols)
      const baseX = depth * PER_DEPTH_X
      const SUB_COL_SPAN = PER_DEPTH_X * 0.8
      const colStep = totalCols > 1 ? SUB_COL_SPAN / (totalCols - 1) : 0
      const rowStep = ROW_PITCH
      bucket.forEach((n, idx) => {
        const subCol = idx % totalCols
        const subRow = Math.floor(idx / totalCols)
        const colOffset = (subCol - (totalCols - 1) / 2) * colStep
        const rowOffset = (subRow - (totalRows - 1) / 2) * rowStep
        cells.set(n.id, {
          tx: baseX + colOffset,
          ty: rowOffset,
          totalCols,
          totalRows,
        })
      })
    }
    return cells
  }, [layout.nodes])

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
        // Issue #493 — seed initial X/Y from the precomputed grid cell
        // when one exists (high-fan-out depth buckets). Otherwise fall
        // back to depth-anchor + jitter for sparse layouts.
        const cell = gridTargets.get(n.id)
        const initX = cell ? cell.tx : baseX + (seed.fx - 0.5) * NODE_RADIUS * 1.5
        const initY = cell
          ? baseY + cell.ty
          : baseY + (seed.fy - 0.5) * Y_SCATTER_PX * 2
        const fresh: SimNode = {
          id: n.id,
          depth: n.depth,
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
  }, [layout.nodes, depthToX, regionYMid, gridTargets])

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
            const base = regionYMid.get(d.regionId) ?? VIEW_H / 2
            // Issue #493 — high-fan-out depth buckets get a sub-row Y
            // offset relative to regionYMid. Sparse depths still get
            // the seeded jitter so they don't visually align in a row.
            const cell = gridTargets.get(d.id)
            if (cell) return base + cell.ty
            const seed = hashSeed(d.id)
            // Bug #481 — clamp the per-node Y target inside ±Y_SCATTER_PX
            // so the soft `forceY` cannot stretch the graph into a
            // kilometers-tall column. Previous value (±180) was the
            // root of the "scattered between left + right panes" symptom.
            return base + (seed.fy - 0.5) * Y_SCATTER_PX * 2
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
        for (const n of simNodes) {
          const cell = gridTargets.get(n.id)
          const baseY = regionYMid.get(n.regionId) ?? VIEW_H / 2
          if (cell) {
            const ROW_PITCH = NODE_RADIUS * 2 + COLLIDE_PADDING
            const SUB_COL_SPAN = PER_DEPTH_X * 0.8
            const colSlot = cell.totalCols > 1
              ? SUB_COL_SPAN / (cell.totalCols - 1)
              : PER_DEPTH_X
            const targetY = baseY + cell.ty
            const xMin = cell.tx - colSlot * 0.5
            const xMax = cell.tx + colSlot * 0.5
            const yMin = targetY - ROW_PITCH * 0.5
            const yMax = targetY + ROW_PITCH * 0.5
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
          // Sparse-depth fallback — original ±PER_DEPTH_X / ±Y_SCATTER_PX*2
          // clamp around (depthX, regionYMid). See Bug #481.
          const xMin = baseX - PER_DEPTH_X
          const xMax = baseX + PER_DEPTH_X
          if (typeof n.x === 'number') {
            if (n.x < xMin) n.x = xMin
            else if (n.x > xMax) n.x = xMax
          }
          const yMin = baseY - Y_SCATTER_PX * 2
          const yMax = baseY + Y_SCATTER_PX * 2
          if (typeof n.y === 'number') {
            if (n.y < yMin) n.y = yMin
            else if (n.y > yMax) n.y = yMax
          }
        }
        setTick((t) => t + 1)
      })

    simRef.current = sim
    return () => {
      sim.stop()
    }
  }, [simNodes, layout.edges, depthToX, regionYMid, gridTargets])

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

  // Bug #481 (reopened 2026-05-01) — operator's exact words:
  //   "they could have put a simple rule saying the max distance of a
  //    line cannot be longer than a percentage of canvas, or they could
  //    say no single bubble could be outside of the canvas etc."
  //
  // Live failure on otech17: a single dependency chain reached depth
  // 190 (e.g. bp-catalyst-platform → bp-langfuse → ... long chain). At
  // PER_DEPTH_X=160 that placed leaves at x=30,400+. Per-tick clamps
  // bound nodes around their own depth-anchor (which itself was at
  // 30,400) so they never came back into view — and the viewBox
  // ceiling MAX_VBOX_W=1200 captured only a 1200px slice of a 30,000px
  // cluster. The yellow horizontal lines on screen were the few edges
  // that happened to cross the visible 1200px window; every bubble was
  // off-canvas.
  //
  // The bounded tests passed because they asserted positions inside
  // the viewBox for 5-80 sibling stars, but never modelled a deep
  // chain (the actual production shape).
  //
  // Structural fix per operator's ask: "no single bubble could be
  // outside of the canvas". After the natural bbox is computed, clamp
  // it to MAX so the viewBox stays bounded, then HARD-CLAMP every
  // rendered position into the viewBox. Edges are then drawn between
  // already-clamped positions, so the second rule ("max line length")
  // is also bounded as a side effect (any link is at most the
  // viewBox's diagonal).
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
  const vbW = Math.min(MAX_VBOX_W, Math.max(MIN_VBOX_W, naturalW))
  const vbH = Math.min(MAX_VBOX_H, Math.max(MIN_VBOX_H, naturalH))
  // Bug #481 — when the natural bbox is wider than MAX_VBOX, anchor
  // the viewBox at the LEFT-MOST cluster point (depth 0) instead of
  // centring on the cluster centroid. Centring put depth 0 at
  // x=-15,000 off-canvas; left-anchor keeps the visible 1200px slice
  // starting at the actual left edge of the data, where the operator
  // expects depth-0 root nodes to live.
  const naturalCx = Number.isFinite(bbMinX) ? (bbMinX + bbMaxX) / 2 : vbW / 2
  const naturalCy = Number.isFinite(bbMinY) ? (bbMinY + bbMaxY) / 2 : vbH / 2
  const vbX = naturalW > MAX_VBOX_W && Number.isFinite(bbMinX)
    ? bbMinX - PAD_X
    : naturalCx - vbW / 2
  const vbY = naturalH > MAX_VBOX_H && Number.isFinite(bbMinY)
    ? bbMinY - PAD_Y_TOP
    : naturalCy - vbH / 2

  // Bug #481 — Constraint A: hard-clamp every render position into the
  // viewBox so no bubble ever drifts off-canvas, regardless of what the
  // simulation, depth-anchor, or grid-target produced. Operator's exact
  // request: "no single bubble could be outside of the canvas".
  //
  // For pathological-width clusters (depth-190 chains with naturalW
  // ≈ 30,000 vs MAX_VBOX_W=1200) plain clamping would pile every
  // distant node on the right edge. Solve that by scaling the X axis
  // proportionally to fit the viewBox, then clamping as a final
  // safety net. Y gets the same treatment.
  const CLAMP_INSET = NODE_RADIUS + 8
  const usableW = vbW - CLAMP_INSET * 2
  const usableH = vbH - CLAMP_INSET * 2
  const xScale = naturalW > vbW && (bbMaxX - bbMinX) > 0
    ? usableW / (bbMaxX - bbMinX)
    : 1
  const yScale = naturalH > vbH && (bbMaxY - bbMinY) > 0
    ? usableH / (bbMaxY - bbMinY)
    : 1
  const project = (p: { x: number; y: number }) => {
    let x = p.x
    let y = p.y
    if (xScale < 1 && Number.isFinite(bbMinX)) {
      x = vbX + CLAMP_INSET + (p.x - bbMinX) * xScale
    }
    if (yScale < 1 && Number.isFinite(bbMinY)) {
      y = vbY + CLAMP_INSET + (p.y - bbMinY) * yScale
    }
    // Final safety clamp — even when scaling, FP drift / partial-tick
    // values can land a fraction outside; the inset clamp guarantees
    // the bubble's full diameter stays visible.
    x = Math.min(vbX + vbW - CLAMP_INSET, Math.max(vbX + CLAMP_INSET, x))
    y = Math.min(vbY + vbH - CLAMP_INSET, Math.max(vbY + CLAMP_INSET, y))
    return { x, y }
  }
  const renderPos = new Map<string, { x: number; y: number }>()
  for (const [id, p] of livePos) {
    renderPos.set(id, project(p))
  }

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
