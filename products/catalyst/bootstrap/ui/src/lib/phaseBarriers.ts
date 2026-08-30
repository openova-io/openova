/**
 * phaseBarriers — synthesize the cross-phase dependency edges the /jobs
 * DAG would otherwise leave implicit (Refs #6727).
 *
 * # Why this exists
 *
 * The openova-flow stream emits two edge kinds: `finish-to-start` (a real
 * per-job dependency) and `contains` (a job's membership in a phase group).
 * On the live provisioning graph the finish-to-start edges are almost
 * entirely SAME-type: 236 `HelmRelease -> HelmRelease` (`spec.dependsOn`),
 * 10 cutover `step -> step`, 3 `OpenTofu -> OpenTofu`. The coupling BETWEEN
 * the engine types — a HelmRelease cannot install until the cluster is
 * provisioned, cutover cannot start until the installs finish — lives only
 * in the phase order (Provision -> Bootstrap -> Cutover -> Handover ->
 * Apps), carried by the group->group spine edges. It is never drawn at the
 * leaf level, so a HelmRelease renders as an island with no visible link to
 * OpenTofu, and cutover looks disconnected from the installs.
 *
 * `addPhaseBarrierDeps` closes that gap. For each consecutive phase
 * boundary `A -> B` (taken from the group->group spine), it connects the
 * **sink** leaves of A (leaves that nothing else in A depends on — i.e. A's
 * terminal work) to the **root** leaves of B (leaves that depend on nothing
 * inside B — i.e. B's entry work), by appending A's sinks to each B-root's
 * `dependsOn`. `flowLayoutOrganic` then renders those as ordinary
 * depends-on edges.
 *
 * The result is one connected DAG: every HelmRelease is reachable from the
 * provisioning terminal, cutover hangs off the Bootstrap installs, and
 * nothing floats. Transitive precedence stays implicit — we deliberately do
 * NOT draw the all-pairs closure (548 `OpenTofu -> HelmRelease` etc.), which
 * would be an unreadable hairball; a barrier at each boundary is the honest,
 * minimal representation and the transitive relation is recovered by walking
 * the graph.
 *
 * Pure and idempotent: returns a new Job[] with augmented `dependsOn`; the
 * input is untouched. Derived entirely from the existing `contains`
 * hierarchy + spine — no new backend data.
 */

import type { Job } from './jobs.types'

/** A group is a "top phase" when it has no parent group (spine lives here). */
function isTopPhase(j: Job): boolean {
  return j.type === 'group' && !j.parentId
}

/** All leaf (non-group) descendants under a group, following childIds. */
function leavesUnder(groupId: string, byId: Map<string, Job>): string[] {
  const out: string[] = []
  const stack = [...(byId.get(groupId)?.childIds ?? [])]
  const seen = new Set<string>()
  while (stack.length > 0) {
    const id = stack.pop()!
    if (seen.has(id)) continue
    seen.add(id)
    const j = byId.get(id)
    if (!j) continue
    if (j.type === 'group') stack.push(...(j.childIds ?? []))
    else out.push(id)
  }
  return out
}

/**
 * Roots and sinks of a phase, computed over INTRA-phase dependency edges
 * only:
 *   • root  — a leaf whose `dependsOn` contains nothing from this phase
 *             (its entry work; e.g. `Install Flux`).
 *   • sink  — a leaf that no other leaf in this phase depends on (its
 *             terminal work; e.g. `Bootstrap Cluster`).
 */
function rootsAndSinks(
  leafIds: readonly string[],
  byId: Map<string, Job>,
): { roots: string[]; sinks: string[] } {
  const inPhase = new Set(leafIds)
  const hasIntraDep = new Set<string>()
  const isIntraDependedOn = new Set<string>()
  for (const id of leafIds) {
    const j = byId.get(id)
    if (!j) continue
    for (const dep of j.dependsOn ?? []) {
      if (inPhase.has(dep)) {
        hasIntraDep.add(id)
        isIntraDependedOn.add(dep)
      }
    }
  }
  const roots = leafIds.filter((id) => !hasIntraDep.has(id))
  const sinks = leafIds.filter((id) => !isIntraDependedOn.has(id))
  return { roots, sinks }
}

/**
 * Return a new Job[] with phase-barrier edges added to `dependsOn`.
 *
 * For every consecutive spine boundary `pred -> phase` (i.e. `phase`'s
 * group `dependsOn` includes the top-phase group `pred`), each root leaf of
 * `phase` gains every sink leaf of `pred`. Existing `dependsOn` entries are
 * preserved; duplicates are de-duped. Groups are never mutated. Safe to run
 * on a graph with no groups / no spine (returns an equivalent array).
 */
export function addPhaseBarrierDeps(jobs: readonly Job[]): Job[] {
  const byId = new Map<string, Job>()
  for (const j of jobs) byId.set(j.id, j)

  const topPhases = jobs.filter(isTopPhase)
  if (topPhases.length === 0) return jobs.map((j) => ({ ...j }))

  // Precompute leaves + roots/sinks per top phase.
  const leavesByPhase = new Map<string, string[]>()
  const rsByPhase = new Map<string, { roots: string[]; sinks: string[] }>()
  for (const g of topPhases) {
    const leaves = leavesUnder(g.id, byId)
    leavesByPhase.set(g.id, leaves)
    rsByPhase.set(g.id, rootsAndSinks(leaves, byId))
  }
  const topPhaseIds = new Set(topPhases.map((g) => g.id))

  // extra[leafId] = set of sink ids to append as barrier deps.
  const extra = new Map<string, Set<string>>()
  for (const g of topPhases) {
    // predecessors of this phase = its group.dependsOn that are top phases.
    const preds = (g.dependsOn ?? []).filter((id) => topPhaseIds.has(id))
    if (preds.length === 0) continue
    const roots = rsByPhase.get(g.id)?.roots ?? []
    if (roots.length === 0) continue
    for (const predId of preds) {
      const predSinks = rsByPhase.get(predId)?.sinks ?? []
      if (predSinks.length === 0) continue
      for (const rootId of roots) {
        let set = extra.get(rootId)
        if (!set) {
          set = new Set<string>()
          extra.set(rootId, set)
        }
        for (const s of predSinks) set.add(s)
      }
    }
  }

  if (extra.size === 0) return jobs.map((j) => ({ ...j }))

  return jobs.map((j) => {
    const add = extra.get(j.id)
    if (!add || add.size === 0) return { ...j }
    const merged = new Set<string>(j.dependsOn ?? [])
    for (const s of add) merged.add(s)
    return { ...j, dependsOn: [...merged] }
  })
}
