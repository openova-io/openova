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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { JobsPage } from './JobsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderJobs(deploymentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <JobsPage disableStream disableJobsBackfill />,
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
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}/jobs`] }),
  })
  // Each render gets its own QueryClient so the live-jobs-backfill
  // query cache never bleeds between tests. Even with backfill
  // disabled the JobsPage's useQuery() still requires a provider.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
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

  it('renders the canonical columns including Kind (issue #3646)', async () => {
    renderJobs('d-1')
    const table = await screen.findByTestId('jobs-table')
    const headers = within(table).getAllByRole('columnheader').map((h) => (h.textContent ?? '').toLowerCase().trim())
    // Kind inserted after Name (#3646); Runs after Status (#3925 run-history
    // depth); Parent (not the legacy "batch") is the canonical column;
    // Actions trails when a deploymentId is threaded (JobsPage threads it).
    // Assert the stable left columns.
    expect(headers.slice(0, 9)).toEqual([
      'name', 'kind', 'app', 'deps', 'parent', 'status', 'runs', 'started', 'duration',
    ])
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

describe('JobsPage — batches concept removed (issue #351)', () => {
  it('does NOT render the per-batch progress strip', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('batch-progress')).toBeNull()
    const batchRows = document.querySelectorAll('[data-testid^="batch-row-"]')
    expect(batchRows.length).toBe(0)
  })

  it('parent chip in a row links to that parent group\'s home page', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    const chip = screen.getByTestId('jobs-cell-parent-bp-cilium') as HTMLAnchorElement
    expect(chip.tagName.toLowerCase()).toBe('a')
    // Issue #351: parent chip → /provision/$id/jobs/$parentId
    // (the parent group is itself a Job and has its own home page).
    const href = chip.getAttribute('href') ?? ''
    expect(href).toMatch(/^\/provision\/d-1\/jobs\//)
  })
})

// #3701 ("one honest /jobs canvas", Refs #3646) retired the Flow view of
// /jobs: it dropped `flowJobs` / `synthesizeJobFromFlowNode` as a list
// source (JobsPage.tsx:32-33), deleted JobsPage.flow-merge.test.tsx, and
// removed the "Show as Flow" line from the JobsPage docblock. The tab
// strip went with it. What survives is the single table canvas.
//
// The former 'exposes a "Show as Flow" button that navigates to /flow'
// case is deleted rather than repaired: `sov-jobs-show-as-flow` was only
// ever added to THIS test file (git log -S over src/ matches the test and
// nothing else), and `/provision/$deploymentId/flow` is not a route in
// src/app/router.tsx — the test stood up a private flowRoute in its own
// harness to make the target exist. Adding the button to satisfy it would
// ship a link to a 404.
describe('JobsPage — v3 routing (single table canvas, no Tab strip)', () => {
  it('does NOT render a jobs-view-tabs strip', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('jobs-view-tabs')).toBeNull()
    expect(screen.queryByTestId('jobs-view-tab-table')).toBeNull()
    expect(screen.queryByTestId('jobs-view-tab-flow')).toBeNull()
  })

  it('does NOT offer a Flow affordance (retired by #3701)', async () => {
    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('sov-jobs-show-as-flow')).toBeNull()
  })
})
