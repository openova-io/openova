/**
 * TopologyTab.dr-honesty.test.tsx — UAT rows 62 + 63 + 71 regression locks.
 *
 * Row 62 ("the Topology DR section shows the live Continuum status (Ready /
 * lease holder / standby) from the live API, not a static badge") failed on
 * three separate legs, each measured on hw292:
 *
 *   62a — the standby card's health line was
 *         `drStatus?.streamingState || 'hot replica (follows WAL)'` against a
 *         backend that returns `streamingState: ""` for every CR-derived
 *         reading. So it printed a green claim UNCONDITIONALLY, including over
 *         a standby the same payload had just declared unverifiable. A static
 *         badge, in a row whose clause forbids one.
 *   62b — no phase and no lease holder rendered anywhere.
 *   62c — `standbyAbsent` keyed ONLY on `standbyAvailable === false`, a field
 *         the backend omits whenever the leg is undetermined, while it always
 *         publishes a `standby-available` health gate. A payload whose own gate
 *         read Fail rendered NO red banner.
 *
 * Row 63 ("...the control DISABLED...") could not be verified because NO
 * switchover element rendered on an unbacked app at all — absent is not
 * disabled.
 *
 * Row 71 is the backend half (the streaming gate could never Pass); its lock
 * lives in continuum_dr_extras_row62_71_test.go. What is asserted HERE is the
 * consumer contract that made that backend Warn visible: a live reading with
 * passing gates must render a NUMBER, not "—".
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

/** An Application declaring active-hot-standby across two regions. */
const AHS_APP = {
  name: 'shared-pg',
  namespace: 'shared-data',
  spec: {
    placement: {
      targets: [
        { region: 'hw-me-east-215-a-rtz-prod', cluster: 'hw-me-east-215-a-rtz-prod', role: 'Primary' },
        {
          region: 'hw-me-east-215-b-rtz-prod',
          cluster: 'hw-me-east-215-b-rtz-prod',
          role: 'Standby',
          standbyType: 'Hot',
        },
      ],
    },
  },
  status: { placementRecon: 'Reconciled' },
}

function renderTab() {
  return render(
    withProviders(
      <TopologyTab
        sovereignId="dep-hw292"
        applicationName="shared-pg"
        namespace="shared-data"
        initialApp={AHS_APP as never}
      />,
    ),
  )
}

beforeEach(() => {
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
  getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('row 62c — the standby-absent banner must be reachable from the gate', () => {
  it('renders the red Standby absent banner on a standby-available gate that reads Fail', async () => {
    // The payload shape the backend emits when the controller's standby probe
    // reports the leg is GONE but the tri-state bool did not survive to this
    // consumer's read. The gate IS the verdict; it must arm the banner.
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      streamingState: '',
      source: 'live',
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [
        {
          name: 'standby-available',
          status: 'Fail',
          severity: 'critical',
          message: 'required hot-standby is unreachable',
        },
      ],
    })

    renderTab()

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-standby-absent')).toBeTruthy()
    })
    // ...and the lag must NOT print a zero next to it.
    expect(screen.getByTestId('topology-tab-dr-lag-value').textContent).toContain('—')
  })

  it('CONTROL — a standby-available gate that PASSES renders no absent banner', async () => {
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      streamingState: 'streaming',
      source: 'live',
      standbyAvailable: true,
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [
        { name: 'standby-available', status: 'Pass', severity: 'info' },
        { name: 'streaming-replication', status: 'Pass', severity: 'info' },
        { name: 'wal-lag-under-rpo', status: 'Pass', severity: 'info' },
      ],
    })

    renderTab()

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-regions')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-dr-standby-absent')).toBeNull()
  })
})

describe('row 62a — the standby health line is never a hardcoded green claim', () => {
  it('does not print "hot replica (follows WAL)" when the API reports no streaming state and the gate is unverified', async () => {
    // The EXACT hw292 payload: source live, streamingState "", standby leg
    // reported unverifiable. The old code printed the green claim anyway.
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      streamingState: '',
      source: 'live',
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [
        {
          name: 'standby-available',
          status: 'Warn',
          severity: 'warning',
          message: 'standby leg not verifiable from live cluster state',
        },
      ],
    })

    renderTab()

    const line = await screen.findByTestId(
      'topology-tab-dr-standby-hw-me-east-215-b-rtz-prod-health',
    )
    expect(line.textContent).not.toContain('hot replica')
    expect(line.textContent).toContain('not verified')
  })

  it('CONTROL — a real streaming state from the API is rendered verbatim', async () => {
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      streamingState: 'streaming',
      source: 'live',
      standbyAvailable: true,
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [{ name: 'standby-available', status: 'Pass', severity: 'info' }],
    })

    renderTab()

    const line = await screen.findByTestId(
      'topology-tab-dr-standby-hw-me-east-215-b-rtz-prod-health',
    )
    expect(line.textContent).toContain('streaming')
  })
})

