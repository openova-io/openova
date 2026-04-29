/**
 * JobsPage.test.tsx — pixel-port lock-in for the global jobs surface.
 *
 *   • Page heading + tagline render
 *   • Vertical stack of JobCard rows (one per Job)
 *   • Phase 0 (4 tofu) + cluster-bootstrap + per-component jobs all
 *     render — the operator never has to scroll past anything to find
 *     a row.
 *   • NO `/job/$jobId` route — clicking the app-name on a per-component
 *     row navigates to AppDetail; the page itself never registers an
 *     extra route.
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
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([jobsRoute, homeRoute, detailRoute, wizardRoute])
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
    // There are multiple "Jobs" texts — sidebar nav link + page heading.
    // The H1 is the heading we care about.
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

describe('JobsPage — list', () => {
  it('renders Phase 0 tofu rows + cluster-bootstrap + per-component rows', async () => {
    renderJobs('d-1')
    const list = await screen.findByTestId('sov-jobs-list')
    // 4 Phase 0 tofu rows (init/plan/apply/output)
    expect(within(list).queryByTestId('sov-job-card-infrastructure:tofu-init')).toBeTruthy()
    expect(within(list).queryByTestId('sov-job-card-infrastructure:tofu-plan')).toBeTruthy()
    expect(within(list).queryByTestId('sov-job-card-infrastructure:tofu-apply')).toBeTruthy()
    expect(within(list).queryByTestId('sov-job-card-infrastructure:tofu-output')).toBeTruthy()
    // cluster-bootstrap row
    expect(within(list).queryByTestId('sov-job-card-cluster-bootstrap')).toBeTruthy()
    // At least one per-component row from BOOTSTRAP_KIT
    expect(within(list).queryByTestId('sov-job-card-bp-cilium')).toBeTruthy()
  })
})
