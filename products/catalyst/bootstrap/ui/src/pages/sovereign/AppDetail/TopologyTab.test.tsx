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
import { render, screen, cleanup, waitFor, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/* ── Mock the API + infra clients so no network is hit ─────────────────── */

const getApplicationStatus = vi.fn()
const getCatalogItem = vi.fn()
const getApplicationPlacement = vi.fn()
const getHierarchicalInfrastructure = vi.fn()
const getContinuumReplicationStatus = vi.fn()
const getSwitchoverPreview = vi.fn()
const requestSwitchover = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  getApplicationStatus: (...a: unknown[]) => getApplicationStatus(...a),
  getCatalogItem: (...a: unknown[]) => getCatalogItem(...a),
  getApplicationPlacement: (...a: unknown[]) => getApplicationPlacement(...a),
}))

// The DR section (#3375 rows 51/52/56/57) reads live replication telemetry
// off the Continuum CR via this client. Re-export the real lagBucket so the
// component's bucket→colour mapping is exercised, not stubbed.
vi.mock('@/lib/continuum.api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/lib/continuum.api')>()
  return {
    ...actual,
    getContinuumReplicationStatus: (...a: unknown[]) => getContinuumReplicationStatus(...a),
    // #4552 — the armed SwitchoverDialog runs a preflight + confirm. Mock both
    // so opening the dialog from the DR panel doesn't hit the network.
    getSwitchoverPreview: (...a: unknown[]) => getSwitchoverPreview(...a),
    requestSwitchover: (...a: unknown[]) => requestSwitchover(...a),
  }
})

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
  // DR replication-status default: 404 (no Continuum CR backs this app), so
  // the DR section renders the calm "no cross-region replica" note unless a
  // case overrides it. Mirrors the endpoint's real not-found behaviour.
  getContinuumReplicationStatus.mockRejectedValue(new Error('continuum replication-status: HTTP 404'))
  // #4552 — the switchover preflight defaults to a promotable result so the
  // armed dialog can be exercised; cases override per-scenario.
  getSwitchoverPreview.mockResolvedValue({
    continuum: 'dr-shared-pg',
    namespace: 'shared-data',
    targetRegion: 'me-east-215-b',
    currentPrimary: 'me-east-215-a',
    currentLagSec: 0,
    estimatedDurationSec: 60,
    estimatedDuration: '60s',
    blockingChecks: [],
    promotable: true,
    message: 'preview only',
  })
  requestSwitchover.mockResolvedValue({
    name: 'dr-shared-pg',
    namespace: 'shared-data',
    targetRegion: 'me-east-215-b',
    fromRegion: 'me-east-215-a',
    toRegion: 'me-east-215-b',
    requestedAt: new Date().toISOString(),
    message: 'switchover completed',
    applied: true,
    completed: true,
  })
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

  it('#3969 §8d: reads status.placementRecon while status.placement is the legacy OBJECT (no collision)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    // The REAL controller shape: status.placement is an OBJECT
    // ({vcluster, source, regions}) AND the ONE recon value lives in the
    // dedicated status.placementRecon string. The tab must read the recon
    // field and never choke on the object (the pre-#3969 code cast
    // status.placement to a string and would have shown the wrong status).
    getApplicationStatus.mockResolvedValue({
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
      status: {
        placement: { vcluster: 'mgmt', source: 'instance', regions: ['region-a', 'region-b'] },
        placementRecon: 'Reconciling',
        placementReason: 'region-b Standby·Hot is reconciling',
      },
    })

    render(withProviders(<TopologyTab sovereignId="test-sov" applicationName="keycloak" namespace="keycloak" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('Reconciling')
    })
    // The plain reason is surfaced, never a second contradictory class.
    expect(document.body.textContent).toContain('region-b Standby·Hot is reconciling')
    expect(document.body.textContent).not.toContain('[object Object]')
    expect(document.body.textContent).not.toContain('mandate unbuilt')
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

  it('#4000: queries placement with EMPTY namespace for a bootstrap component (workload Pods run in their own ns, not the flux-system HR ns)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-alloy" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(getApplicationPlacement).toHaveBeenCalled()
    })
    // bp-alloy's HelmRelease is in flux-system but its DaemonSet Pods run in ns
    // `alloy` (21-alloy.yaml targetNamespace). Passing flux-system would filter
    // them ALL out → false singleton; empty namespace matches across all ns.
    expect(getApplicationPlacement).toHaveBeenCalledWith('test-sov', 'bp-alloy', undefined)
  })

  it('#4000: queries placement WITH the app namespace for a non-bootstrap app (no regression)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'wordpress', namespace: 'qa-omantel', spec: {}, status: {} })

    render(withProviders(<TopologyTab sovereignId="test-sov" applicationName="wordpress" namespace="qa-omantel" />))

    await waitFor(() => {
      expect(getApplicationPlacement).toHaveBeenCalledWith('test-sov', 'wordpress', 'qa-omantel')
    })
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

describe('TopologyTab — #5515 an empty placement is "not reported", never a confident singleton', () => {
  it('targets: [] renders Pattern "not reported" beside the empty note — NOT singleton', async () => {
    // The live hw291 `cilium` case: /placement reports targets: [] and the
    // panel already says "No placement targets reported yet." — yet the Pattern
    // label read `singleton`, the one pattern that MEANS "no failover, and
    // that's fine". Two contradictory claims in one panel.
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'cilium', namespace: 'kube-system', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })

    render(withProviders(<TopologyTab sovereignId="dep-hw291" applicationName="cilium" namespace="kube-system" />))

    // POSITIVE CONTROL — the panel + the pattern cell really rendered, so the
    // negative assertion below is not passing on an empty render.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-placement-panel')).toBeTruthy()
    })
    const cell = screen.getByTestId('topology-tab-pattern')
    expect(cell.textContent).toBe('not reported')
    // THE regression: the confident pattern must not be claimed.
    expect(cell.textContent).not.toBe('singleton')
    // And the panel's own empty note is still there — the two now AGREE.
    expect(screen.getByTestId('topology-tab-placement-empty')).toBeTruthy()
  })

  it('CONTROL — a real single-Primary app still renders the confident "singleton"', async () => {
    // The other direction: the guard must not swallow real data. A component
    // that returns "not reported" unconditionally fails here.
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'harbor', namespace: 'harbor', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [{ region: 'me-east-215-a', cluster: 'c', vcluster: 'mgmt', role: 'Primary' }],
      derivedFromRuntime: true,
    })

    render(withProviders(<TopologyTab sovereignId="dep-hw291" applicationName="harbor" namespace="harbor" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-target-card-0')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('singleton')
    expect(screen.getByTestId('topology-tab-pattern').textContent).not.toBe('not reported')
    expect(screen.queryByTestId('topology-tab-placement-empty')).toBeNull()
  })
})

