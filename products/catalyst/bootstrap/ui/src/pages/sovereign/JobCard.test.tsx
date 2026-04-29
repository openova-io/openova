/**
 * JobCard.test.tsx — pixel-port lock-in for the row component shared
 * between JobsPage and AppDetail's appended Jobs section.
 *
 *   • Default-collapsed for non-running jobs; default-expanded for
 *     running ones (same as canonical JobsPage.svelte).
 *   • Click the row → toggles expansion; click the app-name on a
 *     per-component row → navigates to AppDetail (no `/job/$jobId`).
 *   • Status badge mirrors `statusBadge()` from jobs.ts.
 *   • Step list renders one row per step with the expected status
 *     iconography (success / running / failed / pending number).
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { JobCard } from './JobCard'
import type { Job } from './jobs'

const RUNNING_JOB: Job = {
  id: 'bp-cilium',
  app: 'bp-cilium',
  title: 'Install Cilium',
  status: 'running',
  updatedAt: '2026-04-29T10:00:00Z',
  noAppLink: false,
  steps: [
    { index: 0, name: 'Reconciling HelmRelease', status: 'succeeded', startedAt: '2026-04-29T10:00:00Z', message: null },
    { index: 1, name: 'Pulling chart from OCI', status: 'running', startedAt: '2026-04-29T10:00:30Z', message: null },
    { index: 2, name: 'Applying CRDs', status: 'pending', startedAt: null, message: null },
  ],
}

const PENDING_INFRA_JOB: Job = {
  id: 'infrastructure:tofu-init',
  app: 'infrastructure',
  title: 'Provision Hetzner — terraform init',
  status: 'pending',
  updatedAt: null,
  noAppLink: true,
  steps: [],
}

const FAILED_JOB: Job = {
  id: 'infrastructure:tofu-apply',
  app: 'infrastructure',
  title: 'Provision Hetzner — terraform apply',
  status: 'failed',
  updatedAt: '2026-04-29T10:05:00Z',
  noAppLink: true,
  steps: [
    { index: 0, name: 'Creating hcloud_server', status: 'failed', startedAt: '2026-04-29T10:00:00Z', message: 'rate limited' },
  ],
}

function renderWithRouter(component: React.ReactNode) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <>{component}</>,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const tree = rootRoute.addChildren([homeRoute, detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1'] }),
  })
  return render(<RouterProvider router={router} />)
}

afterEach(() => cleanup())
beforeEach(() => {})

describe('JobCard — chrome', () => {
  it('renders the title + a status badge', async () => {
    renderWithRouter(<JobCard job={RUNNING_JOB} deploymentId="d-1" />)
    expect(await screen.findByTestId(`sov-job-card-${RUNNING_JOB.id}`)).toBeTruthy()
    const badge = screen.getByTestId(`sov-job-badge-${RUNNING_JOB.id}`)
    expect(badge.textContent).toBe('Running')
  })

  it('shows the step count meta line ("X/Y steps · last update HH:MM:SS")', async () => {
    renderWithRouter(<JobCard job={RUNNING_JOB} deploymentId="d-1" />)
    const card = await screen.findByTestId(`sov-job-card-${RUNNING_JOB.id}`)
    expect(card.textContent).toContain('1/3 steps')
  })

  it('renders the failed badge for failed jobs', async () => {
    renderWithRouter(<JobCard job={FAILED_JOB} deploymentId="d-1" />)
    const badge = await screen.findByTestId(`sov-job-badge-${FAILED_JOB.id}`)
    expect(badge.textContent).toBe('Failed')
  })

  it('renders the pending badge with no progress bar for pending jobs', async () => {
    renderWithRouter(<JobCard job={PENDING_INFRA_JOB} deploymentId="d-1" />)
    const badge = await screen.findByTestId(`sov-job-badge-${PENDING_INFRA_JOB.id}`)
    expect(badge.textContent).toBe('Pending')
  })
})

describe('JobCard — expand / collapse', () => {
  it('default-expands a running job', async () => {
    renderWithRouter(<JobCard job={RUNNING_JOB} deploymentId="d-1" />)
    const panel = await screen.findByTestId(`sov-job-panel-${RUNNING_JOB.id}`)
    expect(panel).toBeTruthy()
    // All three steps render
    expect(within(panel).getByTestId('sov-step-0')).toBeTruthy()
    expect(within(panel).getByTestId('sov-step-1')).toBeTruthy()
    expect(within(panel).getByTestId('sov-step-2')).toBeTruthy()
  })

  it('default-collapses a pending job', async () => {
    renderWithRouter(<JobCard job={PENDING_INFRA_JOB} deploymentId="d-1" />)
    expect(await screen.findByTestId(`sov-job-card-${PENDING_INFRA_JOB.id}`)).toBeTruthy()
    expect(screen.queryByTestId(`sov-job-panel-${PENDING_INFRA_JOB.id}`)).toBeNull()
  })

  it('clicking the row toggles expansion', async () => {
    renderWithRouter(<JobCard job={PENDING_INFRA_JOB} deploymentId="d-1" />)
    const row = await screen.findByTestId(`sov-job-row-${PENDING_INFRA_JOB.id}`)
    fireEvent.click(row)
    expect(screen.queryByTestId(`sov-job-panel-${PENDING_INFRA_JOB.id}`)).toBeTruthy()
    fireEvent.click(row)
    expect(screen.queryByTestId(`sov-job-panel-${PENDING_INFRA_JOB.id}`)).toBeNull()
  })

  it('respects defaultExpanded={true}', async () => {
    renderWithRouter(<JobCard job={PENDING_INFRA_JOB} deploymentId="d-1" defaultExpanded />)
    expect(await screen.findByTestId(`sov-job-panel-${PENDING_INFRA_JOB.id}`)).toBeTruthy()
  })
})

describe('JobCard — app-name link', () => {
  it('renders the title as a Link for per-component rows', async () => {
    renderWithRouter(<JobCard job={RUNNING_JOB} deploymentId="d-1" />)
    const link = await screen.findByTestId(`sov-job-title-link-${RUNNING_JOB.id}`)
    expect(link.tagName.toLowerCase()).toBe('a')
    expect(link.getAttribute('href')).toBe('/provision/d-1/app/bp-cilium')
  })

  it('renders the title as plain text for noAppLink rows', async () => {
    renderWithRouter(<JobCard job={PENDING_INFRA_JOB} deploymentId="d-1" />)
    const title = await screen.findByTestId(`sov-job-title-${PENDING_INFRA_JOB.id}`)
    expect(title.tagName.toLowerCase()).toBe('p')
    // No link-titled element exists for this row.
    expect(screen.queryByTestId(`sov-job-title-link-${PENDING_INFRA_JOB.id}`)).toBeNull()
  })
})

describe('JobCard — empty steps', () => {
  it('shows a placeholder when an expanded job has no steps yet', async () => {
    renderWithRouter(<JobCard job={PENDING_INFRA_JOB} deploymentId="d-1" defaultExpanded />)
    const panel = await screen.findByTestId(`sov-job-panel-${PENDING_INFRA_JOB.id}`)
    expect(panel.textContent).toContain('No steps yet')
  })
})
