/**
 * ReconciliationPage.test.tsx — wiring lock-in for the #3925 surface-B
 * Reconciliation page (two-tab design, operator 2026-06-20).
 *
 * Coverage:
 *   • renders the N/M-Reconciled header
 *   • DAG tab (default): one GRAPH node per declared component + dependsOn
 *     rendered as graph EDGES (not inline text) — the real dependency view
 *   • Reconciliation vocabulary — Reconciled/Reconciling/Degraded — and
 *     NEVER Success/Succeeded/Failed
 *   • ZERO scanner/Job nodes (bounded DAG only)
 *   • List tab: switching surfaces the scannable status rows + dependsOn
 *   • the not-yet-tracked footnote renders
 *   • empty state
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, within, fireEvent } from '@testing-library/react'
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

  it('defaults to the DAG tab and renders one graph node per declared component', async () => {
    renderPage()
    await screen.findByTestId('reconciliation-dag-svg')
    const nodes = screen.getAllByTestId(/^reconciliation-dag-node-/)
    expect(nodes).toHaveLength(5)
    // ZERO scanner/Job nodes — only HelmRelease + Kustomization kinds.
    for (const n of nodes) {
      expect(['HelmRelease', 'Kustomization']).toContain(n.getAttribute('data-kind'))
    }
  })

  it('renders dependsOn as graph EDGES (the real dependency view)', async () => {
    renderPage()
    const edges = await screen.findByTestId('reconciliation-dag-edges')
    // Two declared deps: bp-keycloak→bp-cilium, bp-newapi→bp-keycloak.
    expect(edges.querySelectorAll('polyline')).toHaveLength(2)
  })

  it('renders the Reconciliation vocabulary and NEVER Success/Failed', async () => {
    renderPage()
    const dag = await screen.findByTestId('reconciliation-dag')
    const text = dag.textContent ?? ''
    expect(text).toMatch(/Reconciled/)
    expect(text).toMatch(/Reconciling/)
    expect(text).toMatch(/Degraded/)
    expect(text).not.toMatch(/Success/i)
    expect(text).not.toMatch(/Succeeded/i)
    expect(text).not.toMatch(/\bFailed\b/i)
  })

  it('switches to the List tab and surfaces status rows + dependsOn', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('reconciliation-tab-list'))
    const nodes = await screen.findAllByTestId('reconciliation-node')
    expect(nodes).toHaveLength(5)
    const keycloak = nodes.find((n) => n.getAttribute('data-node-id') === 'bp-keycloak')
    expect(keycloak).toBeTruthy()
    expect(within(keycloak!).getByTestId('reconciliation-node-deps').textContent).toContain('bp-cilium')
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
