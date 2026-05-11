/**
 * flow-bridge — translates legacy `Job[]` → new FlowNode + Relationship[].
 */

import { describe, it, expect } from 'vitest'
import { adaptJobsToFlow, buildFlowFromJobs } from './flow-bridge'
import type { Job } from './jobs.types'

function leaf(id: string, parentId = '', dependsOn: string[] = []): Job {
  return {
    id,
    jobName: id,
    type: 'install',
    appId: 'bp-' + id,
    parentId,
    dependsOn,
    childIds: [],
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  }
}

function group(id: string, childIds: string[]): Job {
  return {
    id,
    jobName: id,
    displayName: id,
    type: 'group',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds,
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  }
}

describe('flow-bridge — adaptJobsToFlow', () => {
  it('emits one FlowNode per Job with flowId propagated', () => {
    const jobs: Job[] = [leaf('a'), leaf('b'), group('grp', ['a', 'b'])]
    const { nodes } = adaptJobsToFlow(jobs, 'dep-1')
    expect(nodes.length).toBe(3)
    for (const n of nodes) {
      expect(n.flowId).toBe('dep-1')
    }
  })

  it('emits a `contains` Relationship for each non-empty parentId', () => {
    const jobs: Job[] = [
      group('grp', ['a', 'b']),
      leaf('a', 'grp'),
      leaf('b', 'grp'),
    ]
    const { relationships } = adaptJobsToFlow(jobs, 'dep-1')
    const containsRels = relationships.filter((r) => r.type === 'contains')
    expect(containsRels.length).toBe(2)
    expect(containsRels.find((r) => r.fromId === 'a' && r.toId === 'grp')).toBeTruthy()
    expect(containsRels.find((r) => r.fromId === 'b' && r.toId === 'grp')).toBeTruthy()
  })

  it('emits a `finish-to-start` Relationship per dependsOn entry', () => {
    const jobs: Job[] = [leaf('a'), leaf('b', '', ['a'])]
    const { relationships } = adaptJobsToFlow(jobs, 'dep-1')
    const blocking = relationships.filter((r) => r.type === 'finish-to-start')
    expect(blocking.length).toBe(1)
    expect(blocking[0].fromId).toBe('a')
    expect(blocking[0].toId).toBe('b')
    expect(blocking[0].condition).toBe('on-success')
  })

  it('drops dependsOn entries pointing at unknown ids (safer than dangling)', () => {
    const jobs: Job[] = [leaf('b', '', ['nonexistent', 'self-ref'])]
    const { relationships } = adaptJobsToFlow(jobs, 'dep-1')
    expect(relationships.length).toBe(0)
  })

  it('preserves appId / jobName / durationMs in meta', () => {
    const j: Job = {
      ...leaf('x'),
      appId: 'bp-cilium',
      jobName: 'install-cilium',
      durationMs: 1500,
    }
    const { nodes } = adaptJobsToFlow([j], 'dep-1')
    expect(nodes[0].meta?.appId).toBe('bp-cilium')
    expect(nodes[0].meta?.jobName).toBe('install-cilium')
    expect(nodes[0].meta?.durationMs).toBe(1500)
  })

  it('label falls back to jobName stripped of leading `install-`', () => {
    const j: Job = { ...leaf('x'), jobName: 'install-cilium' }
    const { nodes } = adaptJobsToFlow([j], 'dep-1')
    expect(nodes[0].label).toBe('cilium')
  })

  it('label uses displayName when present', () => {
    const j: Job = { ...leaf('x'), displayName: 'Apply Cilium' }
    const { nodes } = adaptJobsToFlow([j], 'dep-1')
    expect(nodes[0].label).toBe('Apply Cilium')
  })

  it('parses ISO timestamps to unix ms', () => {
    const j: Job = {
      ...leaf('x'),
      startedAt: '2026-05-01T00:00:00.000Z',
      finishedAt: '2026-05-01T00:00:01.000Z',
    }
    const { nodes } = adaptJobsToFlow([j], 'dep-1')
    expect(nodes[0].startedAt).toBe(Date.parse('2026-05-01T00:00:00.000Z'))
    expect(nodes[0].endedAt).toBe(Date.parse('2026-05-01T00:00:01.000Z'))
  })
})

describe('flow-bridge — buildFlowFromJobs', () => {
  it('rolls up flow.status from leaf statuses (failed wins if all terminal)', () => {
    const jobs: Job[] = [
      { ...leaf('a'), status: 'succeeded' },
      { ...leaf('b'), status: 'failed' },
    ]
    const out = buildFlowFromJobs({ jobs, flowId: 'dep-1' })
    expect(out.flow.status).toBe('failed')
  })

  it('rolls up flow.status to running while any job is still pending', () => {
    const jobs: Job[] = [
      { ...leaf('a'), status: 'succeeded' },
      { ...leaf('b'), status: 'pending' },
    ]
    const out = buildFlowFromJobs({ jobs, flowId: 'dep-1' })
    expect(out.flow.status).toBe('running')
  })

  it('preserves an explicit flowStatus override', () => {
    const jobs: Job[] = [{ ...leaf('a'), status: 'succeeded' }]
    const out = buildFlowFromJobs({ jobs, flowId: 'dep-1', flowStatus: 'archived' })
    expect(out.flow.status).toBe('archived')
  })

  it('region/multi-region sanity: bridge forwards `meta` opaquely so prov-#34 can paint regions even pre-server', () => {
    // The bridge does NOT compute region — the host still owns that
    // via perNodeHints. But the bridge preserves `meta.region` if a
    // future Job shape carries it, so the new layout has somewhere to
    // pick it up without the catalyst-api needing to bump first.
    const jobs: Job[] = [leaf('a')]
    const out = buildFlowFromJobs({ jobs, flowId: 'dep-1' })
    expect(out.nodes[0].meta).toBeDefined()
    // The legacy Job shape doesn't have `region`; the bridge doesn't
    // INVENT one. Region remains a host concern routed via
    // perNodeHints — which is exactly the Agent #1 contract.
    expect(out.nodes[0].region).toBeUndefined()
  })
})
