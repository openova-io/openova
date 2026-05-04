/**
 * AppsPage.handover.test.tsx — issues #764 + #768 lock-in.
 *
 * Ground truth:
 *   • When catalyst-api auto-fires the handover JWT mint after Phase-1
 *     reaches OutcomeReady, the wizard's provision page MUST render the
 *     "Open your Sovereign console →" affordance immediately and start
 *     a 5-second auto-redirect timer.
 *   • The signal arrives one of two ways: a typed SSE
 *     `handover-ready` event (live), OR the GET /deployments/{id}
 *     reply carrying `handoverURL` (replay). Both paths exercise the
 *     same UI surface.
 *
 * What this file proves:
 *
 *   1. GET-replay path: a /deployments/{id}/events response with
 *      `state.handoverURL` non-empty renders the green banner + the
 *      CTA link (anchor with the canonical href).
 *   2. The CTA link points at the handoverURL verbatim.
 *   3. The auto-redirect timer fires after 5 seconds (faked via
 *      Vitest fake timers).
 *   4. The document title flips to "✓ Sovereign ready — <fqdn>" while
 *      the handover-ready state is active.
 *
 * The live-SSE path (typed `handover-ready` event) is exercised by a
 * direct unit test on the hook; AppsPage gets the signal through the
 * same `handoverReady` state regardless of which source set it.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AppsPage } from './AppsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { NotificationProvider } from '@/shared/ui/notifications'

const HANDOVER_URL = 'https://console.otech.example.com/auth/handover?token=AAA.BBB.CCC'

function renderProvisionWithReadyHandover(deploymentId: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <AppsPage disableStream />,
  })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const jobsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/jobs',
    component: () => <div data-testid="jobs-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([provisionRoute, detailRoute, jobsRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/provision/${deploymentId}`] }),
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
  // Stub fetch so useDeploymentEvents' history-replay path returns a
  // ready-with-handover snapshot. Mirrors the on-the-wire shape
  // catalyst-api emits after fireHandover persists the URL on the
  // record (see internal/handler.fireHandover).
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () =>
        Promise.resolve({
          events: [],
          state: {
            id: 'dep-handover-1',
            status: 'ready',
            sovereignFQDN: 'otech.example.com',
            handoverURL: HANDOVER_URL,
            handoverFiredAt: '2026-05-04T15:00:00Z',
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

describe('AppsPage — handover-ready (#764, #768)', () => {
  it('renders the green "Open your Sovereign console →" banner when handoverURL is present', async () => {
    renderProvisionWithReadyHandover('dep-handover-1')

    const banner = await screen.findByTestId('sov-handover-ready-banner')
    expect(banner).toBeTruthy()

    const cta = await screen.findByTestId('sov-handover-ready-cta')
    expect(cta).toBeTruthy()
    // Link points at the canonical handover URL — no SPA wrapping.
    expect(cta.getAttribute('href')).toBe(HANDOVER_URL)
    expect(cta.textContent).toContain('Open your Sovereign console')
  })

  it('renders the FQDN inside the banner title', async () => {
    renderProvisionWithReadyHandover('dep-handover-2')
    const banner = await screen.findByTestId('sov-handover-ready-banner')
    expect(banner.textContent).toContain('Sovereign is ready')
    expect(banner.textContent).toContain('otech.example.com')
  })

  it('schedules a window.location.href redirect after the 5-second auto-redirect timer', async () => {
    // Stub window.location.href — the production code performs a real
    // top-level navigation, which jsdom can't follow. We replace
    // window.location with a writeable proxy whose href setter is a
    // spy. We use real timers + a short window.setTimeout poll because
    // mixing vi.useFakeTimers() with @testing-library waitFor (which
    // needs the microtask queue + real timeouts) caused the test to
    // hang on the post-banner-render flush.
    const original = window.location
    const hrefSpy = vi.fn()
    delete (window as { location?: Location }).location
    ;(window as unknown as { location: Partial<Location> }).location = {
      ...original,
      get href() {
        return original.href
      },
      set href(v: string) {
        hrefSpy(v)
      },
    } as Location

    try {
      renderProvisionWithReadyHandover('dep-handover-3')

      // The banner is rendered — that proves handoverReady is set,
      // which in turn proves the 5-second timer has been scheduled
      // (the timer-arming effect runs in the same render cycle as
      // the banner).
      const cta = await screen.findByTestId('sov-handover-ready-cta')
      expect(cta.getAttribute('href')).toBe(HANDOVER_URL)

      // Wait up to 6 seconds (with a small buffer) for the timer to
      // fire window.location.href = handoverURL.
      await waitFor(
        () => {
          expect(hrefSpy).toHaveBeenCalledWith(HANDOVER_URL)
        },
        { timeout: 6000, interval: 200 },
      )
    } finally {
      // Restore window.location.
      ;(window as unknown as { location: Location }).location = original
    }
  }, 10000)

  it('flips document.title to "✓ Sovereign ready — <fqdn>" while handover is active', async () => {
    renderProvisionWithReadyHandover('dep-handover-4')
    await waitFor(() => {
      expect(document.title).toContain('Sovereign ready')
      expect(document.title).toContain('otech.example.com')
    })
  })
})
