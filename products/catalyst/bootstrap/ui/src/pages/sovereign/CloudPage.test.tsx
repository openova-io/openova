/**
 * CloudPage.test.tsx — shell wiring lock-in for the Sovereign Cloud
 * surface (issue #350 — IA restructure).
 *
 * Coverage:
 *   1. Header renders with the canonical title ("Cloud").
 *   2. The shell renders the in-page View toggle (Graph | List) as a
 *      single segmented control. The legacy in-page category tab strip
 *      remains absent.
 *   3. The shell exposes a Fullscreen toggle button.
 *   4. The shell renders the dispatched body (graph or list) when no
 *      contentOverride is supplied. With contentOverride, the override
 *      wins (used by these tests to keep the heavyweight body out).
 *   5. PortalShell wires (sidebar present).
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { CloudPage } from './CloudPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

const EMPTY_TREE = {
  cloud: [],
  topology: { pattern: 'solo' as const, regions: [] },
  storage: { pvcs: [], buckets: [], volumes: [] },
}

function renderShell(deploymentId: string, suffix: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const cloudRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/cloud',
    component: () => (
      <CloudPage
        disableStream
        contentOverride={<div data-testid="cloud-content-stub">{suffix}</div>}
        initialDataOverride={EMPTY_TREE}
        deploymentsOverride={[]}
      />
    ),
  })
  // Register every legal sub-route as a no-op child so TanStack
  // Router resolves /cloud/{architecture,...} to the parent shell +
  // an empty child, mirroring production routing without mounting
  // the heavyweight sub-page components in the shell test.
  const subRoute = (s: string) =>
    createRoute({
      getParentRoute: () => cloudRoute,
      path: `/${s}`,
      component: () => <div data-testid={`cloud-sub-${s}`}>{s}</div>,
    })
  const tree = rootRoute.addChildren([
    cloudRoute.addChildren([
      subRoute('architecture'),
      subRoute('compute'),
      subRoute('storage'),
      subRoute('network'),
    ]),
  ])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/cloud/${suffix}`],
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
  globalThis.fetch = (() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ events: [], state: undefined, done: false }),
    } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CloudPage — shell', () => {
  it('renders the Cloud title', async () => {
    renderShell('d-1', 'architecture')
    const title = await screen.findByTestId('cloud-title')
    expect(title.textContent).toBe('Cloud')
  })

  it('mounts inside the PortalShell (sidebar present)', async () => {
    renderShell('d-1', 'architecture')
    expect(await screen.findByTestId('sov-portal-shell')).toBeTruthy()
    expect(screen.getByTestId('admin-sidebar')).toBeTruthy()
  })

  it('renders the Outlet content', async () => {
    renderShell('d-1', 'architecture')
    expect(await screen.findByTestId('cloud-content-stub')).toBeTruthy()
  })
})

describe('CloudPage — view toggle and fullscreen', () => {
  it('does NOT render the legacy infrastructure category tab strip', async () => {
    renderShell('d-1', 'architecture')
    await screen.findByTestId('cloud-title')
    expect(screen.queryByTestId('infrastructure-tabs')).toBeNull()
    expect(screen.queryByTestId('cloud-tabs')).toBeNull()
  })

  it('renders the Graph | List view toggle (segmented control)', async () => {
    renderShell('d-1', 'architecture')
    await screen.findByTestId('cloud-title')
    const toggle = screen.getByTestId('cloud-page-view-toggle')
    expect(toggle.getAttribute('role')).toBe('tablist')
    const graph = screen.getByTestId('cloud-page-view-graph')
    const list = screen.getByTestId('cloud-page-view-list')
    expect(graph.tagName).toBe('BUTTON')
    expect(list.tagName).toBe('BUTTON')
    // Default = graph.
    expect(graph.getAttribute('aria-selected')).toBe('true')
    expect(list.getAttribute('aria-selected')).toBe('false')
  })

  it('renders the Fullscreen toggle button with aria-pressed reflecting state', async () => {
    renderShell('d-1', 'architecture')
    const fs = await screen.findByTestId('cloud-page-fullscreen-toggle')
    expect(fs.tagName).toBe('BUTTON')
    expect(fs.getAttribute('aria-pressed')).toBe('false')
  })
})
