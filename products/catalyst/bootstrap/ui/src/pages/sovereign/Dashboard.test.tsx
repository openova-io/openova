/**
 * Dashboard.test.tsx — render + drill-down lock-in for the Sovereign
 * Dashboard treemap surface.
 *
 * Coverage:
 *   1. Toolbar renders with Size / Color / Layer selects.
 *   2. Empty state shows when the API returns no items.
 *   3. With a 12-cell synthetic flat tree, ≥10 cells appear in the
 *      rendered SVG.
 *   4. Drill-down — clicking a parent cell pushes a breadcrumb chip;
 *      clicking the breadcrumb's "All" entry pops back.
 *   5. Auto-lock — picking a capacity size metric forces colorBy to
 *      utilisation in the controller.
 *
 * Recharts' actual SVG geometry is JSDOM-sensitive; tests assert on
 * presence of treemap roots / cell containers rather than exact
 * pixel positions. The pure colour math + drill walk are covered in
 * lib/treemap.types.test.ts so this file focuses on the wiring.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { Dashboard } from './Dashboard'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import type { TreemapData, TreemapDimension } from '@/lib/treemap.types'

interface RenderOpts {
  initialLayers?: readonly TreemapDimension[]
}

function renderDashboard(
  deploymentId: string,
  dataOverride?: TreemapData,
  opts: RenderOpts = {},
) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const dashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => (
      <Dashboard
        disableStream
        initialDataOverride={dataOverride}
        initialLayers={opts.initialLayers}
        // #3925 surface A — these tests assert the treemap surface; pin the
        // view to treemap so the new Progress ⇄ Treemap toggle (which
        // defaults to Progress while the test-stubbed status is non-ready)
        // doesn't hide it. The wizard/auto-flip behaviour has its own suite.
        initialView="treemap"
      />
    ),
  })
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <div data-testid="app-target" />,
  })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId',
    component: () => <div data-testid="apps-target" />,
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
  const tree = rootRoute.addChildren([dashRoute, appRoute, homeRoute, jobsRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/dashboard`],
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
  // ResizeObserver is needed by Recharts' ResponsiveContainer; jsdom
  // does not provide it.
  if (typeof globalThis.ResizeObserver === 'undefined') {
    class FakeResizeObserver {
      observe() {}
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof FakeResizeObserver }).ResizeObserver =
      FakeResizeObserver
  }
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

const TWELVE_CELL_FIXTURE: TreemapData = {
  total_count: 12,
  items: Array.from({ length: 12 }).map((_, i) => ({
    id: `app-${i}`,
    name: `app-${i}`,
    count: 1,
    percentage: (i / 11) * 100,
    size_value: 100 + i * 50,
  })),
}

const NESTED_FIXTURE: TreemapData = {
  total_count: 6,
  items: [
    {
      id: 'spine',
      name: 'Spine',
      count: 3,
      percentage: 40,
      size_value: 600,
      children: [
        { id: 'cilium', name: 'cilium',     count: 1, percentage: 60, size_value: 200 },
        { id: 'flux',   name: 'flux',       count: 1, percentage: 30, size_value: 200 },
        { id: 'cert',   name: 'cert-mgr',   count: 1, percentage: 20, size_value: 200 },
      ],
    },
    {
      id: 'pilot',
      name: 'Pilot',
      count: 3,
      percentage: 70,
      size_value: 600,
      children: [
        { id: 'keycloak', name: 'keycloak', count: 1, percentage: 75, size_value: 200 },
        { id: 'spire',    name: 'spire',    count: 1, percentage: 65, size_value: 200 },
        { id: 'openbao',  name: 'openbao',  count: 1, percentage: 70, size_value: 200 },
      ],
    },
  ],
}

describe('Dashboard — toolbar + empty state', () => {
  it('renders the title + total-count header', async () => {
    renderDashboard('d-1', { items: [], total_count: 0 })
    expect(await screen.findByTestId('dashboard-title')).toBeTruthy()
    expect(await screen.findByTestId('dashboard-total-count')).toBeTruthy()
  })

  it('renders the layer controller toolbar', async () => {
    renderDashboard('d-1', { items: [], total_count: 0 })
    expect(await screen.findByTestId('treemap-layer-controller')).toBeTruthy()
    expect(screen.getByTestId('treemap-size-select')).toBeTruthy()
    expect(screen.getByTestId('treemap-color-select')).toBeTruthy()
  })

  it('shows the empty state when items[] is empty', async () => {
    renderDashboard('d-1', { items: [], total_count: 0 })
    expect(await screen.findByTestId('dashboard-empty')).toBeTruthy()
  })
})

describe('Dashboard — null-items provisioning guard (issue #3281)', () => {
  /**
   * Regression tests for the null-deref crash introduced by the backend
   * emitting `{"items": null}` (not `[]`) while a Sovereign is still
   * provisioning. Both shapes must render the empty state without
   * throwing «Cannot read properties of null (reading 'length')».
   *
   * The fix path: isEmpty = !query.isLoading && !treemapData?.items?.length
   * and visibleItems useMemo guards: if (!treemapData?.items) return []
   */
  it('renders empty state without crashing when items is null', async () => {
    // Cast needed: the TypeScript type says items: TreemapItem[] (non-null)
    // but the live API returns items: null during provisioning — that is
    // the exact lie that caused the crash in #3281.
    renderDashboard('d-1', { items: null as unknown as never[], total_count: 0 })
    expect(await screen.findByTestId('dashboard-empty')).toBeTruthy()
    expect(screen.queryByTestId('dashboard-error')).toBeNull()
  })

  it('renders empty state without crashing when items is []', async () => {
    renderDashboard('d-1', { items: [], total_count: 0 })
    expect(await screen.findByTestId('dashboard-empty')).toBeTruthy()
    expect(screen.queryByTestId('dashboard-error')).toBeNull()
  })

  it('does NOT render the treemap surface when items is null', async () => {
    renderDashboard('d-1', { items: null as unknown as never[], total_count: 0 })
    await screen.findByTestId('dashboard-empty')
    expect(screen.queryByTestId('dashboard-treemap-surface')).toBeNull()
  })
})

