/**
 * DecommissionPage.test.tsx — sovereign self-decommission UI lock-in.
 *
 * Coverage:
 *   1. Submit button is disabled by default.
 *   2. All four gates (typed FQDN, Hetzner token >20 chars, data-loss
 *      acknowledgement, valid backup destination) must be satisfied
 *      before the submit unlocks.
 *   3. Choosing S3 backup reveals four extra inputs and re-locks the
 *      submit until they are filled.
 *   4. Clicking submit fires `POST /api/v1/deployments/{id}/wipe` with
 *      the typed Hetzner token + the backup-destination payload.
 *   5. A non-200 response surfaces an error pre tag without rendering
 *      the success view.
 *
 * The page is purely presentational; the wipe endpoint is mocked at
 * the global fetch level. We do NOT exercise the real catalyst-api or
 * the real PDM client here — those are covered by the Go tests.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { DecommissionPage } from './DecommissionPage'

// useDeploymentEvents — stub the hook so the page doesn't try to attach
// an EventSource to a fake catalyst-api. The page needs:
//   • snapshot.sovereignFQDN — the typed-confirmation hint.
//   • state.eventsByTarget   — the unified LogPane log lines (issue
//                              #766). The synthetic events below
//                              mirror the real wipe-handler emissions
//                              (phase="wipe") so the streaming-view
//                              tests assert the LogPane renders them.
//
// `useDeploymentEventsLastOpts` records the most-recent invocation so
// the streaming-view tests can assert that `disableStream: false` was
// passed once the wipe POST landed (i.e. the SSE stream was actually
// attached during decommission, not just left disabled like during the
// pre-submit form view).
const useDeploymentEventsLastOpts: { current: { disableStream?: boolean } } = {
  current: {},
}
vi.mock('./useDeploymentEvents', () => ({
  useDeploymentEvents: (opts: { disableStream?: boolean }) => {
    useDeploymentEventsLastOpts.current = opts
    return {
      state: {
        apps: {},
        hetznerInfra: { status: 'pending', message: null, lastEventTime: null, seenResources: new Set() },
        clusterBootstrap: { status: 'pending', message: null, lastEventTime: null },
        phase1WatchSkipped: false,
        phase1WatchSkippedReason: null,
        eventsByTarget: {
          // Synthetic wipe events — the catalyst-api emits these with
          // phase="wipe" today; the reducer's fall-through routes them
          // to the cluster-bootstrap bucket. The DecommissionPage
          // flatten collects every bucket so the precise key here
          // doesn't matter for the contract.
          __cluster_bootstrap__: [
            {
              time: '2026-05-04T17:00:00Z',
              phase: 'wipe',
              level: 'info',
              message: 'Wipe initiated for omantel.omani.works (was: failed)',
            },
            {
              time: '2026-05-04T17:00:01Z',
              phase: 'wipe',
              level: 'info',
              message: 'hetzner: Deleting LB lb-omantel-cp… Deleted (200)',
            },
            {
              time: '2026-05-04T17:00:02Z',
              phase: 'wipe',
              level: 'info',
              message:
                'hetzner: Hetzner orphan purge removed 4 resource(s) (servers: 1, lbs: 1, networks: 1, firewalls: 1, ssh-keys: 0, s3-buckets: 0)',
            },
          ],
        },
      },
      snapshot: {
        sovereignFQDN: 'omantel.omani.works',
        result: { sovereignFQDN: 'omantel.omani.works' },
      },
      streamStatus: 'completed',
      streamError: null,
      startedAt: null,
      finishedAt: null,
      retry: () => {},
    }
  },
}))

function renderPage(deploymentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const decommRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/decommission/$deploymentId',
    component: () => <DecommissionPage />,
  })
  const provisionRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="provision-target" />,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([decommRoute, provisionRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/decommission/${deploymentId}`],
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

const longHetznerToken = 'a'.repeat(64) // > 20 chars to satisfy the gate

describe('DecommissionPage', () => {
  beforeEach(() => {
    // Default fetch stub — overridden per-test for the success / error
    // paths.
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      throw new Error('fetch should not be called by default')
    })
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('disables submit by default', async () => {
    renderPage('dep-1')
    const submit = await screen.findByTestId('decommission-submit')
    expect((submit as HTMLButtonElement).disabled).toBe(true)
  })

  it('unlocks submit once all four gates pass', async () => {
    renderPage('dep-1')
    const submit = await screen.findByTestId('decommission-submit')
    fireEvent.change(screen.getByTestId('decommission-confirm-text'), {
      target: { value: 'omantel.omani.works' },
    })
    fireEvent.change(screen.getByTestId('decommission-hetzner-token'), {
      target: { value: longHetznerToken },
    })
    fireEvent.click(screen.getByTestId('decommission-acknowledge'))
    expect((submit as HTMLButtonElement).disabled).toBe(false)
  })

  it('re-locks the submit when S3 backup is chosen but inputs are empty', async () => {
    renderPage('dep-1')
    const submit = await screen.findByTestId('decommission-submit')
    fireEvent.change(screen.getByTestId('decommission-confirm-text'), {
      target: { value: 'omantel.omani.works' },
    })
    fireEvent.change(screen.getByTestId('decommission-hetzner-token'), {
      target: { value: longHetznerToken },
    })
    fireEvent.click(screen.getByTestId('decommission-acknowledge'))
    expect((submit as HTMLButtonElement).disabled).toBe(false)

    // Switch to S3 — submit should re-lock until endpoint/bucket/keys
    // are populated.
    fireEvent.click(screen.getByTestId('decommission-backup-s3'))
    expect((submit as HTMLButtonElement).disabled).toBe(true)

    fireEvent.change(screen.getByTestId('decommission-backup-s3-endpoint'), {
      target: { value: 'https://fsn1.your-objectstorage.com' },
    })
    fireEvent.change(screen.getByTestId('decommission-backup-s3-bucket'), {
      target: { value: 'omantel-backup' },
    })
    fireEvent.change(screen.getByTestId('decommission-backup-s3-access'), {
      target: { value: 'AKIA-FAKE-KEY' },
    })
    fireEvent.change(screen.getByTestId('decommission-backup-s3-secret'), {
      target: { value: 'super-secret-fake-1234' },
    })
    expect((submit as HTMLButtonElement).disabled).toBe(false)
  })

  it('POSTs the wipe call with the chosen backup payload and renders the success view', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = typeof input === 'string' ? input : (input as Request).url
      expect(url).toContain('/v1/deployments/dep-1/wipe')
      return new Response(
        JSON.stringify({
          deploymentId: 'dep-1',
          sovereignFQDN: 'omantel.omani.works',
          tofuDestroyed: true,
          hetznerPurge: {
            servers: ['s1'],
            load_balancers: ['lb1'],
            networks: [],
            firewalls: [],
            ssh_keys: [],
            errors: [],
          },
          pdmReleased: true,
          localCleaned: true,
          errors: [],
          wipedAt: '2026-05-01T00:00:00Z',
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    })

    renderPage('dep-1')
    fireEvent.change(await screen.findByTestId('decommission-confirm-text'), {
      target: { value: 'omantel.omani.works' },
    })
    fireEvent.change(screen.getByTestId('decommission-hetzner-token'), {
      target: { value: longHetznerToken },
    })
    fireEvent.click(screen.getByTestId('decommission-acknowledge'))
    fireEvent.click(screen.getByTestId('decommission-submit'))

    await waitFor(() => screen.getByTestId('decommission-success'))
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    const body = JSON.parse(fetchSpy.mock.calls[0][1]?.body as string)
    expect(body.hetznerToken).toBe(longHetznerToken)
    expect(body.backup).toEqual({ kind: 'none' })
  })

  it('surfaces a non-200 response as an inline error', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      return new Response('hetznerToken is required', { status: 400 })
    })
    renderPage('dep-1')
    fireEvent.change(await screen.findByTestId('decommission-confirm-text'), {
      target: { value: 'omantel.omani.works' },
    })
    fireEvent.change(screen.getByTestId('decommission-hetzner-token'), {
      target: { value: longHetznerToken },
    })
    fireEvent.click(screen.getByTestId('decommission-acknowledge'))
    fireEvent.click(screen.getByTestId('decommission-submit'))

    const errPre = await screen.findByTestId('decommission-error')
    expect(errPre.textContent).toContain('HTTP 400')
    expect(screen.queryByTestId('decommission-success')).toBeNull()
  })
})

/* ── #766 — verbose live exec-log view during the wipe ──────────────
 *
 * Lock-in for the streaming view that replaces the static
 * "Decommissioning…" banner. The wipe handler in api/internal/handler/
 * wipe.go ALREADY emits a per-resource SSE stream — the page now
 * subscribes to it via useDeploymentEvents and pipes the events
 * through the unified LogPane (the same component /provision/<id>
 * uses for per-job logs). Assertions:
 *
 *   1. While the wipe POST is in flight, the streaming layout (LogPane
 *      + STREAMING chip + per-event log lines) is rendered, NOT the
 *      pre-submit form.
 *   2. The SSE stream is actually attached: the page passes
 *      `disableStream: false` to useDeploymentEvents during streaming.
 *   3. Once the wipe POST resolves, the layout flips to the COMPLETE
 *      chip + green checkmark + Hetzner-sweep summary + countdown row.
 *      The historical log is preserved (operator can scroll back).
 *   4. The summary surfaces every Hetzner resource kind with the
 *      removed count — the founder-verbatim "0 of every kind" DoD.
 */