describe('row 62b — the live Continuum status (phase + lease holder) renders', () => {
  it('renders the reconciled phase and the witness-lease holder from the API', async () => {
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      streamingState: 'streaming',
      source: 'live',
      standbyAvailable: true,
      phase: 'Healthy',
      leaseHolder: 'hw-me-east-215-a-rtz-prod',
      leaseExpiresAt: '2026-08-10T05:54:58Z',
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [{ name: 'standby-available', status: 'Pass', severity: 'info' }],
    })

    renderTab()

    expect((await screen.findByTestId('topology-tab-dr-phase')).textContent).toContain('Healthy')
    expect(screen.getByTestId('topology-tab-dr-lease-holder').textContent).toBe(
      'hw-me-east-215-a-rtz-prod',
    )
    expect(screen.getByTestId('topology-tab-dr-lease-expires').textContent).toContain(
      '2026-08-10T05:54:58Z',
    )
  })

  it('CONTROL — an API that reports no phase/lease renders no status strip (never an invented Healthy)', async () => {
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      streamingState: 'streaming',
      source: 'live',
      standbyAvailable: true,
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [{ name: 'standby-available', status: 'Pass', severity: 'info' }],
    })

    renderTab()

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-dr-regions')).toBeTruthy()
    })
    expect(screen.queryByTestId('topology-tab-dr-continuum-status')).toBeNull()
  })
})

describe('row 71 — a fully-verified live reading renders a lag NUMBER', () => {
  it('prints the numeric lag when every health gate passes', async () => {
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0.2,
      streamingState: 'streaming',
      syncState: 'sync',
      source: 'live',
      standbyAvailable: true,
      phase: 'Healthy',
      leaseHolder: 'hw-me-east-215-a-rtz-prod',
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [
        { name: 'streaming-replication', status: 'Pass', severity: 'info' },
        { name: 'wal-lag-under-rpo', status: 'Pass', severity: 'info' },
        { name: 'standby-available', status: 'Pass', severity: 'info' },
      ],
    })

    renderTab()

    const lag = await screen.findByTestId('topology-tab-dr-lag-value')
    expect(lag.textContent).toContain('0.2 s')
  })

  it('CONTROL — the SAME lag renders "—" while the gates say unverified', async () => {
    // This is what hw292 actually served before the backend fix: a real 0.2s
    // reading behind a streaming-replication gate that could never Pass.
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0.2,
      streamingState: '',
      source: 'live',
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [
        {
          name: 'streaming-replication',
          status: 'Warn',
          severity: 'warning',
          message: 'replication health not reported by the Continuum CR; unverified',
        },
      ],
    })

    renderTab()

    const lag = await screen.findByTestId('topology-tab-dr-lag-value')
    expect(lag.textContent).toContain('—')
  })
})

describe('row 63 — an active-hot-standby app with NO Continuum backing', () => {
  const UNBACKED = {
    continuum: 'dr-uat-ahs-pg',
    namespace: 'hw292-omani-works',
    primaryRegion: 'hw-me-east-215-a-rtz-prod',
    currentPrimary: 'hw-me-east-215-a-rtz-prod',
    walLagSeconds: 0,
    replicaPromotable: false,
    streamingState: '',
    syncState: '',
    source: 'pending',
    replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
    healthGates: [{ name: 'live-status', status: 'Warn', severity: 'info' }],
  }

  it('renders the switchover control DISABLED with "Switchover unavailable" copy', async () => {
    getContinuumReplicationStatus.mockResolvedValue(UNBACKED)

    renderTab()

    const btn = (await screen.findByTestId(
      'topology-tab-dr-switchover-open',
    )) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('topology-tab-dr-switchover-reason').textContent).toContain(
      'Switchover unavailable',
    )
  })

  it('and still synthesizes NO lag, standby or target-region values', async () => {
    getContinuumReplicationStatus.mockResolvedValue(UNBACKED)

    renderTab()

    // Wait for the SETTLED no-backing state (the reason line only renders once
    // the poll resolves), not merely for the panel to mount.
    await screen.findByTestId('topology-tab-dr-switchover-reason')
    expect(screen.getByTestId('topology-tab-dr-none')).toBeTruthy()
    // No numeric lag row, no standby card, no armed target region.
    expect(screen.queryByTestId('topology-tab-dr-lag-value')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-regions')).toBeNull()
    expect(screen.getByTestId('topology-tab-dr-switchover-reason').textContent).not.toContain(
      'promote standby',
    )
  })

  it('CONTROL — a BACKED app arms the same control, so "disabled" above is chosen by the backing, not printed unconditionally', async () => {
    getContinuumReplicationStatus.mockResolvedValue({
      continuum: 'dr-shared-pg',
      namespace: 'shared-data',
      primaryRegion: 'hw-me-east-215-a-rtz-prod',
      currentPrimary: 'hw-me-east-215-a-rtz-prod',
      walLagSeconds: 0,
      replicaPromotable: true,
      streamingState: 'streaming',
      source: 'live',
      standbyAvailable: true,
      replicas: [{ region: 'hw-me-east-215-b-rtz-prod', role: 'replica' }],
      healthGates: [{ name: 'standby-available', status: 'Pass', severity: 'info' }],
    })

    renderTab()

    const btn = (await screen.findByTestId(
      'topology-tab-dr-switchover-open',
    )) as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    expect(screen.getByTestId('topology-tab-dr-switchover-reason').textContent).toContain(
      'promote standby',
    )
  })
})
