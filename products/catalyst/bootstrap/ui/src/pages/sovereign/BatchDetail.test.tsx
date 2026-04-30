/**
 * BatchDetail.test.tsx — coverage for the per-batch detail surface
 * served at `/sovereign/provision/$deploymentId/batches/$batchId`
 * (epic openova-io/openova#204 item #4).
 *
 * Asserts:
 *   • Route renders + back-link points at /provision/$deploymentId/jobs
 *   • Single-batch progress card renders (batch-progress-single)
 *   • Progress bar carries the correct aria-valuenow for the picked batch
 *   • The embedded JobsTable is filtered to the picked batch's rows only
 *   • Batch filter dropdown is hidden (already pre-filtered)
 *   • Not-found state renders when the URL batchId has no matching jobs
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { BatchDetail } from './BatchDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderBatchDetail(deploymentId: string, batchId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const batchRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/batches/$batchId',
    component: () => <BatchDetail disableStream />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
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
    batchRoute,
    jobsRoute,
    homeRoute,
    jobDetailRoute,
    wizardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/batches/${batchId}`],
    }),
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

describe('BatchDetail — chrome', () => {
  it('renders the batch title from the URL parameter', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    const title = await screen.findByTestId('sov-batch-title')
    expect(title.textContent).toBe('phase-0-infra')
  })

  it('back-link points at /provision/$deploymentId/jobs', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    const link = await screen.findByTestId('sov-batch-back-to-jobs')
    expect(link.getAttribute('href')).toBe('/provision/d-1/jobs')
  })

  it('mounts inside the PortalShell', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
  })

  it('renders a breadcrumb above the title', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    const crumb = await screen.findByTestId('sov-batch-breadcrumb')
    expect(crumb.textContent).toMatch(/jobs/i)
    expect(crumb.textContent).toMatch(/batch/i)
  })
})

describe('BatchDetail — single batch progress card', () => {
  it('renders the batch-progress-single card for the picked batch', async () => {
    // The default Phase 0 batch is auto-derived from the wizard's
    // bootstrap-kit components — `phase-0-infra` exists from mount.
    renderBatchDetail('d-1', 'phase-0-infra')
    const card = await screen.findByTestId('batch-progress-single')
    expect(card).toBeTruthy()
    // The progress card renders one progressbar with aria-valuenow.
    const bar = await screen.findByTestId('batch-card-bar-phase-0-infra')
    const valuenow = bar.getAttribute('aria-valuenow')
    expect(valuenow).not.toBeNull()
    // Value must be an integer 0..100.
    const n = Number(valuenow)
    expect(Number.isFinite(n)).toBe(true)
    expect(n).toBeGreaterThanOrEqual(0)
    expect(n).toBeLessThanOrEqual(100)
  })

  it('renders the per-status counters (running / pending / succeeded / failed / total)', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    expect(await screen.findByTestId('batch-card-stat-running-phase-0-infra')).toBeTruthy()
    expect(screen.getByTestId('batch-card-stat-pending-phase-0-infra')).toBeTruthy()
    expect(screen.getByTestId('batch-card-stat-succeeded-phase-0-infra')).toBeTruthy()
    expect(screen.getByTestId('batch-card-stat-failed-phase-0-infra')).toBeTruthy()
    expect(screen.getByTestId('batch-card-stat-total-phase-0-infra')).toBeTruthy()
  })
})

describe('BatchDetail — filtered JobsTable', () => {
  it('embeds a JobsTable with no batch filter dropdown (already pre-filtered)', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('jobs-filter-batch')).toBeNull()
  })

  it('filtered JobsTable shows ONLY rows whose batchId matches the URL param', async () => {
    renderBatchDetail('d-1', 'phase-0-infra')
    await screen.findByTestId('jobs-table')
    const rows = screen.getAllByTestId(/^jobs-table-row-/)
    expect(rows.length).toBeGreaterThan(0)
    // Spot-check the four phase-0 rows from deriveJobs are in the
    // table (they all carry batchId='phase-0-infra').
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-init')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-plan')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-apply')).toBeTruthy()
    expect(screen.queryByTestId('jobs-table-row-infrastructure:tofu-output')).toBeTruthy()
    // And the cluster-bootstrap row (different batch) is NOT in this
    // filtered view.
    expect(screen.queryByTestId('jobs-table-row-cluster-bootstrap')).toBeNull()
  })
})

describe('BatchDetail — not-found state', () => {
  it('renders a not-found notice when the URL batchId has no matching jobs', async () => {
    renderBatchDetail('d-1', 'no-such-batch')
    expect(await screen.findByTestId('sov-batch-not-found')).toBeTruthy()
    // The single-batch progress card is NOT rendered when the batch
    // is missing — the component falls back to the empty state.
    expect(screen.queryByTestId('batch-progress-single')).toBeNull()
  })
})
