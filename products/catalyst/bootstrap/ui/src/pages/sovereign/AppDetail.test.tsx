/**
 * AppDetail.test.tsx — lock-in for the per-Application page.
 *
 * Post qa-loop iter-12 Fix #51 (target-state shape):
 *
 *   • Hero renders the title + status chip on first paint.
 *   • Default landing tab is `overview` (was: `jobs`). The matrix-
 *     canonical tab strip ships in this order, with two extra wizard-
 *     context tabs appended after Members:
 *       Overview · Topology · Resources · Compliance · Logs · Settings
 *       · Members · Jobs · Dependencies
 *   • Tab BUTTONS expose `data-testid="app-tab-{name}"` (matrix-
 *     canonical, TC-106). The legacy `app-{name}-tab` ids are mirrored
 *     via `data-testid-alt` for any external selector still pointing
 *     at them.
 *   • There is NO legacy [data-testid^="job-row-"] / "job-expansion-"
 *     accordion markup — anti-regression.
 *   • Back link returns to /dashboard for both wizard provision flow
 *     and chroot Sovereign console.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import { render, screen, cleanup, within, fireEvent } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AppDetail } from './AppDetail'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

function renderDetail(deploymentId: string, componentId: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const detailRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/app/$componentId',
    component: () => <AppDetail disableStream />,
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
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => <div data-testid="wizard-target" />,
  })
  const tree = rootRoute.addChildren([detailRoute, homeRoute, jobDetailRoute, wizardRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({
      initialEntries: [`/provision/${deploymentId}/app/${componentId}`],
    }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
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

describe('AppDetail — hero', () => {
  it('renders the hero with the Application title', async () => {
    renderDetail('d-1', 'bp-cilium')
    const hero = await screen.findByTestId('sov-hero')
    // The hero renders the title as "— Cilium" (em-dash subtitle), so an
    // exact getByText('Cilium') misses; assert on the hero's text content.
    expect(hero.textContent).toContain('Cilium')
  })

  it('back link points to the dashboard', async () => {
    renderDetail('d-1', 'bp-cilium')
    const back = await screen.findByTestId('sov-back-link')
    expect(back.getAttribute('href')).toBe('/dashboard')
  })

  it('renders a not-found fallback for an unknown componentId', async () => {
    renderDetail('d-1', 'bp-does-not-exist')
    expect(await screen.findByTestId('sov-app-not-found')).toBeTruthy()
  })
})

describe('AppDetail — Overview default tab + sections', () => {
  it('default-selects the Overview tab on first paint', async () => {
    renderDetail('d-1', 'bp-cilium')
    const tab = await screen.findByTestId('app-tab-overview')
    expect(tab.getAttribute('aria-selected')).toBe('true')
    expect(await screen.findByTestId('app-tab-overview-panel')).toBeTruthy()
  })

  it('renders About / Organization sections on the Overview tab', async () => {
    renderDetail('d-1', 'bp-cilium')
    const panel = await screen.findByTestId('app-tab-overview-panel')
    expect(within(panel).getByTestId('sov-section-about')).toBeTruthy()
    expect(within(panel).getByTestId('sov-section-organization')).toBeTruthy()
  })

  it('does NOT render legacy accordion testids', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('app-tab-overview-panel')
    const rows = document.querySelectorAll('[data-testid^="job-row-"]')
    expect(rows.length).toBe(0)
    const expansions = document.querySelectorAll('[data-testid^="job-expansion-"]')
    expect(expansions.length).toBe(0)
    const cards = document.querySelectorAll('[data-testid^="sov-job-card-"]')
    expect(cards.length).toBe(0)
  })
})

describe('AppDetail — matrix-canonical tab seam (TC-036 + TC-106)', () => {
  // The matrix asserts the tab BUTTONS expose `data-testid="app-tab-{name}"`
  // (NOT the legacy `app-{name}-tab`) so Playwright matrix selectors
  // find them on first paint without a tab-click navigation.
  const TABS = [
    'overview',
    'topology',
    'resources',
    'compliance',
    'logs',
    'settings',
    'members',
    'jobs',
    'dependencies',
  ] as const

  it.each(TABS)('renders the %s tab button with the matrix-canonical test-id', async (name) => {
    renderDetail('d-1', 'bp-cilium')
    const btn = await screen.findByTestId(`app-tab-${name}`)
    expect(btn.getAttribute('role')).toBe('tab')
  })

  it('renders the tabs in the matrix-canonical order (overview first)', async () => {
    renderDetail('d-1', 'bp-cilium')
    const tablist = await screen.findByTestId('app-detail-tablist')
    const tabs = within(tablist).getAllByRole('tab')
    const ids = tabs.map((t) => t.getAttribute('data-testid'))
    expect(ids).toEqual([
      'app-tab-overview',
      'app-tab-topology',
      'app-tab-resources',
      'app-tab-compliance',
      'app-tab-logs',
      'app-tab-settings',
      'app-tab-members',
      // `endpoints` ships in the strip after Members (EndpointsTab) — the
      // expected list had drifted from the rendered tablist. Kept in
      // canonical position so the order assertion matches reality.
      'app-tab-endpoints',
      'app-tab-jobs',
      'app-tab-dependencies',
    ])
  })

  it('clicking the Topology tab reveals the topology panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-tab-topology'))
    expect(await screen.findByTestId('app-tab-topology-panel')).toBeTruthy()
  })

  it('clicking the Settings tab reveals upgrade + uninstall buttons', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-tab-settings'))
    expect(await screen.findByTestId('app-tab-settings-panel')).toBeTruthy()
    expect(screen.getByTestId('settings-tab-upgrade-btn')).toBeTruthy()
    expect(screen.getByTestId('settings-tab-uninstall-btn')).toBeTruthy()
  })

  it('clicking the Members tab reveals the members panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-tab-members'))
    expect(await screen.findByTestId('app-tab-members-panel')).toBeTruthy()
  })

  it('clicking the Resources tab reveals the resources panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-tab-resources'))
    expect(await screen.findByTestId('app-tab-resources-panel')).toBeTruthy()
  })

  it('clicking the Compliance tab reveals the compliance panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-tab-compliance'))
    expect(await screen.findByTestId('app-tab-compliance-panel')).toBeTruthy()
  })

  it('clicking the Logs tab reveals the logs panel', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-tab-logs'))
    expect(await screen.findByTestId('app-tab-logs-panel')).toBeTruthy()
  })

  it('clicking the Jobs tab swaps the panel to the JobsTable', async () => {
    renderDetail('d-1', 'bp-cilium')
    const jobsTab = await screen.findByTestId('app-tab-jobs')
    fireEvent.click(jobsTab)
    expect(jobsTab.getAttribute('aria-selected')).toBe('true')
    const panel = await screen.findByTestId('app-tab-jobs-panel')
    expect(within(panel).queryByTestId('jobs-table')).toBeTruthy()
  })

  it('clicking the Dependencies tab swaps the panel contents', async () => {
    renderDetail('d-1', 'bp-cilium')
    const depTab = await screen.findByTestId('app-tab-dependencies')
    fireEvent.click(depTab)
    expect(depTab.getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByTestId('app-tab-overview-panel')).toBeNull()
    expect(screen.queryByTestId('app-tab-dependencies-panel')).toBeTruthy()
  })
})

/**
 * #3090 — AppDetail is the per-INSTANCE page. The class-level instances
 * list + "+ New instance" button moved to the CLASS page (CatalogDetail).
 * The instance page must NOT render them; it must still surface the
 * "Open" button (silent-SSO Launch) when the Application has an external
 * URL.
 */