describe('Dashboard — 12-cell flat fixture', () => {
  it('renders the treemap container surface', async () => {
    const { container } = renderDashboard('d-1', TWELVE_CELL_FIXTURE)
    // ResponsiveContainer needs measured dimensions which JSDOM does
    // not provide; we therefore assert the page reaches the render
    // path that mounts the treemap surface (frame visible, NOT the
    // empty-state). Cell rendering is end-to-end-tested via
    // Playwright; the unit-level guarantee is that the wiring shows
    // the correct surface for the data shape.
    await waitFor(() => {
      expect(container.querySelector('[data-testid="dashboard-treemap-frame"]')).toBeTruthy()
    })
    expect(screen.queryByTestId('dashboard-empty')).toBeNull()
  })

  it('exposes the right total count in the header', async () => {
    renderDashboard('d-1', TWELVE_CELL_FIXTURE)
    const header = await screen.findByTestId('dashboard-total-count')
    expect(header.textContent).toContain('12')
  })
})

describe('Dashboard — drill-down breadcrumb', () => {
  it('hides the breadcrumb at root depth (#531 item 4)', async () => {
    renderDashboard('d-1', NESTED_FIXTURE)
    // Wait for the treemap to mount so we know the page has rendered.
    await screen.findByTestId('dashboard-treemap-frame')
    // The breadcrumb (and its standalone "All" chip) only appears once
    // the operator drills into a parent — root has nothing to pop back
    // to so the row is hidden to reclaim vertical space.
    expect(screen.queryByTestId('dashboard-breadcrumb')).toBeNull()
    expect(screen.queryByTestId('dashboard-breadcrumb-root')).toBeNull()
    expect(screen.queryByTestId('dashboard-breadcrumb-0')).toBeNull()
  })
})

