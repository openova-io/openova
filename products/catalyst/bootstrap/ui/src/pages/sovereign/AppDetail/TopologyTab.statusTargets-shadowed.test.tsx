/**
 * TopologyTab.statusTargets-shadowed.test.tsx — UAT row 63 regression lock.
 *
 * Row 63 asserts an `active-hot-standby` Application renders its Primary +
 * Standby pair on the Topology tab. On hw293 it rendered `Pattern: singleton`
 * with a single region-A card and NO `topology-tab-dr-*` element at all —
 * while the same page's Status panel read `Standby·Hot is reconciling`.
 *
 * Mechanism (TopologyTab.tsx, the `resolved` chain):
 *
 *   rung 1   /placement targets      → [] on this app
 *   rung 2   spec.placement.targets  → absent (installed pre-#3969 shape)
 *   rung 2b  status.perCluster       → [{cluster: <region-a>, role: 'singleton'}]
 *            ...and this rung RETURNED, so
 *   rung 2c  status.targets          → NEVER READ
 *
 * `status.perCluster` is an OBSERVATION of where workloads were seen; it has
 * no standbyType, no vCluster, and a Standby whose Pods have not landed yet is
 * simply absent from it. `status.targets` is the controller's own placement
 * TARGET LIST and carried the correct pair on the very same object. So an
 * incomplete observation shadowed the placement it is an observation OF, and
 * the panel asserted `singleton` over data that said otherwise.
 *
 * The lock has two halves, and BOTH must hold:
 *
 *   • the AHS app renders Primary + Standby, `active-hot-standby`, and a DR
 *     panel;
 *   • the CONTROL — a genuine single-region app whose `status.targets` is
 *     itself one Primary — still renders `singleton` with no DR panel. Without
 *     that half this file would pass just as well against a change that
 *     deleted the perCluster rung outright, or that made every app look
 *     multi-region.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const getApplicationStatus = vi.fn()
const getCatalogItem = vi.fn()
const getApplicationPlacement = vi.fn()
const getHierarchicalInfrastructure = vi.fn()
const getContinuumReplicationStatus = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  getApplicationStatus: (...a: unknown[]) => getApplicationStatus(...a),
  getCatalogItem: (...a: unknown[]) => getCatalogItem(...a),
  getApplicationPlacement: (...a: unknown[]) => getApplicationPlacement(...a),
}))

vi.mock('@/lib/continuum.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/continuum.api')>()
  return {
    ...actual,
    getContinuumReplicationStatus: (...a: unknown[]) => getContinuumReplicationStatus(...a),
  }
})

vi.mock('@/lib/infrastructure.types', () => ({
  getHierarchicalInfrastructure: (...a: unknown[]) => getHierarchicalInfrastructure(...a),
}))

vi.mock('@/widgets/topology/PlacementEditor', () => ({
  PlacementEditor: () => <div data-testid="stub-placement-editor" />,
}))

import { TopologyTab } from './TopologyTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

const REGION_A = 'hw-me-east-215-a-rtz-prod'
const REGION_B = 'hw-me-east-215-b-rtz-prod'

/**
 * The hw293 shape, verbatim in structure: NO spec.placement, a perCluster
 * observation that saw only region-A, and a status.targets that carries the
 * real Primary + Standby·Hot pair.
 */
const AHS_APP_STATUS_TARGETS_ONLY = {
  name: 'shared-pg',
  namespace: 'shared-data',
  spec: {},
  status: {
    perCluster: [{ cluster: REGION_A, role: 'singleton' }],
    targets: [
      { region: REGION_A, cluster: REGION_A, role: 'Primary' },
      { region: REGION_B, cluster: REGION_B, role: 'Standby', standbyType: 'Hot' },
    ],
  },
}

/**
 * THE CONTROL. A genuine singleton: one cluster observed, and a status.targets
 * that agrees it is one Primary. This must keep rendering `singleton` with no
 * DR panel — the fix has to DISCRIMINATE, not blanket-promote every app to a
 * pair.
 */
const GENUINE_SINGLETON = {
  name: 'marketing-site',
  namespace: 'acme-prod',
  spec: {},
  status: {
    perCluster: [{ cluster: REGION_A, role: 'singleton' }],
    targets: [{ region: REGION_A, cluster: REGION_A, role: 'Primary' }],
  },
}

beforeEach(() => {
  vi.clearAllMocks()
  // Rung 1 answers EMPTY — the hw293 condition that opens the fallback chain.
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
  getCatalogItem.mockResolvedValue({ name: 'bp-postgres', placementCapability: 'active-hot-standby' })
  getHierarchicalInfrastructure.mockResolvedValue({ regions: [] })
  // No live Continuum backing — so the DR panel's presence is decided purely
  // by the placement pair, which is the thing under test.
  getContinuumReplicationStatus.mockResolvedValue({ source: 'pending' })
})

afterEach(() => cleanup())

describe('row 63 — status.targets must not be shadowed by a poorer perCluster observation', () => {
  it('renders the Primary + Standby pair when perCluster saw only one region', async () => {
    getApplicationStatus.mockResolvedValue(AHS_APP_STATUS_TARGETS_ONLY)
    render(
      withProviders(
        <TopologyTab sovereignId="dep-1" applicationName="shared-pg" namespace="shared-data" />,
      ),
    )

    // Assert on the VALUE, not on the element existing: the pattern chip is
    // present from first paint carrying `not reported`, so an element-presence
    // assertion here would pass on nothing.
    await waitFor(() =>
      expect(screen.getByTestId('topology-tab-pattern').textContent).toContain('active-hot-standby'),
    )
    expect(screen.getByTestId('topology-tab-pattern').textContent).not.toContain('singleton')

    const cards = await screen.findByTestId('topology-tab-target-cards')
    expect(cards.textContent).toContain(REGION_A)
    expect(cards.textContent).toContain(REGION_B)

    // The DR panel is gated on the placement carrying a Standby. Its absence
    // was the other half of the row-63 failure.
    await waitFor(() => expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy())
  })

  it('CONTROL — a genuine single-target app still renders singleton and no DR panel', async () => {
    getApplicationStatus.mockResolvedValue(GENUINE_SINGLETON)
    render(
      withProviders(
        <TopologyTab sovereignId="dep-1" applicationName="marketing-site" namespace="acme-prod" />,
      ),
    )

    await waitFor(() =>
      expect(screen.getByTestId('topology-tab-pattern').textContent).toContain('singleton'),
    )

    const cards = await screen.findByTestId('topology-tab-target-cards')
    expect(cards.textContent).toContain(REGION_A)
    expect(cards.textContent).not.toContain(REGION_B)

    expect(screen.queryByTestId('topology-tab-dr-panel')).toBeNull()
  })

  it('CONTROL — perCluster still answers when status.targets is absent', async () => {
    // #5420 must survive: with no status.targets, the perCluster observation is
    // still the rung that answers, ahead of the legacy mode+regions projection
    // that fabricates a Standby card for a region holding nothing.
    getApplicationStatus.mockResolvedValue({
      name: 'alloy',
      namespace: 'alloy',
      spec: { regions: [REGION_A, REGION_B], placement: 'active-hot-standby' },
      status: { perCluster: [{ cluster: REGION_A, role: 'singleton' }] },
    })
    render(withProviders(<TopologyTab sovereignId="dep-1" applicationName="alloy" namespace="alloy" />))

    await waitFor(() =>
      expect(screen.getByTestId('topology-tab-pattern').textContent).toContain('singleton'),
    )
    const cards = await screen.findByTestId('topology-tab-target-cards')
    expect(cards.textContent).not.toContain(REGION_B)
  })
})
