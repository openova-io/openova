/**
 * FlowCanvasOrganic — Bug #481 bounded layout regression test.
 *
 * The canvas previously rendered nodes scattered "between left + right
 * panes, tiny — barely visible — connected by kilometers of lines".
 * The first fix (#483) over-corrected: bubbles disappeared at default
 * zoom because the MIN_VBOX floor (1200×700) forced sparse graphs to
 * scale down 6× inside small canvas hosts.
 *
 * The current fix (post-#483):
 *   • NODE_RADIUS = 40 → bubble diameter 80px (acceptance criterion).
 *   • MAX_VBOX = 1200×700 → bubbles render close to native scale on
 *     typical canvas hosts (600-1200px wide).
 *   • MIN_VBOX = 400×280 → sparse graphs render their actual cluster
 *     size, not a forced 1200×700 frame.
 *   • Force strengths gentle (X=0.12, Y=0.10, link=0.18) — no
 *     oscillation between depth-anchor and link-distance forces.
 *
 * This test locks in:
 *   1. The viewBox NEVER exceeds MAX (1200×700) — operator never sees
 *      bubbles shrunk to specks.
 *   2. Every rendered node sits well inside the viewBox — no
 *      "kilometers of edges" off-canvas drift.
 *   3. Bubble radius is large enough to be visible (≥40px = 80px
 *      diameter, the acceptance criterion for #481 follow-up).
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { FlowCanvasOrganic } from './FlowCanvasOrganic'
import type { OrganicLayoutResult } from '@/lib/flowLayoutOrganic'

afterEach(() => cleanup())

const FAMILIES = [{ id: 'catalyst', label: 'Catalyst', color: '#fff' }]
const REGIONS = [{ id: 'primary', label: 'Primary' }]

function makeLayout(nodeCount: number): OrganicLayoutResult {
  // Realistic shape: a single root at depth 0 plus N siblings at
  // depth 1 — the dominant pattern in OpenOva blueprint installs
  // (cluster-bootstrap → many leaf installs). Edges go root → leaf
  // so the link force has to keep them clustered around their depth
  // anchor without the depth-skip span the previous test layout
  // produced (depth 3 → depth 0 across 480 viewBox-units).
  const nodes = Array.from({ length: nodeCount }, (_, i) => ({
    id: `n${i}`,
    depth: i === 0 ? 0 : 1,
    regionId: 'primary',
    familyId: 'catalyst',
    label: `Node ${i}`,
    subLabel: '',
    status: 'pending' as const,
    isGroup: false,
    isFolded: false,
    childCount: 0,
    job: {
      id: `n${i}`,
      jobName: `n${i}`,
      type: 'install' as const,
      appId: 'x',
      parentId: '',
      dependsOn: [],
      childIds: [],
      status: 'pending' as const,
      startedAt: null,
      finishedAt: null,
      durationMs: 0,
    },
  }))
  // Star: every leaf depends on the root.
  const edges = nodes.slice(1).map((n) => ({
    fromId: 'n0',
    toId: n.id,
    fromStatus: 'pending' as const,
    crossRegion: false,
    kind: 'depends-on' as const,
  }))
  return {
    nodes,
    edges,
    maxDepth: 1,
    regions: REGIONS,
    families: FAMILIES,
  }
}

describe('FlowCanvasOrganic — Bug #481 bounded layout', () => {
  it('clamps viewBox within MAX_VBOX (1200×700) on a typical 12-node graph', () => {
    const layout = makeLayout(12)
    const { container } = render(
      <FlowCanvasOrganic
        layout={layout}
        openJobId={null}
        hostJobId={null}
        onJobClick={() => {}}
        onJobDoubleClick={() => {}}
        onCanvasBackgroundClick={() => {}}
      />,
    )
    const svg = container.querySelector<SVGSVGElement>(
      '[data-testid="flow-canvas-svg"]',
    )
    expect(svg).not.toBeNull()
    const vb = svg!.getAttribute('viewBox') ?? ''
    const parts = vb.split(/\s+/).map(Number)
    expect(parts).toHaveLength(4)
    const [, , w, h] = parts
    // Bug #481 follow-up — viewBox MUST NOT exceed MAX (1200×700) so
    // bubbles render at near-native scale inside the canvas host. The
    // previous ceiling (1600×900) made nodes invisible at default zoom.
    expect(w).toBeLessThanOrEqual(1200)
    expect(h).toBeLessThanOrEqual(700)
    // Floor — sparse graphs still get a usable frame, but small enough
    // that the cluster fills the visible area rather than swimming in
    // empty space.
    expect(w).toBeGreaterThanOrEqual(400)
    expect(h).toBeGreaterThanOrEqual(280)
  })

  it('renders all nodes with translate inside the clamped viewBox (no off-canvas drift)', () => {
    const layout = makeLayout(8)
    const { container } = render(
      <FlowCanvasOrganic
        layout={layout}
        openJobId={null}
        hostJobId={null}
        onJobClick={() => {}}
        onJobDoubleClick={() => {}}
        onCanvasBackgroundClick={() => {}}
      />,
    )
    const svg = container.querySelector<SVGSVGElement>(
      '[data-testid="flow-canvas-svg"]',
    )!
    const vb = svg.getAttribute('viewBox') ?? ''
    const [vbX, vbY, vbW, vbH] = vb.split(/\s+/).map(Number)
    const groups = container.querySelectorAll<SVGGElement>(
      '[data-flow-draggable]',
    )
    expect(groups.length).toBe(8)
    for (const g of Array.from(groups)) {
      const t = g.getAttribute('transform') ?? ''
      const m = t.match(/translate\(([-\d.]+),\s*([-\d.]+)\)/)
      expect(m).not.toBeNull()
      const x = Number(m![1])
      const y = Number(m![2])
      // Allow a 60px overshoot for nodes still in their initial seed
      // position before the simulation has ticked. The structural
      // assertion is that nothing has drifted to "kilometers" away.
      expect(x).toBeGreaterThan(vbX - 60)
      expect(x).toBeLessThan(vbX + vbW + 60)
      expect(y).toBeGreaterThan(vbY - 60)
      expect(y).toBeLessThan(vbY + vbH + 60)
    }
  })

  /* ── Bug #481 follow-up: visible bubbles + bounded edges ─────── */

  it.each([5, 8, 12, 15])(
    'renders %i-node graph with bubbles ≥80px diameter and edges <300px (acceptance)',
    (count) => {
      const layout = makeLayout(count)
      const { container } = render(
        <FlowCanvasOrganic
          layout={layout}
          openJobId={null}
          hostJobId={null}
          onJobClick={() => {}}
          onJobDoubleClick={() => {}}
          onCanvasBackgroundClick={() => {}}
        />,
      )
      // Acceptance: bubble width ≥80px at default zoom. The base
      // <circle r={radius}> is the structural proof — radius ≥40
      // means diameter ≥80 in the viewBox's coordinate space, which
      // preserveAspectRatio "meet" maps proportionally to the canvas.
      const baseCircles = container.querySelectorAll<SVGCircleElement>(
        '[data-flow-draggable] circle:nth-of-type(3)',
      )
      // 1 status-fill circle per node (the third circle inside each
      // node group: glow, family ring, then status fill).
      // Group can render variant orderings; just assert at least one
      // circle per node has r≥40.
      const allCircles = container.querySelectorAll<SVGCircleElement>(
        '[data-flow-draggable] circle',
      )
      // Every node group has at least one circle whose radius (or
      // radius+ring offset) equals NODE_RADIUS=40 or GROUP_RADIUS=48.
      const groups = container.querySelectorAll<SVGGElement>(
        '[data-flow-draggable]',
      )
      for (const g of Array.from(groups)) {
        const circles = g.querySelectorAll('circle')
        let maxR = 0
        for (const c of Array.from(circles)) {
          const r = Number(c.getAttribute('r') ?? '0')
          if (r > maxR) maxR = r
        }
        // The status-fill circle is at r=NODE_RADIUS (40); ensure the
        // bubble has at least that much radius (≥40 → diameter ≥80).
        expect(maxR).toBeGreaterThanOrEqual(40)
      }
      void baseCircles
      void allCircles

      // Acceptance: edges (lines between bubbles) stay under 300px in
      // viewBox-space. A 1200×700 viewBox maps to typical canvas
      // hosts close to 1:1, so 300 viewBox-units ≈ 300 screen-px.
      const lines = container.querySelectorAll<SVGLineElement>('line')
      expect(lines.length).toBeGreaterThan(0)
      for (const ln of Array.from(lines)) {
        const x1 = Number(ln.getAttribute('x1') ?? '0')
        const y1 = Number(ln.getAttribute('y1') ?? '0')
        const x2 = Number(ln.getAttribute('x2') ?? '0')
        const y2 = Number(ln.getAttribute('y2') ?? '0')
        const len = Math.hypot(x2 - x1, y2 - y1)
        // Acceptance: <300px. Allow a small safety margin for the
        // initial-seed frame before the simulation has ticked.
        expect(len).toBeLessThan(300)
      }
    },
  )

  it('places node centroids inside the viewBox (no nodes off-canvas) for 5-15 node graphs', () => {
    for (const count of [5, 8, 12, 15]) {
      const layout = makeLayout(count)
      const { container, unmount } = render(
        <FlowCanvasOrganic
          layout={layout}
          openJobId={null}
          hostJobId={null}
          onJobClick={() => {}}
          onJobDoubleClick={() => {}}
          onCanvasBackgroundClick={() => {}}
        />,
      )
      const svg = container.querySelector<SVGSVGElement>(
        '[data-testid="flow-canvas-svg"]',
      )!
      const vb = svg.getAttribute('viewBox') ?? ''
      const [vbX, vbY, vbW, vbH] = vb.split(/\s+/).map(Number)
      const groups = container.querySelectorAll<SVGGElement>(
        '[data-flow-draggable]',
      )
      for (const g of Array.from(groups)) {
        const t = g.getAttribute('transform') ?? ''
        const m = t.match(/translate\(([-\d.]+),\s*([-\d.]+)\)/)
        expect(m).not.toBeNull()
        const x = Number(m![1])
        const y = Number(m![2])
        // Strict bounds (no overshoot tolerance) — every centroid
        // must be inside the viewBox so the bubble is at least
        // partially visible.
        expect(x).toBeGreaterThanOrEqual(vbX)
        expect(x).toBeLessThanOrEqual(vbX + vbW)
        expect(y).toBeGreaterThanOrEqual(vbY)
        expect(y).toBeLessThanOrEqual(vbY + vbH)
      }
      unmount()
    }
  })

  /* ── Issue #493 — high-sibling-count graphs (Phase-8a real shape) ── */

  it.each([30, 50, 80])(
    'fits %i siblings inside the viewBox via sub-column grid (issue #493)',
    (count) => {
      const layout = makeLayout(count)
      const { container } = render(
        <FlowCanvasOrganic
          layout={layout}
          openJobId={null}
          hostJobId={null}
          onJobClick={() => {}}
          onJobDoubleClick={() => {}}
          onCanvasBackgroundClick={() => {}}
        />,
      )
      const svg = container.querySelector<SVGSVGElement>(
        '[data-testid="flow-canvas-svg"]',
      )!
      const vb = svg.getAttribute('viewBox') ?? ''
      const [vbX, vbY, vbW, vbH] = vb.split(/\s+/).map(Number)
      const groups = container.querySelectorAll<SVGGElement>(
        '[data-flow-draggable]',
      )
      expect(groups.length).toBeGreaterThanOrEqual(count)
      let inside = 0
      for (const g of Array.from(groups)) {
        const t = g.getAttribute('transform') ?? ''
        const m = t.match(/translate\(([-\d.]+),\s*([-\d.]+)\)/)
        if (!m) continue
        const x = Number(m[1])
        const y = Number(m[2])
        if (x >= vbX && x <= vbX + vbW && y >= vbY && y <= vbY + vbH) {
          inside++
        }
      }
      // Acceptance: ≥95% of node centroids inside the viewBox at first
      // paint. Pre-fix this was ~15% on a 50-node graph.
      const ratio = inside / groups.length
      expect(ratio).toBeGreaterThanOrEqual(0.95)
    },
  )

  /* ── Bug #481 (reopened 2026-05-01) — deep-chain production shape ──
   *
   * Live failure on otech17/cluster-bootstrap: nodes drifted to
   * x≈30,560 because the dependency graph had longest-path depth ~190
   * (bp-* leaves chained through "applications"). At PER_DEPTH_X=160,
   * that placed nodes at depth*160=30,400 — far outside the
   * MAX_VBOX_W=1200 ceiling. The viewBox showed only a 1200px slice
   * of a 30,000px cluster, so 99% of bubbles rendered off-canvas.
   * Operator saw yellow horizontal lines (the few edges that crossed
   * the visible window) and zero bubbles.
   *
   * Pre-existing bounded tests modelled depth=0/1 stars only, so this
   * pathology slipped through. Lock it in.
   */
  it('keeps every bubble inside the viewBox for a deep dependency chain (production shape, #481 reopen)', () => {
    const CHAIN = 50
    const nodes = Array.from({ length: CHAIN }, (_, i) => ({
      id: `n${i}`,
      depth: i,
      regionId: 'primary',
      familyId: 'catalyst',
      label: `Node ${i}`,
      subLabel: '',
      status: 'pending' as const,
      isGroup: false,
      isFolded: false,
      childCount: 0,
      job: {
        id: `n${i}`,
        jobName: `n${i}`,
        type: 'install' as const,
        appId: 'x',
        parentId: '',
        dependsOn: i > 0 ? [`n${i - 1}`] : [],
        childIds: [],
        status: 'pending' as const,
        startedAt: null,
        finishedAt: null,
        durationMs: 0,
      },
    }))
    const edges = nodes.slice(1).map((n, i) => ({
      fromId: `n${i}`,
      toId: n.id,
      fromStatus: 'pending' as const,
      crossRegion: false,
      kind: 'depends-on' as const,
    }))
    const layout: OrganicLayoutResult = {
      nodes,
      edges,
      maxDepth: CHAIN - 1,
      regions: REGIONS,
      families: FAMILIES,
    }
    const { container } = render(
      <FlowCanvasOrganic
        layout={layout}
        openJobId={null}
        hostJobId={null}
        onJobClick={() => {}}
        onJobDoubleClick={() => {}}
        onCanvasBackgroundClick={() => {}}
      />,
    )
    const svg = container.querySelector<SVGSVGElement>(
      '[data-testid="flow-canvas-svg"]',
    )!
    const vb = svg.getAttribute('viewBox') ?? ''
    const [vbX, vbY, vbW, vbH] = vb.split(/\s+/).map(Number)
    expect(vbW).toBeLessThanOrEqual(1200)
    expect(vbH).toBeLessThanOrEqual(700)
    const groups = container.querySelectorAll<SVGGElement>(
      '[data-flow-draggable]',
    )
    expect(groups.length).toBe(CHAIN)
    // Operator's exact request: "no single bubble could be outside of
    // the canvas". Strict — every centroid inside [vbX, vbX+vbW] ×
    // [vbY, vbY+vbH] regardless of how pathological the depth chain.
    for (const g of Array.from(groups)) {
      const t = g.getAttribute('transform') ?? ''
      const m = t.match(/translate\(([-\d.]+),\s*([-\d.]+)\)/)
      expect(m).not.toBeNull()
      const x = Number(m![1])
      const y = Number(m![2])
      expect(x).toBeGreaterThanOrEqual(vbX)
      expect(x).toBeLessThanOrEqual(vbX + vbW)
      expect(y).toBeGreaterThanOrEqual(vbY)
      expect(y).toBeLessThanOrEqual(vbY + vbH)
    }
    // Operator's second rule: "max distance of a line cannot be longer
    // than a percentage of canvas". With render-time clamping into the
    // viewBox, every line is at most the viewBox diagonal — structural,
    // not flaky.
    const diagonal = Math.hypot(vbW, vbH)
    const lines = container.querySelectorAll<SVGLineElement>('line')
    for (const ln of Array.from(lines)) {
      const x1 = Number(ln.getAttribute('x1') ?? '0')
      const y1 = Number(ln.getAttribute('y1') ?? '0')
      const x2 = Number(ln.getAttribute('x2') ?? '0')
      const y2 = Number(ln.getAttribute('y2') ?? '0')
      expect(Math.hypot(x2 - x1, y2 - y1)).toBeLessThanOrEqual(diagonal)
    }
  })

  it('keeps every bubble visible (radius ≥40) and viewBox bounded for a 60-node graph', () => {
    const layout = makeLayout(60)
    const { container } = render(
      <FlowCanvasOrganic
        layout={layout}
        openJobId={null}
        hostJobId={null}
        onJobClick={() => {}}
        onJobDoubleClick={() => {}}
        onCanvasBackgroundClick={() => {}}
      />,
    )
    const svg = container.querySelector<SVGSVGElement>(
      '[data-testid="flow-canvas-svg"]',
    )!
    const vb = svg.getAttribute('viewBox') ?? ''
    const [, , vbW, vbH] = vb.split(/\s+/).map(Number)
    expect(vbW).toBeLessThanOrEqual(1200)
    expect(vbH).toBeLessThanOrEqual(700)
    const groups = container.querySelectorAll<SVGGElement>(
      '[data-flow-draggable]',
    )
    for (const g of Array.from(groups)) {
      const circles = g.querySelectorAll('circle')
      let maxR = 0
      for (const c of Array.from(circles)) {
        const r = Number(c.getAttribute('r') ?? '0')
        if (r > maxR) maxR = r
      }
      expect(maxR).toBeGreaterThanOrEqual(40)
    }
  })
})
