/**
 * AppDetail.placement-6192.test.tsx — #6192 (#3969): the Overview tab's
 * PLACEMENT cell and the Topology tab's "Pattern:" line are ONE value.
 *
 * The defect, measured on hw295 (grafana / keycloak) and hw307
 * (gitea / harbor / keycloak / openbao): the Overview printed the posture
 * STORED on the CR (`singleton`) while the Topology tab, one click away,
 * derived `active-active · runtime-observed` from the live targets. #3969
 * forbids a second contradictory value on one screen. #5422 had already
 * removed the `?? 'singleton'` fail-open; it did not remove the second
 * SOURCE.
 *
 * This is a RENDER assertion across the two tabs, as the issue asks: the
 * subject is a stored-`singleton` app whose runtime shows two Primaries —
 * the one shape on which the two cells disagreed. A unit test on the
 * resolver alone could not have caught it, because the resolver was never
 * what was wrong on screen.
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
import * as catalogApi from '@/lib/catalog.api'
import { AppDetail } from './AppDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

// The Topology tab mounts the editor + DR widgets; neither is the subject.
vi.mock('@/widgets/topology/PlacementEditor', () => ({
  PlacementEditor: () => <div data-testid="stub-placement-editor" />,
}))
vi.mock('@/widgets/continuum/SwitchoverDialog', () => ({
  SwitchoverDialog: () => null,
}))

function renderDetail() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <AppDetail disableStream />,
  })
  const tree = rootRoute.addChildren([detailRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/app/keycloak'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

/** The hw307 `keycloak` CR: a chart-templated bootstrap CR declaring `singleton`. */
const detail = {
  name: 'keycloak',
  namespace: 'keycloak',
  blueprint: 'bp-keycloak',
  phase: 'Ready',
  bootstrap: true,
  placement: 'singleton',
  regions: ['platform-bootstrap-owned-host'],
} as unknown as Awaited<ReturnType<typeof catalogApi.getApplication>>

const status = {
  name: 'keycloak',
  namespace: 'keycloak',
  phase: 'Ready',
  spec: { placement: { mode: 'singleton' }, regions: ['platform-bootstrap-owned-host'] },
  status: { placement: { mode: 'singleton', primaryRegion: 'platform-bootstrap-owned-host' } },
} as unknown as Awaited<ReturnType<typeof catalogApi.getApplicationStatus>>

/** What GET …/placement answered for `keycloak` on hw307 (2026-11-06). */
const runtime = {
  targets: [
    { region: 'hw-me-east-215-a-rtz-prod', cluster: '9a1f230f320d7ff9', role: 'Primary' },
    { region: 'hw-me-east-215-b-rtz-prod', cluster: '9a1f230f320d7ff9-me-east-215-b-1', role: 'Primary' },
  ],
  derivedFromRuntime: true,
  regionsDeclared: 2,
  regionsObserved: 2,
} as unknown as Awaited<ReturnType<typeof catalogApi.getApplicationPlacement>>

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  // Everything not mocked below answers an empty, well-formed body.
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => {
  vi.restoreAllMocks()
  cleanup()
})

describe('#6192 Overview PLACEMENT is the Topology Pattern, not the stored posture', () => {
  it('renders the runtime-derived pattern on Overview and the SAME text on Topology', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue(detail)
    vi.spyOn(catalogApi, 'getApplicationStatus').mockResolvedValue(status)
    vi.spyOn(catalogApi, 'getApplicationPlacement').mockResolvedValue(runtime)
    renderDetail()

    // Overview: the live truth, not the stored `singleton`.
    const overview = await screen.findByTestId('app-detail-overview-placement')
    await waitFor(() => expect(overview.textContent).toBe('active-active'))
    expect(overview.textContent).not.toContain('singleton')
    expect(screen.getByTestId('app-detail-overview-placement-source').textContent).toBe(
      'runtime-observed',
    )
    const overviewText = overview.textContent

    // Topology: the same screen, one click away — the same value.
    fireEvent.click(screen.getByTestId('app-tab-topology'))
    const topology = await screen.findByTestId('topology-tab-pattern')
    await waitFor(() => expect(topology.textContent).toBe('active-active'))
    expect(topology.textContent).toBe(overviewText)
    expect(screen.getByTestId('topology-tab-placement-source').textContent).toBe('runtime-observed')
  })

  it('falls back to the stored posture — flagged DECLARED — only when nothing was resolved', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue(detail)
    vi.spyOn(catalogApi, 'getApplicationStatus').mockResolvedValue({
      name: 'keycloak',
      namespace: 'keycloak',
    } as unknown as Awaited<ReturnType<typeof catalogApi.getApplicationStatus>>)
    vi.spyOn(catalogApi, 'getApplicationPlacement').mockResolvedValue({
      targets: [],
      derivedFromRuntime: false,
    } as unknown as Awaited<ReturnType<typeof catalogApi.getApplicationPlacement>>)
    renderDetail()

    const overview = await screen.findByTestId('app-detail-overview-placement')
    await waitFor(() => expect(overview.textContent).toBe('singleton'))
    // It is the CR's declaration, and says so — never passed off as observed.
    expect(screen.getByTestId('app-detail-overview-placement-source').textContent).toBe(
      'declared · not observed',
    )
  })

  it('still says "not reported" when neither the resolver nor the detail endpoint has a value', async () => {
    vi.spyOn(catalogApi, 'getApplication').mockResolvedValue({
      ...(detail as object),
      placement: undefined,
    } as unknown as Awaited<ReturnType<typeof catalogApi.getApplication>>)
    vi.spyOn(catalogApi, 'getApplicationStatus').mockResolvedValue({
      name: 'keycloak',
      namespace: 'keycloak',
    } as unknown as Awaited<ReturnType<typeof catalogApi.getApplicationStatus>>)
    vi.spyOn(catalogApi, 'getApplicationPlacement').mockResolvedValue({
      targets: [],
      derivedFromRuntime: false,
    } as unknown as Awaited<ReturnType<typeof catalogApi.getApplicationPlacement>>)
    renderDetail()

    const overview = await screen.findByTestId('app-detail-overview-placement')
    expect(overview.textContent).toBe('not reported')
    expect(overview.className).toContain('italic')
    expect(screen.queryByTestId('app-detail-overview-placement-source')).toBeNull()
  })
})
