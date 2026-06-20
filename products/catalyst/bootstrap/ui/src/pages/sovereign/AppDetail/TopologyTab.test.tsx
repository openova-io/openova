/**
 * TopologyTab.test.tsx — #3969 + #3656 regression locks.
 *
 * #3969: the Topology tab is EXACTLY two panels { Placement, Status }. It
 * renders the derived pattern + recon status, with NO declared/observed/
 * effective/DR machinery (the deleted "mandate unbuilt" contradiction).
 *
 * #3656: a bootstrap-kit HelmRelease with NO Application CR must NOT poll
 * the status endpoint (the 404 loop). Keys on `isBootstrap`, never a
 * blueprint name.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/* ── Mock the API + infra clients so no network is hit ─────────────────── */

const getApplicationStatus = vi.fn()
const getCatalogItem = vi.fn()
const getApplicationPlacement = vi.fn()
const getHierarchicalInfrastructure = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  getApplicationStatus: (...a: unknown[]) => getApplicationStatus(...a),
  getCatalogItem: (...a: unknown[]) => getCatalogItem(...a),
  getApplicationPlacement: (...a: unknown[]) => getApplicationPlacement(...a),
}))

vi.mock('@/lib/infrastructure.types', () => ({
  getHierarchicalInfrastructure: (...a: unknown[]) => getHierarchicalInfrastructure(...a),
}))

// Stub the editor — the tab's panel structure + status are what we assert,
// not the editor internals (covered by PlacementEditor's own tests).
vi.mock('@/widgets/topology/PlacementEditor', () => ({
  PlacementEditor: () => <div data-testid="stub-placement-editor" />,
}))

import { TopologyTab } from './TopologyTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

beforeEach(() => {
  // #3982 — default the runtime-placement endpoint to "no runtime targets"
  // so the legacy spec/status projection still drives the existing asserts.
  // Tests that exercise the runtime path override this per-case.
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('TopologyTab — #3969 { Placement, Status }', () => {
  it('renders exactly two panels (Placement + Status), no DR/effective machinery', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })

    const initialApp = {
      name: 'keycloak',
      namespace: 'keycloak',
      spec: {
        placement: {
          targets: [
            { region: 'region-a', cluster: 'mgmt-A', vcluster: 'mgmt', role: 'Primary' },
            { region: 'region-b', cluster: 'mgmt-B', vcluster: 'mgmt', role: 'Standby', standbyType: 'Hot' },
          ],
        },
      },
      status: { placement: 'Reconciled' },
    }

    render(
      withProviders(
        <TopologyTab
          sovereignId="test-sov"
          applicationName="keycloak"
          namespace="keycloak"
          initialApp={initialApp as never}
          disableNetwork
        />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-placement-panel')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-status-panel')).toBeTruthy()

    // Derived pattern is active-hot-standby (1 Primary + 1 Hot standby).
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('active-hot-standby')

    // Recon status renders as a single value — no second contradictory value.
    expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('Reconciled')

    // The deleted contradiction must NOT appear anywhere on the screen.
    expect(document.body.textContent).not.toContain('mandate unbuilt')
    expect(document.body.textContent).not.toContain('Effective class')
    expect(document.body.textContent).not.toContain('Disaster Recovery')

    // Two target cards rendered.
    expect(screen.getByTestId('topology-tab-target-card-0')).toBeTruthy()
    expect(screen.getByTestId('topology-tab-target-card-1')).toBeTruthy()
  })

  it('singleton placement shows one card, pattern singleton, no contradiction', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    const initialApp = {
      name: 'grafana',
      namespace: 'grafana',
      spec: { placement: { targets: [{ region: 'region-a', cluster: 'mgmt-A', vcluster: 'mgmt', role: 'Primary' }] } },
      status: { placement: 'Reconciled' },
    }
    render(
      withProviders(
        <TopologyTab
          sovereignId="test-sov"
          applicationName="grafana"
          initialApp={initialApp as never}
          disableNetwork
        />,
      ),
    )
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('singleton')
    })
    expect(screen.queryByTestId('topology-tab-target-card-1')).toBeNull()
    expect(document.body.textContent).not.toContain('DEGRADED')
  })
})

describe('TopologyTab — bootstrap HelmRelease status poll (#3656)', () => {
  it('does NOT poll the status endpoint for a bootstrap component (no 404 loop)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-alloy" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-status-bootstrap')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-status-loading')).toBeNull()
    expect(getApplicationStatus).not.toHaveBeenCalled()
  })

  it('DOES poll the status endpoint for a non-bootstrap app (Application CR exists)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({
      name: 'wordpress',
      namespace: 'qa-omantel',
      spec: { placement: 'single-region', regions: [] },
      status: {},
    })

    render(withProviders(<TopologyTab sovereignId="test-sov" applicationName="wordpress" namespace="qa-omantel" />))

    await waitFor(() => {
      expect(getApplicationStatus).toHaveBeenCalled()
    })
    expect(screen.queryByTestId('topology-tab-status-bootstrap')).toBeNull()
  })
})

describe('TopologyTab — #3982 runtime-derived placement', () => {
  it('a bootstrap component running in BOTH regions shows 2 targets, pattern ≠ singleton (the hw173 grafana bug)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    // grafana: a bootstrap HelmRelease (no Application CR) whose pods run in
    // BOTH regions. Before #3982 this fell to derivePattern([]) = singleton.
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'dep-x', vcluster: 'mgmt', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'dep-x-b', vcluster: 'mgmt', role: 'Primary' },
      ],
      derivedFromRuntime: true,
    })

    render(
      withProviders(
        <TopologyTab sovereignId="dep-x" applicationName="grafana" namespace="mgmt" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-target-card-0')).toBeTruthy()
    })
    // TWO targets, both regions present.
    expect(screen.getByTestId('topology-tab-target-card-1')).toBeTruthy()
    expect(screen.queryByTestId('topology-tab-target-card-2')).toBeNull()
    // Pattern is active-active (2 primaries), NOT the false singleton.
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('active-active')
    expect(screen.getByTestId('topology-tab-pattern').textContent).not.toBe('singleton')
    // The empty "No placement targets reported yet" state is gone.
    expect(screen.queryByTestId('topology-tab-placement-empty')).toBeNull()
  })

  it('runtime targets override the legacy spec projection (reality wins)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    // CR app whose spec says single-region, but the workloads actually run
    // as a CNPG pair across 2 regions → runtime wins → active-hot-standby.
    getApplicationStatus.mockResolvedValue({
      name: 'shared-pg',
      namespace: 'shared-data',
      spec: { placement: { targets: [{ region: 'region-a', cluster: 'c', vcluster: 'host', role: 'Primary' }] } },
      status: { placement: 'Reconciled' },
    })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'region-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'region-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })

    render(
      withProviders(<TopologyTab sovereignId="dep-y" applicationName="shared-pg" namespace="shared-data" />),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-target-card-1')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('active-hot-standby')
  })
})
