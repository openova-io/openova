/**
 * TopologyTab.dr.test.tsx — #3375 DR read-back + DR-UI honesty.
 *
 * Locks the hw158-walk fixes:
 *   1. The Topology tab READS BACK the live DR state for an app — the observed
 *      primary/standby/phase/lag from GET /continuums/dr-<app> — not a blank
 *      declared-only editor.
 *   2. Replication lag renders the REAL live value, never the hardcoded "—".
 *   3. When no live DR pair exists (the API 404s), the observed strip says so
 *      honestly AND the Switchover control is DISABLED (not armed against a
 *      phantom dr-<app> Continuum).
 *
 * All generic: the tab keys on the app's declared topology + the live
 * Continuum record, never on a blueprint name.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

/* ── Mock the API + infra clients so no network is hit ─────────────────── */

const getApplicationStatus = vi.fn()
const getCatalogItem = vi.fn()
const getHierarchicalInfrastructure = vi.fn()
const getContinuum = vi.fn()

vi.mock('@/lib/catalog.api', () => ({
  getApplicationStatus: (...a: unknown[]) => getApplicationStatus(...a),
  getCatalogItem: (...a: unknown[]) => getCatalogItem(...a),
}))

vi.mock('@/lib/infrastructure.types', () => ({
  getHierarchicalInfrastructure: (...a: unknown[]) => getHierarchicalInfrastructure(...a),
}))

// Keep the real parseLagSeconds (pure helper) but stub the network call.
vi.mock('@/lib/continuum.api', async (orig) => {
  const actual = (await orig()) as Record<string, unknown>
  return {
    ...actual,
    getContinuum: (...a: unknown[]) => getContinuum(...a),
  }
})

// Stub the heavy editor; the DR section is NOT stubbed here — we assert its
// armed/disabled state reflects the tab-derived drPairLive.
vi.mock('@/widgets/topology/TopologyEditor', () => ({
  TopologyEditor: () => <div data-testid="stub-topology-editor" />,
}))

import { TopologyTab } from './TopologyTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

