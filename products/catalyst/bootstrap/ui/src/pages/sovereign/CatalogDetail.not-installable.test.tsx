/**
 * CatalogDetail.not-installable.test.tsx — SVC-030: the class page refuses
 * a catalog entry that has no published chart.
 *
 * A catalog entry at version 0.0.0 has no chart. Its "+ New instance"
 * buttons (in the header and in the empty state) are disabled, and the
 * page says "Not installable: no published chart". Any other version
 * keeps both buttons enabled and shows no such text.
 */

import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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
import { CatalogDetail } from './CatalogDetail'

const versionHolder = { value: '0.0.0' }

function jsonRes(body: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

function installFetch() {
  globalThis.fetch = ((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/instances')) return jsonRes({ items: [] })
    if (url.includes('/catalog/')) {
      return jsonRes({
        name: 'bp-ferretdb',
        version: versionHolder.value,
        card: { title: 'Ferretdb', summary: 'MongoDB-compatible database' },
        origin: 2,
        source: 'sovereign',
        raw: { spec: { multiInstance: { enabled: true } } },
      })
    }
    return jsonRes({})
  }) as typeof fetch
}

function renderDetail() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/$blueprintName',
    component: CatalogDetail,
  })
  const tree = rootRoute.addChildren([catalogRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/catalog/bp-ferretdb'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  versionHolder.value = '0.0.0'
  installFetch()
})

afterEach(() => cleanup())

describe('CatalogDetail — no published chart (SVC-030)', () => {
  it('version 0.0.0: both "+ New instance" buttons are disabled and the reason is shown', async () => {
    renderDetail()
    const header = (await screen.findByTestId('btn-new-instance')) as HTMLButtonElement
    const empty = (await screen.findByTestId('btn-new-instance-empty')) as HTMLButtonElement
    expect(header.disabled).toBe(true)
    expect(empty.disabled).toBe(true)
    expect(screen.getByTestId('sov-instances-not-installable').textContent).toBe(
      'Not installable: no published chart',
    )
    fireEvent.click(header)
    fireEvent.click(empty)
    expect(screen.queryByTestId('dialog-new-instance')).toBeNull()
  })

  it('a published version: the buttons stay enabled and no reason is shown', async () => {
    versionHolder.value = '1.2.3'
    renderDetail()
    const header = (await screen.findByTestId('btn-new-instance')) as HTMLButtonElement
    const empty = (await screen.findByTestId('btn-new-instance-empty')) as HTMLButtonElement
    expect(header.disabled).toBe(false)
    expect(empty.disabled).toBe(false)
    expect(screen.queryByTestId('sov-instances-not-installable')).toBeNull()
    expect(screen.queryByText('Not installable: no published chart')).toBeNull()
  })
})
