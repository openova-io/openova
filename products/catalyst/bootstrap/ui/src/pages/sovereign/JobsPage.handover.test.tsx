/**
 * JobsPage.handover.test.tsx — post-#3646 lock-in.
 *
 * Gap C (the `applyHandoverStageOverride` client-side coercion) is GONE:
 * after the #3646 de-merge the JobsPage renders ONE backend list and no
 * longer ingests the openova-flow snapshot's synthetic Apps/Handover/
 * Cutover phantom groups as a list source — so there is nothing to coerce.
 * The honest replacement assertion lives in
 * `JobsPage — #3646 honest list (no phantom lifecycle rows)`: a backend
 * `/jobs` payload that carries NO Apps/Handover phantom rows produces a
 * table with NO Apps/Handover rows (the client never fabricates them).
 *
 * Gap D — the handover redirect banner — is UNCHANGED and still covered:
 * once handover fires (status=ready / handoverFiredAt non-null) the
 * operator sees a 3-2-1 countdown banner with an "Open your Sovereign
 * console →" CTA + a Cancel button; the timer auto-fires
 * window.location.assign(handoverURL) once when the countdown reaches 0.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

// eslint-disable-next-line import/first
import { JobsPage } from './JobsPage'

const DEP_ID = 'f9472ed4d2b9cc2d'
const HANDOVER_URL =
  'https://console.t120.omani.works/auth/handover?token=eyJ.AAA.BBB'

function renderJobs(opts: { disableHandoverAutoRedirect?: boolean; disableJobsBackfill?: boolean } = {}) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => (
      <JobsPage
        disableStream
        disableJobsBackfill={opts.disableJobsBackfill ?? true}
        disableHandoverAutoRedirect={opts.disableHandoverAutoRedirect ?? true}
      />
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
  const flowRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/flow',
    component: () => <div data-testid="flow-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const dashboardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard',
    component: () => <div data-testid="dashboard-target" />,
  })
  const tree = rootRoute.addChildren([
    jobsRoute,
    homeRoute,
    jobDetailRoute,
    flowRoute,
    wizardRoute,
    dashboardRoute,
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${DEP_ID}/jobs`],
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

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })

  // Stub the deployment events fetch — returns a 'ready' snapshot with
  // handoverURL + handoverFiredAt populated. Mirrors the wire shape
  // catalyst-api emits after fireHandover persists the record.
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () =>
        Promise.resolve({
          events: [],
          state: {
            id: DEP_ID,
            status: 'ready',
            sovereignFQDN: 't120.omani.works',
            handoverURL: HANDOVER_URL,
            handoverFiredAt: '2026-05-16T09:55:06Z',
            phase1Outcome: 'ready',
          },
          done: true,
        }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => {
  cleanup()
  vi.useRealTimers()
})

describe('JobsPage — #3646 honest list (no phantom lifecycle rows)', () => {
  it('renders no Apps/Handover rows when the backend list carries none — the client never fabricates them', async () => {
    renderJobs()
    // The banner proves the page mounted + handover snapshot loaded.
    await screen.findByTestId('sov-jobs-handover-redirect-banner')
    // With the de-merge the client no longer synthesizes the openova-flow
    // Apps/Handover phantom groups; the backend list (empty here) is the
    // sole source, so NO phantom lifecycle rows appear.
    expect(screen.queryByTestId(`jobs-table-row-${DEP_ID}:apps`)).toBeNull()
    expect(screen.queryByTestId(`jobs-table-row-${DEP_ID}:handover`)).toBeNull()
  })
})

describe('JobsPage — Gap D: handover redirect banner', () => {
  it('renders the 3-2-1 countdown banner with the canonical handoverURL when handover has fired', async () => {
    renderJobs({ disableHandoverAutoRedirect: true })

    const banner = await screen.findByTestId('sov-jobs-handover-redirect-banner')
    expect(banner).toBeTruthy()
    expect(banner.textContent).toContain('Sovereign is ready')
    expect(banner.textContent).toContain('t120.omani.works')

    const cta = await screen.findByTestId('sov-jobs-handover-redirect-cta')
    expect(cta.getAttribute('href')).toBe(HANDOVER_URL)
    expect(cta.textContent).toContain('Open your Sovereign console')

    // Initial countdown value is rendered (3 seconds).
    const countdown = await screen.findByTestId(
      'sov-jobs-handover-redirect-countdown',
    )
    expect(countdown.textContent).toBe('3')
  })

  it('Cancel button suppresses the banner for the rest of the page lifetime', async () => {
    renderJobs({ disableHandoverAutoRedirect: true })

    const cancel = await screen.findByTestId('sov-jobs-handover-redirect-cancel')
    fireEvent.click(cancel)

    // Banner is removed from the DOM after cancel.
    await waitFor(() => {
      expect(
        screen.queryByTestId('sov-jobs-handover-redirect-banner'),
      ).toBeNull()
    })
  })

  it('auto-redirect fires window.location.assign(handoverURL) once when the countdown reaches 0', async () => {
    // Stub window.location.assign — production code performs a real
    // top-level navigation, which jsdom can't follow. Replace
    // window.location with a writeable proxy whose `assign` is a spy.
    const original = window.location
    const assignSpy = vi.fn()
    delete (window as { location?: Location }).location
    ;(window as unknown as { location: Partial<Location> }).location = {
      ...original,
      assign: assignSpy as unknown as Location['assign'],
    } as Location

    try {
      // Render with the auto-redirect ENABLED so the interval timer
      // ticks down to 0. The countdown is 3 seconds; with the 1s
      // setInterval that's a 3-4 second real-time wait.
      renderJobs({ disableHandoverAutoRedirect: false })

      const banner = await screen.findByTestId(
        'sov-jobs-handover-redirect-banner',
      )
      expect(banner).toBeTruthy()

      await waitFor(
        () => {
          expect(assignSpy).toHaveBeenCalledWith(HANDOVER_URL)
        },
        { timeout: 6000, interval: 200 },
      )
      // The redirect MUST fire exactly once across the countdown lifecycle.
      expect(assignSpy).toHaveBeenCalledTimes(1)
    } finally {
      ;(window as unknown as { location: Location }).location = original
    }
  }, 10000)
})
