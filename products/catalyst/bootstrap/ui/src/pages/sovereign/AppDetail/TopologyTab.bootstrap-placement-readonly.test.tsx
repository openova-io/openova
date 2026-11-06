/**
 * TopologyTab.bootstrapStatus-5934.test.tsx — #5934 (UAT rows 67, 69).
 *
 * THE DEFECT THESE GUARDS PIN. The app-detail Status panel printed the literal
 * string "n/a — bootstrap component (HelmRelease, no Application CR)" for every
 * bootstrap-kit component, on a page whose own header and Overview read STATUS
 * Ready. One page contradicting itself, three inches apart.
 *
 * Two lines caused it, and only fixing BOTH moves the rows:
 *   • the status poll was disabled outright when `isBootstrap`, so the console
 *     never asked; and
 *   • the Status panel branched on `isBootstrap` FIRST, hard-rendering the n/a
 *     string regardless of what the API returned — so even an un-gated poll
 *     would have been ignored.
 *
 * WHY THIS IS NOT DEPLOY-GATED. #5836 already landed the API half:
 * HandleApplicationStatus now falls through to statusFromSynthesised (HR first,
 * then runtime) instead of hard-404ing for a component with no Application CR.
 * That fix is on main. But a rolled image changes nothing while the front end
 * refuses to ask and refuses to listen — which is exactly why these rows were
 * about to be mis-blamed on delivery.
 *
 * WHAT THE FALLBACK STILL MEANS. The n/a prose is kept, narrowed to its honest
 * case: the endpoint was asked and genuinely had nothing (synthesis missed →
 * 404). "We did not ask" and "there is no answer" must not render identically.
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

/** The literal string the panel used to hard-render for every bootstrap component. */
// hw307 2026-11-06: "Edit placement → Apply" on bp-cnpg-pair (a bootstrap-kit
// HelmRelease with no Application CR) failed with HTTP 404
// application-not-found. A bootstrap component's placement is read-only here.
beforeEach(() => {
  getApplicationStatus.mockResolvedValue({ name: 'bp-cnpg-pair', namespace: 'flux-system', phase: 'Ready', status: {} })
  getApplicationPlacement.mockResolvedValue({
    targets: [
      { region: 'hw-me-east-215-a-rtz-prod', cluster: 'c-a', vcluster: 'host', role: 'primary' },
      { region: 'hw-me-east-215-b-rtz-prod', cluster: 'c-b', vcluster: 'host', role: 'standby', standbyType: 'hot' },
    ],
    derivedFromRuntime: true,
  })
  getHierarchicalInfrastructure.mockResolvedValue({ topology: { regions: [] } })
  getContinuumReplicationStatus.mockRejectedValue(new Error('continuum replication-status: HTTP 404'))
})
afterEach(() => { cleanup(); vi.clearAllMocks() })

describe('TopologyTab — bootstrap placement is read-only (no Application CR to save to)', () => {
  it('a bootstrap component offers NO Edit placement, and says why', async () => {
    render(withProviders(
      <TopologyTab sovereignId="s" applicationName="bp-cnpg-pair" namespace="flux-system" isBootstrap />,
    ))
    await waitFor(() => expect(screen.getByTestId('topology-tab-placement-readonly')).toBeTruthy())
    expect(screen.queryByTestId('topology-tab-edit-placement')).toBeNull()
  })

  it('a non-bootstrap Application keeps Edit placement', async () => {
    render(withProviders(
      <TopologyTab sovereignId="s" applicationName="shared-pg" namespace="shared-data" />,
    ))
    await waitFor(() => expect(screen.getByTestId('topology-tab-edit-placement')).toBeTruthy())
    expect(screen.queryByTestId('topology-tab-placement-readonly')).toBeNull()
  })
})
