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
  // HEALTH axis (issue #3646) — recurring/reconciler kinds reuse the
  // closest one-shot visual tone: healthy≈succeeded, degraded≈pending
  // (amber), failing≈failed, with health-specific labels.
  healthy: {
    fill: 'var(--bubble-fill-succeeded)',
    ring: 'var(--bubble-ring-succeeded)',
    glyph: 'var(--bubble-glyph-succeeded)',
    glow: 'var(--bubble-glow-succeeded)',
    edge: 'var(--bubble-edge-succeeded)',
    arrow: 'var(--bubble-arrow-succeeded)',
    label: 'Healthy',
  },
  degraded: {
    fill: 'var(--bubble-fill-pending)',
    ring: 'var(--bubble-ring-pending)',
    glyph: 'var(--bubble-glyph-pending)',
    glow: 'var(--bubble-glow-pending)',
    edge: 'var(--bubble-edge-pending)',
    arrow: 'var(--bubble-arrow-pending)',
    label: 'Degraded',
  },
  failing: {
    fill: 'var(--bubble-fill-failed)',
    ring: 'var(--bubble-ring-failed)',
    glyph: 'var(--bubble-glyph-failed)',
    glow: 'var(--bubble-glow-failed)',
    edge: 'var(--bubble-edge-failed)',
    arrow: 'var(--bubble-arrow-failed)',
    label: 'Failing',
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
  // HEALTH axis (issue #3646) — reuse the nearest one-shot fallback.
  healthy:   '#16A34A',
  degraded:  '#94A3B8',
  failing:   '#B91C1C',
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
/** Issue #669 round 2 — adaptive bubble + edge sizing.
 *
 *  Bubbles render at a per-layout effective radius `R` clamped to
 *  [MIN_NODE_RADIUS, MAX_NODE_RADIUS]. The chosen R is the largest
 *  size that keeps the densest depth bucket fitting vertically inside
 *  the host. With one bubble on a wide canvas R=MAX (the bubble doesn't
 *  inflate to fill the screen); with 30+ siblings in a tight column
 *  R shrinks to MIN before any flow shape compromises kick in.
 *  Founder-verbatim 2026-05-03: "if there is only one buble on
 *  the page... we prefer making them sammller instead of compromising
 *  from their flow view". */
const MIN_NODE_RADIUS = 16
const MAX_NODE_RADIUS = 40
const GROUP_RADIUS_DELTA = 8
const COLLIDE_PADDING = 12
/** Initial host dimensions used until ResizeObserver fires. */
const MIN_HOST_W = 1200
const MIN_HOST_H = 700
/** Per-depth horizontal step floor / ceiling — `layoutMetrics` picks
 *  inside this range based on layout density and host width. */
const MIN_PER_DEPTH_X = 110
const MAX_PER_DEPTH_X = 200
/** Force strengths re-tuned post-#483. Gentle X-anchor lets the link
 *  force pull connected nodes together without the X-force fighting
 *  back and producing the oscillation that read as "infinite stretch". */
const FORCE_X_STRENGTH = 0.18
const FORCE_Y_STRENGTH = 0.22
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

/* ── Additive fold-disclosure props (2026-05-11 restore) ──────────
 *
 * `onFoldToggle` — when supplied AND a node is a group, a top-right
 *   "⊕ K" (folded) or "⊖" (expanded) badge renders on the bubble.
 *   Click invokes the callback with the group id. K reads from
 *   `descendantCountByGroup`, supplied as the optional `badgeCounts`
 *   prop (defaults to `node.childCount` when absent).
 *
 * `nodeActions` + `onNodeAction` — when supplied AND a node is a
 *   group, a right-click on the bubble opens a small floating menu
 *   with the action labels. Actions are domain-supplied (e.g. "Fold
 *   subtree", "Expand all under here") so the canvas stays purely
 *   presentational.
 */
export interface FlowOrganicAction {
  id: string
  label: string
  /** Predicate — return false to hide this action for the given node. */
  enabled?: (nodeId: string) => boolean
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
  /** Additive — toggle fold for a group node from a click on the
   *  per-bubble disclosure badge. When undefined, no badge renders
   *  (the natural-view's pre-2026-05-11 contract). */
  onFoldToggle?: (jobId: string) => void
  /** Additive — descendant counts for fold-disclosure badges. Keyed by
   *  group id; absent ids fall back to `node.childCount`. */
  badgeCounts?: ReadonlyMap<string, number>
  /** Additive — right-click menu items for group nodes. When empty or
   *  omitted, no menu opens (the natural-view's pre-2026-05-11
   *  contract). */
  nodeActions?: ReadonlyArray<FlowOrganicAction>
  /** Additive — invoked when the operator picks a menu item. */
  onNodeAction?: (jobId: string, actionId: string) => void
}

export function FlowCanvasOrganic(props: FlowCanvasOrganicProps) {
  const {
    layout,
    openJobId,
    hostJobId,
    onJobClick,
    onJobDoubleClick,
    onCanvasBackgroundClick,
    onFoldToggle,
    badgeCounts,
    nodeActions,
    onNodeAction,
  } = props
  /* Context-menu state — null = closed. */
  const [menu, setMenu] = useState<{
    nodeId: string
    x: number
    y: number
  } | null>(null)
  const onCloseMenu = useCallback(() => setMenu(null), [])
  /* Close the menu on any outside click / escape. */
  useEffect(() => {
    if (!menu) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') setMenu(null)
    }
    function onDocClick() {
      setMenu(null)
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('click', onDocClick)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('click', onDocClick)
    }
  }, [menu])

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
    /* Issue #669 round 3 — debounced + epsilon-gated ResizeObserver.
     *
     * Background: closing/opening the LogPane animates `padding-right`
     * over 220ms (cubic-bezier). During that animation the canvas
     * host width changes by ~1-2 px every animation frame; round-2
     * fired setHostSize on every rAF, which restarted the d3-force
     * sim every frame — the operator saw the bubbles "trying never
     * stabilizing".
     *
     * Fix: wait for the host size to be stable for 180ms before
     * pushing it to React state, AND ignore changes smaller than 8 px
     * in either dimension (the layoutMetrics snap-to-4 + slot
     * snap-to-8 combination tolerates ±4 px without re-emitting any
     * different metric, but explicit gating keeps the sim restart
     * effect downstream from running unnecessarily). */
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
            return prev // ignore — sub-threshold flicker
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

  /* Issue #669 round 2 — adaptive layout metrics.
   *
   * For each layout we compute the effective bubble radius `R`, group
   * radius `GR`, per-depth horizontal step `perDepthX`, and link
   * distance `linkDistance`. The chosen R is the largest that lets
   * the densest depth bucket fit vertically inside the host AND the
   * depth-chain fit horizontally; if neither constraint binds, R
   * snaps to MAX_NODE_RADIUS. A single bubble does NOT inflate; many
   * bubbles in a tight host shrink toward MIN before any flow-shape
   * compromise is made.
   *
   * The metrics depend on layout.nodes + hostSize.{w,h} so they
   * re-run when LogPane opens/closes or window resizes, which —
   * together with the sim-restart effect below that re-seeds free
   * nodes to their fresh targets — gives bubbles a visible re-flow
   * pass on every host change. */
  /* Issue #669 round 3 — variable-width depth columns.
   *
   * Founder-verbatim 2026-05-03 (round-2 UAT): "while 2/3 of the
   * screen is empty, it is trying to pile up everything in the right
   * edge. And it keep trying never stabilizing. Despite y axis
   * homogeneous distribution looks fine, x axis distribution is
   * terrible".
   *
   * Root cause: round-2 used a CONSTANT `perDepthX` for every depth
   * column. With one-bubble depths next to a 30+ sibling depth, the
   * dense bucket got 80% × perDepthX (~128px) of horizontal room and
   * had to pile into 8+ sub-columns; the sparse depths each got their
   * own perDepthX (~160px) for one bubble. End result: 60% of canvas
   * unused on the left, dense cluster jammed at right.
   *
   * Round-3 fix: each depth bucket gets a horizontal slot whose width
   * equals the bucket's *natural* extent at radius R. Sparse buckets
   * (1 sibling) need 2R + small gap; dense buckets need
   * `(totalCols - 1) * (2R + COLLIDE_PADDING)` to fit their
   * sub-columns side-by-side. Total layout width = sum(slots) + gaps
   * between slots. depthToX returns the centerline of slot[depth].
   *
   * Stabilization: round R to nearest 4 and slot widths to nearest 8
   * so sub-pixel ResizeObserver fires during pane-transition
   * animations don't perturb the metrics — without snapping, every
   * frame of a 220ms padding-right transition recomputes the
   * `Math.floor(rFit)` value and restarts the sim → never settles. */
  /* Issue #669 round 4 — sqrt-aspect-ratio dense-bucket grids.
   *
   * Round-3 grew dense slot width with bucket size, but used the
   * vertical-fit COL_CAPACITY (~14 rows on a 700px host) which still
   * forced wide buckets into thin tall columns. Result: 30 leaves in
   * 3 sub-cols × 10 rows = ~120 px wide slot, only ~28% of total
   * X-extent.
   *
   * Round-4: target a square-ish-but-wider aspect for dense buckets.
   *   targetRows = round(sqrt(count / 1.6))
   * gives e.g. 30 → 4 rows, 8 cols → ~700 px slot. Densest bucket's
   * targetRows then sets R via vertical-fit so all rows of the
   * tightest column fit in hostSize.h. With R = 40 (max), sparse
   * depths get 2R = 80 px slots and dense buckets sprawl horizontally
   * exactly enough to read as a block, not a pile.
   *
   * Stabilization (carries from round-3): R snaps to nearest 4, slot
   * widths snap to nearest 8, ResizeObserver debounces 180ms with
   * 8px epsilon — sub-pixel flicker during pane-transition animations
   * doesn't perturb the metrics. */
  const layoutMetrics = useMemo(() => {
    const buckets = new Map<number, number>()
    let maxBucket = 0
    let maxDepth = 0
    for (const n of layout.nodes) {
      const c = (buckets.get(n.depth) ?? 0) + 1
      buckets.set(n.depth, c)
      if (c > maxBucket) maxBucket = c
      if (n.depth > maxDepth) maxDepth = n.depth
    }
    const depthCount = maxDepth + 1

    // Target rows for a bucket of given size — slightly wider than tall.
    const targetRowsFor = (count: number) =>
      Math.max(1, Math.round(Math.sqrt(Math.max(1, count) / 1.6)))

    // R derived from the densest bucket's *target* rows (NOT the host's
    // raw vertical capacity), so wide buckets actually claim X-room.
    const denseRows = targetRowsFor(maxBucket)
    let r = MAX_NODE_RADIUS
    if (maxBucket > 1) {
      const usableH = Math.max(60, hostSize.h - 2 * (MAX_NODE_RADIUS + COLLIDE_PADDING))
      const pitchAvail = usableH / denseRows
      const rFit = (pitchAvail - COLLIDE_PADDING) / 2
      r = Math.min(MAX_NODE_RADIUS, Math.max(MIN_NODE_RADIUS, Math.floor(rFit)))
    }
    r = Math.max(MIN_NODE_RADIUS, Math.min(MAX_NODE_RADIUS, Math.round(r / 4) * 4))

    /* computeAt(radius, rowMultiplier) — recomputes slot widths at the
     * given radius. rowMultiplier > 1 forces dense buckets to use MORE
     * rows (and fewer columns) per bucket, compressing the slot
     * horizontally at the cost of more vertical space. Used when the
     * chain still overflows the host width even at R = MIN_NODE_RADIUS:
     * we trade horizontal sprawl for vertical until totalWidth fits. */
    const computeAt = (radius: number, rowMultiplier = 1) => {
      const ROW_PITCH = radius * 2 + COLLIDE_PADDING
      const Y_RANGE = Math.max(radius * 2, hostSize.h - 2 * (radius + COLLIDE_PADDING))
      const hardRowCap = Math.max(1, Math.floor(Y_RANGE / ROW_PITCH))
      const slotInfo = new Map<number, { cols: number; rows: number; width: number }>()
      for (const [d, count] of buckets) {
        const baseRows = targetRowsFor(count)
        const rows = Math.min(Math.max(1, Math.ceil(baseRows * rowMultiplier)), hardRowCap, count)
        const cols = Math.max(1, Math.ceil(count / rows))
        const naturalW = cols > 1
          ? (cols - 1) * (radius * 2 + COLLIDE_PADDING) + radius * 2
          : radius * 2
        const width = Math.round(Math.max(naturalW, radius * 2) / 8) * 8
        slotInfo.set(d, { cols, rows, width })
      }
      const gap = Math.max(MIN_PER_DEPTH_X, Math.min(MAX_PER_DEPTH_X, radius * 4))
      const xByDepth = new Map<number, number>()
      let cursor = radius + COLLIDE_PADDING
      for (let d = 0; d <= maxDepth; d++) {
        const w = slotInfo.get(d)?.width ?? radius * 2
        xByDepth.set(d, cursor + w / 2)
        cursor += w + gap
      }
      const totalWidth = cursor - gap + radius + COLLIDE_PADDING
      return { slotInfo, gap, xByDepth, totalWidth, hardRowCap }
    }

    /* Issue #669 round 5 — aggressive fit-to-host horizontally.
     *
     * Founder UAT: "the graph is not fitting into the full page,
     * getting out of the full screen with huge extended x axis".
     *
     * Two-stage fit:
     *   Stage 1 — shrink the inter-depth gap proportionally toward
     *             a tight floor (R*2.2) before touching R. Wide
     *             gaps are the first thing to give up.
     *   Stage 2 — if even a tight gap doesn't fit, shrink R by 4
     *             and recompute everything. R has its own MIN floor;
     *             past that we accept horizontal scroll.
     */
    let attempt = computeAt(r)
    // Stage 1: tighten gap if it would let us fit.
    if (attempt.totalWidth > hostSize.w && depthCount > 1) {
      const slotsTotal = Array.from(attempt.slotInfo.values()).reduce((acc, s) => acc + s.width, 0)
      const margin = 2 * (r + COLLIDE_PADDING)
      const fitGap = (hostSize.w - slotsTotal - margin) / (depthCount - 1)
      // Floor on inter-depth gap = just enough to prevent adjacent
      // depth bubbles from visually touching (R*2.2 → 9.6px clear gap
      // at R=16). MIN_PER_DEPTH_X is the *aesthetic* preference for
      // sparse layouts; when the chain doesn't fit, function over form.
      const minGap = r * 2.2
      if (fitGap >= minGap) {
        // Recompute xByDepth + totalWidth with the tighter gap.
        const newGap = Math.floor(fitGap)
        const newX = new Map<number, number>()
        let cursor = r + COLLIDE_PADDING
        for (let d = 0; d <= maxDepth; d++) {
          const w = attempt.slotInfo.get(d)?.width ?? r * 2
          newX.set(d, cursor + w / 2)
          cursor += w + newGap
        }
        attempt = {
          ...attempt,
          gap: newGap,
          xByDepth: newX,
          totalWidth: cursor - newGap + r + COLLIDE_PADDING,
        }
      }
    }
    // Stage 2: shrink R if still overflows.
    while (attempt.totalWidth > hostSize.w && r > MIN_NODE_RADIUS) {
      r = Math.max(MIN_NODE_RADIUS, r - 4)
      attempt = computeAt(r)
      if (attempt.totalWidth > hostSize.w && depthCount > 1) {
        const slotsTotal = Array.from(attempt.slotInfo.values()).reduce((acc, s) => acc + s.width, 0)
        const margin = 2 * (r + COLLIDE_PADDING)
        const fitGap = (hostSize.w - slotsTotal - margin) / (depthCount - 1)
        // Floor on inter-depth gap = just enough to prevent adjacent
      // depth bubbles from visually touching (R*2.2 → 9.6px clear gap
      // at R=16). MIN_PER_DEPTH_X is the *aesthetic* preference for
      // sparse layouts; when the chain doesn't fit, function over form.
      const minGap = r * 2.2
        if (fitGap >= minGap) {
          const newGap = Math.floor(fitGap)
          const newX = new Map<number, number>()
          let cursor = r + COLLIDE_PADDING
          for (let d = 0; d <= maxDepth; d++) {
            const w = attempt.slotInfo.get(d)?.width ?? r * 2
            newX.set(d, cursor + w / 2)
            cursor += w + newGap
          }
          attempt = {
            ...attempt,
            gap: newGap,
            xByDepth: newX,
            totalWidth: cursor - newGap + r + COLLIDE_PADDING,
          }
        }
      }
    }
    /* Stage 3 — at MIN_R with min gap and STILL overflowing? Trade
     * horizontal extent for vertical: increase the row-multiplier on
     * dense buckets so they pile MORE vertically and less
     * horizontally. Each step multiplies targetRows by 1.4 (so a
     * bucket that wanted 3 rows × 4 cols becomes 5 rows × 3 cols,
     * shrinking slot from 356 → 264 px). Stops when fits or
     * rowMultiplier exceeds the bucket's hard row cap. */
    let rowMul = 1
    while (attempt.totalWidth > hostSize.w && rowMul < 4) {
      rowMul *= 1.4
      const next = computeAt(r, rowMul)
      // Re-apply gap tightening at the new radius/rowMul.
      let stage1 = next
      if (next.totalWidth > hostSize.w && depthCount > 1) {
        const slotsTotal = Array.from(next.slotInfo.values()).reduce((acc, s) => acc + s.width, 0)
        const margin = 2 * (r + COLLIDE_PADDING)
        const fitGap = (hostSize.w - slotsTotal - margin) / (depthCount - 1)
        const minGap = r * 2.2
        if (fitGap >= minGap) {
          const newGap = Math.floor(fitGap)
          const newX = new Map<number, number>()
          let cursor = r + COLLIDE_PADDING
          for (let d = 0; d <= maxDepth; d++) {
            const w = next.slotInfo.get(d)?.width ?? r * 2
            newX.set(d, cursor + w / 2)
            cursor += w + newGap
          }
          stage1 = {
            ...next,
            gap: newGap,
            xByDepth: newX,
            totalWidth: cursor - newGap + r + COLLIDE_PADDING,
          }
        }
      }
      // Only adopt if it actually improves fit (or fits exactly).
      if (stage1.totalWidth <= attempt.totalWidth) {
        attempt = stage1
      } else {
        break
      }
    }
    const { slotInfo, gap, xByDepth, totalWidth, hardRowCap } = attempt

    const linkDistance = gap * 0.625
    const gr = r + GROUP_RADIUS_DELTA

    return {
      r,
      gr,
      gap,
      slotInfo,
      xByDepth,
      totalWidth,
      linkDistance,
      maxBucket,
      depthCount,
      hardRowCap,
    }
  }, [layout.nodes, hostSize.w, hostSize.h])

  const { r: R, gr: GR, gap: PER_DEPTH_X, linkDistance: LINK_DISTANCE, xByDepth, slotInfo } = layoutMetrics

  const depthToX = useCallback(
    (depth: number) => xByDepth.get(depth) ?? (R + COLLIDE_PADDING + depth * (R * 4)),
    [xByDepth, R],
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
  /* Issue #669 round 2 — per-depth-bucket Y rank.
   *
   * Founder-verbatim 2026-05-03: "y axis should be equally splitted
   * into y and -y... items distributed in +y and -y axis accepting
   * the x axis as their separator". The diagonal layout came from a
   * global topo-rank for Y — depth-0 nodes piled at the top while
   * deeper ranks slid down, producing a left-top → right-bottom
   * diagonal. Replace with per-depth ranks so each depth column
   * distributes its siblings symmetrically around y=h/2. */
  const Y_CENTER = hostSize.h / 2
  const PITCH = R * 2 + COLLIDE_PADDING

  const bucketRank = useMemo(() => {
    const rank = new Map<string, { idx: number; size: number }>()
    const seen = new Map<number, OrganicNode[]>()
    for (const n of layout.nodes) {
      let arr = seen.get(n.depth)
      if (!arr) { arr = []; seen.set(n.depth, arr) }
      arr.push(n)
    }
    for (const arr of seen.values()) {
      arr.sort((a, b) => {
        const ra = resolvedDepRank.get(a.id) ?? 0
        const rb = resolvedDepRank.get(b.id) ?? 0
        return ra - rb
      })
      arr.forEach((n, i) => rank.set(n.id, { idx: i, size: arr.length }))
    }
    return rank
  }, [layout.nodes, resolvedDepRank])

  /* Per-bucket centred Y target — sibling at index `i` of `n` lands
   * at h/2 + (i - (n-1)/2) * pitch. Median sibling on the X-axis
   * centerline; rest spread symmetrically above and below.
   *
   * Issue #669 round 5 — for SINGLETON depth buckets (size === 1) we
   * zigzag Y by depth parity so a long sequential chain (e.g. Phase 0
   * → cluster-bootstrap → cilium → cert-manager) reads as a gentle
   * wave that uses the vertical canvas instead of a flat horizontal
   * line. The amplitude (R*1.5) keeps adjacent depths visually
   * connected while filling more of the host height. */
  const depthByNodeId = useMemo(() => {
    const m = new Map<string, number>()
    for (const n of layout.nodes) m.set(n.id, n.depth)
    return m
  }, [layout.nodes])
  /* Zigzag amplitude — sized to USE the host height, not R. With a
   * 900-px host, amplitude is ~270 px → singletons sit at
   * y = h/2 ± 270, filling the upper and lower fifths of the host.
   * Bounded at host_h × 0.32 (so the bubble's full diameter never
   * crosses the edge) and at host_h/2 - R - COLLIDE_PADDING (hard
   * floor to keep bubble inside viewBox). */
  /* Modest amplitude — just enough to lift singletons off the
   * centerline so the chain reads as a gentle wave instead of a flat
   * timeline, but short enough that adjacent depths stay visually
   * connected without dragging the eye across the canvas. Founder
   * UAT 2026-05-03: "even if it is kept short, it would give similar
   * result and look nicer". */
  const ZIGZAG_AMPLITUDE = Math.min(R * 3, hostSize.h * 0.12)
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
      tx: number
      ty: number
      totalCols: number
      totalRows: number
    }
    const ROW_PITCH = R * 2 + COLLIDE_PADDING
    const Y_RANGE_LOCAL = Math.max(R * 2, hostSize.h - 2 * (R + COLLIDE_PADDING))
    const Y_CENTER_LOCAL = hostSize.h / 2
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
      const info = slotInfo.get(depth)
      // Sparse depths (cols=1, rows=1, slot=2R) skip the grid pre-pass —
      // the force-anchor + per-bucket Y target handles them naturally.
      if (!info || (info.cols <= 1 && info.rows <= 1)) continue
      // Issue #669 round 4 — read sub-grid dims from layoutMetrics
      // (sqrt-aspect target) instead of recomputing here. Guarantees
      // gridTargets and slotWidth agree on cols/rows.
      const baseX = xByDepth.get(depth) ?? (R + COLLIDE_PADDING + depth * (R * 4))
      const totalCols = info.cols
      const totalRows = info.rows
      // Sub-column inner span = slot width minus one bubble at each
      // edge (centerlines stay inside the slot).
      const SUB_COL_SPAN = Math.max(0, info.width - R * 2)
      const colStep = totalCols > 1 ? SUB_COL_SPAN / (totalCols - 1) : 0
      // Issue #532 founder verbatim: "homogenously spread". Distribute
      // rows evenly across the full Y range (not packed at the top).
      const rowStep = totalRows > 1
        ? Math.min(ROW_PITCH * 1.2, Y_RANGE_LOCAL / (totalRows - 1))
        : 0
      bucket.forEach((n, idx) => {
        const subCol = Math.floor(idx / totalRows)
        const subRow = idx % totalRows
        const colOffset = totalCols > 1
          ? (subCol - (totalCols - 1) / 2) * colStep
          : 0
        // Issue #669 round 2 — centred row spread around y=h/2 instead
        // of starting from the top margin. Median row lands on the
        // X-axis centerline; rest spread above and below.
        const ty = totalRows > 1
          ? Y_CENTER_LOCAL + (subRow - (totalRows - 1) / 2) * rowStep
          : Y_CENTER_LOCAL
        cells.set(n.id, {
          tx: baseX + colOffset,
          ty,
          totalCols,
          totalRows,
        })
      })
    }
    return cells
  }, [layout.nodes, hostSize.h, xByDepth, slotInfo, R])

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
        const seed = hashSeed(n.id)
        const cell = gridTargets.get(n.id)
        const initX = cell ? cell.tx : baseX + (seed.fx - 0.5) * R * 1.5
        // Issue #669 round 2 — initial Y from per-bucket centred target.
        const initY = cell
          ? cell.ty
          : yForBucket(n.id) + (seed.fy - 0.5) * R * 0.6
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
  }, [layout.nodes, depthToX, yForBucket, gridTargets, resolvedDepRank, R])

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
    /* Issue #669 round 2 — visible re-flow on every layout/host change.
     * Snap each free (un-pinned) node to its fresh target so the
     * operator sees the reflow rather than the sim drifting back to
     * old positions over 2s. Pinned (dragged) nodes keep fx/fy. */
    for (const n of simNodes) {
      if (typeof n.fx === 'number' && typeof n.fy === 'number') continue
      const cell = gridTargets.get(n.id)
      const seed = hashSeed(n.id)
      n.x = cell ? cell.tx : depthToX(n.depth) + (seed.fx - 0.5) * R * 1.5
      n.y = cell ? cell.ty : yForBucket(n.id) + (seed.fy - 0.5) * R * 0.6
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
      .force(
        'x',
        forceX<SimNode>()
          .x((d) => {
            const cell = gridTargets.get(d.id)
            return cell ? cell.tx : depthToX(d.depth)
          })
          .strength(FORCE_X_STRENGTH),
      )
      .force(
        'y',
        forceY<SimNode>()
          .y((d) => {
            // Issue #669 round 2 — Y target now per-bucket centred
            // (yForBucket) instead of global topo ladder.
            const cell = gridTargets.get(d.id)
            if (cell) return cell.ty
            return yForBucket(d.id)
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
            // Issue #669 round 4 — narrow per-cell clamp.
            //
            // Each grid cell's clamp window is half the cell pitch in
            // each axis, MINUS R, so the bubble's edge can never reach
            // its neighbour's centre. Old code used colSlot = full
            // pitch which let two adjacent cells' clamp windows
            // overlap → forceCollide pushed bubbles into neighbour
            // territory, the clamp ratcheted them in, and centres
            // could collapse to <2R apart. The narrow window keeps
            // forceCollide's pairwise floor intact at runtime. */
            const xPitch = cell.totalCols > 1
              ? layoutMetrics.slotInfo.get(n.depth)?.width
                  ? Math.max(1, ((layoutMetrics.slotInfo.get(n.depth)!.width - R * 2) / (cell.totalCols - 1)))
                  : R * 2 + COLLIDE_PADDING
              : layoutMetrics.gap
            const yPitch = cell.totalRows > 1
              ? Math.max(R * 2 + COLLIDE_PADDING, (hostSize.h - 2 * (R + COLLIDE_PADDING)) / (cell.totalRows - 1))
              : hostSize.h - 2 * (R + COLLIDE_PADDING)
            const xHalf = Math.max(R, xPitch * 0.5 - R)
            const yHalf = Math.max(R, yPitch * 0.5 - R)
            const xMin = cell.tx - xHalf
            const xMax = cell.tx + xHalf
            const yMin = cell.ty - yHalf
            const yMax = cell.ty + yHalf
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
          const xMin = Math.max(R, baseX - PER_DEPTH_X)
          const xMax = Math.min(layoutMetrics.totalWidth - R, baseX + PER_DEPTH_X)
          if (typeof n.x === 'number') {
            if (n.x < xMin) n.x = xMin
            else if (n.x > xMax) n.x = xMax
          }
          const targetY = yForBucket(n.id)
          const Y_HALF_BAND = (R * 2 + COLLIDE_PADDING)
          const yMin = Math.max(R, targetY - Y_HALF_BAND)
          const yMax = Math.min(hostSize.h - R, targetY + Y_HALF_BAND)
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
  }, [simNodes, layout.edges, depthToX, yForBucket, gridTargets, hostSize.h, hostSize.w, R, GR, PER_DEPTH_X, LINK_DISTANCE])

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
  /* Issue #669 round 4 — viewBox tracks the LARGER of host width or
   * total layout extent. When the dep chain doesn't fit horizontally
   * even after R shrinks to MIN_NODE_RADIUS, the SVG renders at full
   * layout width and the parent host scrolls horizontally instead of
   * piling bubbles at the right edge. Y still tracks host height. */
  const vbX = 0
  const vbY = 0
  const vbW = Math.max(hostSize.w, layoutMetrics.totalWidth)
  const vbH = hostSize.h

  const CLAMP_INSET = R + 8
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
        overflowX: layoutMetrics.totalWidth > hostSize.w ? 'auto' : 'hidden',
        overflowY: 'hidden',
      }}
    >
    <svg
      ref={svgRef}
      width={vbW}
      height="100%"
      viewBox={`${vbX} ${vbY} ${vbW} ${vbH}`}
      preserveAspectRatio="xMinYMin meet"
      className="flow-canvas-svg-organic"
      data-testid="flow-canvas-svg"
      role="img"
      aria-label="Provisioning dependency flow"
      style={{ display: 'block', minWidth: '100%', height: '100%' }}
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

      {/* EDGES LAYER — wrapped in an explicit <g> rendered BEFORE the
          nodes layer so SVG paint order guarantees wires sit behind
          every bubble. Without the wrapper the JSX siblings already
          paint in source order, but a future code change that inserts
          another element between edges and nodes could quietly defeat
          that contract — the wrapper makes the layering explicit. */}
      <g className="flow-edges-layer" data-layer="edges">
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
            r={R}
          />
        )
      })}
      </g>

      {/* NODES LAYER — opaque bubbles painted OVER the edges layer.
          The bubble inner fill is solid (no alpha) so any edge whose
          path crosses the bubble bounding box is occluded. */}
      <g className="flow-nodes-layer" data-layer="nodes">
      {layout.nodes.map((node) => {
        // Bug #481 — render at clamped position so no bubble ever sits
        // outside the viewBox.
        const pos = renderPos.get(node.id)
        if (!pos) return null
        const family = familyById.get(node.familyId) ?? null
        const isNeighbor = neighborIds.has(node.id)
        const isOpen = openJobId === node.id
        const isHost = hostJobId === node.id
        // Badge renders only when (a) the caller wired onFoldToggle AND
        // (b) the node is a real group. Folded → "⊕ K" (where K is the
        // descendant count). Expanded → "⊖".
        const showBadge = !!onFoldToggle && node.isGroup
        const badgeCount = badgeCounts?.get(node.id) ?? node.childCount
        const hasMenuActions = !!nodeActions && nodeActions.length > 0 && node.isGroup
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
            r={R}
            gr={GR}
            showBadge={showBadge}
            badgeCount={badgeCount}
            onBadgeClick={onFoldToggle}
            onContextMenu={
              hasMenuActions
                ? (e) => {
                    e.preventDefault()
                    e.stopPropagation()
                    setMenu({
                      nodeId: node.id,
                      x: e.clientX,
                      y: e.clientY,
                    })
                  }
                : undefined
            }
          />
        )
      })}
      </g>
    </svg>
    {menu && nodeActions && onNodeAction ? (
      <FlowNodeMenu
        nodeId={menu.nodeId}
        x={menu.x}
        y={menu.y}
        actions={nodeActions}
        onPick={(actionId) => {
          onNodeAction(menu.nodeId, actionId)
          setMenu(null)
        }}
        onClose={onCloseMenu}
      />
    ) : null}
    </div>
  )
}

