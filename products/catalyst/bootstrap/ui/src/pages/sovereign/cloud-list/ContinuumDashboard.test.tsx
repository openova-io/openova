/**
 * ContinuumDashboard.test.tsx — ADR-0015 D8 central topology-transition view.
 *
 * Fixtures are Continuum CRs exactly as the D8 controller writes their status
 * (Ready condition + status.transition + status.activity[] in the D7 shape),
 * one copy per region as the k8s stream delivers them. The dashboard must:
 * fold the region copies, group members by status.transition.group, render
 * every phase verbatim per member, and link each member to its own resource
 * pages — with no action control anywhere.
 */
import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import { objectKey } from '@/widgets/architecture-graph/useK8sCacheStream'
import { groupContinuums } from './continuumDashboard.model'

const A = 'hw-me-east-215-a-rtz-prod'
const B = 'hw-me-east-215-b-rtz-prod'
const GROUP = 'acme--dr-db_dr-wordpress'

const cloudState: { k8sSnapshot: K8sSnapshot; k8sStatus: string; k8sRevision: number; deploymentId: string } = {
  k8sSnapshot: new Map(),
  k8sStatus: 'open',
  k8sRevision: 1,
  deploymentId: '',
}
vi.mock('../CloudPage', () => ({ useCloud: () => cloudState }))
afterEach(cleanup)

const { ContinuumDashboard } = await import('./ContinuumDashboard')

function act(phase: string, app: string, state: string, message: string) {
  return { id: `${phase}-acme-${app}`, phase, member: app, state, startedAt: '2026-11-08T10:00:00Z', ...(state === 'succeeded' ? { finishedAt: '2026-11-08T10:01:00Z' } : {}), message }
}

function continuum(name: string, app: string, cluster: string, ready: 'True' | 'False' | 'Unknown', reason: string, activity: unknown[], withRecord = true): [string, K8sObject] {
  const obj: K8sObject = {
    apiVersion: 'dr.openova.io/v1',
    kind: 'Continuum',
    metadata: { namespace: 'acme', name, uid: `${cluster}-${name}` },
    clusterId: cluster,
    spec: { applicationRef: `acme/${app}`, desiredPrimaryRegion: B },
    status: {
      phase: ready === 'True' ? 'Healthy' : 'Transitioning',
      primaryRegion: A,
      desiredPrimaryRegion: B,
      leaseHolder: B,
      conditions: [{ type: 'Ready', status: ready, reason, message: `${app}: ${reason}` }],
      transition: withRecord
        ? { id: `${A}-to-${B}-1`, group: GROUP, from: A, to: B, startedAt: '2026-11-08T10:00:00Z', outcome: 'Transitioning', message: 'data-primary db: waiting: CNPG Cluster wordpress-db-b not promoted yet' }
        : { group: GROUP, outcome: 'Transitioning' },
      activity,
    },
  }
  return [objectKey('continuum', obj, cluster), obj]
}

function fixture(): Array<[string, K8sObject]> {
  const wpActs = [act('traffic-off', 'wordpress', 'succeeded', 'traffic off in A'), act('data-primary', 'wordpress', 'pending', ''), act('peers', 'wordpress', 'pending', ''), act('traffic-on', 'wordpress', 'pending', '')]
  const dbActs = [act('traffic-off', 'db', 'succeeded', 'CNPG Cluster wordpress-db-a demoted; demotion token published'), act('data-primary', 'db', 'running', 'waiting: CNPG Cluster wordpress-db-b has not promoted with the demotion token yet'), act('peers', 'db', 'pending', ''), act('traffic-on', 'db', 'pending', '')]
  return [
    continuum('dr-wordpress', 'wordpress', A, 'False', 'Transitioning', wpActs),
    // Region B's copy of the same Continuum mirrors the same record.
    continuum('dr-wordpress', 'wordpress', B, 'False', 'Transitioning', wpActs),
    continuum('dr-db', 'db', B, 'False', 'Transitioning', dbActs),
    // A stale copy without a record must not win the fold.
    continuum('dr-db', 'db', A, 'Unknown', 'Pending', [], false),
  ]
}

function renderDashboard(entries: Array<[string, K8sObject]>) {
  cloudState.k8sSnapshot = new Map(entries)
  cloudState.k8sRevision += 1
  return render(<ContinuumDashboard />)
}

describe('ContinuumDashboard — ADR-0015 D8', () => {
  it('folds region copies and groups members by status.transition.group', () => {
    const groups = groupContinuums(fixture().map(([, o]) => o))
    expect(groups).toHaveLength(1)
    expect(groups[0].id).toBe(GROUP)
    expect(groups[0].members.map((m) => m.name)).toEqual(['dr-db', 'dr-wordpress'])
    const db = groups[0].members.find((m) => m.name === 'dr-db')!
    expect(db.activity, 'the copy carrying the transition record must win the fold').toHaveLength(4)
  })

  it('renders one group with per-member phases verbatim and links each member to its own resources', () => {
    renderDashboard(fixture())
    const group = screen.getByTestId(`continuum-group-${GROUP}`)
    expect(within(group).getByTestId(`continuum-group-${GROUP}-from`).textContent).toBe(A)
    expect(within(group).getByTestId(`continuum-group-${GROUP}-to`).textContent).toBe(B)
    expect(within(group).getByTestId(`continuum-group-${GROUP}-lease`).textContent).toBe(B)

    const dbData = screen.getByTestId('continuum-member-acme-dr-db-data-primary')
    expect(within(dbData).getByTestId('continuum-member-acme-dr-db-data-primary-state').textContent).toBe('running')
    expect(dbData.textContent).toContain('has not promoted with the demotion token yet')
    expect(screen.getByTestId('continuum-member-acme-dr-db-traffic-off-state').textContent).toBe('succeeded')

    // The member's own Ready condition, verbatim through the D7 badge.
    const badge = screen.getByTestId('continuum-member-acme-dr-wordpress-status')
    expect(badge.textContent).toBe('NotReady')
    expect(screen.getByTestId('continuum-member-acme-dr-wordpress-reason').textContent).toContain('Transitioning')

    expect(screen.getByTestId('continuum-member-acme-dr-wordpress-app-link').getAttribute('href')).toContain('/resource/application/acme/wordpress/')
    expect(screen.getByTestId('continuum-member-acme-dr-wordpress-continuum-link').getAttribute('href')).toContain('/resource/continuum/acme/dr-wordpress/')
  })

  it('offers no action: a transition is a placement edit, not a button', () => {
    renderDashboard(fixture())
    const root = screen.getByTestId('continuum-dashboard')
    expect(root.querySelectorAll('button')).toHaveLength(0)
    expect(root.textContent?.toLowerCase()).not.toMatch(/switchover|failover|failback|disaster/)
  })

  it('says so honestly when nothing is protected', () => {
    renderDashboard([])
    expect(screen.getByTestId('continuum-dashboard-empty').textContent).toContain('No Continuum objects')
  })
})
