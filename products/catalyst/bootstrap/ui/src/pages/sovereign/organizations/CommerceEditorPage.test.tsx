/**
 * CommerceEditorPage.test.tsx — the commerce editor surface (issue #3378
 * DoD 7/8). Renders the Plans editor table from the exact store.go struct
 * fields, opens the create modal, and exercises the field renderers
 * (features list, included_quotas k/v, product_slug). Uses the
 * initialRowsOverride seam so it's deterministic without the fetch path.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { CommerceEditorPage } from './CommerceEditorPage'
import type { CommercePlan } from '@/lib/commerce.api'

const PLANS: CommercePlan[] = [
  {
    id: 'p-s',
    slug: 's',
    name: 'Small',
    description: 'starter',
    cpu: '1',
    memory: '2Gi',
    storage: '20Gi',
    price_omr: 5,
    popular: false,
    sort_order: 1,
    features: ['1 vCPU', '2 GiB'],
    included_quotas: { apps: '3' },
  },
  {
    id: 'p-w',
    slug: 'test-w',
    name: 'Test W',
    description: 'walk plan',
    cpu: '2',
    memory: '4Gi',
    storage: '40Gi',
    price_omr: 9,
    popular: false,
    sort_order: 2,
    features: [],
  },
]

function renderEditor(rows: readonly CommercePlan[]) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations/commerce/plans',
    component: () => (
      <CommerceEditorPage
        kind="plans"
        initialRowsOverride={rows as unknown as Record<string, unknown>[]}
      />
    ),
  })
  const orgRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/organizations',
    component: () => <div data-testid="orgs-stub" />,
  })
  const tree = rootRoute.addChildren([route, orgRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/organizations/commerce/plans'] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('CommerceEditorPage — Plans (DoD 7)', () => {
  it('renders one row per plan with the table columns', async () => {
    renderEditor(PLANS)
    expect(await screen.findByTestId('commerce-plans-page')).toBeTruthy()
    expect(screen.getByTestId('commerce-row-s')).toBeTruthy()
    expect(screen.getByTestId('commerce-row-test-w')).toBeTruthy()
    // price column renders the OMR value
    expect(screen.getByTestId('commerce-cell-price_omr-test-w').textContent).toBe('9')
  })

  it('opens the create modal with every store.go field', async () => {
    renderEditor(PLANS)
    fireEvent.click(await screen.findByTestId('commerce-create-button'))
    expect(screen.getByTestId('commerce-modal')).toBeTruthy()
    // the field renderers for the struct fields are present
    expect(screen.getByTestId('commerce-field-slug')).toBeTruthy()
    expect(screen.getByTestId('commerce-field-price_omr')).toBeTruthy()
    expect(screen.getByTestId('commerce-field-features')).toBeTruthy() // list editor
    expect(screen.getByTestId('commerce-field-included_quotas')).toBeTruthy() // k/v editor
    expect(screen.getByTestId('commerce-field-product_slug')).toBeTruthy()
  })

  it('opens the edit modal pre-filled and locks the slug', async () => {
    renderEditor(PLANS)
    fireEvent.click(await screen.findByTestId('commerce-edit-test-w'))
    const slug = screen.getByTestId('commerce-field-slug') as HTMLInputElement
    expect(slug.value).toBe('test-w')
    expect(slug.disabled).toBe(true)
    const price = screen.getByTestId('commerce-field-price_omr') as HTMLInputElement
    expect(price.value).toBe('9')
  })

  it('renders the empty state when there are no plans', async () => {
    renderEditor([])
    expect(await screen.findByTestId('commerce-empty')).toBeTruthy()
  })
})
