/**
 * AppsPage.orgscope.test.tsx — #4113 + #4119 lock-in.
 *
 * Two founder-visible gaps on the Org-scoped customer console
 * (console.<org>.<pool>, e.g. console.demo.omani.homes):
 *
 *   GAP 1 (#4119): the Sovereign provisioning/deployment-stream surface must
 *     NOT mount for an Org session. catalyst-api 403s /api/v1/deployments/*
 *     for an Org session, so an attached stream flips to `unreachable` → the
 *     "Couldn't reach the deployment stream … Retry / Cancel & Wipe" banner
 *     leaking to a customer. This file proves the Org session fires ZERO
 *     /deployments/* requests and renders NO failure banner.
 *
 *   GAP 2 (#4113): the Apps page must project the Org's OWN apps (from
 *     /api/v1/org/applications, with X-Tenant-Host), not the sovereign-wide
 *     catalog feed (/api/v1/sovereign/apps). This file proves the live query
 *     targets /org/applications and renders the agenity card with a working
 *     Open button, and renders NO sovereign bootstrap-kit spine cards.
 *
 * The complementary sovereign-admin behaviour (full provisioning surface,
 * /sovereign/apps feed) is unchanged and covered by AppsPage.test.tsx +
 * AppsPage.handover.test.tsx (which run with the default orgScoped=false).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
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

// Sovereign mode so the live-apps query runs (an Org console IS a Sovereign
// host — console.demo.omani.homes). detectMode reads the hostname; mock it to
// the demo Org console host.
vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'sovereign', sovereignFQDN: 'demo.omani.homes' },
  detectMode: () => ({ mode: 'sovereign', sovereignFQDN: 'demo.omani.homes' }),
}))

// Force the console scope to Org-scoped (demo). This is the #4112 hook the
// real session resolves from GET /whoami; here we pin it so the page renders
// the Org branch deterministically.
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => ({ orgScoped: true, org: 'demo', loading: false }),
}))

// Observe the silent-SSO launch path without a network round-trip.
const getLaunchURL = vi.fn()
vi.mock('@/lib/catalog.api', () => ({
  getLaunchURL: (...args: unknown[]) => getLaunchURL(...args),
}))

import { AppsPage } from './AppsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

const ORG_APPS_RESPONSE = {
  apps: [
    {
      id: 'agenity-demo',
      slug: 'agenity',
      blueprint: 'bp-agenity',
      status: 'installed',
      environment: 'demo-prod',
      externalURL: 'https://agenity.demo.omani.homes',
      instance: true,
      topology: '',
    },
    {
      id: 'demo-wp-shop',
      slug: 'wordpress',
      blueprint: 'bp-wordpress',
      status: 'installing',
      environment: 'demo-prod',
      externalURL: '',
      instance: true,
      topology: '',
    },
  ],
  bootstrapKit: [],
}

/** Records every fetched URL so the test can assert which endpoints were hit. */
let fetchedURLs: string[]
let orgAppsHeaders: Headers | null

function installFetchMock() {
  fetchedURLs = []
  orgAppsHeaders = null
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString()
      fetchedURLs.push(url)
      if (url.includes('/v1/org/applications')) {
        orgAppsHeaders = new Headers(init?.headers)
        return new Response(JSON.stringify(ORG_APPS_RESPONSE), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      }
      // Any /deployments/* or /sovereign/apps hit is a BUG for an Org session
      // — return 403 so a regression surfaces as a visible failure rather than
      // a silent pass.
      return new Response('{"error":"org-scoped-forbidden"}', { status: 403 })
    }),
  )
}

function renderOrgApps() {
  const rootRoute = createRootRoute({
    component: () => (
      <NotificationProvider>
        <Outlet />
      </NotificationProvider>
    ),
  })
  // Clean-root /apps route (the Sovereign Console URL shape — no deploymentId
  // param; resolved via /sovereign/self, which our fetch mock answers 403 →
  // useResolvedDeploymentId returns null, exactly as an Org session behaves).
  const appsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/apps',
    component: () => <AppsPage />,
  })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog',
    component: () => <div data-testid="catalog-page-target" />,
  })
  const tree = rootRoute.addChildren([appsRoute, catalogRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/apps'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('AppsPage — Org-scoped console (#4113 + #4119)', () => {
  beforeEach(() => {
    useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
    installFetchMock()
    // Pin the host so X-Tenant-Host carries the demo Org console host.
    Object.defineProperty(window, 'location', {
      value: { host: 'console.demo.omani.homes', hostname: 'console.demo.omani.homes', search: '', pathname: '/apps', hash: '' },
      writable: true,
    })
  })
  afterEach(() => {
    cleanup()
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  // Plain-DOM helpers — this suite avoids jest-dom matchers
  // (toBeInTheDocument/toHaveAttribute), which are NOT registered in this
  // project's vitest setup (src/test/setup.ts). Mirrors the querySelector +
  // chai-truthy style of AppsPage.open-button.test.tsx.
  const byId = (id: string) => document.querySelector(`[data-testid="${id}"]`)

  it('GAP 2: projects the Org estate from /org/applications with X-Tenant-Host + renders agenity Open', async () => {
    renderOrgApps()

    // agenity card renders with a working Open button.
    await waitFor(() => expect(byId('sov-app-open-agenity-demo')).toBeTruthy(), {
      timeout: 4000,
    })
    expect(byId('sov-app-open-agenity-demo')?.getAttribute('data-external-url')).toBe(
      'https://agenity.demo.omani.homes',
    )
    // The wordpress instance card renders too.
    expect(byId('sov-app-card-demo-wp-shop')).toBeTruthy()

    // The live query targeted the per-Org projection, NOT the sovereign feed.
    expect(fetchedURLs.some((u) => u.includes('/v1/org/applications'))).toBe(true)
    expect(fetchedURLs.some((u) => u.includes('/v1/sovereign/apps'))).toBe(false)
    // X-Tenant-Host carries the Org console host.
    expect(orgAppsHeaders?.get('X-Tenant-Host')).toBe('console.demo.omani.homes')
  })

  it('GAP 1: never fetches the deployment-stream + renders NO failure banner', async () => {
    renderOrgApps()

    await waitFor(() => expect(byId('sov-app-card-agenity-demo')).toBeTruthy(), {
      timeout: 4000,
    })

    // ZERO /deployments/* requests — the stream is gated off for an Org session.
    const deploymentHits = fetchedURLs.filter((u) => u.includes('/v1/deployments/'))
    expect(deploymentHits).toEqual([])

    // No "Couldn't reach the deployment stream" / "Provisioning failed" banner,
    // no Cancel & Wipe action, no provisioning pill.
    expect(screen.queryByText(/Couldn’t reach the deployment stream/i)).toBeNull()
    expect(screen.queryByText(/Provisioning failed/i)).toBeNull()
    expect(byId('sov-failure-wipe')).toBeNull()
    expect(byId('sov-provisioning-pill')).toBeNull()
  })

  it('GAP 2: does NOT render sovereign bootstrap-kit spine cards', async () => {
    renderOrgApps()
    await waitFor(() => expect(byId('sov-app-card-agenity-demo')).toBeTruthy(), {
      timeout: 4000,
    })
    // Only the org-projection instance cards render; no cilium/flux/etc spine.
    expect(byId('sov-app-card-bp-cilium')).toBeNull()
    expect(byId('sov-app-card-bp-flux')).toBeNull()
  })
})
