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
// an EventSource to a fake catalyst-api. The page only needs the
// snapshot.sovereignFQDN to render the typed-confirmation hint.
vi.mock('./useDeploymentEvents', () => ({
  useDeploymentEvents: () => ({
    state: {},
    snapshot: {
      sovereignFQDN: 'omantel.omani.works',
      result: { sovereignFQDN: 'omantel.omani.works' },
    },
    streamStatus: 'completed',
    streamError: null,
    startedAt: null,
    finishedAt: null,
    retry: () => {},
  }),
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