/* ── FlowNodeMenu — right-click action menu (additive 2026-05-11) ── */

interface FlowNodeMenuProps {
  nodeId: string
  x: number
  y: number
  actions: ReadonlyArray<FlowOrganicAction>
  onPick: (actionId: string) => void
  onClose: () => void
}

function FlowNodeMenu({ nodeId, x, y, actions, onPick }: FlowNodeMenuProps) {
  const visible = actions.filter(
    (a) => a.enabled === undefined || a.enabled(nodeId),
  )
  if (visible.length === 0) return null
  return (
    <div
      role="menu"
      data-testid={`flow-node-menu-${nodeId}`}
      style={{
        position: 'fixed',
        top: y,
        left: x,
        zIndex: 100,
        background: 'var(--color-surface)',
        border: '1px solid var(--color-border)',
        borderRadius: 6,
        boxShadow: '0 4px 16px rgba(0,0,0,0.35)',
        padding: '4px 0',
        minWidth: 180,
        fontSize: 12,
      }}
      onClick={(e) => e.stopPropagation()}
    >
      {visible.map((a) => (
        <button
          key={a.id}
          type="button"
          role="menuitem"
          data-testid={`flow-node-menu-item-${a.id}`}
          onClick={() => onPick(a.id)}
          style={{
            appearance: 'none',
            background: 'transparent',
            border: 0,
            width: '100%',
            textAlign: 'left',
            padding: '6px 12px',
            color: 'var(--color-text)',
            cursor: 'pointer',
            font: 'inherit',
          }}
        >
          {a.label}
        </button>
      ))}
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
  r: number
}

function FlowEdge({ from, to, status, kind, highlighted, r }: FlowEdgeProps) {
  const tone = STATUS_TONE[status]
  const dx = to.x - from.x
  const dy = to.y - from.y
  const len = Math.hypot(dx, dy) || 1
  const trim = r + 6
  const fx = from.x + (dx / len) * r
  const fy = from.y + (dy / len) * r
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
  r: number
  gr: number
  /** Additive — render the per-bubble fold-disclosure badge. */
  showBadge?: boolean
  /** Additive — descendant count shown next to ⊕ when folded. */
  badgeCount?: number
  /** Additive — click handler for the disclosure badge. */
  onBadgeClick?: (nodeId: string) => void
  /** Additive — right-click handler (group bubbles only). */
  onContextMenu?: (e: ReactMouseEvent<SVGGElement>) => void
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
  r,
  gr,
  showBadge = false,
  badgeCount,
  onBadgeClick,
  onContextMenu,
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
  const radius = node.isGroup ? gr : r
  // Founder rule (prov #75 review): bubbles MUST stay opaque so wires
  // never bleed through. The dimmed visual treatment is now done via
  // SVG `filter: grayscale + brightness` on the OUTER ring/glow halo,
  // NOT via group-level opacity — opacity = 0.35 made the bubble fill
  // see-through and the wires-behind-bubble paint order was wasted.
  const grpStyle: CSSProperties = isDimmed
    ? { cursor: 'grab', filter: 'grayscale(70%) brightness(0.65)' }
    : { cursor: 'grab' }
  const groupOpacity = 1
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
      onContextMenu={onContextMenu}
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

      {/* Fold-disclosure badge — top-right corner. Renders only when
          the caller wired onBadgeClick AND the node is a parent group.
          Folded → "⊕ K" with the descendant count K. Expanded → "⊖". */}
      {showBadge && onBadgeClick ? (
        (() => {
          const bx = radius * 0.7
          const by = -radius * 0.7
          const folded = node.isFolded
          const count = typeof badgeCount === 'number' ? badgeCount : node.childCount
          const text = folded ? `⊕ ${count}` : '⊖'
          const w = folded ? 30 : 18
          const h = 14
          return (
            <g
              data-testid={`flow-fold-badge-${node.id}`}
              data-folded={folded ? 'true' : 'false'}
              transform={`translate(${bx.toFixed(1)}, ${by.toFixed(1)})`}
              style={{ cursor: 'pointer' }}
              onClick={(e) => {
                e.stopPropagation()
                onBadgeClick(node.id)
              }}
            >
              <rect
                x={-w / 2}
                y={-h / 2}
                width={w}
                height={h}
                rx={4}
                ry={4}
                fill={tone.fill}
                stroke={tone.ring}
                strokeWidth={1.2}
                opacity={0.95}
              />
              <text
                x={0}
                y={3}
                textAnchor="middle"
                fontSize={9}
                fontWeight={700}
                fill={tone.glyph}
                fontFamily="ui-sans-serif, system-ui, sans-serif"
                pointerEvents="none"
              >
                {text}
              </text>
            </g>
          )
        })()
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
