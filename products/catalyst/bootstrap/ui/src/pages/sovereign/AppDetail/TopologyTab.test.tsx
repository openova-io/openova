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
const getContinuumReplicationStatus = vi.fn()
const getSwitchoverPreview = vi.fn()

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
    // The read-only switchover PREVIEW the placement editor shows before Save
    // (desired-state model — there is no switchover request any more).
    getSwitchoverPreview: (...a: unknown[]) => getSwitchoverPreview(...a),
  }
})

vi.mock('@/lib/infrastructure.types', () => ({
  getHierarchicalInfrastructure: (...a: unknown[]) => getHierarchicalInfrastructure(...a),
}))

// Stub the editor — the tab's panel structure + status are what we assert,
// not the editor internals (covered by PlacementEditor's own tests). The stub
// can flip which target is Primary (reporting the draft via onTargetsChange,
// exactly what the real editor does on a role edit) and renders the tab's
// `beforeActions` slot so the save preview is observable.
type StubTarget = { region: string; cluster: string; vcluster?: string; role: string; standbyType?: string }
vi.mock('@/widgets/topology/PlacementEditor', () => ({
  PlacementEditor: (props: {
    initialTargets: StubTarget[]
    onTargetsChange?: (t: StubTarget[]) => void
    beforeActions?: React.ReactNode
  }) => (
    <div data-testid="stub-placement-editor">
      <button
        type="button"
        data-testid="stub-flip-primary"
        onClick={() =>
          props.onTargetsChange?.(
            props.initialTargets.map((t) => ({
              ...t,
              role: t.role === 'Primary' ? 'Standby' : 'Primary',
            })),
          )
        }
      >
        flip
      </button>
      {props.beforeActions}
    </div>
  ),
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
  // The save-preview preflight defaults to a passing result; cases override.
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
      objectStatus: {
        state: 'Ready',
        reason: 'Reconciled',
        message: 'installed across 2 region(s)',
        perRegion: [
          { region: 'region-a', state: 'Ready', reason: 'Serving', message: 'GET / answered 200' },
          {
            region: 'region-b',
            state: 'NotReady',
            reason: 'SynchronousStandbyNotStreaming',
            message: 'standby wordpress-db-replica not streaming',
          },
        ],
      },
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

    // ADR-0015 D7 — the Status panel renders statusOf(object) verbatim.
    expect(screen.getByTestId('topology-tab-object-status-badge').textContent).toBe('Ready')
    expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('installed across 2 region(s)')

    // Per placement target: DECLARED role + per-region status, verbatim.
    expect(screen.getByTestId('topology-tab-target-card-0-role').textContent).toContain('PRIMARY')
    expect(screen.getByTestId('topology-tab-target-region-a-status-badge').textContent).toBe('Ready')
    expect(screen.getByTestId('topology-tab-target-region-b-status-badge').textContent).toBe('NotReady')
    expect(screen.getByTestId('topology-tab-target-card-1').textContent).toContain(
      'SynchronousStandbyNotStreaming — standby wordpress-db-replica not streaming',
    )

    // D7 deleted the whole DR section.
    expect(document.body.textContent).not.toContain('Disaster recovery')
    expect(screen.queryByTestId('topology-tab-dr-panel')).toBeNull()
    // No transition running → no link.
    expect(screen.queryByTestId('topology-tab-active-transition-job')).toBeNull()

    // The deleted contradiction must NOT appear anywhere on the screen.
    expect(document.body.textContent).not.toContain('mandate unbuilt')
    expect(document.body.textContent).not.toContain('Effective class')
    expect(document.body.textContent).not.toContain('Disaster Recovery')

    // Two target cards rendered.
    expect(screen.getByTestId('topology-tab-target-card-0')).toBeTruthy()
    expect(screen.getByTestId('topology-tab-target-card-1')).toBeTruthy()
  })

  it('D7: a region with no perRegion entry renders Unknown — never a browser-default Reconciling', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
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
      // Legacy recon fields the deleted browser verdict used to read.
      status: {
        placement: { vcluster: 'mgmt', source: 'instance', regions: ['region-a', 'region-b'] },
        placementRecon: 'Reconciled',
      },
      objectStatus: { state: 'Unknown', reason: 'Progressing', message: 'HelmRelease not yet present' },
    })

    render(withProviders(<TopologyTab sovereignId="test-sov" applicationName="keycloak" namespace="keycloak" />))

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-object-status-badge').textContent).toBe('Unknown')
    })
    expect(screen.getByTestId('topology-tab-recon-status').textContent).toContain('Progressing — HelmRelease not yet present')
    expect(screen.getByTestId('topology-tab-target-region-a-status-badge').textContent).toBe('Unknown')
    expect(screen.getByTestId('topology-tab-target-region-b-status-badge').textContent).toBe('Unknown')
    // The legacy placementRecon no longer drives anything on screen.
    expect(screen.getByTestId('topology-tab-recon-status').textContent).not.toContain('Reconciled')
    expect(document.body.textContent).not.toContain('[object Object]')
  })

  it('D8 slot: activeTransitionJobLink renders a link when present', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    render(
      withProviders(
        <TopologyTab
          sovereignId="test-sov"
          applicationName="keycloak"
          initialApp={{ name: 'keycloak', namespace: 'keycloak', status: {} } as never}
          activeTransitionJobLink={{ href: '/jobs/x:topology-transition-a-b', label: 'Topology transition running' }}
          disableNetwork
        />,
      ),
    )
    const link = await screen.findByTestId('topology-tab-active-transition-job')
    expect(link.textContent).toBe('Topology transition running')
    expect(link.querySelector('a')?.getAttribute('href')).toBe('/jobs/x:topology-transition-a-b')
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

describe('TopologyTab — bootstrap HelmRelease status poll (#3656 → #5934)', () => {
  // SUPERSEDED CONTRACT, rewritten rather than deleted. This case used to
  // assert `getApplicationStatus` was NEVER called for a bootstrap component.
  // That was correct while the endpoint 404'd forever, and it is wrong now:
  // #5836 gave /applications/{name}/status the same synthesiser fallback its
  // sibling detail endpoint has, so bp-alloy et al. DO have a status. Leaving
  // the old assertion in place would have pinned the very defect rows 67/69
  // measured — the console printing "n/a — bootstrap component" under a header
  // reading Ready. The 404-LOOP concern #3656 actually cared about is retained
  // and asserted in TopologyTab.bootstrapStatus-5934.test.tsx (the poll backs
  // off the moment the endpoint answers with nothing).
  it('#5934: DOES poll the status endpoint for a bootstrap component, and renders the answer', async () => {
    getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
    getApplicationStatus.mockResolvedValue({
      name: 'bp-alloy',
      namespace: 'flux-system',
      phase: 'Ready',
      status: {},
    })

    render(
      withProviders(
        <TopologyTab sovereignId="test-sov" applicationName="bp-alloy" namespace="flux-system" isBootstrap />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-recon-status')).toBeTruthy()
    })
    expect(getApplicationStatus).toHaveBeenCalled()
    expect(screen.queryByTestId('topology-tab-status-bootstrap')).toBeNull()
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
