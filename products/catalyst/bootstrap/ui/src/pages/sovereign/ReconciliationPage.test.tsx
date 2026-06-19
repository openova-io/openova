/**
 * ReconciliationPage.test.tsx — wiring lock-in for the #3925 surface-B
 * Reconciliation page.
 *
 * Coverage:
 *   • renders the N/M-Reconciled header
 *   • renders one node per declared component (bounded: HRs + Kustomizations)
 *   • renders the Reconciliation vocabulary — Reconciled/Reconciling/
 *     Drifted/Degraded — and NEVER Success/Succeeded/Failed
 *   • ZERO scanner/Job nodes (the page is fed the bounded DAG only)
 *   • dependsOn edges surface on the node row
 *   • the not-yet-tracked footnote renders
 */

import { describe, it, expect, afterEach } from 'vitest'
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
import { ReconciliationPage } from './ReconciliationPage'
import type { ReconciliationDAG } from '@/lib/reconciliation.api'

afterEach(cleanup)

const SAMPLE_DAG: ReconciliationDAG = {
  nodes: [
    { id: 'kustomization/bootstrap-kit', label: 'Bootstrap', kind: 'Kustomization', state: 'Reconciled' },
    { id: 'bp-cilium', label: 'bp-cilium', kind: 'HelmRelease', state: 'Reconciled' },
    { id: 'bp-keycloak', label: 'bp-keycloak', kind: 'HelmRelease', state: 'Reconciled', dependsOn: ['bp-cilium'] },
    { id: 'bp-newapi', label: 'bp-newapi', kind: 'HelmRelease', state: 'Degraded', dependsOn: ['bp-keycloak'] },
    { id: 'bp-loki', label: 'bp-loki', kind: 'HelmRelease', state: 'Reconciling' },
  ],
  reconciled: 3,
  total: 5,
  watching: true,
  notYetTracked: ['Crossplane', 'cert-manager', 'CNPG', 'External-Secrets', 'CRD controllers'],
}

function renderPage(dag: ReconciliationDAG = SAMPLE_DAG) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const route = createRoute({
    getParentRoute: () => rootRoute,
    path: '/provision/$deploymentId/reconciliation',
    component: () => <ReconciliationPage dataOverride={dag} disablePoll />,
  })
  const router = createRouter({
    routeTree: rootRoute.addChildren([route]),
    history: createMemoryHistory({ initialEntries: ['/provision/d-1/reconciliation'] }),
  })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  )
}

describe('ReconciliationPage', () => {
  it('renders the N/M-Reconciled header', async () => {
    renderPage()
    const count = await screen.findByTestId('reconciliation-header-count')
    expect(count.textContent).toContain('3/5')
    expect(count.textContent).toMatch(/reconciled/i)
  })

  it('renders one node per declared component (bounded set)', async () => {
    renderPage()
    await screen.findByTestId('reconciliation-dag')
    const nodes = screen.getAllByTestId('reconciliation-node')
    expect(nodes).toHaveLength(5)
  })

  it('renders the Reconciliation vocabulary and NEVER Success/Failed', async () => {
    renderPage()
    const dag = await screen.findByTestId('reconciliation-dag')
    const text = dag.textContent ?? ''
    // Vocabulary present.
    expect(text).toMatch(/Reconciled/)
    expect(text).toMatch(/Reconciling/)
    expect(text).toMatch(/Degraded/)
    // Forbidden finite-end words ABSENT.
    expect(text).not.toMatch(/Success/i)
    expect(text).not.toMatch(/Succeeded/i)
    expect(text).not.toMatch(/\bFailed\b/i)
  })

  it('renders ZERO scanner/Job nodes (only HelmRelease + Kustomization kinds)', async () => {
    renderPage()
    await screen.findByTestId('reconciliation-dag')
    const nodes = screen.getAllByTestId('reconciliation-node')
    for (const n of nodes) {
      const kind = n.getAttribute('data-kind')
      expect(['HelmRelease', 'Kustomization']).toContain(kind)
    }
  })

  it('surfaces dependsOn edges on the node row', async () => {
    renderPage()
    await screen.findByTestId('reconciliation-dag')
    const keycloak = screen
      .getAllByTestId('reconciliation-node')
      .find((n) => n.getAttribute('data-node-id') === 'bp-keycloak')
    expect(keycloak).toBeTruthy()
    const deps = within(keycloak!).getByTestId('reconciliation-node-deps')
    expect(deps.textContent).toContain('bp-cilium')
  })

  it('renders the not-yet-tracked footnote', async () => {
    renderPage()
    const note = await screen.findByTestId('reconciliation-not-tracked')
    expect(note.textContent).toMatch(/Crossplane/)
    expect(note.textContent).toMatch(/cert-manager/)
  })

  it('renders the empty state when no reconcilers are observed', async () => {
    renderPage({ nodes: [], reconciled: 0, total: 0, watching: false, notYetTracked: [] })
    expect(await screen.findByTestId('reconciliation-empty')).toBeTruthy()
  })
})
