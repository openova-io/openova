/**
 * AppDetail.status-d7.test.tsx — ADR-0015 D7 (Refs #7067): the app header and
 * Overview STATUS render catalyst-api's statusOf(object) VERBATIM.
 *
 * The live defect: `sovereign/test` carried Ready=False EnvironmentMissing
 * `Environment "sovereign-prod" not found`, and the console showed a bare
 * "Pending" — the browser mapped a phase (and let the wizard event-stream
 * compState win over the API) instead of reading the object's own condition.
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
import * as catalogApi from '@/lib/catalog.api'
import { AppDetail } from './AppDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderDetail() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <AppDetail disableStream />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([detailRoute]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/app/test'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

type Detail = Awaited<ReturnType<typeof catalogApi.getApplication>>

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => {
  vi.restoreAllMocks()
  cleanup()
})

describe('ADR-0015 D7 — AppDetail renders statusOf verbatim', () => {
  it('sovereign/test: shows NotReady + EnvironmentMissing + the message verbatim, never "Pending"', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue({
      name: 'test',
      namespace: 'sovereign',
      blueprint: 'bp-wordpress',
      // The raw CR phase is Pending; the browser must NOT render it as the status.
      phase: 'Pending',
      conditions: [],
      objectStatus: {
        state: 'NotReady',
        reason: 'EnvironmentMissing',
        message: 'Environment "sovereign-prod" not found',
        kind: 'Application',
        namespace: 'sovereign',
        name: 'test',
      },
    } as unknown as Detail)
    renderDetail()

    const badge = await screen.findByTestId('app-detail-phase')
    expect(badge.textContent).toBe('NotReady')
    expect(screen.getByTestId('app-detail-status-reason').textContent).toBe(
      'EnvironmentMissing — Environment "sovereign-prod" not found',
    )
    const overview = screen.getByTestId('app-detail-overview-phase')
    expect(overview.textContent).toContain('NotReady')
    expect(overview.textContent).toContain('EnvironmentMissing')
    expect(overview.textContent).toContain('Environment "sovereign-prod" not found')
    expect(overview.textContent).not.toContain('Pending')
    expect(badge.textContent).not.toContain('Pending')
  })

  it('absent objectStatus renders Unknown — never a guessed Pending or Ready', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue({
      name: 'test',
      namespace: 'sovereign',
      blueprint: 'bp-wordpress',
      phase: 'Ready',
      conditions: [],
    } as unknown as Detail)
    renderDetail()

    const badge = await screen.findByTestId('app-detail-phase')
    expect(badge.textContent).toBe('Unknown')
    expect(screen.getByTestId('app-detail-overview-phase').textContent).toContain('Unknown')
  })

  it('the deleted HR-overlay telemetry no longer renders', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue({
      name: 'test',
      namespace: 'sovereign',
      blueprint: 'bp-wordpress',
      conditions: [],
      hrReady: true,
      phaseFromCR: 'Provisioning',
      objectStatus: { state: 'Ready', reason: 'Reconciled', message: 'installed across 1 region(s)' },
    } as unknown as Detail)
    renderDetail()

    const source = await screen.findByTestId('app-detail-source')
    expect(source.textContent).toBe('source: Application CR')
    expect(source.getAttribute('data-hr-overlay')).toBeNull()
    expect(document.body.textContent).not.toContain('HR-overlayed')
  })
})
