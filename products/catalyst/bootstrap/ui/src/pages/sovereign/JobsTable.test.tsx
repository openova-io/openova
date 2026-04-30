/**
 * JobsTable.test.tsx — coverage for the pure helpers + the table
 * surface (issue #204 founder spec).
 *
 * Pure helpers:
 *   • compareJobs — status-priority sort with startedAt-DESC tiebreak
 *     and pending-jumps-to-top semantics (item #10).
 *   • matchJob — search predicate spans jobName / appId / dependsOn /
 *     status / batchId (item #8a).
 *   • formatDuration — "12s" / "1m 24s" / "2h 5m" rendering.
 *
 * Component:
 *   • Renders the canonical column set.
 *   • Search input filters the visible row count.
 *   • Filter dropdowns narrow the visible row count.
 *   • appIdFilter prop short-circuits to a single appId (used by
 *     AppDetail's Jobs tab — item #8b).
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import {
  JobsTable,
  STATUS_PRIORITY,
  compareJobs,
  formatDuration,
  matchJob,
} from './JobsTable'
import { FIXTURE_JOBS } from '@/test/fixtures/jobs.fixture'
import type { Job } from '@/lib/jobs.types'

afterEach(() => cleanup())

function renderTable(props: Parameters<typeof JobsTable>[0]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsTable {...props} />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const tree = rootRoute.addChildren([homeRoute, detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/jobs'] }),
  })
  return render(<RouterProvider router={router} />)
}

describe('JobsTable — STATUS_PRIORITY', () => {
  it('orders running > pending > succeeded > failed', () => {
    expect(STATUS_PRIORITY.running).toBeLessThan(STATUS_PRIORITY.pending)
    expect(STATUS_PRIORITY.pending).toBeLessThan(STATUS_PRIORITY.succeeded)
    expect(STATUS_PRIORITY.succeeded).toBeLessThan(STATUS_PRIORITY.failed)
  })
})

describe('JobsTable — compareJobs', () => {
  function makeJob(partial: Partial<Job>): Job {
    return {
      id: partial.id ?? 'j',
      jobName: partial.jobName ?? 'Job',
      appId: partial.appId ?? 'bp-x',
      batchId: partial.batchId ?? 'b',
      dependsOn: partial.dependsOn ?? [],
      status: partial.status ?? 'pending',
      startedAt: partial.startedAt ?? null,
      finishedAt: partial.finishedAt ?? null,
      durationMs: partial.durationMs ?? 0,
    }
  }

  it('running sorts before pending', () => {
    const r = makeJob({ id: 'r', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    const p = makeJob({ id: 'p', status: 'pending' })
    expect(compareJobs(r, p)).toBeLessThan(0)
    expect(compareJobs(p, r)).toBeGreaterThan(0)
  })

  it('pending sorts before succeeded', () => {
    const p = makeJob({ id: 'p', status: 'pending' })
    const s = makeJob({ id: 's', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(p, s)).toBeLessThan(0)
  })

  it('succeeded sorts before failed', () => {
    const s = makeJob({ id: 's', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z' })
    const f = makeJob({ id: 'f', status: 'failed', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(s, f)).toBeLessThan(0)
  })

  it('within same status: startedAt DESC (newer first)', () => {
    const newer = makeJob({ id: 'newer', status: 'running', startedAt: '2026-04-29T10:05:00Z' })
    const older = makeJob({ id: 'older', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(newer, older)).toBeLessThan(0)
    expect(compareJobs(older, newer)).toBeGreaterThan(0)
  })

  it('null startedAt sorts after a real startedAt within same status', () => {
    const real = makeJob({ id: 'real', status: 'pending', startedAt: '2026-04-29T10:00:00Z' })
    const empty = makeJob({ id: 'empty', status: 'pending', startedAt: null })
    expect(compareJobs(real, empty)).toBeLessThan(0)
  })

  it('pending jumps to top when its status transitions to running', () => {
    // Founder spec item #10: "pending jobs jump to top when they
    // transition to running". This is the consequence of the
    // status-priority ordering — a pending job that starts running
    // immediately outranks every other non-running job in the table.
    const ranOnce = makeJob({ id: 'ran', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z' })
    const wasPending = makeJob({ id: 'pend', status: 'pending', startedAt: null })
    // Simulate the transition.
    const startedRunning = { ...wasPending, status: 'running' as const, startedAt: '2026-04-29T10:10:00Z' }
    // Pre-transition: pending sits BELOW the succeeded-with-realtime job? No —
    // pending (1) outranks succeeded (2), so pending is already higher.
    expect(compareJobs(wasPending, ranOnce)).toBeLessThan(0)
    // Post-transition: running (0) is even higher than pending (1).
    expect(compareJobs(startedRunning, wasPending)).toBeLessThan(0)
    // And running (0) is above succeeded (2) regardless of startedAt.
    expect(compareJobs(startedRunning, ranOnce)).toBeLessThan(0)
  })

  it('id ASC tiebreak when status + startedAt are equal', () => {
    const a = makeJob({ id: 'a', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    const b = makeJob({ id: 'b', status: 'running', startedAt: '2026-04-29T10:00:00Z' })
    expect(compareJobs(a, b)).toBeLessThan(0)
    expect(compareJobs(b, a)).toBeGreaterThan(0)
  })
})

describe('JobsTable — matchJob (search filter)', () => {
  const job: Job = {
    id: 'job-1',
    jobName: 'Install Cilium',
    appId: 'bp-cilium',
    batchId: 'batch-2',
    dependsOn: ['job-flux-bootstrap'],
    status: 'succeeded',
    startedAt: '2026-04-29T10:00:00Z',
    finishedAt: '2026-04-29T10:00:45Z',
    durationMs: 45_000,
  }

  it('returns true for empty / whitespace queries', () => {
    expect(matchJob(job, '')).toBe(true)
    expect(matchJob(job, '   ')).toBe(true)
  })

  it('matches case-insensitively across jobName', () => {
    expect(matchJob(job, 'cilium')).toBe(true)
    expect(matchJob(job, 'CILIUM')).toBe(true)
    expect(matchJob(job, 'Install')).toBe(true)
  })

  it('matches across appId', () => {
    expect(matchJob(job, 'bp-cilium')).toBe(true)
  })

  it('matches across batchId', () => {
    expect(matchJob(job, 'batch-2')).toBe(true)
  })

  it('matches across status', () => {
    expect(matchJob(job, 'succeeded')).toBe(true)
  })

  it('matches across dependsOn entries', () => {
    expect(matchJob(job, 'job-flux-bootstrap')).toBe(true)
    expect(matchJob(job, 'flux')).toBe(true)
  })

  it('returns false when no field matches', () => {
    expect(matchJob(job, 'nothing-matches-this')).toBe(false)
  })
})

describe('JobsTable — formatDuration', () => {
  it('renders short durations as Ns', () => {
    expect(formatDuration(12_000)).toBe('12s')
  })

  it('renders mid durations as Mm Ss', () => {
    expect(formatDuration(84_000)).toBe('1m 24s')
  })

  it('renders long durations as Hh Mm', () => {
    expect(formatDuration(7_500_000)).toBe('2h 5m')
  })

  it('renders 0 / negative as em-dash', () => {
    expect(formatDuration(0)).toBe('—')
    expect(formatDuration(-100)).toBe('—')
    expect(formatDuration(NaN)).toBe('—')
  })
})

describe('JobsTable — render', () => {
  it('renders all 8 fixture rows by default', async () => {
    renderTable({ jobs: FIXTURE_JOBS, deploymentId: 'd-1' })
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(FIXTURE_JOBS.length)
  })

  it('search input filters the visible row count', async () => {
    renderTable({ jobs: FIXTURE_JOBS, deploymentId: 'd-1' })
    await screen.findByTestId('jobs-table')
    const search = screen.getByTestId('jobs-search') as HTMLInputElement
    // Search for a query that exists in exactly one fixture job's
    // jobName/appId/dependsOn — "cert-manager" only appears on the
    // `job-install-cert-manager` row (jobName + appId).
    fireEvent.change(search, { target: { value: 'cert-manager' } })
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(1)
    expect(rows[0]!.getAttribute('data-testid')).toContain('cert-manager')
  })

  it('status filter narrows to a single status', async () => {
    renderTable({ jobs: FIXTURE_JOBS, deploymentId: 'd-1' })
    await screen.findByTestId('jobs-table')
    const statusFilter = screen.getByTestId('jobs-filter-status') as HTMLSelectElement
    fireEvent.change(statusFilter, { target: { value: 'failed' } })
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(1)
    expect(rows[0]!.getAttribute('data-status')).toBe('failed')
  })

  it('appIdFilter prop short-circuits to one appId (AppDetail Jobs tab — item #8b)', async () => {
    renderTable({ jobs: FIXTURE_JOBS, deploymentId: 'd-1', appIdFilter: 'bp-cilium' })
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    // Only `job-install-cilium` carries appId='bp-cilium' in the fixture.
    expect(rows.length).toBe(1)
    expect(rows[0]!.getAttribute('data-testid')).toBe('jobs-table-row-job-install-cilium')
    expect(screen.queryByTestId('jobs-filter-app')).toBeNull()
  })

  it('renders all seven canonical columns', async () => {
    renderTable({ jobs: FIXTURE_JOBS, deploymentId: 'd-1' })
    await screen.findByTestId('jobs-table')
    const headers = screen
      .getAllByRole('columnheader')
      .map((h) => (h.textContent ?? '').toLowerCase().trim())
    expect(headers).toEqual(['name', 'app', 'deps', 'batch', 'status', 'started', 'duration'])
  })

  it('row link points at /provision/$deploymentId/jobs/$jobId', async () => {
    renderTable({ jobs: FIXTURE_JOBS, deploymentId: 'd-1' })
    await screen.findByTestId('jobs-table')
    const link = screen.getByTestId('jobs-row-link-job-install-cilium') as HTMLAnchorElement
    expect(link.tagName.toLowerCase()).toBe('a')
    expect(link.getAttribute('href')).toBe('/provision/d-1/jobs/job-install-cilium')
  })

  // Issue #232 verbatim: "simulates 0 reducer-derived jobs + 5
  // backend-API jobs, expects 5 rows rendered with backend data".
  // The JobsPage merges reducer-derived + live-backfill via mergeJobs()
  // before passing the array to JobsTable; this test exercises the
  // table's render path with the merged input directly.
  it('renders all rows when fed exclusively from a backend-jobs API list (issue #232)', async () => {
    const liveOnly: Job[] = [
      {
        id: 'bp-cilium', jobName: 'Install Cilium', appId: 'bp-cilium',
        batchId: 'applications', dependsOn: [], status: 'succeeded',
        startedAt: '2026-04-29T10:00:00Z', finishedAt: '2026-04-29T10:01:00Z',
        durationMs: 60_000,
      },
      {
        id: 'bp-cert-manager', jobName: 'Install cert-manager', appId: 'bp-cert-manager',
        batchId: 'applications', dependsOn: [], status: 'succeeded',
        startedAt: '2026-04-29T10:01:00Z', finishedAt: '2026-04-29T10:02:00Z',
        durationMs: 60_000,
      },
      {
        id: 'bp-flux', jobName: 'Install Flux', appId: 'bp-flux',
        batchId: 'applications', dependsOn: [], status: 'succeeded',
        startedAt: '2026-04-29T10:02:00Z', finishedAt: '2026-04-29T10:03:00Z',
        durationMs: 60_000,
      },
      {
        id: 'bp-crossplane', jobName: 'Install Crossplane', appId: 'bp-crossplane',
        batchId: 'applications', dependsOn: [], status: 'running',
        startedAt: '2026-04-29T10:03:00Z', finishedAt: null, durationMs: 0,
      },
      {
        id: 'bp-vault', jobName: 'Install Vault', appId: 'bp-vault',
        batchId: 'applications', dependsOn: [], status: 'pending',
        startedAt: null, finishedAt: null, durationMs: 0,
      },
    ]
    renderTable({ jobs: liveOnly, deploymentId: 'd-1' })
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBe(5)
    // Statuses surface verbatim from the backend list (no demotion to pending).
    expect(screen.getByTestId('jobs-cell-status-bp-cilium').textContent?.toLowerCase()).toContain('succeeded')
    expect(screen.getByTestId('jobs-cell-status-bp-crossplane').textContent?.toLowerCase()).toContain('running')
    expect(screen.getByTestId('jobs-cell-status-bp-vault').textContent?.toLowerCase()).toContain('pending')
  })
})
