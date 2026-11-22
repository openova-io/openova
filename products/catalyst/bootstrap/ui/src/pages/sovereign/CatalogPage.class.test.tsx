/**
 * CatalogPage.class.test.tsx — contract C1 on the Catalog page.
 *
 *   • The chip of a card that only the live catalog serves is the label of
 *     the Blueprint's class (spec.class, from the build-time catalogue), not
 *     a card.category or card.family.
 *   • Every card whose Blueprint is experimental or deprecated shows that
 *     lifecycle as a badge.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, within, waitFor, act } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { CatalogPage } from './CatalogPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'
import { NotificationProvider } from '@/shared/ui/notifications'
import { ALL_BLUEPRINTS } from '@/shared/constants/catalog.generated'
import { resolveApplications } from './applicationCatalog'
import {
  CLASS_LABELS,
  CLASS_ORDER,
  PLATFORM_CLASS,
  classOfBlueprint,
  type BlueprintClass,
} from '@/shared/constants/classes'

const STATIC_IDS = new Set(resolveApplications(INITIAL_WIZARD_STATE.selectedComponents).map((a) => a.id))
// An AI service the static component graph does not hold: the live catalog
// alone brings its card, so its chip comes from its class. (Both data
// services left in the catalogue, bp-postgres and bp-valkey, are in the
// static graph since SVC-162 deleted the scaffold ones.)
const LIVE_ONLY_SERVICE = ALL_BLUEPRINTS.find((b) => b.class === 'ai-service' && !STATIC_IDS.has(b.id))

const liveHolder: { value: Array<Record<string, unknown>> } = { value: [] }

vi.mock('@/lib/useCatalog', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/useCatalog')>()
  return {
    ...actual,
    useCatalog: () => ({ data: liveHolder.value, isSuccess: true, isError: false, isLoading: false }),
  }
})

function renderCatalog(path: string) {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/catalog',
    component: () => <CatalogPage />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([catalogRoute]),
    history: createMemoryHistory({ initialEntries: [path] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  liveHolder.value = [
    {
      name: LIVE_ONLY_SERVICE?.id ?? '',
      version: '1.0.0',
      visibility: 'listed',
      // A card category the page must no longer show.
      card: { title: 'Live data service', summary: 'Seeded only', category: 'database', family: 'postgres' },
      origin: 2,
      source: 'sovereign',
    },
  ]
  globalThis.fetch = (() =>
    Promise.resolve({ ok: true, json: () => Promise.resolve({ apps: [] }) } as unknown as Response)) as typeof fetch
})

afterEach(() => cleanup())

describe('CatalogPage — class and lifecycle (contract C1)', () => {
  it('the fixture holds: an AI service the static graph does not hold', () => {
    expect(LIVE_ONLY_SERVICE?.id).toMatch(/^bp-/)
  })

  it('the chip of a live-only card is the label of its class', async () => {
    renderCatalog('/provision/d-1/catalog?unlisted=1')
    const card = await screen.findByTestId(`sov-app-card-${LIVE_ONLY_SERVICE?.id}`)
    expect(within(card).getByText('AI services')).toBeTruthy()
    expect(within(card).queryByText('database')).toBeNull()
    expect(within(card).queryByText('postgres')).toBeNull()
  })

  it('an experimental Blueprint shows the badge Experimental', async () => {
    renderCatalog('/provision/d-1/catalog?unlisted=1')
    const badge = await screen.findByTestId(`sov-catalog-lifecycle-${LIVE_ONLY_SERVICE?.id}`)
    expect(badge.textContent).toBe('Experimental')
  })

  it('a deprecated Blueprint shows the badge Deprecated', async () => {
    const deprecated = ALL_BLUEPRINTS.find((b) => b.lifecycle === 'deprecated')
    expect(deprecated?.id).toMatch(/^bp-/)
    liveHolder.value = [
      { name: deprecated?.id ?? '', version: '1.0.0', visibility: 'unlisted', card: { title: 'Old' }, origin: 2, source: 'sovereign' },
    ]
    renderCatalog('/provision/d-1/catalog?unlisted=1')
    const badge = await screen.findByTestId(`sov-catalog-lifecycle-${deprecated?.id}`)
    expect(badge.textContent).toBe('Deprecated')
  })
})

// SVC-180 — the grouping test of this page, and the Organization rule.
describe('CatalogPage — grouped by class (SVC-180)', () => {
  const sectionClass = (s: HTMLElement) => s.getAttribute('data-testid')!.replace('sov-catalog-class-', '')

  it('groups the cards by class in the class order, each card under its own class', async () => {
    renderCatalog('/provision/d-1/catalog?unlisted=1')
    const grid = await screen.findByTestId('sov-catalog-grid')
    // A sovereign-admin with Show unlisted sees the platform components too,
    // once the console knows the scope.
    await within(grid).findByTestId('sov-app-card-bp-cilium')
    const sections = within(grid).getAllByTestId(/^sov-catalog-class-(?!grid-)[a-z-]+$/)
    const order = sections.map(sectionClass)
    expect(order.length).toBeGreaterThan(2)
    expect(order).toEqual(CLASS_ORDER.filter((c) => order.includes(c)))
    expect(order).toContain(PLATFORM_CLASS)
    for (const s of sections) {
      expect(s.textContent).toContain(CLASS_LABELS[sectionClass(s) as BlueprintClass].plural)
      const cards = within(s).getAllByTestId(/^sov-app-card-bp-/)
      expect(cards.length).toBeGreaterThan(0)
      for (const c of cards) {
        expect(classOfBlueprint(c.getAttribute('data-testid')!.replace('sov-app-card-', ''))).toBe(sectionClass(s))
      }
    }
  })

  it('an Organization user never sees a platform component, not even with Show unlisted', async () => {
    let whoami = 0
    globalThis.fetch = ((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString()
      if (url.includes('/v1/whoami')) {
        whoami += 1
        return Promise.resolve({
          ok: true,
          status: 200,
          json: () => Promise.resolve({ orgScoped: true, org: 'uatco' }),
        } as unknown as Response)
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({ apps: [] }) } as unknown as Response)
    }) as typeof fetch
    renderCatalog('/provision/d-1/catalog?unlisted=1')
    const grid = await screen.findByTestId('sov-catalog-grid')
    // The scope has answered, so the hiding below is the Organization rule and
    // not the loading state.
    await waitFor(() => expect(whoami).toBeGreaterThan(0))
    await act(async () => {})
    expect(within(grid).getAllByTestId(/^sov-app-card-bp-/).length).toBeGreaterThan(0)
    expect(within(grid).queryByTestId('sov-app-card-bp-cilium')).toBeNull()
    expect(within(grid).queryByTestId(`sov-catalog-class-${PLATFORM_CLASS}`)).toBeNull()
  })
})
