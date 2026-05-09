/**
 * AppDetail.test.tsx — lock-in for the per-Application page after the
 * issue #204 founder rework:
 *
 *   • Hero renders the title + status chip on first paint.
 *   • Sections render in canonical order: About → (Connection if
 *     service) → Bundled deps → Tenant → (Configuration if schema) →
 *     Jobs.
 *   • The Jobs section now exposes a tab affordance (founder spec
 *     item #9: "AppDetail → Jobs tab filtered to that app's jobs only")
 *     in addition to its h2 heading.
 *   • Default landing tab is Jobs, rendering <JobsTable> filtered to
 *     this app's componentId (item #8b).
 *   • There is NO legacy [data-testid^="job-row-"] / "job-expansion-"
 *     accordion markup anywhere on the page — anti-regression for the
 *     founder's "NEVER use accordions" rule.
 *   • Back link returns to /provision/$deploymentId.
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
  // QueryClientProvider is required because the EPIC-2 sub-tabs (Settings,
  // Topology, Compliance, Members) use @tanstack/react-query under the
  // hood. Production wraps the entire app in main.tsx; the test must
  // mirror that or AppDetail's children throw "No QueryClient set" and
  // the page never paints — making every sov-* / app-* assertion fail.
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
    expect(await screen.findByTestId('sov-hero')).toBeTruthy()
    expect(screen.getByText('Cilium')).toBeTruthy()
  })

  it('back link points to the dashboard (chroot Sovereign + provision flows share one back target)', async () => {
    // Post-#1052 (chroot Sovereign in-cluster fallback) the AppDetail
    // back link routes to /dashboard for BOTH the wizard /provision
    // tree and the chroot /app tree. The Sovereign Dashboard is the
    // canonical "home" for an installed Sovereign; the wizard's apps
    // grid (/provision/$deploymentId) is the staging surface that
    // disappears once a deployment lands.
    renderDetail('d-1', 'bp-cilium')
    const back = await screen.findByTestId('sov-back-link')
    expect(back.getAttribute('href')).toBe('/dashboard')
  })

  it('renders a not-found fallback for an unknown componentId', async () => {
    renderDetail('d-1', 'bp-does-not-exist')
    expect(await screen.findByTestId('sov-app-not-found')).toBeTruthy()
  })
})

describe('AppDetail — section order', () => {
  it('renders About / (Connection?) / Bundled deps / Tenant / Jobs sections in order', async () => {
    renderDetail('d-1', 'bp-cilium')
    const detail = await screen.findByTestId(/sov-app-detail-/)
    const sections = within(detail).getAllByTestId(/^sov-section-/)
    const ids = sections.map((s) => s.getAttribute('data-testid'))
    const aboutIdx = ids.indexOf('sov-section-about')
    const tenantIdx = ids.indexOf('sov-section-tenant')
    const jobsIdx = ids.indexOf('sov-section-jobs')
    expect(aboutIdx).toBeGreaterThanOrEqual(0)
    expect(tenantIdx).toBeGreaterThan(aboutIdx)
    expect(jobsIdx).toBeGreaterThan(tenantIdx)
  })
})

describe('AppDetail — Jobs tab (founder spec #9 + #8b)', () => {
  it('renders a tab labelled "Jobs"', async () => {
    renderDetail('d-1', 'bp-cilium')
    const tab = await screen.findByTestId('app-jobs-tab')
    expect(tab.getAttribute('role')).toBe('tab')
    expect((tab.textContent ?? '').toLowerCase()).toContain('jobs')
  })

  it('default-selects the Jobs tab so the table renders on first paint', async () => {
    renderDetail('d-1', 'bp-cilium')
    const tab = await screen.findByTestId('app-jobs-tab')
    expect(tab.getAttribute('aria-selected')).toBe('true')
    const panel = await screen.findByTestId('sov-app-tabpanel-jobs')
    // Inside the panel, the JobsTable surface is present.
    expect(within(panel).queryByTestId('jobs-table')).toBeTruthy()
  })

  it('filters the JobsTable to this app — bp-cilium row is visible', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('jobs-table')
    expect(screen.queryByTestId('jobs-table-row-bp-cilium')).toBeTruthy()
  })

  it('does NOT render legacy accordion testids', async () => {
    renderDetail('d-1', 'bp-cilium')
    await screen.findByTestId('sov-section-jobs')
    const rows = document.querySelectorAll('[data-testid^="job-row-"]')
    expect(rows.length).toBe(0)
    const expansions = document.querySelectorAll('[data-testid^="job-expansion-"]')
    expect(expansions.length).toBe(0)
    // sov-job-card-* was the old per-row testid; gone too.
    const cards = document.querySelectorAll('[data-testid^="sov-job-card-"]')
    expect(cards.length).toBe(0)
  })

  it('switching to the Dependencies tab swaps the panel contents', async () => {
    renderDetail('d-1', 'bp-cilium')
    const depTab = await screen.findByTestId('app-dependencies-tab')
    fireEvent.click(depTab)
    expect(depTab.getAttribute('aria-selected')).toBe('true')
    expect(screen.queryByTestId('sov-app-tabpanel-jobs')).toBeNull()
    expect(screen.queryByTestId('sov-app-tabpanel-dependencies')).toBeTruthy()
  })
})

describe('AppDetail — qa-loop iter-1 tab test-id contract (TC-043..048)', () => {
  // Per the qa-loop seam map: every tab BUTTON in the tablist exposes
  // `data-testid="app-<name>-tab"` so Playwright matrix selectors find
  // them on first paint without needing to know the legacy `sov-app-tab-*`
  // alias. The corresponding tab PANEL exposes `app-<name>-tabpanel`.
  const TABS = [
    'jobs',
    'dependencies',
    'topology',
    'resources',
    'compliance',
    'logs',
    'settings',
    'members',
  ] as const

  it.each(TABS)('renders the %s tab button with the seam-map test-id', async (name) => {
    renderDetail('d-1', 'bp-cilium')
    const btn = await screen.findByTestId(`app-${name}-tab`)
    expect(btn.getAttribute('role')).toBe('tab')
  })

  it('clicking the Topology tab reveals the app-topology-tabpanel content', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-topology-tab'))
    expect(await screen.findByTestId('app-topology-tabpanel')).toBeTruthy()
  })

  it('clicking the Settings tab reveals upgrade + uninstall buttons', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-settings-tab'))
    expect(await screen.findByTestId('app-settings-tabpanel')).toBeTruthy()
    expect(screen.getByTestId('settings-tab-upgrade-btn')).toBeTruthy()
    expect(screen.getByTestId('settings-tab-uninstall-btn')).toBeTruthy()
  })

  it('clicking the Members tab reveals the app-members-tabpanel content', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-members-tab'))
    expect(await screen.findByTestId('app-members-tabpanel')).toBeTruthy()
  })

  it('clicking the Resources tab reveals the app-resources-tabpanel content', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-resources-tab'))
    expect(await screen.findByTestId('app-resources-tabpanel')).toBeTruthy()
  })

  it('clicking the Compliance tab reveals the app-compliance-tabpanel content', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-compliance-tab'))
    expect(await screen.findByTestId('app-compliance-tabpanel')).toBeTruthy()
  })

  it('clicking the Logs tab reveals the app-logs-tabpanel content', async () => {
    renderDetail('d-1', 'bp-cilium')
    fireEvent.click(await screen.findByTestId('app-logs-tab'))
    expect(await screen.findByTestId('app-logs-tabpanel')).toBeTruthy()
  })
})
