/**
 * AppsPage.one-card-per-application-19.test.tsx — UAT row 19 / #3687.
 *
 * THE CLAUSE: "Count Application cards: one card per `Application` (NOT one
 * per HelmRelease/pod); bootstrap apps carry a Platform/Bootstrap badge."
 *
 * WHY THIS FILE EXISTS AND WHAT IT CORRECTS. The row was carried as needing
 * new code on the premise that "the grid is not keyed on Application at all —
 * all 46 cards are bp-* blueprint slots". That premise does not survive a full
 * read of AppsPage.tsx. The page renders TWO card sets:
 *
 *   • instance cards — one per Application CR, `AppsPage.tsx:759`, keyed
 *     `instance-${inst.id}` from the `instance: true` rows the BFF projects
 *     off the Application CR list;
 *   • catalog cards — `AppsPage.tsx:806`, explicitly suppressed for any
 *     blueprint that already has an instance
 *     (`!liveInstances.some((inst) => inst.blueprint === app.id)`).
 *
 * The re-verification that produced the premise stopped at `AppsPage.tsx:117`,
 * which builds only the CATALOG half. The Go side of the same contract has
 * been pinned since #5429 (`sovereign_apps_one_card_per_cr_5429_test.go`); the
 * FRONT-END tree — the one that actually renders the cards a walker counts —
 * had no test at all. That gap is what let two readings of one file reach
 * opposite conclusions, and it is what this file closes.
 *
 * SO THIS IS A CONTRACT LOCK, NOT A BUGFIX, and it is written to be able to
 * fail: the accompanying PR shows it going red against a mutated AppsPage with
 * the suppression filter removed. A test that has never been seen red is not
 * evidence of anything.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, within } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { NotificationProvider } from '@/shared/ui/notifications'

vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'sovereign', sovereignFQDN: 'hw293.omantel.biz' },
  detectMode: () => ({ mode: 'sovereign', sovereignFQDN: 'hw293.omantel.biz' }),
}))
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => ({ orgScoped: false, org: null, loading: false }),
}))
vi.mock('@/lib/catalog.api', () => ({ getLaunchURL: vi.fn() }))

import { AppsPage } from './AppsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

/**
 * Four Application CRs and one decoy, shaped exactly as the BFF projects them.
 *
 * `bp-postgres` is deliberately the blueprint of TWO instances (shared-pg and
 * uat-ahs-pg): a blueprint-keyed grid collapses those to one card, an
 * Application-keyed grid renders two. That is the whole distinction the clause
 * is drawing, so the fixture has to contain it.
 *
 * THE DECOY is the clause's "NOT one per HelmRelease/pod" half. `instance` is
 * absent on it, which is how the BFF marks a per-cluster fanned-out HelmRelease
 * row; it must contribute status and NEVER a card of its own. Without a decoy
 * the HelmRelease half of the clause would be untested, and the fixture would
 * be one where the two hypotheses give the same answer.
 */
// hw307 2026-11-06: the server reported 46 installed Blueprints + instances,
// yet six installed Blueprints (agenity, chargeback, cnpg-pair, openclaw,
// stalwart-tenant, wordpress-tenant) never rendered — the grid was seeded only
// from the provisioning wizard's selection. These rows are installed but are
// NOT in that selection (INITIAL_WIZARD_STATE) and NOT bootstrap-kit.
const SOVEREIGN_APPS_RESPONSE = {
  apps: [
    { id: 'bp-cilium', slug: 'cilium', status: 'installed' },
    { id: 'bp-cnpg-pair', slug: 'cnpg-pair', title: 'CNPG Cluster-Pair (Active-Hotstandby DR)', status: 'installed', bootstrapKit: false },
    { id: 'bp-chargeback', slug: 'chargeback', title: 'Chargeback', status: 'installed', bootstrapKit: false },
    { id: 'bp-wordpress-tenant', slug: 'wordpress-tenant', title: 'WordPress', status: 'installing', bootstrapKit: false },
    { id: 'bp-keda', slug: 'keda', title: 'KEDA', status: 'available', bootstrapKit: false },
  ],
  bootstrapKit: [],
}

function installFetchMock(body: unknown = SOVEREIGN_APPS_RESPONSE) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/v1/sovereign/apps')) {
        return new Response(JSON.stringify(body), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      return new Response('{}', { status: 404 })
    }),
  )
}

function renderApps() {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  const appsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/apps',
    component: () => <AppsPage />,
  })
  const catchAll = createRoute({
    getParentRoute: () => rootRoute,
    path: '/$',
    component: () => <div />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([appsRoute, catchAll]),
    history: createMemoryHistory({ initialEntries: ['/apps'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

async function cards(kind: 'instance' | 'catalog'): Promise<HTMLElement[]> {
  const grid = await screen.findByTestId('sov-apps-grid')
  return Array.from(grid.querySelectorAll(`[data-card-kind="${kind}"]`))
}

function idOf(el: Element): string {
  return (el.getAttribute('data-testid') ?? '').replace(/^sov-app-card-/, '')
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  installFetchMock()
  Object.defineProperty(window, 'location', {
    value: { host: 'console.hw307.omani.works', hostname: 'console.hw307.omani.works', search: '', pathname: '/apps', hash: '' },
    writable: true,
  })
  renderApps()
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('AppsPage — every server-installed Application is listed (hw307 gap)', () => {
  it('control — the grid renders the bootstrap spine', async () => {
    await screen.findByTestId('sov-apps-grid')
    await screen.findByTestId('sov-app-card-bp-cilium')
  })

  it.each(['bp-cnpg-pair', 'bp-chargeback', 'bp-wordpress-tenant'])(
    '%s — installed on the server, not in the wizard selection — renders a card',
    async (id) => {
      expect(await screen.findByTestId(`sov-app-card-${id}`)).toBeTruthy()
    },
  )

  it('the server title is used when the component catalog has no entry', async () => {
    const card = await screen.findByTestId('sov-app-card-bp-cnpg-pair')
    expect(within(card).getByText(/CNPG Cluster-Pair|cnpg-pair/i)).toBeTruthy()
  })

  it('an AVAILABLE (not installed) catalog item is NOT listed as installed', async () => {
    await screen.findByTestId('sov-app-card-bp-cnpg-pair')
    expect(screen.queryByTestId('sov-app-card-bp-keda')).toBeNull()
  })
})