describe('TopologyTab — DR / replication surface (#3375 rows 51/52/56/57)', () => {
  const standbyPlacement = {
    targets: [
      { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
      { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
    ],
    derivedFromRuntime: true,
  }

  it('renders the standby region + LIVE replication lag in seconds for a cross-region app (rows 51/52/56)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({
      name: 'shared-pg',
      namespace: 'shared-data',
      spec: {},
      status: { placement: 'Reconciled' },
    })
    getApplicationPlacement.mockResolvedValue(standbyPlacement)
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 1.7,
      streamingState: 'streaming',
      syncState: 'async',
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    // The continuum name is derived as dr-<app>.
    await waitFor(() => {
      expect(getContinuumReplicationStatus).toHaveBeenCalledWith('dep-z', 'dr-shared-pg', { namespace: 'shared-data' })
    })
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    // Primary + standby region cards.
    expect(screen.getByTestId('topology-tab-dr-primary').textContent).toContain('me-east-215-a')
    expect(screen.getByTestId('topology-tab-dr-standby-me-east-215-b').textContent).toContain('me-east-215-b')
    // LIVE lag in seconds — not a hardcoded dash.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-lag-value').textContent).toContain('1.7 s')
    })
    // Honest "live" source badge.
    expect(screen.getByTestId('topology-tab-dr-source').textContent).toContain('live')
  })

  it('Switchover affordance is ARMED — opens the confirm dialog (row 57, #4552)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationPlacement.mockResolvedValue(standbyPlacement)
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0.4,
      source: 'live',
    })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-switchover-open')).toBeTruthy()
    })
    const btn = screen.getByTestId('topology-tab-dr-switchover-open') as HTMLButtonElement
    // Armed (not the old deferred disabled affordance).
    expect(btn.disabled).toBe(false)
    // No dialog until the operator clicks.
    expect(screen.queryByTestId('continuum-switchover-dialog')).toBeNull()
    fireEvent.click(btn)
    // The confirm dialog opens with the from→to diff (the standby is the target).
    await waitFor(() => {
      expect(screen.getByTestId('continuum-switchover-dialog')).toBeTruthy()
    })
    expect(screen.getByTestId('continuum-switchover-dialog-diff').textContent).toContain('me-east-215-b')
  })

  it('#4552: a paired CNPG whose placement projects a false singleton STILL renders the pair + switchover (no stale singleton)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    // The placement projection reports ONLY the primary (the CNPG replica half
    // isn't surfaced as a Standby target) → hasStandby is false → the tab used
    // to render a stale singleton with no DR. But the LIVE continuum status
    // confirms a real cross-region pair, so the DR panel + switchover must
    // render off it.
    getApplicationPlacement.mockResolvedValue({
      targets: [{ region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' }],
      derivedFromRuntime: true,
    })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0,
      replicas: [{ region: 'me-east-215-b', role: 'replica', lagSeconds: 0 }],
      streamingState: 'streaming',
      syncState: 'sync',
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    // DR panel renders off the live pair even though placement said singleton.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-dr-primary').textContent).toContain('me-east-215-a')
    expect(screen.getByTestId('topology-tab-dr-standby').textContent).toContain('me-east-215-b')
    // And the switchover control is armed for it.
    const btn = screen.getByTestId('topology-tab-dr-switchover-open') as HTMLButtonElement
    expect(btn.disabled).toBe(false)
  })

  it('#4923: a VERIFIED-absent standby renders the explicit standby-absent condition, never a calm follower card', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue(standbyPlacement)
    // The backend verified the standby leg off the live cnpg-pair replica
    // half and reports it ABSENT (the #4901 region-kill shape) — lag stays 0
    // during the outage, so the UI must key off standbyAvailable, not lag.
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0,
      streamingState: 'interrupted',
      standbyAvailable: false,
      replicas: [{ region: 'me-east-215-b', role: 'replica', lagSeconds: 0 }],
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    // The explicit honest condition banner renders.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-standby-absent').textContent).toContain('Standby absent')
    })
    // The standby card reads unreachable — NOT the calm 'hot replica' text.
    const card = screen.getByTestId('topology-tab-dr-standby-me-east-215-b')
    expect(card.textContent).toContain('standby unreachable')
    expect(card.textContent).not.toContain('hot replica (follows WAL)')
  })

  it('#4923: an UNVERIFIABLE standby (standbyAvailable omitted) renders NO absent banner (unknown is not a fault claim)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue(standbyPlacement)
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0.4,
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-dr-standby-absent')).toBeNull()
  })

  it('a SINGLETON app shows NO DR panel + NO Switchover (honestly hidden, row 58)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    const initialApp = {
      name: 'grafana',
      namespace: 'grafana',
      spec: { placement: { targets: [{ region: 'region-a', cluster: 'mgmt-A', vcluster: 'mgmt', role: 'Primary' }] } },
      status: { placement: 'Reconciled' },
    }
    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="grafana" initialApp={initialApp as never} disableNetwork />,
      ),
    )
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('singleton')
    })
    expect(screen.queryByTestId('topology-tab-dr-panel')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-switchover')).toBeNull()
    // The DR endpoint is never polled for a singleton.
    expect(getContinuumReplicationStatus).not.toHaveBeenCalled()
  })

  it('a cross-region app with NO Continuum CR (404) shows the calm no-replica note, never a fabricated lag', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'sso-bridge', namespace: 'sso-bridge', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue(standbyPlacement)
    // Default beforeEach mock = 404.

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="sso-bridge" namespace="sso-bridge" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-none')).toBeTruthy()
    })
    // No live lag value rendered (the section short-circuits on error).
    expect(screen.queryByTestId('topology-tab-dr-lag-value')).toBeNull()
  })
})

