import { describe, it, expect } from 'vitest'
import { addPhaseBarrierDeps } from './phaseBarriers'
import type { Job } from './jobs.types'

/** Minimal Job factory — only the fields phaseBarriers reads matter. */
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
 * Fixture mirroring the live shape: a Provision phase (tofu chain +
 * terminal) that gates a Bootstrap phase (root install + a dependent
 * install).
 *
 *   Provision(group, spine root)
 *     tofu-init  ->  tofu-out          (tofu-out dependsOn tofu-init)
 *   Bootstrap(group, dependsOn Provision)
 *     helm-flux                        (root — no intra dep)
 *     helm-valkey  dependsOn helm-flux (not a root)
 */
function twoPhaseGraph(): Job[] {
  return [
    job({ id: 'Provision', type: 'group', childIds: ['tofu-init', 'tofu-out'] }),
    job({ id: 'tofu-init', type: 'install', parentId: 'Provision' }),
    job({ id: 'tofu-out', type: 'install', parentId: 'Provision', dependsOn: ['tofu-init'] }),
    job({
      id: 'Bootstrap',
      type: 'group',
      childIds: ['helm-flux', 'helm-valkey'],
      dependsOn: ['Provision'], // the spine edge
    }),
    job({ id: 'helm-flux', type: 'install', parentId: 'Bootstrap' }),
    job({ id: 'helm-valkey', type: 'install', parentId: 'Bootstrap', dependsOn: ['helm-flux'] }),
  ]
}

const depsOf = (jobs: Job[], id: string) =>
  jobs.find((j) => j.id === id)!.dependsOn

describe('addPhaseBarrierDeps', () => {
  it('links the downstream phase ROOT to the upstream phase SINK', () => {
    const out = addPhaseBarrierDeps(twoPhaseGraph())
    // helm-flux is Bootstrap's root; tofu-out is Provision's sink.
    expect(depsOf(out, 'helm-flux')).toContain('tofu-out')
    // it does NOT pick up the non-sink upstream node.
    expect(depsOf(out, 'helm-flux')).not.toContain('tofu-init')
  })

  it('does NOT add a barrier to a non-root downstream leaf', () => {
    const out = addPhaseBarrierDeps(twoPhaseGraph())
    // helm-valkey depends on helm-flux (intra-phase) so it is not a root.
    expect(depsOf(out, 'helm-valkey')).toEqual(['helm-flux'])
  })

  it('preserves existing dependsOn and de-dupes', () => {
    const g = twoPhaseGraph()
    // give the root a pre-existing (redundant) barrier dep
    g.find((j) => j.id === 'helm-flux')!.dependsOn = ['tofu-out']
    const out = addPhaseBarrierDeps(g)
    const d = depsOf(out, 'helm-flux')
    expect(d.filter((x) => x === 'tofu-out')).toHaveLength(1)
  })

  it('never mutates group jobs and is pure (input untouched)', () => {
    const g = twoPhaseGraph()
    const snapshot = JSON.stringify(g)
    const out = addPhaseBarrierDeps(g)
    expect(JSON.stringify(g)).toBe(snapshot) // input unchanged
    const bootstrap = out.find((j) => j.id === 'Bootstrap')!
    expect(bootstrap.dependsOn).toEqual(['Provision']) // group deps untouched
  })

  it('is a no-op equivalent when there are no groups/spine', () => {
    const flat = [
      job({ id: 'a', type: 'install' }),
      job({ id: 'b', type: 'install', dependsOn: ['a'] }),
    ]
    const out = addPhaseBarrierDeps(flat)
    expect(depsOf(out, 'a')).toEqual([])
    expect(depsOf(out, 'b')).toEqual(['a'])
  })

  it('handles a three-phase spine (consecutive barriers only)', () => {
    const g = [
      ...twoPhaseGraph(),
      job({ id: 'Cutover', type: 'group', childIds: ['gitea-mirror'], dependsOn: ['Bootstrap'] }),
      job({ id: 'gitea-mirror', type: 'install', parentId: 'Cutover' }),
    ]
    const out = addPhaseBarrierDeps(g)
    // Cutover's root gains Bootstrap's sinks (helm-valkey is the sink).
    expect(depsOf(out, 'gitea-mirror')).toContain('helm-valkey')
    // and NOT a transitive Provision node (barriers are consecutive only).
    expect(depsOf(out, 'gitea-mirror')).not.toContain('tofu-out')
  })
})
