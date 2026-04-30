/**
 * flowLayoutOrganic — pure data prep for the organic Flow canvas.
 *
 * Replaces flowLayoutV4.ts (stage-column / Sugiyama grid). The grid was
 * the cause of the "8x5 squashed in middle 1/3" bug operators kept
 * rejecting. This module returns only the topology — depth (longest
 * path from a root), region, family, status — and lets the canvas's
 * d3-force simulation do the actual positioning.
 *
 * Design contract per operator (2026-04-30):
 *   • Bubbles spread organically across the FULL canvas width, x-axis
 *     determined by dependency depth (depth 0 → leftmost, deepest →
 *     rightmost).
 *   • Same-depth siblings scatter loosely vertically; they MUST NOT be
 *     vertically aligned in a strict column.
 *   • Edges are direct depth-aware bezier curves between live positions
 *     with arrowheads and source-status colour.
 *   • NO "STAGE 1/2/..." labels. NO column dividers. The layout has no
 *     concept of "stage".
 *   • Batch mode: same nodes API, but the canvas collapses into one
 *     bubble per batchId before calling here.
 *
 * Pure: same input → same output. No DOM, no React, no side effects.
 */

import type { Job } from './jobs.types'

/** A blueprint family — used to colour-code bubbles. */
export interface OrganicFamily {
  id: string
  label: string
  color: string
}

/** A region descriptor — used to vertically group bubbles. */
export interface OrganicRegion {
  id: string
  label: string
  meta?: string
}

/** Per-job hint provided by the page — region + family + extra dep ids. */
export interface OrganicNodeHints {
  regionId: string
  familyId: string
  /** Optional jobIds to add as extra depsOn edges (component-graph deps). */
  extraDepIds?: string[]
}

/** A node in the organic layout output. */
export interface OrganicNode {
  id: string
  /** 0-based longest-path depth from any root (no in-edge node). */
  depth: number
  regionId: string
  familyId: string
  /** Display label (jobName less leading "install-"). */
  label: string
  /** Status drives ring colour. */
  status: Job['status']
  /** Sub-label (duration). */
  subLabel: string
  /** Underlying job for tooltip / click handler. */
  job: Job
}

/** A directed edge between two nodes. */
export interface OrganicEdge {
  fromId: string
  toId: string
  fromStatus: Job['status']
  /** Cross-region edge — drawn with a different (warm) tone. */
  crossRegion: boolean
}

/** The output. */
export interface OrganicLayoutResult {
  nodes: OrganicNode[]
  edges: OrganicEdge[]
  /** Max depth across all nodes — useful for x-axis scale. */
  maxDepth: number
  regions: OrganicRegion[]
  families: OrganicFamily[]
}

export const FALLBACK_REGION_ID = 'primary'

/**
 * Compute the layout-ready data from raw jobs + hints.
 *
 * Depth assignment uses Kahn-style topological depth: for each node,
 * depth = max(depth(parent)) + 1; nodes with no incoming edges have
 * depth 0. Cycles are broken (impossible by construction here, but
 * defensive — capped at jobs.length iterations).
 */
export function flowLayoutOrganic(
  jobs: readonly Job[],
  opts: {
    hints: ReadonlyMap<string, OrganicNodeHints>
    regions: readonly OrganicRegion[]
    families: readonly OrganicFamily[]
  },
): OrganicLayoutResult {
  const { hints, regions, families } = opts

  const fallbackRegion: OrganicRegion =
    regions.find((r) => r.id === FALLBACK_REGION_ID) ?? regions[0] ?? {
      id: FALLBACK_REGION_ID,
      label: 'Primary Region',
    }

  // Build adjacency from Job.dependsOn + hint.extraDepIds.
  const idSet = new Set(jobs.map((j) => j.id))
  const inEdges = new Map<string, Set<string>>()
  const outEdges = new Map<string, Set<string>>()
  for (const j of jobs) {
    inEdges.set(j.id, new Set())
    outEdges.set(j.id, new Set())
  }
  function addEdge(from: string, to: string) {
    if (!idSet.has(from) || !idSet.has(to)) return
    if (from === to) return
    inEdges.get(to)!.add(from)
    outEdges.get(from)!.add(to)
  }
  for (const j of jobs) {
    for (const dep of j.dependsOn ?? []) addEdge(dep, j.id)
    const h = hints.get(j.id)
    for (const dep of h?.extraDepIds ?? []) addEdge(dep, j.id)
  }

  // Compute depth = longest-path from a root (any node with no in-edges).
  // Iterative relaxation: depth[v] = max(depth[u] + 1) for u in inEdges[v].
  const depth = new Map<string, number>()
  for (const j of jobs) depth.set(j.id, 0)
  let changed = true
  let iterations = 0
  const cap = jobs.length + 2 // cycle defence
  while (changed && iterations < cap) {
    changed = false
    iterations++
    for (const j of jobs) {
      const ins = inEdges.get(j.id)!
      let bestParent = -1
      for (const p of ins) bestParent = Math.max(bestParent, depth.get(p) ?? 0)
      const want = bestParent + 1
      if (want > 0 && want > (depth.get(j.id) ?? 0)) {
        depth.set(j.id, want)
        changed = true
      }
    }
  }

  // Nodes.
  const nodes: OrganicNode[] = jobs.map((j) => {
    const h = hints.get(j.id)
    const regionId = h?.regionId && regions.some((r) => r.id === h.regionId)
      ? h.regionId
      : fallbackRegion.id
    const familyId = h?.familyId ?? 'platform'
    const label = j.jobName.replace(/^install-/, '')
    const subLabel = j.durationMs > 0 ? formatDurationShort(j.durationMs) : ''
    return {
      id: j.id,
      depth: depth.get(j.id) ?? 0,
      regionId,
      familyId,
      label,
      status: j.status,
      subLabel,
      job: j,
    }
  })

  // Edges from outEdges adjacency.
  const edges: OrganicEdge[] = []
  const nodeById = new Map(nodes.map((n) => [n.id, n]))
  for (const from of jobs) {
    const outs = outEdges.get(from.id) ?? new Set()
    for (const toId of outs) {
      const fromNode = nodeById.get(from.id)
      const toNode = nodeById.get(toId)
      if (!fromNode || !toNode) continue
      edges.push({
        fromId: from.id,
        toId,
        fromStatus: fromNode.status,
        crossRegion: fromNode.regionId !== toNode.regionId,
      })
    }
  }

  const maxDepth = nodes.reduce((m, n) => Math.max(m, n.depth), 0)

  return {
    nodes,
    edges,
    maxDepth,
    regions: [...regions],
    families: [...families],
  }
}

/** Format ms → "1m 23s" / "12s" / "152ms". */
function formatDurationShort(ms: number): string {
  if (ms < 1000) return `${ms}ms`
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  if (m < 60) return rs ? `${m}m ${rs}s` : `${m}m`
  const h = Math.floor(m / 60)
  const rm = m % 60
  return rm ? `${h}h ${rm}m` : `${h}h`
}
