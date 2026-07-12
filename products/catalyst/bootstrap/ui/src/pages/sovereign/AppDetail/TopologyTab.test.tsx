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
