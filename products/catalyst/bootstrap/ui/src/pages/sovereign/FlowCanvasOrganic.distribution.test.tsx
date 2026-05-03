/**
 * FlowCanvasOrganic — X-axis distribution regression (issue #669 round 3).
 *
 * Reproduces the exact shape from the founder's UAT screenshot:
 *   • 1 bubble at depth 0 ("Provision Hetzner network")
 *   • 1 bubble at depth 1 ("Provision Hetzner ...")
 *   • 30 siblings at depth 2 (real provision graph has 30+ blueprint
 *     installs at the deepest depth)
 *
 * Round-2 (constant perDepthX) failed: the dense bucket piled into a
 * thin sub-grid against the right edge while 60% of the canvas sat
 * empty on the left. Round-3 introduces variable-width depth slots —
 * dense buckets claim more X-extent so the cluster spreads laterally.
 *
 * Asserts:
 *   1. depth-0 X < depth-1 X < depth-2 (centerline) — left-to-right.
 *   2. Dense bucket spans ≥ 30% of total layout width — not piled.
 *   3. Sparse bubbles (depth 0/1) sit comfortably inside the canvas
 *      (i.e. X ≥ R, X ≤ totalWidth - R).
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { FlowCanvasOrganic } from './FlowCanvasOrganic'

afterEach(() => cleanup())

const FAMILIES = [{ id: 'catalyst', label: 'Catalyst', color: '#fff' }]
const REGIONS = [{ id: 'primary', label: 'Primary' }]

function makeNode(opts: {
  id: string
  depth: number
  depRank: number
  parentId?: string
  dependsOn?: string[]
}) {
  return {
    id: opts.id,
    depth: opts.depth,
    depRank: opts.depRank,
    regionId: 'primary',
    familyId: 'catalyst',
    label: opts.id,
    subLabel: '',
    status: 'pending' as const,
    isGroup: false,
    isFolded: false,
    childCount: 0,
    job: {
      id: opts.id,
      jobName: opts.id,
      type: 'install' as const,
      appId: 'x',
      parentId: opts.parentId ?? '',
      dependsOn: opts.dependsOn ?? [],
      childIds: [],
      status: 'pending' as const,
      startedAt: null,
      finishedAt: null,
      durationMs: 0,
    },
  }
}

function buildScreenshotLayout() {
  // Replicates the founder's screenshot:
  //   d0: 1 (provision-hetzner-network)
  //   d1: 1 (provision-hetzner-control-plane) depends-on d0
  //   d2: 30 leaves all depending on d1
  const root = makeNode({ id: 'd0', depth: 0, depRank: 0 })
  const phase1 = makeNode({ id: 'd1', depth: 1, depRank: 1, dependsOn: ['d0'] })
  const leaves = Array.from({ length: 30 }, (_, i) =>
    makeNode({
      id: `leaf-${i}`,
      depth: 2,
      depRank: 2 + i,
      dependsOn: ['d1'],
    }),
  )
  const nodes = [root, phase1, ...leaves]
  const edges = [
    { fromId: 'd0', toId: 'd1', fromStatus: 'pending' as const, crossRegion: false, kind: 'depends-on' as const },
    ...leaves.map((n) => ({
      fromId: 'd1',
      toId: n.id,
      fromStatus: 'pending' as const,
      crossRegion: false,
      kind: 'depends-on' as const,
    })),
  ]
  return { nodes, edges, families: FAMILIES, regions: REGIONS, maxDepth: 2 }
}

function readPositions(container: HTMLElement) {
  const groups = container.querySelectorAll<SVGGElement>('[data-flow-draggable]')
  const out = new Map<string, { x: number; y: number }>()
  for (const g of Array.from(groups)) {
    const id = g.getAttribute('data-job-id') ?? ''
    const t = g.getAttribute('transform') ?? ''
    const m = t.match(/translate\(([-\d.]+),\s*([-\d.]+)\)/)
    if (!m) continue
    out.set(id, { x: Number(m[1]), y: Number(m[2]) })
  }
  return out
}

describe('FlowCanvasOrganic — X-axis distribution under dense-bucket shape (#669 round 3)', () => {
  it('does NOT pile the dense bucket against the right edge', async () => {
    const layout = buildScreenshotLayout()
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
    // Let the d3-force sim converge.
    await new Promise((r) => setTimeout(r, 300))

    const pos = readPositions(container)
    expect(pos.size).toBe(32)

    // 1. Depth ordering on X — left to right.
    const x0 = pos.get('d0')!.x
    const x1 = pos.get('d1')!.x
    const leafXs = Array.from({ length: 30 }, (_, i) => pos.get(`leaf-${i}`)!.x)
    expect(x0).toBeLessThan(x1)
    for (const lx of leafXs) {
      expect(x1).toBeLessThan(lx) // every leaf is right of the depth-1 anchor
    }

    // 2. Dense bucket spread — leaf cluster width ≥ 30% of total
    // layout width. (Total layout width = max X across all bubbles.)
    const allXs = [x0, x1, ...leafXs]
    const totalWidth = Math.max(...allXs) - Math.min(...allXs)
    const leafSpan = Math.max(...leafXs) - Math.min(...leafXs)
    // Round-2 failure mode: leafSpan ≈ 0.13 × totalWidth (sub-grid
    // crammed into 80% × perDepthX = ~128 px). Round-3 fix puts the
    // dense bucket in its own slot whose width tracks sibling count,
    // so leafSpan should occupy a meaningful fraction of the total.
    expect(leafSpan / totalWidth).toBeGreaterThanOrEqual(0.3)

    // 3. Sparse bubbles must NOT all share the same X as the leftmost
    // leaf — i.e., depth 0/1 are visibly separated from the dense
    // cluster, not crowding the same x-band.
    const minLeafX = Math.min(...leafXs)
    expect(x1).toBeLessThan(minLeafX)
    // The gap between depth-1 anchor and the leftmost leaf is ≥ R
    // (one bubble radius — visual breathing room).
    const svg = container.querySelector<SVGSVGElement>('[data-testid="flow-canvas-svg"]')!
    const someGroup = container.querySelector<SVGGElement>('[data-flow-draggable]')!
    const lastCircle = someGroup.querySelectorAll('circle')[someGroup.querySelectorAll('circle').length - 1]
    const r = Number(lastCircle?.getAttribute('r') ?? '40')
    expect(minLeafX - x1).toBeGreaterThanOrEqual(r)

    // 4. Sanity — every bubble is inside the SVG viewBox.
    const vb = (svg.getAttribute('viewBox') ?? '').split(/\s+/).map(Number)
    const [, , vbW, vbH] = vb
    for (const [, p] of pos) {
      expect(p.x).toBeGreaterThanOrEqual(0)
      expect(p.x).toBeLessThanOrEqual(vbW)
      expect(p.y).toBeGreaterThanOrEqual(0)
      expect(p.y).toBeLessThanOrEqual(vbH)
    }
  })

  it('keeps adjacent leaves at least 2R apart (forceCollide invariant under variable slots)', async () => {
    const layout = buildScreenshotLayout()
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
    await new Promise((r) => setTimeout(r, 300))
    const pos = readPositions(container)
    const someGroup = container.querySelector<SVGGElement>('[data-flow-draggable]')!
    const lastCircle = someGroup.querySelectorAll('circle')[someGroup.querySelectorAll('circle').length - 1]
    const r = Number(lastCircle?.getAttribute('r') ?? '40')
    const minDist = 2 * r // bubble rims must not overlap (some collide-pad slack tolerated)
    const TOL = 4
    const ids = Array.from(pos.keys())
    for (let i = 0; i < ids.length; i++) {
      for (let j = i + 1; j < ids.length; j++) {
        const a = pos.get(ids[i])!
        const b = pos.get(ids[j])!
        const d = Math.hypot(a.x - b.x, a.y - b.y)
        expect(d).toBeGreaterThanOrEqual(minDist - TOL)
      }
    }
  })
})
