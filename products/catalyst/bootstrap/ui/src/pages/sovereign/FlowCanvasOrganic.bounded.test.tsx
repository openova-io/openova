/**
 * FlowCanvasOrganic — Bug #481 bounded layout regression test.
 *
 * The canvas previously rendered nodes scattered "between left + right
 * panes, tiny — barely visible — connected by kilometers of lines".
 * Root cause: weak forceY (strength 0.05) + weak link force (strength
 * 0.08) + ±140px Y scatter on first paint + no viewBox ceiling.
 *
 * The fix tightens four knobs (force strengths, seed scatter) AND adds
 * a hard MAX_VBOX clamp on the SVG viewBox + a per-tick bounding-box
 * clamp on every node's x/y. This test locks in the structural ceiling
 * — the SVG's effective viewBox MUST stay within the documented
 * MAX_VBOX_W × MAX_VBOX_H rectangle no matter what positions the
 * simulation produces.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { FlowCanvasOrganic } from './FlowCanvasOrganic'
import type { OrganicLayoutResult } from '@/lib/flowLayoutOrganic'

afterEach(() => cleanup())

const FAMILIES = [{ id: 'catalyst', label: 'Catalyst', color: '#fff' }]
const REGIONS = [{ id: 'primary', label: 'Primary' }]

function makeLayout(nodeCount: number): OrganicLayoutResult {
  const nodes = Array.from({ length: nodeCount }, (_, i) => ({
    id: `n${i}`,
    depth: i % 4,
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
  // Chain edges so the link force has work to do.
  const edges = nodes.slice(1).map((_, i) => ({
    fromId: `n${i}`,
    toId: `n${i + 1}`,
    fromStatus: 'pending' as const,
    crossRegion: false,
    kind: 'depends-on' as const,
  }))
  return {
    nodes,
    edges,
    maxDepth: 3,
    regions: REGIONS,
    families: FAMILIES,
  }
}

describe('FlowCanvasOrganic — Bug #481 bounded layout', () => {
  it('clamps viewBox width within MAX_VBOX_W (1600) on a typical 12-node graph', () => {
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
    // Bug #481 — must NOT exceed the documented hard ceiling.
    expect(w).toBeLessThanOrEqual(1600)
    expect(h).toBeLessThanOrEqual(900)
    // Floor (MIN_VBOX_W / MIN_VBOX_H) — readable on small graphs.
    expect(w).toBeGreaterThanOrEqual(1200)
    expect(h).toBeGreaterThanOrEqual(700)
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
})
