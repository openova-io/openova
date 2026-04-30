/**
 * JobsPage.test.tsx — lock-in for the table-view jobs surface (issue
 * #204 founder spec). Asserts:
 *
 *   • Page heading + tagline render
 *   • <table data-testid="jobs-table"> renders (NOT a vertical accordion)
 *   • All seven columns present: Name / App / Deps / Batch / Status /
 *     Started / Duration
 *   • The legacy accordion testids ([data-testid^="job-row-"] and
 *     [data-testid^="job-expansion-"]) are GONE — anti-regression for
 *     the founder's "NEVER use accordions" rule.
 *   • Phase 0 + cluster-bootstrap + per-component rows all render.
 *   • Back-to-apps link points at /provision/$deploymentId.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { JobsPage } from './JobsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderJobs(deploymentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsPage disableStream />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const jobDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs/$jobId',
    component: () => <div data-testid="job-detail-target" />,
  })
  const batchDetailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/batches/$batchId',
    component: () => <div data-testid="batch-detail-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([
    jobsRoute,
    homeRoute,
    detailRoute,
    jobDetailRoute,
    batchDetailRoute,
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}/jobs`] }),
  })
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('JobsPage — chrome', () => {
  it('renders the Jobs heading', async () => {
    renderJobs('d-1')
    const heading = await screen.findByRole('heading', { level: 1, name: 'Jobs' })
    expect(heading).toBeTruthy()
  })

  it('mounts inside the PortalShell', async () => {
    renderJobs('d-1')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
  })

  it('back-to-apps link points at /provision/$deploymentId', async () => {
    renderJobs('d-1')
    const link = await screen.findByTestId('sov-jobs-back-to-apps')
    expect(link.getAttribute('href')).toBe('/provision/d-1')
  })
})

describe('JobsPage — table view (NOT accordion)', () => {
  it('renders <table data-testid="jobs-table">', async () => {
    renderJobs('d-1')
    const table = await screen.findByTestId('jobs-table')
    expect(table.tagName.toLowerCase()).toBe('table')
  })

  it('has the seven canonical columns', async () => {
    renderJobs('d-1')
    const table = await screen.findByTestId('jobs-table')
    const headers = within(table).getAllByRole('columnheader').map((h) => (h.textContent ?? '').toLowerCase().trim())
    expect(headers).toEqual(['name', 'app', 'deps', 'batch', 'status', 'started', 'duration'])
  })

  it('does NOT render any legacy accordion testids', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    // The old accordion shape exposed [data-testid^=job-row-] buttons
    // that toggled [data-testid^=job-expansion-] panels. The founder
    // rejected that pattern verbatim ("NEVER use accordions").
    const rows = document.querySelectorAll('[data-testid^="job-row-"]')
    expect(rows.length).toBe(0)
    const expansions = document.querySelectorAll('[data-testid^="job-expansion-"]')
    expect(expansions.length).toBe(0)
  })

  it('renders Phase 0 + cluster-bootstrap + per-component rows', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    // Each row carries a per-id testid via the JobsTable row
    // component (jobs-table-row-<jobId>). Spot-check the four tofu
    // phases + cluster-bootstrap + at least one bootstrap-kit row.
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-init')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-plan')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-apply')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-output')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-cluster-bootstrap')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-bp-cilium')).toBeTruthy()
  })

  it('row link target points at /provision/$deploymentId/jobs/$jobId', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    const link = screen.getByTestId('jobs-row-link-bp-cilium') as HTMLAnchorElement
    expect(link.tagName.toLowerCase()).toBe('a')
    expect(link.getAttribute('href')).toBe('/provision/d-1/jobs/bp-cilium')
  })
})

describe('JobsPage — search', () => {
  it('exposes a jobs-search input', async () => {
    renderJobs('d-1')
    const search = await screen.findByTestId('jobs-search')
    expect(search.tagName.toLowerCase()).toBe('input')
  })
})

describe('JobsPage — batches strip removed (epic #204 item #4)', () => {
  it('does NOT render the per-batch progress strip', async () => {
    // Founder verbatim: "On the jobs page the top 3 cards are not
    // required, the progress bar needs to be shown only when I click
    // a specific batch and it shows the batch page along with its
    // batch progress at the top". This guard locks in the removal.
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('batch-progress')).toBeNull()
    const batchRows = document.querySelectorAll('[data-testid^="batch-row-"]')
    expect(batchRows.length).toBe(0)
  })

  it('batch chip in a row links to the BatchDetail page', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    const chip = screen.getByTestId('jobs-cell-batch-bp-cilium') as HTMLAnchorElement
    expect(chip.tagName.toLowerCase()).toBe('a')
    // Spot-check the route shape — the batch id varies by job.
    expect(chip.getAttribute('href')).toMatch(/^\/provision\/d-1\/batches\//)
  })
})
