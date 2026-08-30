/**
 * SovereignSidebar.menu-mapping.test.tsx — EPIC #6723 lane C.
 *
 * The rail renders the MERGED /console-ui/sidebar-entries view:
 *   - enabled top-level entries (Agenity from bp-agenity's consoleUI)
 *     splice in between FLAT_NAV and Settings, keeping the Wave 5.69c
 *     `sov-console-nav-bp-<id>` test id;
 *   - an entry with `parent` renders as a sub-item under that FLAT_NAV
 *     item, inside `sov-console-nav-children-<parent>`;
 *   - an https:// route renders as a plain anchor (it leaves the SPA);
 *   - enabled=false entries are not rendered at all;
 *   - the former static `sandbox` row is gone;
 *   - SIDEBAR_PARENT_OPTIONS (the Settings → Menu dropdown) equals the
 *     server's sidebarParentIDs list, in lockstep with console_ui.go.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import type { SidebarEntry } from '@/lib/console-ui.api'

vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => ({ orgScoped: false, org: null, loading: false }),
}))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'hw310.omani.works' }),
}))

const ENTRIES: SidebarEntry[] = [
  {
    id: 'bp-agenity',
    label: 'Agenity',
    route: '/apps/bp-agenity/dashboard',
    order: 40,
    icon: 'M3 4h18',
    source: 'blueprint',
    enabled: true,
  },
  {
    id: 'app:grafana',
    label: 'Observability',
    route: 'https://grafana.hw310.omani.works/',
    order: 5,
    source: 'application',
    enabled: true,
    parent: 'cloud',
    overridden: true,
  },
  {
    id: 'app:harbor',
    label: 'Registry',
    route: '/app/harbor',
    order: 60,
    source: 'application',
    enabled: true,
    parent: 'apps',
    overridden: true,
  },
  {
    id: 'app:hidden',
    label: 'Hidden',
    route: '/app/hidden',
    order: 50,
    source: 'application',
    enabled: false,
  },
  {
    id: 'app:orphan',
    label: 'Orphan',
    route: '/app/orphan',
    order: 70,
    source: 'application',
    enabled: true,
    parent: 'no-such-parent',
  },
]

vi.mock('@/lib/console-ui.api', () => ({
  getSidebarEntries: async () => ENTRIES,
}))

import { SovereignSidebar } from './SovereignSidebar'
import { SIDEBAR_PARENT_OPTIONS } from './sovereignNav'

function renderSidebar(initialPath = '/apps') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catchAll = createRoute({
    getParentRoute: () => rootRoute,
    path: '/$',
    component: () => <SovereignSidebar sovereignFQDN="hw310.omani.works" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([catchAll]),
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('SovereignSidebar — #6723 mapped menu', () => {
  afterEach(() => cleanup())

  it('renders enabled top-level entries between FLAT_NAV and Settings, hides disabled ones', async () => {
    renderSidebar()
    const agenity = await screen.findByTestId('sov-console-nav-bp-bp-agenity')
    expect(agenity.textContent).toContain('Agenity')
    expect(agenity.getAttribute('data-nav-source')).toBe('blueprint')
    expect(agenity.getAttribute('data-nav-nested')).toBeNull()
    // Ordering: every FLAT_NAV link precedes the top-level mapped entry,
    // and Settings stays pinned last.
    const nav = screen.getByTestId('sov-console-nav')
    const ids = Array.from(nav.querySelectorAll('[data-testid^="sov-console-nav-"]'))
      .map((el) => el.getAttribute('data-testid'))
      .filter((id): id is string => !!id && !id.startsWith('sov-console-nav-children-'))
    expect(ids.indexOf('sov-console-nav-billing')).toBeLessThan(ids.indexOf('sov-console-nav-bp-bp-agenity'))
    expect(ids[ids.length - 1]).toBe('sov-console-nav-settings')
    // Disabled entries never render.
    expect(screen.queryByTestId('sov-console-nav-bp-app:hidden')).toBeNull()
    // The static sandbox row is gone (Agenity is Blueprint-sourced now).
    expect(screen.queryByTestId('sov-console-nav-sandbox')).toBeNull()
  })

  it('nests entries with a parent under that FLAT_NAV item; https routes are plain anchors', async () => {
    renderSidebar()
    const cloudChildren = await screen.findByTestId('sov-console-nav-children-cloud')
    const grafana = within(cloudChildren).getByTestId('sov-console-nav-bp-app:grafana')
    expect(grafana.tagName).toBe('A')
    expect(grafana.getAttribute('href')).toBe('https://grafana.hw310.omani.works/')
    expect(grafana.getAttribute('data-nav-parent')).toBe('cloud')
    expect(grafana.getAttribute('data-nav-nested')).toBe('true')
    expect(grafana.textContent).toContain('Observability')

    const appsChildren = screen.getByTestId('sov-console-nav-children-apps')
    const harbor = within(appsChildren).getByTestId('sov-console-nav-bp-app:harbor')
    expect(harbor.getAttribute('data-nav-parent')).toBe('apps')
    // A console path stays a router link (basepath-aware), not a raw anchor href.
    expect(harbor.getAttribute('href')).toContain('/app/harbor')

    // The sub-menu sits directly after its parent link in DOM order.
    const cloudLink = screen.getByTestId('sov-console-nav-cloud')
    expect(cloudLink.nextElementSibling).toBe(cloudChildren)
  })

  it('degrades an unknown parent to a top-level entry rather than dropping it', async () => {
    renderSidebar()
    const orphan = await screen.findByTestId('sov-console-nav-bp-app:orphan')
    expect(orphan.getAttribute('data-nav-nested')).toBeNull()
    expect(screen.queryByTestId('sov-console-nav-children-no-such-parent')).toBeNull()
  })

  it('lights the mapped entry whose console route matches the location', async () => {
    renderSidebar('/app/harbor')
    const harbor = await screen.findByTestId('sov-console-nav-bp-app:harbor')
    expect(harbor.getAttribute('aria-current')).toBe('page')
    const agenity = screen.getByTestId('sov-console-nav-bp-bp-agenity')
    expect(agenity.getAttribute('aria-current')).toBeNull()
  })

  it('exports the mappable parents in lockstep with the API sidebarParentIDs list', () => {
    // Mirrors `sidebarParentIDs` in
    // products/catalyst/bootstrap/api/internal/handler/console_ui.go —
    // Sovereignty (an anchor into /settings) and Settings are never parents.
    expect(SIDEBAR_PARENT_OPTIONS.map((p) => p.id)).toEqual([
      'dashboard',
      'cloud',
      'apps',
      'catalog',
      'jobs',
      'compliance',
      'users',
      'organizations',
      'billing',
    ])
    expect(SIDEBAR_PARENT_OPTIONS.every((p) => p.label.length > 0)).toBe(true)
  })
})
