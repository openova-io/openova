/**
 * markers.ts — ArchiMate edge marker id helpers + uniqueness pass.
 * Pulled out of GraphCanvas.tsx to satisfy react-refresh/only-export-
 * components: GraphCanvas.tsx now exports nothing but the React
 * component itself.
 *
 * Each (kind, stroke) pair maps to a deterministic SVG marker id so
 * the renderer can include the marker once in a <defs> block and have
 * every <line markerEnd=...> reference it.
 */

import {
  EDGE_MARKER_END,
  EDGE_MARKER_START,
  EDGE_STROKE,
  type ArchEdgeType,
  type EdgeMarker,
  type GraphEdge,
} from './types'

function strokeSlug(stroke: string): string {
  return stroke.replace('#', '').toLowerCase()
}

export function markerId(kind: EdgeMarker, stroke: string): string {
  if (!kind) return ''
  return `archmark-${kind}-${strokeSlug(stroke)}`
}

export interface MarkerDef {
  kind: EdgeMarker
  stroke: string
}

/**
 * Walk the live edge set and return one entry per unique (kind,
 * stroke) pair the SVG <defs> block needs to render.
 */
export function uniqueMarkerDefs(edges: GraphEdge[]): MarkerDef[] {
  const seen = new Set<string>()
  const out: MarkerDef[] = []
  for (const e of edges) {
    const stroke = EDGE_STROKE[e.type as ArchEdgeType] ?? '#888'
    const start = EDGE_MARKER_START[e.type as ArchEdgeType]
    const end = EDGE_MARKER_END[e.type as ArchEdgeType]
    for (const k of [start, end]) {
      if (!k) continue
      const key = `${k}:${stroke}`
      if (seen.has(key)) continue
      seen.add(key)
      out.push({ kind: k, stroke })
    }
  }
  return out
}
