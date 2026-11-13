/**
 * EndpointsTab.test.tsx — unit tests for the G117.3 (#2742) read-only
 * Endpoints/Ingress tab.
 *
 * The catalog.api module is mocked so no network is hit; the tests
 * assert the list rendering and that the tab offers no action that
 * creates, edits or deletes an endpoint.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { EndpointsTab } from './EndpointsTab'
import type { ResolvedEndpoint } from '@/lib/catalog.api'

/* ── Mock the API client ─────────────────────────────────────────────── */

const listEndpoints = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  listEndpoints: (...a: unknown[]) => listEndpoints(...a),
}))

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

const SAMPLE_EP: ResolvedEndpoint = {
  name: 'web',
  hostnameTemplate: '{AppName}.{OrgSlug}.omani.homes',
  hostname: 'shop.acme.omani.homes',
  port: 443,
  protocol: 'https',
  tls: true,
  visibility: 'public',
  ssoEnabled: true,
  launchDefault: true,
  status: 'Ready',
}

function renderTab(overrides: Partial<React.ComponentProps<typeof EndpointsTab>> = {}) {
  return render(
    withProviders(
      <EndpointsTab
        applicationName="shop"
        appUID="uid-123"
        externalURL=""
        sovereignFQDN="t99.omani.works"
        {...overrides}
      />,
    ),
  )
}

beforeEach(() => {
  listEndpoints.mockReset().mockResolvedValue({ items: [SAMPLE_EP] })
})
afterEach(() => cleanup())

describe('EndpointsTab', () => {
  it('lists endpoints from the API with name + hostname + an Open link', async () => {
    renderTab()
    expect(await screen.findByTestId('sov-endpoints-table')).toBeTruthy()
    expect(screen.getByTestId('sov-endpoint-row-shop.acme.omani.homes')).toBeTruthy()
    expect(screen.getByText('web')).toBeTruthy()
    expect(screen.getByText('shop.acme.omani.homes')).toBeTruthy()
    expect(screen.getByTestId('sov-endpoint-open').getAttribute('href')).toBe(
      'https://shop.acme.omani.homes',
    )
    expect(listEndpoints).toHaveBeenCalledWith('uid-123')
  })

  it('offers no button: no New, Edit or Delete action', async () => {
    renderTab()
    await screen.findByTestId('sov-endpoints-table')
    expect(screen.getByTestId('app-tab-endpoints-body').querySelectorAll('button')).toHaveLength(0)
    expect(screen.queryByTestId('endpoint-new')).toBeNull()
    expect(screen.queryByTestId('endpoint-edit-web')).toBeNull()
    expect(screen.queryByTestId('endpoint-delete-web')).toBeNull()
  })

  it('falls back to a read-only externalURL row when the API returns none', async () => {
    listEndpoints.mockResolvedValue({ items: [] })
    renderTab({ externalURL: 'https://shop.acme.omani.homes/' })
    await screen.findByTestId('sov-endpoints-table')
    expect(screen.getByTestId('sov-endpoint-row-shop.acme.omani.homes')).toBeTruthy()
    expect(screen.getByTestId('app-tab-endpoints-body').querySelectorAll('button')).toHaveLength(0)
  })

  it('shows the empty state and calls no API when there is no appUID', async () => {
    renderTab({ appUID: '' })
    expect(await screen.findByTestId('sov-endpoints-empty')).toBeTruthy()
    expect(listEndpoints).not.toHaveBeenCalled()
  })
})
