import { describe, it, expect } from 'vitest'
import { selectGraphJobs, OPEN_ENDED_GRAPH_KINDS } from './graphJobFilter'
import type { Job } from './jobs.types'
import type { GraphKind } from './flowJobKind'

function job(p: Partial<Job> & Pick<Job, 'id' | 'type'>): Job {
  return {
    id: p.id,
    jobName: p.jobName ?? p.id,
    displayName: p.displayName ?? p.id,
    type: p.type,
    appId: '',
    parentId: p.parentId ?? '',
    dependsOn: p.dependsOn ?? [],
    childIds: p.childIds ?? [],
    status: p.status ?? 'succeeded',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  }
}

/**
 * Fixture mirroring the live shape: a Provision group (tofu) + a Bootstrap
 * group holding a HelmRelease AND an open-ended reconcile leaf, plus a
 * Reconcilers group that holds ONLY reconcile leaves (so it must drop out).
 * ids use the prefixes flowJobKind classifies on.
 */
function graph(): Job[] {
  return [
    job({ id: 'provision', type: 'group', childIds: ['tofu-init'] }),
    job({ id: 'tofu-init', type: 'install', parentId: 'provision' }), // flowJobKind → lifecycle
    job({ id: 'bootstrap', type: 'group', childIds: ['install-flux', 'reconcile-vcluster-a'] }),
    job({ id: 'install-flux', type: 'install', parentId: 'bootstrap' }), // → install
    job({ id: 'reconcile-vcluster-a', type: 'install', parentId: 'bootstrap' }), // → reconcile (open-ended)
    job({ id: 'reconcilers', type: 'group', childIds: ['reconcile-vcluster-b'] }),
    job({ id: 'reconcile-vcluster-b', type: 'install', parentId: 'reconcilers' }), // → reconcile
    job({ id: 'cutover', type: 'group', childIds: ['cutover-step-gitea'] }),
    job({ id: 'cutover-step-gitea', type: 'install', parentId: 'cutover' }), // → step
  ]
}

const ids = (jobs: Job[]) => jobs.map((j) => j.id).sort()

// sanity: the prefixes classify as expected
// (tofu→lifecycle, install→install, reconcile→reconcile, cutover→step)

describe('selectGraphJobs', () => {
  it('sanity: reconcile/reconciler are the open-ended set', () => {
    expect([...OPEN_ENDED_GRAPH_KINDS].sort()).toEqual(['reconcile', 'reconciler'])
  })

  it('excludes open-ended reconcile leaves even with NO chip filter', () => {
    const out = selectGraphJobs(graph(), undefined)
    expect(out.map((j) => j.id)).not.toContain('reconcile-vcluster-a')
    expect(out.map((j) => j.id)).not.toContain('reconcile-vcluster-b')
    // finite leaves survive
    expect(out.map((j) => j.id)).toContain('install-flux')
    expect(out.map((j) => j.id)).toContain('tofu-init')
    expect(out.map((j) => j.id)).toContain('cutover-step-gitea')
  })

  it('drops a group that held ONLY reconcilers, keeps groups with a finite leaf', () => {
    const out = selectGraphJobs(graph(), undefined)
    const gids = out.filter((j) => j.type === 'group').map((j) => j.id).sort()
    // `reconcilers` had only reconcile-vcluster-b → gone; the rest survive
    expect(gids).toEqual(['bootstrap', 'cutover', 'provision'])
  })

  it('a chip filter narrows to that kind only (curate → OpenTofu)', () => {
    const only = new Set<GraphKind>(['lifecycle'])
    const out = selectGraphJobs(graph(), only)
    // only the tofu leaf + its group survive
    expect(ids(out)).toEqual(['provision', 'tofu-init'])
  })

  it('never re-admits a reconciler even if its kind is in visibleKinds', () => {
    // defensive: reconcile in the visible set must STILL be excluded
    const withRec = new Set<GraphKind>(['install', 'reconcile'])
    const out = selectGraphJobs(graph(), withRec)
    expect(out.map((j) => j.id)).not.toContain('reconcile-vcluster-a')
    expect(out.map((j) => j.id)).toContain('install-flux')
  })

  it('remove ALL chips (empty set) → empty canvas', () => {
    const out = selectGraphJobs(graph(), new Set<GraphKind>())
    expect(out).toEqual([])
  })

  it('is pure — input array untouched', () => {
    const g = graph()
    const snap = JSON.stringify(g)
    selectGraphJobs(g, new Set<GraphKind>(['install']))
    expect(JSON.stringify(g)).toBe(snap)
  })
})