describe('AppDetail — #3090 instance page (no class content; Open button)', () => {
  // URL-aware fetch: the application GET returns a body carrying
  // externalURL so the Open button renders; everything else (the SSE
  // history replay) returns the empty envelope.
  function installAppFetch(externalURL: string | undefined) {
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/applications/')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              name: 'cilium',
              namespace: 'kube-system',
              blueprint: 'bp-cilium',
              phase: 'Ready',
              conditions: [],
              ...(externalURL ? { externalURL } : {}),
            }),
        } as unknown as Response)
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ events: [], state: undefined, done: false }),
      } as unknown as Response)
    }) as typeof fetch
  }

  it('does NOT render the class instances-list or "+ New instance" button', async () => {
    installAppFetch(undefined)
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('sov-hero')
    // The class-page widgets must be absent on the instance page.
    expect(screen.queryByTestId('btn-new-instance')).toBeNull()
    expect(screen.queryByTestId('sov-section-instances')).toBeNull()
    expect(screen.queryByTestId('sov-instances-table')).toBeNull()
    expect(screen.queryByTestId('dialog-new-instance')).toBeNull()
  })

  it('renders the "Open" button on the Overview tab when an external URL is present', async () => {
    installAppFetch('https://gitea.t99.omani.works')
    renderDetail('d-1', 'bp-cilium')
    // Overview tab is the default landing tab.
    const overviewPanel = await screen.findByTestId('app-tab-overview-panel')
    // #3150 — the launch button now renders TWICE (the prominent hero CTA
    // in the header AND the inline button next to the Overview external-URL
    // row), both carrying data-testid="btn-launch-app". Scope to the
    // Overview panel so this Overview-tab assertion targets exactly one.
    const openBtn = await within(overviewPanel).findByTestId('btn-launch-app')
    expect(openBtn).toBeTruthy()
    // Founder relabel "Launch →" → "Open".
    expect(openBtn.textContent).toContain('Open')
    expect(openBtn.textContent).not.toContain('Launch')
    // The external-URL row is the gate that surfaces the button.
    expect(screen.getByTestId('app-detail-overview-external-url')).toBeTruthy()
    // The prominent hero CTA also renders, reading "Open <App>" (#3150
    // — founder demanded an unmistakable, app-named header button).
    const heroLaunch = await screen.findByTestId('hero-launch')
    const heroBtn = within(heroLaunch).getByTestId('btn-launch-app')
    expect(heroBtn.textContent).toContain('Open')
  })

  it('hides the "Open" button when the Application has no external URL', async () => {
    installAppFetch(undefined)
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('app-tab-overview-panel')
    expect(screen.queryByTestId('btn-launch-app')).toBeNull()
  })

  // #3150 — a bootstrap-kit app (HelmRelease, no Application CR → no uid,
  // bootstrap:true) must still drive silent SSO: clicking Open calls the
  // launch-url endpoint keyed on the blueprint/release NAME (componentId)
  // and opens the returned OIDC-init URL — NOT the plain externalURL.
  it('bootstrap app with no uid keys the launch-url on the blueprint name (#3150)', async () => {
    const launchURLCalls: string[] = []
    const opened: string[] = []
    const origOpen = window.open
    window.open = ((u?: string | URL) => {
      opened.push(typeof u === 'string' ? u : String(u))
      return null
    }) as typeof window.open

    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/launch-url')) {
        launchURLCalls.push(url)
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              url: 'https://grafana.t99.omani.works/login/generic_oauth',
              expiresAt: '2030-01-01T00:00:00Z',
              endpoint: 'ui',
            }),
        } as unknown as Response)
      }
      if (url.includes('/applications/')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () =>
            Promise.resolve({
              name: 'grafana',
              namespace: 'grafana',
              blueprint: 'bp-grafana',
              phase: 'Ready',
              conditions: [],
              bootstrap: true, // HR-backed → no Application CR uid
              externalURL: 'https://grafana.t99.omani.works',
            }),
        } as unknown as Response)
      }
      // /catalog/{bp}/instances fallback → empty (no CR instances).
      if (url.includes('/instances')) {
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ items: [] }),
        } as unknown as Response)
      }
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ events: [], state: undefined, done: false }),
      } as unknown as Response)
    }) as typeof fetch

    try {
      renderDetail('d-1', 'bp-grafana')
      const overviewPanel = await screen.findByTestId('app-tab-overview-panel')
      // #3150 — two launch buttons now render (hero CTA + inline). Either
      // drives the same silent-SSO path; scope to the Overview-tab inline
      // one so this assertion targets exactly one button.
      const openBtn = await within(overviewPanel).findByTestId('btn-launch-app')
      fireEvent.click(openBtn)
      // Allow the async getLaunchURL + window.open microtasks to settle.
      await new Promise((r) => setTimeout(r, 0))
      // The launch-url was called keyed on the blueprint name (bp-grafana),
      // NOT skipped — proving the bootstrap app now drives silent SSO.
      expect(launchURLCalls.length).toBe(1)
      expect(launchURLCalls[0]).toContain('/apps/bp-grafana/launch-url')
      // The opened tab is the OIDC-init URL, not the plain externalURL.
      expect(opened).toContain('https://grafana.t99.omani.works/login/generic_oauth')
    } finally {
      window.open = origOpen
    }
  })
})
