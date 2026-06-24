/**
 * JobsPage.bootstrap-progress.test.tsx — lock-in for issue #3914.
 *
 * The orphan `BootstrapProgress` widget (widgets/bootstrap-progress/) was
 * built + merged but imported NOWHERE, so the ~30-min "Provision
 * <provider>: Success" void on /provision/<id>/jobs was never filled with
 * live per-phase progress. This file asserts the widget is now mounted on
 * the customer's provisioning landing surface (JobsPage) during the live
 * provisioning window, and correctly hidden once provisioning terminates.
 *
 * The EventSource attach is suppressed via `disableStream` (jsdom has no
 * EventSource) — the widget then renders from its initial all-pending
 * phase map, which is exactly what the operator sees on first paint before
 * the SSE delivers events. The DOM-presence gating (in-flight vs.
 * completed vs. sovereign-chroot) is what this file verifies.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
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

function renderJobs(
  deploymentId: string,
  props: { disableBootstrapProgress?: boolean } = {},
) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => (
      // disableStream suppresses BOTH the deployment-events SSE and the
      // bootstrap-progress SSE (jsdom has no EventSource); the widget DOM
      // still renders from its initial all-pending phase map.
      <JobsPage disableStream disableJobsBackfill {...props} />
    ),
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
  const tree = rootRoute.addChildren([jobsRoute, homeRoute, jobDetailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/jobs`],
    }),
  })
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('JobsPage — #3914 BootstrapProgress timeline (in-flight)', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
    // `done: false` keeps the deployment in-flight (streamStatus stays
    // connecting/streaming), which is the window the void appears in.
    globalThis.fetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ events: [], state: undefined, done: false }),
      } as unknown as Response),
    ) as unknown as typeof fetch
  })

  it('mounts the BootstrapProgress timeline while provisioning is in-flight', async () => {
    renderJobs('d-1')
    // Jobs table renders first (waterfall), then the timeline section.
    await screen.findByTestId('jobs-table')
    const section = await screen.findByTestId('sov-jobs-bootstrap-progress')
    expect(section).toBeTruthy()
    // The widget's own landmark (a <nav aria-label="Bootstrap provisioning
    // progress">) confirms the actual BootstrapProgress component — not an
    // empty shell — rendered inside the section.
    const nav = section.querySelector('nav[aria-label="Bootstrap provisioning progress"]')
    expect(nav).toBeTruthy()
  })

  it('renders the Phase-0 OpenTofu + Phase-1 bootstrap-kit section headers', async () => {
    renderJobs('d-1')
    const section = await screen.findByTestId('sov-jobs-bootstrap-progress')
    // The widget always renders both section headers from ALL_PHASES even
    // before any event arrives — the operator sees the full plan up-front.
    expect(section.textContent).toContain('Phase 0')
    expect(section.textContent).toContain('OpenTofu')
    expect(section.textContent).toContain('Phase 1')
    expect(section.textContent).toContain('Bootstrap kit')
  })

  it('honours the disableBootstrapProgress test seam', async () => {
    renderJobs('d-1', { disableBootstrapProgress: true })
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('sov-jobs-bootstrap-progress')).toBeNull()
  })
})

describe('JobsPage — #3914 BootstrapProgress timeline (terminal)', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  })

  it('hides the timeline once the deployment reports ready (void is gone)', async () => {
    // `done: true` + status ready flips streamStatus to 'completed', so the
    // in-flight gate closes and the timeline must NOT render — the jobs
    // table now carries the full history.
    globalThis.fetch = vi.fn(() =>
      Promise.resolve({
        ok: true,
        json: () =>
          Promise.resolve({
            events: [],
            state: { id: 'd-1', status: 'ready', sovereignFQDN: 'x.omani.works' },
            done: true,
          }),
      } as unknown as Response),
    ) as unknown as typeof fetch

    renderJobs('d-1')
    await screen.findByTestId('jobs-table')
    // Allow the GET /events replay microtask to flip streamStatus.
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByTestId('sov-jobs-bootstrap-progress')).toBeNull()
  })
})
