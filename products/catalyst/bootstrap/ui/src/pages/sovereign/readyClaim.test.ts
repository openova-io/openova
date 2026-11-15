/**
 * readyClaim.test.ts — #7902 defects 6 and 8: a page may say "ready" only
 * when the record's regions and the rows it renders agree.
 */

import { describe, it, expect } from 'vitest'
import { readyClaim } from './readyClaim'
import type { DeploymentSnapshot } from './useDeploymentEvents'
import type { Job } from '@/lib/jobs.types'

function J(partial: Partial<Job> & Pick<Job, 'id' | 'jobName'>): Job {
  return {
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
  }
}

const READY: DeploymentSnapshot = { id: 'd', status: 'ready', handoverFiredAt: '2026-11-15T06:15:24Z' }

describe('readyClaim', () => {
  it('holds when nothing contradicts the hand-over', () => {
    expect(readyClaim(READY)).toEqual({ holds: true, reasons: [] })
    expect(readyClaim(null)).toEqual({ holds: true, reasons: [] })
    expect(
      readyClaim({
        ...READY,
        regions: [
          { region: 'me-east-215-a', primary: true, hrReady: 67, hrTotal: 67, degraded: false },
          { region: 'me-east-215-b-1', primary: false, hrReady: 63, hrTotal: 63, degraded: false },
        ],
      }).holds,
    ).toBe(true)
  })

  it('does not hold while a region is short of its releases — the record of 15 November', () => {
    const claim = readyClaim({
      ...READY,
      secondaryDegraded: true,
      regions: [
        { region: 'me-east-215-a', primary: true, hrReady: 67, hrTotal: 67, degraded: false },
        { region: 'me-east-215-b-1', primary: false, hrReady: 55, hrTotal: 63, degraded: true },
      ],
    })
    expect(claim.holds).toBe(false)
    expect(claim.reasons).toEqual(['Region me-east-215-b-1: 55 of 63 releases installed'])
  })

  it('does not hold on the degraded flag alone', () => {
    const claim = readyClaim({ ...READY, secondaryDegraded: true })
    expect(claim.holds).toBe(false)
    expect(claim.reasons).toHaveLength(1)
  })

  it('does not hold while a release install row is running, pending or failed', () => {
    const claim = readyClaim(READY, [
      J({ id: 'd:install-cilium', jobName: 'install-cilium', kind: 'install' }),
      J({ id: 'd:install-b:keycloak', jobName: 'install-b:keycloak', kind: 'install', status: 'running' }),
      J({ id: 'd:install-b:gitea', jobName: 'install-b:gitea', kind: 'install', status: 'pending' }),
      J({ id: 'd:install-b:harbor', jobName: 'install-b:harbor', kind: 'install', status: 'failed' }),
    ])
    expect(claim.holds).toBe(false)
    expect(claim.reasons).toEqual(['Release installs: 2 still installing, 1 failed'])
  })

  it('other kinds of rows and parked releases do not contradict ready', () => {
    const claim = readyClaim(READY, [
      J({ id: 'd:install-cilium', jobName: 'install-cilium', kind: 'install' }),
      // A suspended release is parked on purpose.
      J({ id: 'd:install-velero', jobName: 'install-velero', kind: 'install', status: 'dormant' as never }),
      // The cutover runs after ready; a cron run and a reconciler are day-2.
      J({ id: 'd:cutover-step-gitea-mirror', jobName: 'cutover-step-gitea-mirror', kind: 'step', status: 'running' }),
      J({ id: 'd:cron-scan', jobName: 'cron-scan', kind: 'cron', status: 'running' }),
      J({ id: 'd:group', jobName: 'bootstrap-kit', type: 'group', kind: 'group', status: 'running' }),
    ])
    expect(claim).toEqual({ holds: true, reasons: [] })
  })
})
