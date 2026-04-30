/**
 * FlowCanvasV4.test.tsx — locks the visual-fidelity contract for the
 * Flow canvas itself (separate from the FlowPage data-flow tests so a
 * canvas regression doesn't get masked by an unrelated FlowPage issue).
 *
 * Forcing functions covered:
 *   • Every node renders a <g class="node-glyph"> child with the
 *     family icon path inside (NOT a single-letter glyph).
 *   • Every node circle has r ≥ 28.
 *   • Multi-region fixture renders ≥ 2 [data-testid=flow-region-*]
 *     band frames in the SVG.
 *   • Bezier edges produce SVG `path d` strings containing a `C`
 *     segment (not just `L` straight lines).
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, cleanup } from '@testing-library/react'
import { FlowCanvasV4 } from './FlowCanvasV4'
import {
  flowLayoutV4,
  DEFAULT_FAMILIES,
} from '@/lib/flowLayoutV4'
import { DEMO_TWO_REGION_FIXTURE } from './flowDeploymentTreeData'

afterEach(() => cleanup())

function renderCanvas() {
  const layout = flowLayoutV4(
    [...DEMO_TWO_REGION_FIXTURE.jobs],
    {
      regions: DEMO_TWO_REGION_FIXTURE.regions,
      hints: DEMO_TWO_REGION_FIXTURE.hints,
    },
  )
  return {
    layout,
    ...render(
      <FlowCanvasV4
        layout={layout}
        families={DEFAULT_FAMILIES}
        embedded={false}
        openJobId={null}
        highlightJobId={null}
        onJobClick={() => {}}
        onJobDoubleClick={() => {}}
        onCanvasBackgroundClick={() => {}}
      />,
    ),
  }
}

describe('FlowCanvasV4 — mockup-fidelity contract', () => {
  it('FORCING FUNCTION: every node renders a <g class="node-glyph"> child', () => {
    const { layout } = renderCanvas()
    const nodes = document.querySelectorAll('[data-testid^="flow-job-"]')
    expect(nodes.length).toBe(layout.nodes.length)
    for (const node of Array.from(nodes)) {
      const glyph = node.querySelector('.node-glyph')
      expect(glyph, `Node ${node.getAttribute('data-testid')} is missing <g class="node-glyph">`).toBeTruthy()
      // Glyph should contain at least one <path>.
      const path = glyph!.querySelector('path')
      expect(path).toBeTruthy()
    }
  })

  it('FORCING FUNCTION: every node circle has r >= 28 (= 56px diameter)', () => {
    renderCanvas()
    const circles = document.querySelectorAll('circle[data-testid^="flow-node-circle-"]')
    expect(circles.length).toBeGreaterThan(0)
    for (const c of Array.from(circles)) {
      const r = parseFloat(c.getAttribute('r') ?? '0')
      expect(r, `Node circle ${c.getAttribute('data-testid')} has r=${r} — must be ≥ 28`).toBeGreaterThanOrEqual(28)
    }
  })

  it('FORCING FUNCTION: ≥ 2 [data-testid=flow-region-*] band frames render for multi-region fixture', () => {
    renderCanvas()
    const bands = document.querySelectorAll('[data-testid^="flow-region-"]')
    expect(bands.length).toBeGreaterThanOrEqual(2)
    const ids = Array.from(bands).map((b) => b.getAttribute('data-testid'))
    expect(ids).toContain('flow-region-fsn1')
    expect(ids).toContain('flow-region-nbg1')
  })

  it('FORCING FUNCTION: bezier edges emit SVG `path d` strings containing a `C` segment', () => {
    renderCanvas()
    const edges = document.querySelectorAll('[data-testid^="flow-edge-"]')
    expect(edges.length).toBeGreaterThan(0)
    let bezierCount = 0
    for (const e of Array.from(edges)) {
      const d = e.getAttribute('d') ?? ''
      if (d.includes(' C ')) bezierCount++
    }
    expect(bezierCount).toBeGreaterThan(0)
  })

  it('exposes the family glyph identity via data-family-glyph attribute', () => {
    renderCanvas()
    // Spot-check: the cilium nodes should carry data-family-glyph="spine".
    const ciliumFsn = document.querySelector('[data-testid="flow-job-install-cilium::fsn1"]')
    expect(ciliumFsn).toBeTruthy()
    const glyph = ciliumFsn!.querySelector('[data-family-glyph]')
    expect(glyph).toBeTruthy()
    expect(glyph!.getAttribute('data-family-glyph')).toBe('spine')
  })

  it('renders an empty-state placeholder when given zero nodes + zero regions', () => {
    const layout = flowLayoutV4([])
    render(
      <FlowCanvasV4
        layout={layout}
        families={DEFAULT_FAMILIES}
        embedded={false}
        openJobId={null}
        highlightJobId={null}
        onJobClick={() => {}}
        onJobDoubleClick={() => {}}
        onCanvasBackgroundClick={() => {}}
      />,
    )
    expect(document.querySelector('[data-testid="flow-canvas-empty"]')).toBeTruthy()
  })
})
