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
const SOVEREIGN_APPS_RESPONSE = {
  apps: [
    { id: 'shared-pg', slug: 'postgres', blueprint: 'bp-postgres', status: 'installed', instance: true, environment: 'prod', topology: 'active-passive', contextCount: 3, bootstrapKit: false, org: 'uatco' },
    { id: 'uat-ahs-pg', slug: 'postgres', blueprint: 'bp-postgres', status: 'installed', instance: true, environment: 'prod', topology: 'active-hot-standby', contextCount: 1, bootstrapKit: false, org: 'uatco' },
    { id: 'spine-gitea', slug: 'gitea', blueprint: 'bp-gitea', status: 'installed', instance: true, environment: 'prod', topology: 'singleton', bootstrapKit: true },
    { id: 'uatco-agenity', slug: 'agenity', blueprint: 'bp-agenity', status: 'installed', instance: true, environment: 'prod', topology: 'singleton', bootstrapKit: false, org: 'uatco' },
    // The decoy — a fanned-out HelmRelease row, not an Application.
    { id: 'bp-cilium', slug: 'cilium', status: 'installed' },
  ],
  bootstrapKit: [],
}

const APPLICATION_CR_IDS = ['shared-pg', 'uat-ahs-pg', 'spine-gitea', 'uatco-agenity']

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
    value: { host: 'console.hw293.omantel.biz', hostname: 'console.hw293.omantel.biz', search: '', pathname: '/apps', hash: '' },
    writable: true,
  })
  renderApps()
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.clearAllMocks()
})

describe('AppsPage — row 19: the grid renders one card per Application CR', () => {
  it('control — the grid renders and BOTH collections are present', async () => {
    // Every assertion below is a count, and a count is satisfied by an empty
    // grid, a crashed page, or a query that never resolved. So the shape of
    // the page is established FIRST: if this fails, nothing after it means
    // anything.
    const grid = await screen.findByTestId('sov-apps-grid')
    await screen.findByTestId('sov-app-card-shared-pg')
    expect((await cards('instance')).length).toBeGreaterThan(0)
    expect((await cards('catalog')).length).toBeGreaterThan(0)
    expect(within(grid).getAllByTestId(/^sov-app-card-/).length).toBeGreaterThan(4)
  })

  it('exactly one instance card per Application CR — no more, no fewer', async () => {
    await screen.findByTestId('sov-app-card-shared-pg')
    const ids = (await cards('instance')).map(idOf).sort()
    expect(ids).toEqual([...APPLICATION_CR_IDS].sort())
    // Stated as a number too, because the row is settled by COUNTING: four
    // Application CRs on the wire, four instance cards on the grid.
    expect(ids.length).toBe(APPLICATION_CR_IDS.length)
  })

  it('two Applications sharing one blueprint render TWO cards, not one', async () => {
    // The discriminating case. shared-pg and uat-ahs-pg are both bp-postgres:
    // a blueprint-keyed grid gives one card here, an Application-keyed grid
    // gives two. Without this the fixture would be one where both hypotheses
    // agree and the test would prove nothing.
    await screen.findByTestId('sov-app-card-shared-pg')
    const ids = (await cards('instance')).map(idOf)
    expect(ids.filter((id) => id === 'shared-pg' || id === 'uat-ahs-pg').length).toBe(2)
  })

  it('a HelmRelease row never becomes a card of its own — the NOT half of the clause', async () => {
    await screen.findByTestId('sov-app-card-shared-pg')
    const instanceIds = (await cards('instance')).map(idOf)
    expect(instanceIds).not.toContain('bp-cilium')
  })

  it('a blueprint that already has an instance renders NO catalog slot beside it', async () => {
    await screen.findByTestId('sov-app-card-shared-pg')
    const catalogIds = (await cards('catalog')).map(idOf)
    // The three blueprints with live instances must be absent from the catalog
    // remainder, or each would be counted twice.
    for (const bp of ['bp-postgres', 'bp-gitea', 'bp-agenity']) {
      expect(catalogIds, `catalog remainder still offers ${bp}`).not.toContain(bp)
    }
    // ...and the remainder is NOT empty, so the loop above is not passing
    // because nothing rendered.
    expect(catalogIds.length).toBeGreaterThan(0)
  })

  it('no card id appears twice across the two collections', async () => {
    await screen.findByTestId('sov-app-card-shared-pg')
    const grid = await screen.findByTestId('sov-apps-grid')
    const all = Array.from(grid.querySelectorAll('[data-testid^="sov-app-card-"]')).map(idOf)
    expect(new Set(all).size, `duplicate cards: ${all.join(', ')}`).toBe(all.length)
  })

  it('bootstrap instance cards carry the BOOTSTRAP badge — the second clause', async () => {
    const spine = await screen.findByTestId('sov-app-card-spine-gitea')
    expect(spine.textContent).toContain('BOOTSTRAP')
    // And a customer instance does not, or the badge would be decoration
    // rather than a signal.
    const customer = await screen.findByTestId('sov-app-card-uatco-agenity')
    expect(customer.textContent).not.toContain('BOOTSTRAP')
  })

  it('an estate with zero Applications renders zero instance cards', async () => {
    // Guards the other direction: the instance count must FOLLOW the wire. A
    // grid that renders four instance cards regardless of the feed would pass
    // every assertion above.
    cleanup()
    vi.unstubAllGlobals()
    installFetchMock({ apps: [{ id: 'bp-cilium', slug: 'cilium', status: 'installed' }], bootstrapKit: [] })
    renderApps()
    await screen.findByTestId('sov-apps-grid')
    await screen.findByTestId('sov-app-card-bp-cilium')
    expect((await cards('instance')).length).toBe(0)
  })
})
