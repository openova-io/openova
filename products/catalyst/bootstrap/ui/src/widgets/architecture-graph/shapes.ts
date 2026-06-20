/**
 * shapes.ts — SHAPE channel geometry for the unified Cloud-graph
 * (#3958). The canvas renders one of six polygon shapes per node,
 * driven by NODE_CATEGORY[type]. This module owns ONLY the pure
 * geometry math (points strings + regular-polygon helpers) so
 * GraphCanvas.tsx stays a pure component file
 * (react-refresh/only-export-components).
 *
 * Each shape is sized to a target radius `r` (the circumradius) so a
 * circle, square, triangle, hexagon, pentagon and diamond of the same
 * `r` read as the same visual weight. Per docs/INVIOLABLE-PRINCIPLES.md
 * #4 — the shape comes from the category, never hardcoded at a call
 * site.
 */

import type { NodeCategory } from './types'

/**
 * Regular-polygon vertex points (centred on 0,0). `sides` = number of
 * vertices, `r` = circumradius, `rotationDeg` rotates the first vertex
 * from straight-up (−90°). Returns an SVG `points` string.
 */
export function regularPolygonPoints(sides: number, r: number, rotationDeg = 0): string {
  const pts: string[] = []
  const rot = (rotationDeg * Math.PI) / 180
  for (let i = 0; i < sides; i++) {
    // Start at the top (−90°) and step clockwise.
    const a = -Math.PI / 2 + rot + (i * 2 * Math.PI) / sides
    const x = r * Math.cos(a)
    const y = r * Math.sin(a)
    pts.push(`${x.toFixed(2)},${y.toFixed(2)}`)
  }
  return pts.join(' ')
}

/** Square points (axis-aligned), inscribed so the corners sit at radius r. */
export function squarePoints(r: number): string {
  // A square whose half-diagonal is r. Use a slightly smaller side so
  // the square's visual mass matches the circle of the same r.
  const s = r * 0.82
  return `${-s},${-s} ${s},${-s} ${s},${s} ${-s},${s}`
}

/** Diamond points (square rotated 45°), corners at radius r. */
export function diamondPoints(r: number): string {
  return `0,${-r} ${r},0 0,${r} ${-r},0`
}

/**
 * The kind of SVG element a category renders as, plus its geometry.
 * 'circle' → <circle r>; everything else → <polygon points>.
 */
export interface ShapeGeometry {
  el: 'circle' | 'polygon'
  /** For polygons — the points string (centred on 0,0). */
  points?: string
  /** For circles — the radius. */
  r?: number
}

/**
 * shapeForCategory — the geometry the canvas draws for a given coarse
 * category at radius `r`:
 *   compute → ● circle
 *   control → ■ square
 *   network → ▲ triangle (3 sides)
 *   config  → ◆ diamond
 *   data    → ⬢ hexagon (6 sides, flat-top)
 *   scope   → ⬠ pentagon (5 sides)
 */
export function shapeForCategory(category: NodeCategory, r: number): ShapeGeometry {
  switch (category) {
    case 'compute':
      return { el: 'circle', r }
    case 'control':
      return { el: 'polygon', points: squarePoints(r) }
    case 'network':
      // Upward triangle — pad the radius a touch so the triangle's
      // smaller area still reads at the same weight.
      return { el: 'polygon', points: regularPolygonPoints(3, r * 1.12) }
    case 'config':
      return { el: 'polygon', points: diamondPoints(r) }
    case 'data':
      // Hexagon, flat-top (rotate 30° so a flat edge is on top).
      return { el: 'polygon', points: regularPolygonPoints(6, r, 30) }
    case 'scope':
      // Pentagon, point-up.
      return { el: 'polygon', points: regularPolygonPoints(5, r) }
    default:
      return { el: 'circle', r }
  }
}