describe('TopologyTab — #4886 bootstrap-HR DR off the live Continuum CR', () => {
  // spine-keycloak/gitea/harbor/openbao + cnpg-pair: Continuum-backed bootstrap
  // HelmReleases with NO Application CR. Their Pods run active-active across
  // both regions, so the placement projection labels BOTH regions Primary and
  // hasStandby is false — yet the live continuum carries the real active
  // (leaseHolder) + standby + replicationLagSeconds. The DR section must render
  // off that live status instead of vanishing.
  const activeActivePlacement = {
    targets: [
      { region: 'me-east-215-a', cluster: 'dep-x', vcluster: 'mgmt', role: 'Primary' },
      { region: 'me-east-215-b', cluster: 'dep-x-b', vcluster: 'mgmt', role: 'Primary' },
    ],
    derivedFromRuntime: true,
  }

  it('renders the DR section for a bootstrap-HR app off the LIVE continuum (active region=leaseHolder, standby, numeric lag)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationPlacement.mockResolvedValue(activeActivePlacement)
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-spine-keycloak',
      namespace: 'keycloak',
      primaryRegion: 'me-east-215-a',
      // active region = the witness-lease holder (surfaced as currentPrimary).
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0,
      replicas: [{ region: 'me-east-215-b', role: 'replica', lagSeconds: 0 }],
      streamingState: 'streaming',
      source: 'live',
    })

    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-x"
          applicationName="spine-keycloak"
          namespace="flux-system"
          isBootstrap
        />,
      ),
    )

    // Bootstrap components query with EMPTY namespace so the cluster-wide lookup
    // finds the CR in the spine's namespace (not the flux-system HR ns).
    await waitFor(() => {
      expect(getContinuumReplicationStatus).toHaveBeenCalledWith('dep-x', 'dr-spine-keycloak', {
        namespace: undefined,
      })
    })
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    // Active region = leaseHolder; standby is the OTHER region — NOT a second
    // "PRIMARY / serves writes" card.
    expect(screen.getByTestId('topology-tab-dr-primary').textContent).toContain('me-east-215-a')
    expect(screen.getByTestId('topology-tab-dr-standby').textContent).toContain('me-east-215-b')
    // Numeric replication lag in seconds (row 56/64) — never a bare dash.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-lag-value').textContent).toContain('0.0 s')
    })
    expect(screen.getByTestId('topology-tab-dr-source').textContent).toContain('live')
    // The Status panel's honest "n/a — bootstrap component" note is unaffected.
    expect(screen.getByTestId('topology-tab-status-bootstrap')).toBeTruthy()
  })

  it('#5514: a DECLARED active-hot-standby with ZERO backing arms NOTHING — no lag, no follower card, switchover DISABLED', async () => {
    // The live hw291 phantom (uatcorp/uatwalk-ahs-07300830): the Application
    // DECLARES active-hot-standby, but the standby region has no namespace at
    // all. Two things conspired:
    //   • /placement returns targets: [] → the targetsFromLegacy fallback
    //     projects Primary + Standby·Hot from the DECLARED mode → hasStandby.
    //   • replication-status returns HTTP 200 (NOT a 404) with the
    //     `source:"pending"` fallback envelope carrying a fabricated
    //     replicas[] entry + walLagSeconds 0 + replicaPromotable false.
    // Pre-fix that rendered "Replication lag 0.0 s", a "hot replica (follows
    // WAL)" card, and an ARMED "Switch over…" targeting the phantom region.
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({
      name: 'uatwalk-ahs-07300830',
      namespace: 'uatcorp',
      // The DECLARED legacy posture — no spec.placement.targets[] at all.
      spec: { placement: 'active-hot-standby', regions: ['me-east-215-a', 'me-east-215-b'] },
      status: { placementRecon: 'Reconciling' },
    })
    getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-uatwalk-ahs-07300830',
      namespace: 'uatcorp',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0,
      replicaPromotable: false,
      replicas: [{ region: 'me-east-215-b', role: 'replica' }],
      source: 'pending',
    })

    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-hw291"
          applicationName="uatwalk-ahs-07300830"
          namespace="uatcorp"
        />,
      ),
    )

    // POSITIVE CONTROL (rule 2) — the surface really did render. Without this
    // the absence assertions below would pass on an empty render.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    // The declared standby DID drive the panel open, and the honest
    // not-live badge is present — so we are on the real code path.
    expect(screen.getByTestId('topology-tab-dr-source').textContent).toContain('not live')
    // And the honest unbacked note renders (previously DEAD code, because the
    // endpoint answers 200 and `drQ.isError` never fired).
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-none')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-dr-unbacked').textContent).toContain('DECLARES a standby')

    // ── The three defects, each asserted absent ────────────────────────
    // 1. NO numeric replication lag anywhere.
    expect(screen.queryByTestId('topology-tab-dr-lag-value')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-lag')).toBeNull()
    expect(document.body.textContent).not.toContain('0.0 s')
    // 2. NO "hot replica (follows WAL)" standby card for the phantom region.
    expect(screen.queryByTestId('topology-tab-dr-standby')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-standby-me-east-215-b')).toBeNull()
    expect(document.body.textContent).not.toContain('hot replica (follows WAL)')
    // 3. NO switchover control armed against a region with no namespace.
    expect(screen.queryByTestId('topology-tab-dr-switchover-open')).toBeNull()
    expect(screen.queryByTestId('continuum-switchover-dialog')).toBeNull()
  })

  it('#5514: an explicit replicaPromotable:false on a LIVE reading DISABLES the switchover (the verdict is consulted)', async () => {
    // Backing IS live (so the panel + lag render honestly), but the backend
    // says the replica cannot be promoted. The button must be disabled with a
    // plain reason — pre-fix `disabled={!switchoverTarget}` ignored the verdict
    // entirely and armed off a region STRING.
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 3.2,
      replicaPromotable: false,
      streamingState: 'catchup',
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    // POSITIVE CONTROL — a live backing renders the full panel + real lag.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-switchover-open')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-dr-lag-value').textContent).toContain('3.2 s')
    // The button exists but is DISARMED, with the reason stated.
    const btn = screen.getByTestId('topology-tab-dr-switchover-open') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('topology-tab-dr-switchover').textContent).toContain('not promotable')
    // Clicking a disabled control opens nothing.
    fireEvent.click(btn)
    expect(screen.queryByTestId('continuum-switchover-dialog')).toBeNull()
  })

  it('#5514: a VERIFIED-absent standby disarms the switchover and shows no false 0.0 s lag', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })
    // #4901 shape — lag stays 0 through the outage, so a rendered "0.0 s"
    // reads as perfect health exactly when the standby is gone.
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0,
      standbyAvailable: false,
      streamingState: 'interrupted',
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    // POSITIVE CONTROL — the panel and the absent banner rendered.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-standby-absent')).toBeTruthy()
    })
    // The lag cell renders, but as an honest dash — not a false 0.0 s.
    expect(screen.getByTestId('topology-tab-dr-lag-value').textContent).toContain('—')
    expect(screen.getByTestId('topology-tab-dr-lag-value').textContent).not.toContain('0.0 s')
    // Switchover disarmed: there is no standby leg to promote.
    const btn = screen.getByTestId('topology-tab-dr-switchover-open') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('topology-tab-dr-switchover').textContent).toContain('nothing to promote')
  })

  it('#5508: an UNVERIFIED lag (streaming-replication + standby-available gates Warn) renders as unknown, never 0.0 s green', async () => {
    // The live hw291 shape (dep 2c2d746b578c636b): the API returns
    // walLagSeconds:0 WITH the two Warn gates saying the measurement is
    // unverified — and the tab rendered "Replication lag 0.0 s" behind a
    // green pill, surfacing neither gate. The reductio on the same env:
    // dr-spine-openbao (a raft store with no PostgreSQL at all) reported a
    // PostgreSQL lag of 0 through the identical shape.
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 0,
      streamingState: 'streaming',
      replicas: [{ region: 'me-east-215-b', role: 'replica', lagSeconds: 0 }],
      healthGates: [
        {
          name: 'streaming-replication',
          status: 'Warn',
          severity: 'warning',
          message: 'replication health not reported by the Continuum CR; unverified',
        },
        { name: 'wal-lag-under-rpo', status: 'Pass', severity: 'info' },
        {
          name: 'standby-available',
          status: 'Warn',
          severity: 'warning',
          message:
            'standby leg not verifiable from live cluster state (no cnpg pair resolvable); reporting unknown, not healthy',
        },
      ],
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-hw291" applicationName="shared-pg" namespace="shared-data" />))

    // POSITIVE CONTROL (vacuity check) — the DR panel AND the lag cell really
    // rendered; the negative assertions below cannot pass on an empty render.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    const lagCell = await screen.findByTestId('topology-tab-dr-lag-value')

    // THE regression: an unverified lag must read as unknown, never a
    // numeric zero…
    expect(lagCell.textContent).toContain('—')
    expect(lagCell.textContent).not.toContain('0.0 s')
    expect(document.body.textContent).not.toContain('0.0 s')
    // …and a Warn gate can NEVER sit behind a green pill.
    expect(lagCell.className).not.toContain('text-green-400')
    // The unverified qualifier is stated in plain text.
    expect(screen.getByTestId('topology-tab-dr-lag-unverified')).toBeTruthy()
    // BOTH Warn gates are surfaced next to the reading they qualify.
    expect(screen.getByTestId('topology-tab-dr-gate-streaming-replication').textContent).toContain('unverified')
    expect(screen.getByTestId('topology-tab-dr-gate-standby-available').textContent).toContain('not verifiable')
  })

  it('#5508 CONTROL: all-Pass gates keep the verified numeric lag + green pill unchanged (the guard must not swallow real data)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 1.7,
      streamingState: 'streaming',
      syncState: 'async',
      healthGates: [
        { name: 'streaming-replication', status: 'Pass', severity: 'info' },
        { name: 'wal-lag-under-rpo', status: 'Pass', severity: 'info' },
        { name: 'standby-available', status: 'Pass', severity: 'info', message: 'hot-standby in me-east-215-b is reachable' },
      ],
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    const lagCell = await screen.findByTestId('topology-tab-dr-lag-value')
    // Verified reading renders EXACTLY as before: the number, green bucket.
    await waitFor(() => {
      expect(lagCell.textContent).toContain('1.7 s')
    })
    expect(lagCell.className).toContain('text-green-400')
    // No unverified qualifier, no degraded-gate rows.
    expect(screen.queryByTestId('topology-tab-dr-lag-unverified')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-gates')).toBeNull()
  })

  it('#5508: a Fail gate is a VERIFIED fault — the real number stays but the pill degrades to red, never green', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 3.2,
      streamingState: 'catchup',
      healthGates: [
        {
          name: 'streaming-replication',
          status: 'Fail',
          severity: 'critical',
          message: 'replication reported unhealthy on the Continuum CR',
        },
        { name: 'wal-lag-under-rpo', status: 'Pass', severity: 'info' },
      ],
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    const lagCell = await screen.findByTestId('topology-tab-dr-lag-value')
    await waitFor(() => {
      expect(lagCell.textContent).toContain('3.2 s')
    })
    expect(lagCell.className).toContain('text-red-400')
    expect(lagCell.className).not.toContain('text-green-400')
    // The verified fault is surfaced as an explicit gate row.
    expect(screen.getByTestId('topology-tab-dr-gate-streaming-replication').textContent).toContain('unhealthy')
  })

  it('#5508: an OVER-THRESHOLD wal-lag Warn keeps its genuine numeric reading (only the not-reported branch reads unknown)', async () => {
    // The other Warn cause on the same gate: lag 45s > the 30s promotability
    // threshold. That number is a REAL measurement the operator needs — a
    // blanket gates-warn→dash overcorrection would hide it. The backend's
    // not-reported branch is only reachable with the zero-value lag (<=30),
    // which is how the component discriminates structurally.
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({ name: 'shared-pg', namespace: 'shared-data', spec: {}, status: {} })
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: true,
    })
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'me-east-215-a',
      currentPrimary: 'me-east-215-a',
      walLagSeconds: 45,
      streamingState: 'streaming',
      healthGates: [
        { name: 'streaming-replication', status: 'Pass', severity: 'info' },
        {
          name: 'wal-lag-under-rpo',
          status: 'Warn',
          severity: 'warning',
          message: 'replication lag 45s exceeds the 30s promotability threshold',
        },
        { name: 'standby-available', status: 'Pass', severity: 'info' },
      ],
      source: 'live',
    })

    render(withProviders(<TopologyTab sovereignId="dep-z" applicationName="shared-pg" namespace="shared-data" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-panel')).toBeTruthy()
    })
    const lagCell = await screen.findByTestId('topology-tab-dr-lag-value')
    await waitFor(() => {
      expect(lagCell.textContent).toContain('45.0 s')
    })
    // Not green (the gate warns), and the number is NOT swallowed.
    expect(lagCell.className).not.toContain('text-green-400')
    expect(screen.queryByTestId('topology-tab-dr-lag-unverified')).toBeNull()
    expect(screen.getByTestId('topology-tab-dr-gate-wal-lag-under-rpo').textContent).toContain('exceeds')
  })

  it('does NOT render DR for a SINGLETON bootstrap app (synthesized fallback is never passed off as live)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationPlacement.mockResolvedValue({
      targets: [{ region: 'me-east-215-a', cluster: 'dep-x', vcluster: 'mgmt', role: 'Primary' }],
      derivedFromRuntime: true,
    })
    // The endpoint returns a synthesized shape (source:"synthesized") when no
    // live Continuum/cnpg-pair backs the app — the DR section must stay hidden.
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-cert-manager',
      namespace: 'cert-manager',
      primaryRegion: 'hz-fsn-rtz-prod',
      currentPrimary: 'hz-fsn-rtz-prod',
      walLagSeconds: 2.0,
      replicas: [{ region: 'hz-hel-rtz-prod', role: 'replica' }],
      source: 'synthesized',
    })

    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-x"
          applicationName="cert-manager"
          namespace="flux-system"
          isBootstrap
        />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-status-bootstrap')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-dr-panel')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-switchover')).toBeNull()
  })
})

