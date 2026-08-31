/**
 * graphJobFilter — decide which flow-stream jobs the /jobs GRAPH renders
 * (Refs #6727 follow-up).
 *
 * # Why this exists (the bug it fixes)
 *
 * `/jobs` is the FINITE-jobs surface: the list backend (`jobs.FilterFiniteJobs`)
 * drops the open-ended reconcilers (Flux Kustomization / long-running
 * reconciler Deployments), so the list shows e.g. "Kustomization (0)". But the
 * openova-flow snapshot the GRAPH renders carries the FULL DAG — including
 * those ~22 `reconcile-*` nodes. The result was two live bugs:
 *
 *   • the graph rendered reconcile nodes that have NO chip to control them
 *     (the chip's count comes from the finite list = 0, so it's hidden), so
 *     they showed regardless of selection;
 *   • "remove all chips" still left those nodes on the canvas, because the
 *     chip-strip's visible set never governed them.
 *
 * The graph must therefore honour the SAME finite scope as the list:
 * open-ended reconciler kinds are never rendered on /jobs (they live on the
 * Cloud → Reconciliation lens). With them gone, every remaining node kind has
 * a real chip, so the chip filter — including "remove every chip → empty
 * canvas" — behaves exactly as the operator expects.
 *
 * This module is a PURE function so the behaviour is unit-tested directly,
 * rather than only through a browser walk.
 */

import type { Job } from './jobs.types'
import { flowJobKind, type GraphKind } from './flowJobKind'

/**
 * Kinds excluded from /jobs entirely — the open-ended reconcilers the finite
 * list drops (`jobs.FilterFiniteJobs`). Kept as a named set so the list and
 * the graph share one definition of "not a finite job".
 */
export const OPEN_ENDED_GRAPH_KINDS: ReadonlySet<GraphKind> = new Set<GraphKind>([
  'reconcile',
  'reconciler',
])

/**
 * Select the jobs the /jobs graph should render.
 *
 *   1. A LEAF is kept when its kind is a finite job (not in
 *      {@link OPEN_ENDED_GRAPH_KINDS}) AND — when a chip filter is active —
 *      its kind is in `visibleKinds`.
 *   2. A GROUP is kept only when it still has at least one kept leaf
 *      descendant, so a group that held only reconcilers (or only
 *      chip-filtered kinds) drops out instead of leaving an empty bubble.
 *
 * `visibleKinds === undefined` means "no chip filter yet" → every finite
 * kind is shown. An EMPTY `visibleKinds` (operator removed every chip) keeps
 * no leaves → an empty canvas, which is the correct, expected result.
 *
 * Pure: returns a new filtered array; the input is untouched. Ancestor walk
 * is cycle-guarded.
 */
export function selectGraphJobs(
  jobs: readonly Job[],
  visibleKinds: ReadonlySet<GraphKind> | undefined,
): Job[] {
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)

  const keep = new Set<string>()
  // 1. finite, chip-visible leaves
  for (const j of jobs) {
    if (j.type === 'group') continue
    const kind = flowJobKind(j)
    if (OPEN_ENDED_GRAPH_KINDS.has(kind)) continue
    if (visibleKinds && !visibleKinds.has(kind)) continue
    keep.add(j.id)
  }
  // 2. every ancestor group of a kept leaf survives (walk parentId up)
  for (const leafId of [...keep]) {
    let pid = byId.get(leafId)?.parentId
    const seen = new Set<string>()
    while (pid && !seen.has(pid)) {
      seen.add(pid)
      keep.add(pid)
      pid = byId.get(pid)?.parentId
    }
  }
  return jobs.filter((j) => keep.has(j.id))
}
