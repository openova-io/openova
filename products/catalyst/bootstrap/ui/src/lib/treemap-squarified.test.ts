import { describe, it, expect } from 'vitest'
import {
  computeSquarifiedLayout,
  aspectRatio,
  type SquarifiedRect,
} from './treemap-squarified'
import type { TreemapItem } from './treemap.types'

const item = (name: string, size: number, children?: TreemapItem[]): TreemapItem => ({
  id: name,
  name,
  count: 1,
  percentage: 50,
  size_value: size,
  children,
})

describe('computeSquarifiedLayout', () => {
  it('returns empty array for empty input', () => {
    expect(computeSquarifiedLayout([], 100, 100)).toEqual([])
    expect(computeSquarifiedLayout([item('a', 10)], 0, 100)).toEqual([])
    expect(computeSquarifiedLayout([item('a', 10)], 100, 0)).toEqual([])
  })

  it('lays out a single cell to fill the canvas', () => {
    const out = computeSquarifiedLayout([item('a', 100)], 200, 100)
    expect(out).toHaveLength(1)
    const r = out[0]
    expect(r.x0).toBe(0)
    expect(r.y0).toBe(0)
    expect(r.x1).toBe(200)
    expect(r.y1).toBe(100)
  })

  it('preserves total area within rounding tolerance', () => {
    const items = [
      item('a', 50),
      item('b', 30),
      item('c', 15),
      item('d', 5),
    ]
    const W = 600
    const H = 400
    const out = computeSquarifiedLayout(items, W, H)
    const totalCellArea = out.reduce((s, r) => s + (r.x1 - r.x0) * (r.y1 - r.y0), 0)
    // Allow 1% tolerance for floating-point accumulation.
    expect(totalCellArea).toBeGreaterThan(W * H * 0.99)
    expect(totalCellArea).toBeLessThan(W * H * 1.01)
  })

  it('produces aspect ratios <= 4 for cells > 50px on both axes', () => {
    // Realistic dashboard: many cells of varied sizes, no domination.
    const items: TreemapItem[] = []
    for (let i = 0; i < 30; i++) {
      items.push(item(`app-${i}`, 100 + Math.random() * 200))
    }
    const out = computeSquarifiedLayout(items, 1200, 700)
    for (const r of out) {
      const w = r.x1 - r.x0
      const h = r.y1 - r.y0
      if (w > 50 && h > 50) {
        expect(aspectRatio(r)).toBeLessThanOrEqual(4)
      }
    }
  })

  it('handles a dominant cell without producing horizontal stripes for the rest', () => {
    // The pathology recharts hits: one giant cell, many small ones.
    const items = [
      item('huge', 1000),
      ...Array.from({ length: 8 }, (_, i) => item(`small-${i}`, 20)),
    ]
    const out = computeSquarifiedLayout(items, 800, 500)
    // The huge cell will be elongated — that's fine. But the small
    // cells should be squarish.
    const smalls = out.filter((r) => r.item.name.startsWith('small'))
    for (const r of smalls) {
      expect(aspectRatio(r)).toBeLessThanOrEqual(8)
    }
  })

  it('skips zero/negative sized items', () => {
    const items = [item('a', 50), item('zero', 0), item('neg', -5), item('b', 50)]
    const out = computeSquarifiedLayout(items, 200, 100)
    const names = out.map((r) => r.item.name).sort()
    expect(names).toEqual(['a', 'b'])
  })

  it('recurses into children with header-strip offset', () => {
    const tree = [
      item('parent', 100, [item('child-a', 50), item('child-b', 50)]),
    ]
    const out = computeSquarifiedLayout(tree, 300, 200)
    const parent = out.find((r) => r.item.name === 'parent')!
    const children = out.filter((r) => r.item.name.startsWith('child'))
    expect(parent.isParent).toBe(true)
    expect(children).toHaveLength(2)
    // Children must start below the parent's header strip.
    for (const c of children) {
      expect(c.y0).toBeGreaterThanOrEqual(parent.y0 + 24) // NESTED_HEADER_HEIGHT_PX
      expect(c.x0).toBeGreaterThanOrEqual(parent.x0)
      expect(c.x1).toBeLessThanOrEqual(parent.x1)
      expect(c.y1).toBeLessThanOrEqual(parent.y1)
    }
  })

  it('emits depth 0 for top-level + depth 1 for children', () => {
    const tree = [item('parent', 100, [item('child', 100)])]
    const out = computeSquarifiedLayout(tree, 200, 200)
    const parent = out.find((r) => r.depth === 0)!
    const child = out.find((r) => r.depth === 1)!
    expect(parent.item.name).toBe('parent')
    expect(child.item.name).toBe('child')
  })
})

describe('aspectRatio', () => {
  it('returns w/h or h/w whichever is larger', () => {
    const r: SquarifiedRect = {
      x0: 0,
      y0: 0,
      x1: 100,
      y1: 50,
      depth: 0,
      isParent: false,
      item: item('x', 1),
    }
    expect(aspectRatio(r)).toBe(2)
  })

  it('returns 1 for square', () => {
    const r: SquarifiedRect = {
      x0: 0,
      y0: 0,
      x1: 50,
      y1: 50,
      depth: 0,
      isParent: false,
      item: item('x', 1),
    }
    expect(aspectRatio(r)).toBe(1)
  })
})
