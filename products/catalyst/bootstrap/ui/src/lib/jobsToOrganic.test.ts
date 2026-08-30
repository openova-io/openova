import { describe, it, expect } from 'vitest'
import { jobsToOrganicInputs } from './jobsToOrganic'
import type { Job } from './jobs.types'

function job(partial: Partial<Job> & { id: string }): Job {
  return {
    jobName: partial.id,
    displayName: partial.id,
    type: 'install',
    appId: '',
    parentId: '',
    dependsOn: [],
    childIds: [],
    status: 'succeeded',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...partial,
  } as Job
}

describe('jobsToOrganicInputs', () => {
  it('derives one family per kind, labelled with the real engine name', () => {
    const jobs = [
      job({ id: 'a', kind: 'install' }),
      job({ id: 'b', kind: 'install' }),
      job({ id: 'c', kind: 'cron' }),
      job({ id: 'd', kind: 'step' }),
    ]
    const { families } = jobsToOrganicInputs(jobs)
    const byId = new Map(families.map((f) => [f.id, f]))
    // one per DISTINCT kind
    expect(families.map((f) => f.id).sort()).toEqual(['cron', 'install', 'step'])
    // engine-name labels (JOB_ENGINE_LABELS), not raw ids
    expect(byId.get('install')?.label).toBe('HelmRelease')
    expect(byId.get('cron')?.label).toBe('CronJob')
    // every family has a colour
    expect(families.every((f) => /^#[0-9A-Fa-f]{6}$/.test(f.color))).toBe(true)
  })

  it('maps each job to a hint {regionId=job.region, familyId=job.kind}', () => {
    const jobs = [
      job({ id: 'a', kind: 'install', region: 'me-east-215-a' }),
      job({ id: 'b', kind: 'cron' }), // no region → primary
    ]
    const { hints } = jobsToOrganicInputs(jobs)
    expect(hints.get('a')).toEqual({ regionId: 'me-east-215-a', familyId: 'install' })
    expect(hints.get('b')).toEqual({ regionId: 'primary', familyId: 'cron' })
  })

  it('collects distinct regions; falls back to a single primary region', () => {
    const twoRegion = jobsToOrganicInputs([
      job({ id: 'a', kind: 'install', region: 'me-east-215-a' }),
      job({ id: 'b', kind: 'install', region: 'me-east-215-b' }),
    ])
    expect(twoRegion.regions.map((r) => r.id)).toEqual([
      'me-east-215-a',
      'me-east-215-b',
    ])
    const noRegion = jobsToOrganicInputs([job({ id: 'a', kind: 'install' })])
    expect(noRegion.regions).toEqual([{ id: 'primary', label: 'Primary Region' }])
  })

  it('prettifies region labels when provided', () => {
    const { regions } = jobsToOrganicInputs(
      [job({ id: 'a', kind: 'install', region: 'r1' })],
      [{ id: 'r1', label: 'R1 · Muscat', meta: 'primary' }],
    )
    expect(regions[0]).toEqual({ id: 'r1', label: 'R1 · Muscat', meta: 'primary' })
  })

  it('collects group ids from type:group rows', () => {
    const { groupIds } = jobsToOrganicInputs([
      job({ id: 'g', type: 'group', kind: 'group', childIds: ['a'] }),
      job({ id: 'a', kind: 'install', parentId: 'g' }),
    ])
    expect([...groupIds]).toEqual(['g'])
  })
})
