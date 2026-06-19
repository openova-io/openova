/**
 * ReadinessChip.test.tsx — wiring lock-in for the #3925 surface-D
 * readiness chip + operation banner.
 *
 * Coverage:
 *   • deriveChipState truth table (pure) — the four states + precedence:
 *       converging > operation > degraded > ready
 *   • chip renders the right label/data-state for each status projection
 *   • chip links to /dashboard (click target)
 *   • operationInProgress flips the chip to OPERATION-IN-PROGRESS AND
 *     surfaces the OperationBanner with a "view progress" link
 *   • the banner is absent in every non-operation state
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  ReadinessChip,
  OperationBanner,
  deriveChipState,
  type ReadinessSnapshot,
} from './ReadinessChip'

afterEach(cleanup)

function renderInShell(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const indexRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/',
    component: () => <>{node}</>,
  })
  const dashboardRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/dashboard',
    component: () => <div>dashboard</div>,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([indexRoute, dashboardRoute]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('deriveChipState', () => {
  const cases: Array<[string, ReadinessSnapshot | null, ReturnType<typeof deriveChipState>]> = [
    ['null snapshot → converging (never falsely READY)', null, 'converging'],
    ['ready + idle → ready', { status: 'ready' }, 'ready'],
    ['ready + secondaryDegraded → degraded', { status: 'ready', secondaryDegraded: true }, 'degraded'],
    ['ready + operationInProgress → operation', { status: 'ready', operationInProgress: true }, 'operation'],
    ['phase1-watching → converging', { status: 'phase1-watching' }, 'converging'],
    ['tofu-applying → converging', { status: 'tofu-applying' }, 'converging'],
    ['failed → degraded', { status: 'failed' }, 'degraded'],
    // Precedence: an in-flight provision wins even if operationInProgress
    // somehow also set (it never should, but the provision dominates).
    [
      'converging beats operation',
      { status: 'phase1-watching', operationInProgress: true },
      'converging',
    ],
    // Precedence: operation beats degraded on a ready env.
    [
      'operation beats degraded',
      { status: 'ready', operationInProgress: true, secondaryDegraded: true },
      'operation',
    ],
  ]
  for (const [name, snap, expected] of cases) {
    it(name, () => {
      expect(deriveChipState(snap)).toBe(expected)
    })
  }
})

describe('ReadinessChip render', () => {
  it('renders READY for a ready, idle env and links to /dashboard', async () => {
    renderInShell(
      <ReadinessChip deploymentId="d-1" snapshotOverride={{ status: 'ready' }} disablePoll />,
    )
    const chip = await screen.findByTestId('readiness-chip')
    expect(chip.getAttribute('data-state')).toBe('ready')
    expect(screen.getByTestId('readiness-chip-label').textContent).toBe('Ready')
    // Click target is the Dashboard.
    expect(chip.getAttribute('href')).toContain('/dashboard')
  })

  it('renders CONVERGING (pulse) while provisioning', async () => {
    renderInShell(
      <ReadinessChip
        deploymentId="d-2"
        snapshotOverride={{ status: 'phase1-watching' }}
        disablePoll
      />,
    )
    const chip = await screen.findByTestId('readiness-chip')
    expect(chip.getAttribute('data-state')).toBe('converging')
  })

  it('renders DEGRADED when a secondary region lags', async () => {
    renderInShell(
      <ReadinessChip
        deploymentId="d-3"
        snapshotOverride={{ status: 'ready', secondaryDegraded: true }}
        disablePoll
      />,
    )
    const chip = await screen.findByTestId('readiness-chip')
    expect(chip.getAttribute('data-state')).toBe('degraded')
  })

  it('renders OPERATION-IN-PROGRESS when operationInProgress is true', async () => {
    renderInShell(
      <ReadinessChip
        deploymentId="d-4"
        snapshotOverride={{ status: 'ready', operationInProgress: true }}
        disablePoll
      />,
    )
    const chip = await screen.findByTestId('readiness-chip')
    expect(chip.getAttribute('data-state')).toBe('operation')
    expect(screen.getByTestId('readiness-chip-label').textContent).toMatch(/operation/i)
  })
})

describe('OperationBanner', () => {
  it('renders with a view-progress link ONLY while an operation runs', async () => {
    renderInShell(
      <OperationBanner
        deploymentId="d-5"
        snapshotOverride={{ status: 'ready', operationInProgress: true }}
        disablePoll
      />,
    )
    expect(await screen.findByTestId('operation-banner')).toBeTruthy()
    const link = screen.getByTestId('operation-banner-link')
    expect(link.getAttribute('href')).toContain('/dashboard')
  })

  it('renders null when no operation is in progress (ready)', async () => {
    renderInShell(
      <OperationBanner deploymentId="d-6" snapshotOverride={{ status: 'ready' }} disablePoll />,
    )
    // Let the router settle, then assert the banner never mounts.
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByTestId('operation-banner')).toBeNull()
  })

  it('renders null while merely converging', async () => {
    renderInShell(
      <OperationBanner
        deploymentId="d-7"
        snapshotOverride={{ status: 'phase1-watching' }}
        disablePoll
      />,
    )
    await new Promise((r) => setTimeout(r, 0))
    expect(screen.queryByTestId('operation-banner')).toBeNull()
  })
})