describe('DecommissionPage — #766 streaming exec-log view', () => {
  beforeEach(() => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      throw new Error('fetch should not be called by default')
    })
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders the unified LogPane streaming layout while the wipe POST is in flight', async () => {
    // Pending fetch — the wipe POST hangs forever for this test so we
    // can observe the in-flight UI state without a race against the
    // success handler.
    vi.spyOn(globalThis, 'fetch').mockImplementation(
      () => new Promise(() => {}) as Promise<Response>,
    )
    renderPage('dep-1')
    fireEvent.change(await screen.findByTestId('decommission-confirm-text'), {
      target: { value: 'omantel.omani.works' },
    })
    fireEvent.change(screen.getByTestId('decommission-hetzner-token'), {
      target: { value: longHetznerToken },
    })
    fireEvent.click(screen.getByTestId('decommission-acknowledge'))
    fireEvent.click(screen.getByTestId('decommission-submit'))

    // Streaming layout appears, the form is gone.
    await waitFor(() => screen.getByTestId('decommission-page-streaming'))
    expect(screen.queryByTestId('decommission-page')).toBeNull()
    expect(screen.getByTestId('decommission-streaming-spinner')).toBeTruthy()
    expect(screen.getByTestId('decommission-log-host')).toBeTruthy()
    expect(screen.getByTestId('log-pane')).toBeTruthy()

    // Synthetic wipe events from the useDeploymentEvents mock are
    // surfaced as LogPane fallback lines.
    expect(screen.getByTestId('fallback-log-line-1')).toBeTruthy()
    expect(screen.getByTestId('fallback-log-line-2')).toBeTruthy()
    expect(screen.getByTestId('fallback-log-line-3')).toBeTruthy()
    // Founder-verbatim phrasing — every resource deletion is visible.
    expect(screen.getByText(/Deleting LB lb-omantel-cp/)).toBeTruthy()

    // SSE stream is attached during streaming (NOT disabled).
    expect(useDeploymentEventsLastOpts.current.disableStream).toBe(false)
  })

  it('flips to the COMPLETE chip + Hetzner-sweep summary + countdown when the wipe report arrives', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      return new Response(
        JSON.stringify({
          deploymentId: 'dep-1',
          sovereignFQDN: 'omantel.omani.works',
          tofuDestroyed: true,
          hetznerPurge: {
            servers: [],
            load_balancers: [],
            networks: [],
            firewalls: [],
            ssh_keys: [],
            s3_buckets: [],
            errors: [],
          },
          pdmReleased: true,
          localCleaned: true,
          errors: [],
          wipedAt: '2026-05-04T17:05:00Z',
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      )
    })

    renderPage('dep-1')
    fireEvent.change(await screen.findByTestId('decommission-confirm-text'), {
      target: { value: 'omantel.omani.works' },
    })
    fireEvent.change(screen.getByTestId('decommission-hetzner-token'), {
      target: { value: longHetznerToken },
    })
    fireEvent.click(screen.getByTestId('decommission-acknowledge'))
    fireEvent.click(screen.getByTestId('decommission-submit'))

    // Final state: COMPLETE chip + full sweep summary visible.
    await waitFor(() => screen.getByTestId('decommission-streaming-checkmark'))
    expect(screen.getByTestId('decommission-streaming-title').textContent).toContain(
      'decommissioned',
    )
    const summary = screen.getByTestId('decommission-streaming-summary').textContent ?? ''
    // Founder DoD — "0 of every kind" is visible in the summary block.
    expect(summary).toContain('servers:        0 removed')
    expect(summary).toContain('load_balancers: 0 removed')
    expect(summary).toContain('networks:       0 removed')
    expect(summary).toContain('firewalls:      0 removed')
    expect(summary).toContain('ssh_keys:       0 removed')
    expect(summary).toContain('s3_buckets:     0 removed')

    // Countdown row surfaces an explicit "Returning to wizard" hint so
    // operators know the auto-redirect is coming.
    expect(screen.getByTestId('decommission-countdown').textContent).toMatch(
      /Returning to wizard in \d+s/,
    )

    // Historical scrollback preserved — every wipe event is still in
    // the LogPane after the report lands.
    expect(screen.getByTestId('fallback-log-line-1')).toBeTruthy()

    // The hidden marker for backwards compatibility.
    expect(screen.getByTestId('decommission-success')).toBeTruthy()
  })
})
