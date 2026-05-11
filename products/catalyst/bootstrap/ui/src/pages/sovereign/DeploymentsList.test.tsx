/**
 * DeploymentsList.test.tsx — coverage for the deployments admin list
 * (issue #178): sortable columns + per-row delete-with-two-modes modal.
 *
 * Contract under test:
 *   - Initial sort is startedAt DESC (newest first).
 *   - Clicking a column header changes the sort key.
 *   - Clicking the active header toggles asc/desc.
 *   - Each non-in-flight row exposes a Delete button.
 *   - In-flight rows render Delete but it's disabled.
 *   - Clicking Delete opens the DeleteDeploymentModal with the matching
 *     deployment id wired through.
 *   - Modal exposes record-only + deep radio options.
 *
 * The hook + session mocks are vi.mock-ed at the module level so this
 * test focuses on table behaviour without a real React Query cache.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'

import { DeploymentsList } from './DeploymentsList'
import type { DeploymentListEntry } from '@/shared/lib/useInflightDeployment'

// Mock the hooks so the table renders with a deterministic dataset
// without any real fetch / cookie wiring.
const mockSession = vi.fn()
vi.mock('@/shared/lib/useSession', () => ({
  useSession: () => mockSession(),
}))

const mockUseInflight = vi.fn()
vi.mock('@/shared/lib/useInflightDeployment', async () => {
  const actual = await vi.importActual<typeof import('@/shared/lib/useInflightDeployment')>(
    '@/shared/lib/useInflightDeployment',
  )
  return {
    ...actual,
    useInflightDeployment: () => mockUseInflight(),
  }
})

function renderList(): void {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/sovereign/deployments',
    component: DeploymentsList,
  })
  // Stub the deep-link targets so <Link to=...> resolves without warnings.
  const provDashRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/dashboard',
    component: () => null,
  })
  const wizardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/wizard',
    component: () => null,
  })
  const loginRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/login',
    component: () => null,
  })
  const dashboardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard',
    component: () => null,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, provDashRoute, wizardRoute, loginRoute, dashboardRoute]),
    history: createMemoryHistory({ initialEntries: ['/sovereign/deployments'] }),
  })
  render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

const ROWS: DeploymentListEntry[] = [
  {
    id: 'dep-a',
    status: 'failed',
    sovereignFQDN: 'alpha.example.com',
    region: 'fsn1',
    startedAt: '2026-05-01T08:00:00Z',
    finishedAt: '2026-05-01T09:00:00Z',
  },
  {
    id: 'dep-b',
    status: 'phase1-watching',
    sovereignFQDN: 'bravo.example.com',
    region: 'nbg1',
    startedAt: '2026-05-05T12:00:00Z',
  },
  {
    id: 'dep-c',
    status: 'wiped',
    sovereignFQDN: 'charlie.example.com',
    region: 'hel1',
    startedAt: '2026-05-03T15:00:00Z',
    finishedAt: '2026-05-03T17:00:00Z',
  },
]

beforeEach(() => {
  mockSession.mockReturnValue({
    signedIn: true,
    email: 'alice@example.com',
    sub: 'sub-1',
    loading: false,
    refetch: () => undefined,
    signOut: async () => undefined,
  })
  mockUseInflight.mockReturnValue({
    inflight: null,
    completed: ROWS,
    all: ROWS,
    loading: false,
    isError: false,
  })
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('DeploymentsList — sortable columns', () => {
  it('renders rows sorted by startedAt DESC by default (bravo / charlie / alpha)', async () => {
    renderList()
    const table = await screen.findByTestId('deployments-list-table')
    expect(table.getAttribute('data-sort-key')).toBe('startedAt')
    expect(table.getAttribute('data-sort-order')).toBe('desc')
    const rows = within(table).getAllByRole('row').slice(1) // skip header
    const ids = rows.map((r) => r.getAttribute('data-testid'))
    expect(ids).toEqual([
      'deployments-list-row-dep-b',
      'deployments-list-row-dep-c',
      'deployments-list-row-dep-a',
    ])
  })

  it('clicking the FQDN header sorts by sovereignFQDN ASC (alpha / bravo / charlie)', async () => {
    renderList()
    fireEvent.click(await screen.findByTestId('deployments-list-sort-sovereignFQDN'))
    const table = screen.getByTestId('deployments-list-table')
    expect(table.getAttribute('data-sort-key')).toBe('sovereignFQDN')
    expect(table.getAttribute('data-sort-order')).toBe('asc')
    const ids = within(table).getAllByRole('row').slice(1).map((r) => r.getAttribute('data-testid'))
    expect(ids).toEqual([
      'deployments-list-row-dep-a',
      'deployments-list-row-dep-b',
      'deployments-list-row-dep-c',
    ])
  })

  it('clicking the active header toggles ASC ↔ DESC', async () => {
    renderList()
    const hdr = await screen.findByTestId('deployments-list-sort-sovereignFQDN')
    fireEvent.click(hdr)
    fireEvent.click(hdr)
    const table = screen.getByTestId('deployments-list-table')
    expect(table.getAttribute('data-sort-order')).toBe('desc')
  })

  it('sorts by status ASC alphabetically', async () => {
    renderList()
    fireEvent.click(await screen.findByTestId('deployments-list-sort-status'))
    const table = screen.getByTestId('deployments-list-table')
    const ids = within(table).getAllByRole('row').slice(1).map((r) => r.getAttribute('data-testid'))
    // statuses sorted asc: failed, phase1-watching, wiped
    expect(ids).toEqual([
      'deployments-list-row-dep-a',
      'deployments-list-row-dep-b',
      'deployments-list-row-dep-c',
    ])
  })
})

describe('DeploymentsList — delete button per row', () => {
  it('renders a Delete button on every non-in-flight row', async () => {
    renderList()
    expect(await screen.findByTestId('deployments-list-delete-dep-a')).toBeDefined()
    expect(screen.getByTestId('deployments-list-delete-dep-c')).toBeDefined()
  })

  it('renders the Delete button as DISABLED on in-flight rows', async () => {
    renderList()
    const btn = (await screen.findByTestId('deployments-list-delete-dep-b')) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('opens the DeleteDeploymentModal with both record-only and deep mode radios', async () => {
    renderList()
    fireEvent.click(await screen.findByTestId('deployments-list-delete-dep-a'))
    expect(screen.getByTestId('delete-deployment-mode')).toBeDefined()
    expect(screen.getByTestId('delete-deployment-mode-record-only')).toBeDefined()
    expect(screen.getByTestId('delete-deployment-mode-deep')).toBeDefined()
  })

  it('record-only mode does NOT show the Hetzner token field; deep mode does', async () => {
    renderList()
    fireEvent.click(await screen.findByTestId('deployments-list-delete-dep-a'))
    // Default mode is record-only — no Hetzner token field.
    expect(screen.queryByTestId('delete-deployment-hetzner-token')).toBeNull()
    fireEvent.click(screen.getByTestId('delete-deployment-mode-deep'))
    expect(screen.getByTestId('delete-deployment-hetzner-token')).toBeDefined()
  })
})