describe('TopologyTab — #5568 derivedFromRuntime is consumed, not discarded', () => {
  /**
   * #5568 found `derivedFromRuntime` hardcoded `true` on every return path of
   * GET /applications/{name}/placement — a provenance flag that could not
   * fail. PR #5683 fixed the server half. The client half was still missing:
   * a repo-wide grep for the field found ONE production reference (the type
   * declaration in catalog.api.ts) and a pile of test mocks. Nothing read it.
   *
   * That mattered because the fallback chain below the runtime rung is keyed
   * on EMPTINESS, not provenance: whenever the runtime reports no targets the
   * panel silently projects `spec.placement` / `status.perCluster` and renders
   * the result through the same Pattern chip. So a DECLARED two-region pair
   * and an OBSERVED two-region pair were pixel-identical — the #5513 shape,
   * and the #5731 class: a surface reporting a state it did not observe.
   *
   * Both directions are asserted. The declared cases alone would pass against
   * a component that always printed "declared"; the runtime control alone is
   * what the pre-fix tree already satisfied.
   */
  beforeEach(() => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
  })

  it('runtime-observed targets are labelled runtime-observed (control — must not always say declared)', async () => {
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
      expect(screen.getByTestId('topology-tab-placement-source')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-placement-source').textContent).toBe('runtime-observed')
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('active-active')
  })

  it('the no-data-plane response (derivedFromRuntime:false) must NOT render its spec projection as observed', async () => {
    // The #5568 sharp end: k8sCache==nil returns 200 + targets:[] +
    // derivedFromRuntime:false. No runtime was consulted. The panel still
    // renders SOMETHING (the declared spec) — it must say so.
    getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: false })
    getApplicationStatus.mockResolvedValue({
      name: 'shared-pg',
      namespace: 'shared-data',
      spec: {
        placement: {
          targets: [
            { region: 'me-east-215-a', cluster: 'c-a', vcluster: 'host', role: 'Primary' },
            { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
          ],
        },
      },
      status: {},
    })

    render(
      withProviders(<TopologyTab sovereignId="dep-y" applicationName="shared-pg" namespace="shared-data" />),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-placement-source')).toBeTruthy()
    })
    // The pattern still renders — the operator keeps their declared view.
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('active-hot-standby')
    // But it is NOT presented as an observation.
    const source = screen.getByTestId('topology-tab-placement-source').textContent ?? ''
    expect(source).not.toBe('runtime-observed')
    expect(source).toContain('declared')
  })

  it('a runtime that returned targets but admitted derivedFromRuntime:false is still not an observation', async () => {
    // Provenance beats non-emptiness: a populated list from a response that
    // says no runtime was consulted is a projection, whatever its length.
    getApplicationPlacement.mockResolvedValue({
      targets: [
        { region: 'me-east-215-a', cluster: 'c-a', vcluster: 'host', role: 'Primary' },
        { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
      ],
      derivedFromRuntime: false,
    })

    render(
      withProviders(<TopologyTab sovereignId="dep-y" applicationName="shared-pg" namespace="shared-data" />),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-placement-source')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-placement-source').textContent).not.toBe('runtime-observed')
  })

  it('a runtime observation that found NOTHING must not let the declared spec pose as observed', async () => {
    // derivedFromRuntime:true + targets:[] — the runtime WAS read and genuinely
    // holds nothing. The chain falls back to the declared spec (so the operator
    // still sees their intent), but that intent is not an observation.
    getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
    getApplicationStatus.mockResolvedValue({
      name: 'uatwalk-ahs',
      namespace: 'uatcorp',
      spec: {
        placement: {
          targets: [
            { region: 'me-east-215-a', cluster: 'c-a', vcluster: 'host', role: 'Primary' },
            { region: 'me-east-215-b', cluster: 'c-b', vcluster: 'host', role: 'Standby', standbyType: 'Hot' },
          ],
        },
      },
      status: {},
    })

    render(
      withProviders(<TopologyTab sovereignId="dep-y" applicationName="uatwalk-ahs" namespace="uatcorp" />),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-placement-source')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-placement-source').textContent).not.toBe('runtime-observed')
  })

  it('an un-derivable pattern prints no provenance chip at all (nothing to attribute)', async () => {
    getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: false })
    getApplicationStatus.mockResolvedValue({
      name: 'cilium',
      namespace: 'kube-system',
      spec: {},
      status: {},
    })

    render(
      withProviders(
        <TopologyTab sovereignId="dep-z" applicationName="cilium" namespace="kube-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-placement-empty')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-pattern').textContent).toBe('not reported')
    expect(screen.queryByTestId('topology-tab-placement-source')).toBeNull()
  })
})
