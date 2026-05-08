/**
 * treemap-squarified.ts — squarified treemap layout (Bruls, Huijsen,
 * van Wijk 2000) computed in pure TypeScript.
 *
 * Replaces recharts' default slice-and-dice tiler — recharts ships
 * only one algorithm and it produces the "useless horizontal stripes"
 * pathology when one cell dominates. The squarified algorithm packs
 * cells so each one's aspect ratio is as close to 1 as possible,
 * which is the visually correct choice for capacity dashboards.
 *
 * The implementation follows the original paper exactly: sort children
 * DESC by size, then greedily pack into "rows" (perpendicular to the
 * shorter side of the remaining rectangle), pivoting to a new row
 * whenever the next cell would worsen the worst aspect ratio of the
 * current row.
 *
 * Recursion is explicit: parents render their own header strip, then
 * recurse into their children inside the inner rectangle. The renderer
 * (Dashboard.tsx) consumes flat rectangles tagged with their depth +
 * a back-pointer to the original TreemapItem so cell-content
 * (label, sub-label, click handler) stays unchanged.
 */

import type { TreemapItem } from '@/lib/treemap.types'

export interface SquarifiedRect {
  /** Pixel coords in the parent SVG. */
  x0: number
  y0: number
  x1: number
  y1: number
  /** 0 = root cell, 1 = first nested layer, … */
  depth: number
  /** The original tree node so the renderer can read name/percentage/etc. */
  item: TreemapItem
  /** True when this rect contains nested children (drawn as a frame
   *  with a header strip; the renderer should suppress the body fill). */
  isParent: boolean
}

/** Pixel band reserved at the top of every parent cell for its label. */
export const NESTED_HEADER_HEIGHT_PX = 24
/** Inner padding below the header before children start. */
export const NESTED_PADDING_PX = 2

/** A scaled item — its `area` is in pixel² (post-scaling), so all
 *  packing math operates in a single unit system. */
interface Scaled {
  it: TreemapItem
  area: number
}

/**
 * computeSquarifiedLayout — recursive entry point. Returns a flat
 * array of rectangles (parent first, children after) that the renderer
 * can iterate without re-walking the tree.
 *
 * `width` and `height` are the available pixel canvas. Items with
 * size_value <= 0 are filtered out.
 */
export function computeSquarifiedLayout(
  items: readonly TreemapItem[],
  width: number,
  height: number,
  depth = 0,
): SquarifiedRect[] {
  if (width <= 0 || height <= 0 || items.length === 0) return []
  const positive = items.filter((it) => (it.size_value ?? 0) > 0)
  if (positive.length === 0) return []
  const totalRaw = positive.reduce((s, it) => s + (it.size_value ?? 0), 0)
  if (totalRaw <= 0) return []
  const scale = (width * height) / totalRaw
  // Sort DESC — squarification depends on largest-first ordering.
  const scaled: Scaled[] = [...positive]
    .sort((a, b) => (b.size_value ?? 0) - (a.size_value ?? 0))
    .map((it) => ({ it, area: (it.size_value ?? 0) * scale }))

  const out: SquarifiedRect[] = []
  squarify(scaled, 0, 0, width, height, depth, out)
  return out
}

/** Internal: pack `items` (sorted DESC, already in pixel² area) into rect. */
function squarify(
  items: Scaled[],
  x: number,
  y: number,
  w: number,
  h: number,
  depth: number,
  out: SquarifiedRect[],
): void {
  if (items.length === 0 || w <= 0 || h <= 0) return

  let row: Scaled[] = []
  let rowArea = 0
  let i = 0

  while (i < items.length) {
    const next = items[i]
    const tryRow = [...row, next]
    const tryArea = rowArea + next.area
    const shorter = Math.min(w, h)
    const currentWorst = worstAspect(row, rowArea, shorter)
    const tryWorst = worstAspect(tryRow, tryArea, shorter)

    if (row.length === 0 || tryWorst <= currentWorst) {
      row = tryRow
      rowArea = tryArea
      i++
    } else {
      flushRow(row, rowArea, x, y, w, h, depth, out)
      const consumed = rowArea / shorter
      if (w <= h) {
        y += consumed
        h -= consumed
      } else {
        x += consumed
        w -= consumed
      }
      row = []
      rowArea = 0
    }
  }

  if (row.length > 0) {
    flushRow(row, rowArea, x, y, w, h, depth, out)
  }
}

/** Worst aspect ratio for a row of cells laid perpendicular to the
 *  shorter side. Per Bruls/Huijsen/van Wijk 2000 eq. 1. All values in
 *  pixel² (consistent with `Scaled.area`). */
function worstAspect(row: Scaled[], rowArea: number, shorter: number): number {
  if (rowArea <= 0 || row.length === 0) return Number.POSITIVE_INFINITY
  let max = 0
  let min = Number.POSITIVE_INFINITY
  for (const r of row) {
    if (r.area > max) max = r.area
    if (r.area < min) min = r.area
  }
  const s2 = shorter * shorter
  const r2 = rowArea * rowArea
  return Math.max((s2 * max) / r2, r2 / (s2 * min))
}

/** Lay out one row's cells perpendicular to the shorter side. */
function flushRow(
  row: Scaled[],
  rowArea: number,
  x: number,
  y: number,
  w: number,
  h: number,
  depth: number,
  out: SquarifiedRect[],
): void {
  if (rowArea <= 0 || row.length === 0) return
  const shorter = Math.min(w, h)
  const thickness = rowArea / shorter
  if (w <= h) {
    // Row spans full width along top edge; cell heights = `thickness`.
    let cx = x
    for (const s of row) {
      const cw = s.area / thickness
      addCell(s.it, cx, y, cx + cw, y + thickness, depth, out)
      cx += cw
    }
  } else {
    // Row spans full height along left edge; cell widths = `thickness`.
    let cy = y
    for (const s of row) {
      const ch = s.area / thickness
      addCell(s.it, x, cy, x + thickness, cy + ch, depth, out)
      cy += ch
    }
  }
}

/** Emit one rectangle and recurse if the cell has children. */
function addCell(
  it: TreemapItem,
  x0: number,
  y0: number,
  x1: number,
  y1: number,
  depth: number,
  out: SquarifiedRect[],
): void {
  const isParent = !!(it.children && it.children.length > 0)
  out.push({ x0, y0, x1, y1, depth, item: it, isParent })
  if (isParent) {
    const innerY0 = y0 + NESTED_HEADER_HEIGHT_PX + NESTED_PADDING_PX
    const innerX0 = x0 + NESTED_PADDING_PX
    const innerX1 = x1 - NESTED_PADDING_PX
    const innerY1 = y1 - NESTED_PADDING_PX
    if (innerX1 > innerX0 && innerY1 > innerY0) {
      const childRects = computeSquarifiedLayout(
        it.children!,
        innerX1 - innerX0,
        innerY1 - innerY0,
        depth + 1,
      )
      for (const r of childRects) {
        out.push({
          ...r,
          x0: r.x0 + innerX0,
          y0: r.y0 + innerY0,
          x1: r.x1 + innerX0,
          y1: r.y1 + innerY0,
        })
      }
    }
  }
}

/** Aspect-ratio bound check (used by tests). max(w/h, h/w). */
export function aspectRatio(r: SquarifiedRect): number {
  const w = r.x1 - r.x0
  const h = r.y1 - r.y0
  if (w <= 0 || h <= 0) return Number.POSITIVE_INFINITY
  return Math.max(w / h, h / w)
}