// A 2-region infra so the app's declared active-hot-standby class is the
// effective class (isMultiRegion=true → showDR=true).
const TWO_REGION_INFRA = {
  topology: {
    regions: [
      { name: 'hw-me-east-215-a-rtz-prod', providerRegion: 'me-east-215-a' },
      { name: 'hw-me-east-215-b-rtz-prod', providerRegion: 'me-east-215-b' },
    ],
  },
}

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('TopologyTab — reads back LIVE DR state (#3375)', () => {
  it('renders the observed primary/standby/phase + REAL replication lag from the live Continuum', async () => {
    getHierarchicalInfrastructure.mockResolvedValue(TWO_REGION_INFRA)
    getApplicationStatus.mockResolvedValue({
      name: 'grafana',
      namespace: 'flux-system',
      spec: { placement: 'active-hotstandby', regions: [] },
      status: {},
    })
    // The live Continuum record (could be a reconciled CR or the derived
    // live-cnpg-pair projection — same shape) with a REAL lag.
    getContinuum.mockResolvedValue({
      name: 'dr-grafana',
      namespace: 'shared-data',
      uid: '',
      spec: {
        applicationRef: 'grafana',
        primaryRegion: 'me-east-215-a',
        hotStandbyRegions: ['me-east-215-b'],
        switchover: { mechanism: 'cnpg-pair' },
        source: 'live-cnpg-pair',
      },
      status: {
        phase: 'Healthy',
        primaryRegion: 'me-east-215-a',
        replicaRegion: 'me-east-215-b',
        replicationHealthy: true,
        replicationLagSeconds: 7,
      },
    })

    render(
      withProviders(
        <TopologyTab sovereignId="hw158" applicationName="grafana" namespace="shared-data" />,
      ),
    )

    // The observed strip surfaces the LIVE primary + standby.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-observed-live')).toBeTruthy()
    })
    expect(screen.getByTestId('topology-tab-observed-primary').textContent).toContain('me-east-215-a')
    expect(screen.getByTestId('topology-tab-observed-standby').textContent).toContain('me-east-215-b')
    expect(screen.getByTestId('topology-tab-observed-lag').textContent).toContain('7s')

    // The "Live status" replication-lag reads the REAL value, NOT "—".
    const lagLive = await screen.findByTestId('topology-tab-replication-lag-live')
    expect(lagLive.textContent).toContain('7s')

    // A live pair exists → the Switchover button is ARMED (owner is the
    // default caller tier? No — callerTier undefined → not owner → hidden).
    // We assert the honest disabled-for-tier path is NOT the no-pair path.
    expect(screen.queryByTestId('topology-tab-replication-lag-nopair')).toBeNull()
  })

  it('shows the honest "no live DR pair" state + DISABLED Switchover when the Continuum 404s', async () => {
    getHierarchicalInfrastructure.mockResolvedValue(TWO_REGION_INFRA)
    getApplicationStatus.mockResolvedValue({
      name: 'grafana',
      namespace: 'flux-system',
      spec: { placement: 'active-hotstandby', regions: [] },
      status: {},
    })
    // No live pair on this Sovereign — the backend 404s (the fetch throws).
    getContinuum.mockRejectedValue(new Error('continuum get: HTTP 404'))

    render(
      withProviders(
        <TopologyTab
          sovereignId="hw158"
          applicationName="grafana"
          namespace="shared-data"
          callerTier="owner"
        />,
      ),
    )

    // Observed strip is honest: no live DR pair.
    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-observed-nopair')).toBeTruthy()
    })
    // Replication lag does NOT print a bare "—" value — it says no pair.
    expect(screen.getByTestId('topology-tab-replication-lag-nopair')).toBeTruthy()

    // Critically: the DR section's Switchover is DISABLED (honest), NEVER
    // armed against the phantom dr-grafana. Owner tier, but no live pair.
    await waitFor(() => {
      expect(screen.getByTestId('continuum-dr-switchover-no-pair')).toBeTruthy()
    })
    expect(screen.queryByTestId('continuum-dr-switchover-btn')).toBeNull()
  })

  it('does NOT fetch a Continuum for a non-DR-capable app (no observed strip)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue(TWO_REGION_INFRA)
    getApplicationStatus.mockResolvedValue({
      name: 'cilium',
      namespace: 'kube-system',
      spec: { placement: 'single-region', regions: [] },
      status: {},
    })

    render(
      withProviders(
        <TopologyTab sovereignId="hw158" applicationName="cilium" namespace="kube-system" />,
      ),
    )

    await waitFor(() => {
      expect(screen.getByTestId('topology-tab-declared-panel')).toBeTruthy()
    })
    // cilium's effective class is not DR-capable → no observed strip, no
    // continuum fetch (the gate keys on showDR, not the app name).
    expect(screen.queryByTestId('topology-tab-observed-strip')).toBeNull()
    expect(getContinuum).not.toHaveBeenCalled()
  })

  // #3829 — the "Effective class" honest-floor. A DR-capable mandate that is
  // NOT realized by a live Continuums pair must read as a DEGRADED singleton,
  // never the unbuilt mandate parroted as effective. openbao on a 2-region
  // prov declares active-passive but (default-OFF Continuum, no cnpg-pair)
  // has no live pair → the field must say "singleton (DEGRADED — active-passive
  // mandate unbuilt)", not "active-passive (multi-region · 2 regions)".
  it('Effective class reads DEGRADED-singleton when the declared DR mandate has no live pair (#3829)', async () => {
    getHierarchicalInfrastructure.mockResolvedValue(TWO_REGION_INFRA)
    // Bootstrap app: no Application CR → status fetch yields nothing.
    getApplicationStatus.mockRejectedValue(new Error('application status: HTTP 404'))
    // No live Continuum pair on this Sovereign — the backend 404s.
    getContinuum.mockRejectedValue(new Error('continuum get: HTTP 404'))

    render(
      withProviders(
        <TopologyTab sovereignId="hw165" applicationName="openbao" namespace="openbao" />,
      ),
    )

    const eff = await screen.findByTestId('topology-tab-declared-effective')
    await waitFor(() => {
      expect(eff.textContent).toContain('DEGRADED')
    })
    // The honest floor: a degraded singleton naming the unbuilt mandate …
    expect(eff.textContent).toContain('singleton')
    expect(eff.textContent).toContain('active-passive mandate unbuilt')
    // … and crucially NOT the mandate presented as a realized multi-region class.
    expect(eff.textContent).not.toContain('multi-region')
  })
})
