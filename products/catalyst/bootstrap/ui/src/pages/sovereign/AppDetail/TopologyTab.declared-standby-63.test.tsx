/**
 * TopologyTab.declared-standby-63.test.tsx — UAT row 63, the residual half.
 *
 * Row 63's clause: an Application **declaring** active-hot-standby with NO
 * Continuum backing renders an honest no-backing DR section — "Switchover
 * unavailable" copy, the control DISABLED, and NO synthesized lag / standby /
 * target-region values.
 *
 * #6061 (`c4dd18f21`) fixed the half where `status.perCluster` SHADOWED a
 * `status.targets` that already named the pair. That half is delivered and is
 * locked by TopologyTab.statusTargets-shadowed.test.tsx. This file locks the
 * half underneath it, which #6061 did not touch:
 *
 *   showDR = hasStandby || hasLiveDR
 *
 * Both terms are derived from OBSERVED state — a projected Standby target, or
 * a live Continuum reading. Neither consults what the Application CR DECLARES.
 * So an app whose `spec.placement.mode` says `active-hot-standby` while nothing
 * has been observed yet (empty runtime placement, empty `status.targets`, and a
 * `status.perCluster` that saw only region A) gets `showDR === false` and the
 * whole `topology-tab-dr-*` subtree VANISHES.
 *
 * That is a verdict published from ABSENT evidence: the page states "this app
 * has no DR" by omission, when the truth on the object is "a standby is
 * declared and nothing has reported on it yet". An absent control and a
 * disabled control are not the same statement, which is the precise confusion
 * row 63 exists to forbid.
 *
 * THE CONTROL SHARES THE SUSPECT PROPERTY. `SINGLETON_SAME_OBSERVATION` is
 * byte-identical to the failing fixture — same empty runtime placement, same
 * empty `status.targets`, same one-entry `status.perCluster`, same pending
 * Continuum answer — and differs ONLY in the declared mode. It must keep
 * rendering NO DR panel (row 58: never arm a DR block against a phantom
 * region). Without that half this file would pass just as well against a
 * change that rendered a DR section for every application in the catalog.
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
 * The hw293 `hw293walkone/uat50-ahs-pg` shape, verbatim in structure: the CR
 * DECLARES active-hot-standby with a standby region, and NOTHING has observed
 * that standby — no runtime targets, no `status.targets`, and a
 * `status.perCluster` carrying the single `singleton` entry the
 * application-controller wrote after fanning the HelmRelease out to region A
 * only.
 */
const DECLARED_AHS_NOTHING_OBSERVED = {
  name: 'uat50-ahs-pg',
  namespace: 'hw293walkone',
  spec: { placement: { mode: 'active-hot-standby', standbyRegions: [REGION_B] } },
  status: { perCluster: [{ cluster: REGION_A, role: 'singleton' }] },
}

/**
 * THE CONTROL — identical in every observable respect, declaring `singleton`.
 * Same empty runtime placement, same absent status.targets, same one-entry
 * perCluster, same pending Continuum answer.
 */
const SINGLETON_SAME_OBSERVATION = {
  name: 'uat50-single-pg',
  namespace: 'hw293walkone',
  spec: { placement: { mode: 'singleton' } },
  status: { perCluster: [{ cluster: REGION_A, role: 'singleton' }] },
}

beforeEach(() => {
  vi.clearAllMocks()
  // Rung 1 answers EMPTY — the live hw293 reading was
  // {"targets":[],"derivedFromRuntime":true}.
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
  getCatalogItem.mockResolvedValue({ name: 'bp-postgres', placementCapability: 'active-hot-standby' })
  getHierarchicalInfrastructure.mockResolvedValue({ regions: [] })
  // NO live Continuum backing — the `source:"pending"` fallback envelope the
  // endpoint really answers with (#5514). Never an error, never `live`.
  getContinuumReplicationStatus.mockResolvedValue({ source: 'pending' })
})

afterEach(() => cleanup())

describe('row 63 — a DECLARED standby with nothing observed must render the disabled control, not vanish', () => {
  it('renders the DR section with a DISABLED switchover and a named reason', async () => {
    getApplicationStatus.mockResolvedValue(DECLARED_AHS_NOTHING_OBSERVED)
    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-1"
          applicationName="uat50-ahs-pg"
          namespace="hw293walkone"
        />,
      ),
    )

    // Anchor on the SUBJECT first — the replication answer this case is about —
    // so the assertions below cannot pass on a page that has not painted.
    await waitFor(() => expect(getContinuumReplicationStatus).toHaveBeenCalled())

    // Clause part 1 + 2: the control EXISTS and is DISABLED.
    const btn = await screen.findByTestId('topology-tab-dr-switchover-open')
    expect(btn).toBeTruthy()
    expect((btn as HTMLButtonElement).disabled).toBe(true)

    // Clause part 1: the "Switchover unavailable" copy, with a reason.
    const reason = screen.getByTestId('topology-tab-dr-switchover-reason')
    expect(reason.textContent).toContain('Switchover unavailable')

    // Clause part 3: NO synthesized lag / standby / target-region values.
    // These must stay absent — the disabled control is the whole statement.
    expect(screen.queryByTestId('topology-tab-dr-lag-value')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-regions')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-standby')).toBeNull()
  })

  it('names the declaration as the reason the section is there at all', async () => {
    getApplicationStatus.mockResolvedValue(DECLARED_AHS_NOTHING_OBSERVED)
    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-1"
          applicationName="uat50-ahs-pg"
          namespace="hw293walkone"
        />,
      ),
    )

    // The operator must be told WHY a DR section is showing for an app whose
    // placement panel reads `singleton`: the CR declares a standby and nothing
    // has reported on it. Silence here is the same absent-evidence defect one
    // level down.
    const note = await screen.findByTestId('topology-tab-dr-unbacked')
    expect(note.textContent).toContain('DECLARES a standby')
  })

  it('CONTROL — the same observation declaring `singleton` still shows NO DR panel', async () => {
    getApplicationStatus.mockResolvedValue(SINGLETON_SAME_OBSERVATION)
    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-1"
          applicationName="uat50-single-pg"
          namespace="hw293walkone"
        />,
      ),
    )

    // Same anchor as the positive case, so this negative cannot pass merely
    // because nothing has rendered yet.
    await waitFor(() => expect(getContinuumReplicationStatus).toHaveBeenCalled())
    await waitFor(() => expect(screen.getByTestId('topology-tab-placement-panel')).toBeTruthy())

    expect(screen.queryByTestId('topology-tab-dr-panel')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-switchover')).toBeNull()
    expect(screen.queryByTestId('topology-tab-dr-switchover-open')).toBeNull()
  })
})
