/**
 * JobsTable.status-d7.test.tsx — ADR-0015 D7 (Refs #7067): every reconciling
 * object is visible in Jobs with its OWN reason + message, verbatim.
 *
 * sovereign/test — an Application whose Environment does not exist — must
 * read as ONE Jobs row `blocked: EnvironmentMissing — Environment
 * "sovereign-prod" not found`, and a topology-transition group must render
 * its phase × member rows like any group.
 */
import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { JobsTable } from './JobsTable'
import { jobKind, jobStatusNote, JOB_ENGINE_LABELS, type Job } from '@/lib/jobs.types'
import { JOB_KIND_IDS } from './jobs-list/jobKinds'
import { deriveJobKindCounts } from './jobs-list/jobKindCounts'

afterEach(() => cleanup())

function renderTable(jobs: Job[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsTable jobs={jobs} />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute, detailRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs'] }),
  })
  return render(<RouterProvider router={router} />)
}

function job(p: Partial<Job> & Pick<Job, 'id' | 'jobName'>): Job {
  return {
    appId: '',
    type: 'install',
    parentId: '',
    childIds: [],
    dependsOn: [],
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
    ...p,
  }
}

const SOVEREIGN_TEST = job({
  id: 'd-1:applications-step-sovereign-test',
  jobName: 'applications-step-sovereign-test',
  displayName: 'Application sovereign/test',
  kind: 'application',
  parentId: 'd-1:applications',
  status: 'failed',
  reason: 'EnvironmentMissing',
  message: 'Environment "sovereign-prod" not found',
})

describe('ADR-0015 D7 — Jobs renders the object reason verbatim', () => {
  it('sovereign/test: one row reading blocked: EnvironmentMissing — <message>', async () => {
    renderTable([
      job({ id: 'd-1:applications', jobName: 'applications', displayName: 'Applications', type: 'group', kind: 'group', childIds: [SOVEREIGN_TEST.id], status: 'failed' }),
      SOVEREIGN_TEST,
    ])
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(1)
    expect(screen.getByTestId(`jobs-cell-status-note-${SOVEREIGN_TEST.id}`).textContent).toBe(
      'blocked: EnvironmentMissing — Environment "sovereign-prod" not found',
    )
    expect(screen.getByTestId(`jobs-cell-kind-${SOVEREIGN_TEST.id}`).textContent).toBe('Application')
  })

  it('jobStatusNote: failed+reason → blocked, otherwise the message verbatim, absent → empty', () => {
    expect(jobStatusNote(SOVEREIGN_TEST)).toBe('blocked: EnvironmentMissing — Environment "sovereign-prod" not found')
    expect(jobStatusNote({ status: 'running', reason: 'Progressing', message: 'waiting for HelmRelease' })).toBe(
      'waiting for HelmRelease',
    )
    expect(jobStatusNote({ status: 'failed' })).toBe('')
    expect(jobStatusNote({ status: 'failed', reason: 'X' })).toBe('blocked: X')
  })

  it('topology-transition: group renders its phase × member rows with the kind label', async () => {
    const group = job({
      id: 'd-1:topology-transition-acme-wordpress',
      jobName: 'topology-transition-acme-wordpress',
      displayName: 'Topology transition acme/wordpress',
      type: 'group',
      kind: 'group',
      status: 'running',
      childIds: ['d-1:t-1', 'd-1:t-2'],
    })
    const s1 = job({
      id: 'd-1:t-1',
      jobName: 'topology-transition-acme-wordpress-step-traffic-off-a',
      displayName: 'traffic-off · me-east-215-a',
      kind: 'topology-transition',
      parentId: group.id,
      status: 'succeeded',
    })
    const s2 = job({
      id: 'd-1:t-2',
      jobName: 'topology-transition-acme-wordpress-step-primary-move-db',
      displayName: 'primary-move · wordpress-db',
      kind: 'topology-transition',
      parentId: group.id,
      status: 'running',
      message: 'promoting wordpress-db-replica',
    })
    renderTable([group, s1, s2])
    await screen.findByTestId('jobs-table')
    expect(screen.getByTestId(`jobs-row-link-${s1.id}`).textContent).toBe('traffic-off · me-east-215-a')
    expect(screen.getByTestId(`jobs-row-link-${s2.id}`).textContent).toBe('primary-move · wordpress-db')
    expect(screen.getByTestId(`jobs-cell-kind-${s2.id}`).textContent).toBe('Topology transition')
    expect(screen.getByTestId(`jobs-cell-status-note-${s2.id}`).textContent).toBe('promoting wordpress-db-replica')
  })

  it('the new kinds are chip-filterable and counted', () => {
    expect(JOB_KIND_IDS).toContain('application')
    expect(JOB_KIND_IDS).toContain('topology-transition')
    expect(JOB_ENGINE_LABELS['topology-transition']).toBe('Topology transition')
    expect(jobKind(SOVEREIGN_TEST)).toBe('application')
    const counts = deriveJobKindCounts([SOVEREIGN_TEST])
    expect(counts.application).toBe(1)
    expect(counts['topology-transition']).toBe(0)
  })
})
