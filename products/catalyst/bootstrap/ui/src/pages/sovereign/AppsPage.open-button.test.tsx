/**
 * AppsPage.open-button.test.tsx — #3374.
 *
 * Locks in the RESTORED per-app "Open" launch button on the AppsPage grid
 * card. The founder flagged its disappearance (deleted by the #2743 "V4
 * verdict") as "very important": direct URLs still land signed-in zero-
 * click, but the console lost its one-click launch affordance.
 *
 * Contract asserted here:
 *   1. An app WITH a projected externalURL renders an Open button.
 *   2. An app WITHOUT an externalURL (headless service / no user UI — the
 *      BE suppresses externalURL for these via the #3224 user-UI gate)
 *      renders NO Open button. The dead-button shape never returns.
 *   3. Clicking Open routes through silent-SSO: getLaunchURL(<key>) →
 *      window.open(signedURL) — NOT a raw navigation to externalURL (the
 *      exact regression that motivated the #2743 removal; the fix keeps
 *      the affordance but fixes the SSO path).
 *   4. Clicking Open does NOT navigate the card Link (preventDefault).
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
import { AppCard } from './AppsPage'
import type { ApplicationDescriptor } from './applicationCatalog'

// Mock the silent-SSO launch-url helper so the click path is observable
// without a network round-trip.
const getLaunchURL = vi.fn()
vi.mock('@/lib/catalog.api', () => ({
  getLaunchURL: (...args: unknown[]) => getLaunchURL(...args),
}))

const GRAFANA: ApplicationDescriptor = {
  id: 'bp-grafana',
  bareId: 'grafana',
  title: 'Grafana',
  description: 'Dashboards and observability',
  familyId: 'observability',
  familyName: 'Observability',
  tier: 'optional',
  logoUrl: null,
  dependencies: [],
  bootstrapKit: true,
}

function renderCard(externalURL: string) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const homeRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => (
      <AppCard
        app={GRAFANA}
        status="installed"
        isCatalog={false}
        isService={false}
        environment="dev"
        marketplacePublished={null}
        slug="grafana"
        externalURL={externalURL}
      />
    ),
  })
  // The card Link navigates to /app/$id on the chroot — register a stub so
  // an accidental navigation surfaces as this target rendering.
  const appRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/app/$componentId',
    component: () => <div data-testid="app-detail-target" />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([homeRoute, appRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(<RouterProvider router={router} />)
}

beforeEach(() => {
  getLaunchURL.mockReset()
  vi.spyOn(window, 'open').mockImplementation(() => null)
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('AppsPage card — #3374 per-app Open button', () => {
  it('renders an Open button when the app has a user-facing externalURL', async () => {
    renderCard('https://grafana.hw139.omani.works')
    const open = await screen.findByTestId('sov-app-open-bp-grafana')
    expect(open).toBeTruthy()
    expect(open.textContent).toContain('Open')
    expect(open.getAttribute('data-external-url')).toBe('https://grafana.hw139.omani.works')
  })

  it('renders NO Open button when externalURL is empty (headless / no user UI)', async () => {
    renderCard('')
    // The card itself must render…
    await screen.findByTestId('sov-app-card-bp-grafana')
    // …but with no Open affordance.
    expect(screen.queryByTestId('sov-app-open-bp-grafana')).toBeNull()
  })

  it('clicking Open routes through silent-SSO (getLaunchURL → window.open signed URL)', async () => {
    getLaunchURL.mockResolvedValue({ url: 'https://grafana.hw139.omani.works/oidc?prompt=none' })
    renderCard('https://grafana.hw139.omani.works')
    const open = await screen.findByTestId('sov-app-open-bp-grafana')
    fireEvent.click(open)
    // Keyed on the bp-stripped blueprint/release name (bootstrap-kit HR apps
    // carry no Application CR uid; the BE launch-url resolver strips bp-).
    await waitFor(() => expect(getLaunchURL).toHaveBeenCalledWith('grafana'))
    await waitFor(() =>
      expect(window.open).toHaveBeenCalledWith(
        'https://grafana.hw139.omani.works/oidc?prompt=none',
        '_blank',
        'noopener,noreferrer',
      ),
    )
  })

  it('falls back to the raw externalURL only when launch-url yields nothing', async () => {
    getLaunchURL.mockResolvedValue(null)
    renderCard('https://grafana.hw139.omani.works')
    const open = await screen.findByTestId('sov-app-open-bp-grafana')
    fireEvent.click(open)
    await waitFor(() =>
      expect(window.open).toHaveBeenCalledWith(
        'https://grafana.hw139.omani.works',
        '_blank',
        'noopener,noreferrer',
      ),
    )
  })

  it('clicking Open does not navigate the card Link', async () => {
    getLaunchURL.mockResolvedValue({ url: 'https://grafana.hw139.omani.works/oidc' })
    renderCard('https://grafana.hw139.omani.works')
    const open = await screen.findByTestId('sov-app-open-bp-grafana')
    fireEvent.click(open)
    await waitFor(() => expect(getLaunchURL).toHaveBeenCalled())
    // The Link target must NOT have rendered — the click was consumed by the
    // Open button (preventDefault + stopPropagation).
    expect(screen.queryByTestId('app-detail-target')).toBeNull()
  })
})