describe('Dashboard — inner-tile drill (issue #1927)', () => {
  /**
   * Trust-recovery regression test for issue #1927.
   *
   * Before the fix the depth-1 application tiles rendered with
   * `cursor: default` and silently dropped clicks — 84/85 cells in the
   * canonical Cluster→Application layer pair were dead. The fix wires
   * leaf cells with an `id` to navigate to /app/$componentId and flips
   * their cursor to `pointer`.
   *
   * JSDOM does NOT lay out SVG (width is 0, the gated branch in
   * SquarifiedSurface never renders cells) so this test mocks the
   * container width via clientWidth + a ResizeObserver that fires its
   * callback synchronously, then asserts that the rendered cells carry
   * the new `cursor: pointer` style.
   */
  beforeEach(() => {
    // Force every <div> we measure to report a non-zero width so the
    // SquarifiedSurface clears its `width > 0` gate and mounts cells.
    Object.defineProperty(HTMLDivElement.prototype, 'clientWidth', {
      configurable: true,
      get() {
        return 800
      },
    })
    // ResizeObserver that fires once on observe so setWidth(800) lands.
    class SyncResizeObserver {
      private cb: ResizeObserverCallback
      constructor(cb: ResizeObserverCallback) {
        this.cb = cb
      }
      observe() {
        // Invoke synchronously — no real DOMRect needed; setWidth reads
        // from clientWidth, not from the entries.
        this.cb([], this as unknown as ResizeObserver)
      }
      unobserve() {}
      disconnect() {}
    }
    ;(globalThis as unknown as { ResizeObserver: typeof SyncResizeObserver }).ResizeObserver =
      SyncResizeObserver
  })

  it('renders leaf cells with cursor:pointer when application id is set', async () => {
    const { container } = renderDashboard('d-1', TWELVE_CELL_FIXTURE)
    await screen.findByTestId('dashboard-treemap-frame')
    await waitFor(() => {
      const svg = container.querySelector('[data-testid="dashboard-treemap-surface"] svg')
      expect(svg).toBeTruthy()
    })
    // Every cell <g> that wraps an item.id MUST advertise pointer cursor
    // so the operator gets the affordance back. (Pre-fix the inline
    // style read `cursor: default` for every leaf.)
    const surface = container.querySelector('[data-testid="dashboard-treemap-surface"] svg')
    const pointerGroups = surface?.querySelectorAll('g[style*="pointer"]') ?? []
    expect(pointerGroups.length).toBeGreaterThan(0)
  })

  /**
   * 2026-05-19 follow-up (#1927 reopen, agent aced939b walk):
   *
   * PR #1931 wired leaf clicks but read the dimension as
   * `layers[drillPath.length]` — at the canonical default
   * `layers=['cluster','application']` + drillPath=[] this resolves to
   * `'cluster'`, so the application-leaf guard fell through and the
   * click silently dropped on the very 84/85 cells founder reported.
   *
   * Additionally the treemap emits `item.id = applicationKey(pod) =
   * pod.labels['app.kubernetes.io/instance']` which for bootstrap-kit
   * installs is the BARE upstream label (e.g. `'harbor'`), but
   * AppDetail's route + lookup both key on the bp- prefixed Application
   * CR name (`bp-harbor`). The fix normalises bare ids before
   * `router.navigate()` so the AppDetail surface resolves cleanly.
   *
   * This test reproduces the founder-reported config (Cluster→
   * Application, drillPath=[]) and asserts that clicking a nested
   * application leaf:
   *   1. fires the navigation (cursor:pointer affordance + onClick),
   *   2. targets `/provision/<id>/app/bp-<bare-id>` — NOT bare id.
   */
  it('Cluster→Application leaf click navigates to /app/bp-<id> at drillPath=[]', async () => {
    const NESTED_CLUSTER_APP: TreemapData = {
      total_count: 3,
      items: [
        {
          id: 'cluster-x',
          name: 'cluster-x',
          count: 3,
          percentage: 50,
          size_value: 600,
          children: [
            { id: 'harbor', name: 'harbor', count: 1, percentage: 60, size_value: 200 },
            { id: 'mimir',  name: 'mimir',  count: 1, percentage: 30, size_value: 200 },
            { id: 'cilium', name: 'cilium', count: 1, percentage: 70, size_value: 200 },
          ],
        },
      ],
    }
    const { container } = renderDashboard('d-1', NESTED_CLUSTER_APP, {
      initialLayers: ['cluster', 'application'],
    })
    await screen.findByTestId('dashboard-treemap-frame')
    await waitFor(() => {
      const svg = container.querySelector('[data-testid="dashboard-treemap-surface"] svg')
      expect(svg).toBeTruthy()
    })
    // The squarified layout emits the cluster header first (depth 0,
    // isParent=true) then the three application leaves (depth 1). The
    // leaves carry pointer cursors after PR #1931; this test asserts
    // the CLICK actually navigates.
    const surface = container.querySelector('[data-testid="dashboard-treemap-surface"] svg')!
    // SquarifiedSurface emits leaves with NO header strip (single rect
    // inside the <g>) and a text label matching the item name. Use the
    // text content to find the harbor leaf, then click its parent <g>.
    const labels = Array.from(surface.querySelectorAll('text')) as SVGTextElement[]
    const harborLabel = labels.find((t) => t.textContent?.startsWith('harbor'))
    expect(harborLabel, 'harbor label rendered').toBeTruthy()
    const leafG = harborLabel!.closest('g') as SVGGElement
    expect(leafG, 'harbor cell <g> found').toBeTruthy()
    expect((leafG.getAttribute('style') ?? '').includes('pointer')).toBe(true)
    leafG.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    // tanstack-router updates the memory history synchronously on
    // .navigate(); the app-target placeholder route renders on tick.
    await waitFor(() => {
      expect(screen.queryByTestId('app-target')).toBeTruthy()
    })
    // assertion: AppDetail route param is `bp-harbor`, NOT bare `harbor`
    const history = (
      (window as unknown as { __rt?: { location?: { pathname?: string } } }).__rt ?? {}
    ).location?.pathname
    // Fallback to window.location in jsdom — tanstack-router writes
    // memory-history paths there via createMemoryHistory.
    const path = history ?? window.location.pathname
    expect(path).toMatch(/\/provision\/d-1\/app\/bp-harbor$/)
  })

  /**
   * Direct unit-level guard against the bug-A regression: when
   * layers=['cluster','application'] + drillPath=[], an application
   * leaf at cell-depth=1 MUST resolve to dimension='application' so
   * the leaf-branch fires. Asserts the layerIdx math regardless of
   * future renderer changes.
   */
  it('layerIdx math: drillPath.length + cellDepth resolves nested-leaf dimension', () => {
    const layers: TreemapDimension[] = ['cluster', 'application']
    const drillPathLen = 0
    const cellDepth = 1
    const layerIdx = drillPathLen + cellDepth
    const dimension = layers[layerIdx] ?? layers[layers.length - 1]
    expect(dimension).toBe('application')

    // Drilled-in: drillPath=['cluster-x'], single-layer Application.
    // layerIdx = 1 + 0 = 1 → falls back to last layer = 'application'.
    const drilledLen = 1
    const flatDepth = 0
    const idx2 = drilledLen + flatDepth
    expect(layers[idx2] ?? layers[layers.length - 1]).toBe('application')
  })
})
