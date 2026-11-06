import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
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
  DETECTED_MODE: { mode: 'sovereign', sovereignFQDN: 'hw307.omani.works' },
  detectMode: () => ({ mode: 'sovereign', sovereignFQDN: 'hw307.omani.works' }),
}))
vi.mock('@/shared/lib/useConsoleScope', () => ({
  useConsoleScope: () => ({ orgScoped: false, org: null, loading: false }),
}))
vi.mock('@/lib/catalog.api', () => ({ getLaunchURL: vi.fn() }))

import { AppsPage } from './AppsPage'
import { useWizardStore } from '@/entities/deployment/store'
import { INITIAL_WIZARD_STATE } from '@/entities/deployment/model'

// hw307 2026-11-06: typing "bp-cnpg" (the id the detail page shows) into the
// Apps search returned a blank grid — catalog cards matched title /
// description / family only, never the id.
const BODY = {
  apps: [
    { id: 'bp-cilium', slug: 'cilium', status: 'installed' },
    { id: 'bp-cnpg-pair', slug: 'cnpg-pair', title: 'CNPG Cluster-Pair (Active-Hotstandby DR)', status: 'installed', bootstrapKit: false },
    { id: 'shared-pg', slug: 'postgres', blueprint: 'bp-postgres', status: 'installed', instance: true, environment: 'prod', topology: 'active-passive', contextCount: 1, bootstrapKit: false, org: 'uatco' },
  ],
  bootstrapKit: [],
}

function renderApps() {
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/v1/sovereign/apps')) {
      return new Response(JSON.stringify(BODY), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    return new Response('{}', { status: 404 })
  }))
  const rootRoute = createRootRoute({ component: () => (<NotificationProvider><Outlet /></NotificationProvider>) })
  const appsRoute = createRoute({ getParentRoute: () => rootRoute, path: '/apps', component: () => <AppsPage /> })
  const catchAll = createRoute({ getParentRoute: () => rootRoute, path: '/$', component: () => <div /> })
  const router = createRouter({ routeTree: rootRoute.addChildren([appsRoute, catchAll]), history: createMemoryHistory({ initialEntries: ['/apps'] }) })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}><RouterProvider router={router as never} /></QueryClientProvider>)
}

async function search(q: string) {
  await screen.findByTestId('sov-app-card-bp-cnpg-pair')
  const input = screen.getByPlaceholderText(/Search your/)
  fireEvent.change(input, { target: { value: q } })
}

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
  Object.defineProperty(window, 'location', {
    value: { host: 'console.hw307.omani.works', hostname: 'console.hw307.omani.works', search: '', pathname: '/apps', hash: '' },
    writable: true,
  })
  renderApps()
})
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.clearAllMocks() })

describe('AppsPage search matches ids, not only titles', () => {
  it.each(['bp-cnpg', 'cnpg-pair', 'BP-CNPG-PAIR', 'Cluster-Pair'])('"%s" finds bp-cnpg-pair', async (q) => {
    await search(q)
    expect(screen.queryByTestId('sov-app-card-bp-cnpg-pair')).not.toBeNull()
    expect(screen.queryByTestId('sov-apps-no-match')).toBeNull()
  })

  it('an instance card matches its Organization', async () => {
    await search('uatco')
    expect(screen.queryByTestId('sov-app-card-shared-pg')).not.toBeNull()
  })

  it('a query matching nothing says so instead of rendering a blank grid', async () => {
    await search('no-such-app-xyz')
    expect(screen.getByTestId('sov-apps-no-match').textContent).toContain('no-such-app-xyz')
    expect(screen.queryByTestId('sov-app-card-bp-cnpg-pair')).toBeNull()
  })
})
