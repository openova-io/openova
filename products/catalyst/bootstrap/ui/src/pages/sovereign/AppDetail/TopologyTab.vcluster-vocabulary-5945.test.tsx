/**
 * TopologyTab.vcluster-vocabulary-5945.test.tsx — #5945 regression lock.
 *
 * MEASURED (hw292, 2026-08-10, `/app/uatco-agenity` → Topology → Edit
 * placement): the vCluster select offered `host | mgmt | dmz | rtz` with
 * **mgmt preselected**. #4325 deleted bp-mgmt-vcluster / bp-dmz-vcluster /
 * bp-rtz-vcluster, so no post-#4325 Sovereign has any of those namespaces —
 * the 45-namespace census shows zero, and the only vClusters are per-Org.
 * Choosing one writes a placement targeting a vCluster that cannot exist, and
 * the per-Org vClusters that DO exist were not offered at all.
 *
 * THE MECHANISM, which is why the fix is here and not in the editor. #5616
 * already narrowed PlacementEditor's own default to ['host']. But:
 *   1. TopologyTab's `status.perCluster` projection hardcoded `vcluster:
 *      'mgmt'` on every derived target — `perCluster` reports a CLUSTER and
 *      carries no vCluster at all, so that value was invented; and
 *   2. the editor deliberately re-admits whatever the current target names
 *      (so an existing placement is never silently rewritten), which turned
 *      the invented 'mgmt' back into a selectable option AND the select's
 *      current value; and
 *   3. TopologyTab passed no `availableVClusters`, so the real per-Org
 *      vClusters in the live topology were never offered.
 *
 * Same dead-vocabulary class as #5932/#5937 (the dashboard treemap grouping on
 * a label only the deleted charts ever wrote). This file renders the REAL
 * PlacementEditor — a stub could not see the select at all.
 */
import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
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
  updateApplication: vi.fn(),
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

import { TopologyTab } from './TopologyTab'

function withProviders(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

/**
 * The live hw292 topology: ONE per-Org vCluster, named "vcluster" but running
 * in the Org's own host namespace (`uatco`). No mgmt/dmz/rtz namespace exists.
 */
const LIVE_TOPOLOGY = {
  topology: {
    regions: [
      {
        id: 'r-a',
        name: 'hw-me-east-215-a-rtz-prod',
        clusters: [
          {
            id: 'c-a',
            name: 'hw-me-east-215-a-rtz-prod',
            vclusters: [{ id: 'vc-1', name: 'vcluster', namespace: 'uatco' }],
          },
        ],
      },
    ],
  },
}

/**
 * An Application whose ONLY placement signal is `status.perCluster` — the
 * projection that used to stamp the invented `mgmt`. This is the shape
 * #5945 was measured on.
 */
const PER_CLUSTER_APP = {
  name: 'uatco-agenity',
  namespace: 'uatco',
  spec: {},
  status: {
    placementRecon: 'Reconciled',
    perCluster: [{ cluster: 'hw-me-east-215-a-rtz-prod', role: 'primary' }],
  },
}

async function openEditor() {
  render(
    withProviders(
      <TopologyTab
        sovereignId="dep-hw292"
        applicationName="uatco-agenity"
        namespace="uatco"
        initialApp={PER_CLUSTER_APP as never}
      />,
    ),
  )
  const edit = await screen.findByTestId('topology-tab-edit-placement')
  fireEvent.click(edit)
  return (await screen.findByTestId('placement-editor-target-0-vcluster')) as HTMLSelectElement
}

function optionValues(select: HTMLSelectElement): string[] {
  return Array.from(select.querySelectorAll('option')).map((o) => o.value)
}

beforeEach(() => {
  getApplicationPlacement.mockResolvedValue({ targets: [], derivedFromRuntime: true })
  getHierarchicalInfrastructure.mockResolvedValue(LIVE_TOPOLOGY)
  getContinuumReplicationStatus.mockRejectedValue(new Error('HTTP 404'))
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

describe('#5945 — the placement editor offers only vClusters that exist', () => {
  it('does not offer any of the #4325-deleted tiers', async () => {
    const select = await openEditor()
    const values = optionValues(select)
    for (const dead of ['mgmt', 'dmz', 'rtz']) {
      expect(values).not.toContain(dead)
    }
  })

  it('does not PRESELECT a retired vCluster', async () => {
    const select = await openEditor()
    expect(select.value).not.toBe('mgmt')
    expect(select.value).toBe('host')
  })

  it('offers the OBSERVED per-Org vCluster by its host namespace, plus host', async () => {
    const select = await openEditor()
    const values = optionValues(select)
    // `uatco` is the host namespace of the live vCluster — the real placement
    // identity (#5616). The display name ("vcluster") is not addressable.
    expect(values).toContain('uatco')
    expect(values).toContain('host')
    expect(values).not.toContain('vcluster')
  })

  it('CONTROL — the projected target carries no invented vCluster, and the card says host', async () => {
    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-hw292"
          applicationName="uatco-agenity"
          namespace="uatco"
          initialApp={PER_CLUSTER_APP as never}
        />,
      ),
    )
    const card = await screen.findByTestId('topology-tab-target-card-0')
    expect(card.textContent).not.toContain('mgmt')
    expect(card.textContent).toContain('host')
  })

  it('CONTROL — an Application that GENUINELY declares a legacy vCluster keeps it selectable', async () => {
    // #5616's invariant must survive: the editor may never silently rewrite a
    // placement an operator really committed. This is what makes the three
    // assertions above a statement about INVENTED values, not a blanket ban.
    const declared = {
      name: 'legacy-app',
      namespace: 'uatco',
      spec: {
        placement: {
          targets: [
            {
              region: 'hw-me-east-215-a-rtz-prod',
              cluster: 'hw-me-east-215-a-rtz-prod',
              vcluster: 'mgmt',
              role: 'Primary',
            },
          ],
        },
      },
      status: { placementRecon: 'Reconciled' },
    }
    render(
      withProviders(
        <TopologyTab
          sovereignId="dep-hw292"
          applicationName="legacy-app"
          namespace="uatco"
          initialApp={declared as never}
        />,
      ),
    )
    fireEvent.click(await screen.findByTestId('topology-tab-edit-placement'))
    const select = (await screen.findByTestId(
      'placement-editor-target-0-vcluster',
    )) as HTMLSelectElement
    expect(select.value).toBe('mgmt')
    expect(optionValues(select)).toContain('mgmt')
  })
})
