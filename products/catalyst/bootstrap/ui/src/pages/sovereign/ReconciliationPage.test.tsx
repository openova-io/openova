/**
 * ReconciliationPage.test.tsx — wiring lock-in for the #3925 surface-B
 * Reconciliation page (two-tab design; DAG = the shared GraphCanvas bubble
 * graph, List = the scientific ReconciliationTable).
 *
 * Coverage:
 *   • renders the N/M-Reconciled header
 *   • DAG tab (default): the SHARED GraphCanvas renders (svg present) — the
 *     reconciler graph is the reused force-directed bubble widget, not a
 *     bespoke renderer
 *   • List tab: switching surfaces the scientific table with one row per
 *     reconciler — INCLUDING the non-Flux declarative reconcilers
 *     (cert-manager / CNPG / …), the Reconciliation vocabulary (never
 *     Success/Failed), and dependsOn
 *   • the not-yet-tracked footnote + empty state
 */

import { describe, it, expect, afterEach } from 'vitest'
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
    // Non-Flux declarative reconcilers — must appear in BOTH views.
    { id: 'certificate/catalyst-system/wildcard', label: 'wildcard', kind: 'Certificate', state: 'Reconciling' },
    { id: 'cluster/cnpg/shared-pg-a', label: 'shared-pg-a', kind: 'Cluster', state: 'Reconciled' },
  ],
  reconciled: 4,
  total: 7,
  watching: true,
  notYetTracked: ['Crossplane', 'CRD controllers'],
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
    expect(count.textContent).toContain('4/7')
    expect(count.textContent).toMatch(/reconciled/i)
  })

  it('defaults to the DAG tab and renders the shared GraphCanvas (bubble graph)', async () => {
    renderPage()
    // The DAG tab reuses the shared architecture GraphCanvas — its svg root
    // carries the page's testIdPrefix. Its presence proves we render the
    // shared force-directed widget, not a bespoke graph.
    expect(await screen.findByTestId('reconciliation-dag-svg')).toBeTruthy()
    expect(screen.getByTestId('reconciliation-tab-dag').getAttribute('aria-pressed')).toBe('true')
  })

  it('switches to the List tab — the scientific table with Flux + non-Flux rows', async () => {
    renderPage()
    fireEvent.click(await screen.findByTestId('reconciliation-tab-list'))
    expect(await screen.findByTestId('reconciliation-table')).toBeTruthy()
    const rows = screen.getAllByTestId('reconciliation-table-row')
    expect(rows).toHaveLength(7)
    const kinds = rows.map((r) => r.getAttribute('data-kind'))
    // The non-Flux declarative reconcilers are present alongside the Flux ones.
    expect(kinds).toContain('Certificate')
    expect(kinds).toContain('Cluster')
    expect(kinds).toContain('HelmRelease')
    const text = screen.getByTestId('reconciliation-table').textContent ?? ''
    expect(text).toMatch(/Reconciled/)
    expect(text).toMatch(/Reconciling/)
    expect(text).toMatch(/Degraded/)
    expect(text).not.toMatch(/Success/i)
    expect(text).not.toMatch(/Succeeded/i)
    expect(text).not.toMatch(/\bFailed\b/i)
    // dependsOn surfaces in the row.
    expect(text).toContain('bp-cilium')
  })

  it('renders the not-yet-tracked footnote', async () => {
    renderPage()
    const note = await screen.findByTestId('reconciliation-not-tracked')
    expect(note.textContent).toMatch(/Crossplane/)
  })

  it('renders the empty state when no reconcilers are observed', async () => {
    renderPage({ nodes: [], reconciled: 0, total: 0, watching: false, notYetTracked: [] })
    expect(await screen.findByTestId('reconciliation-empty')).toBeTruthy()
  })
})
