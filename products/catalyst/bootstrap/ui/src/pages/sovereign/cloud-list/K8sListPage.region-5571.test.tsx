/**
 * K8sListPage.region-5571.test.tsx — #5571 item (2): the Cloud list
 * pages must SHOW which region each object came from.
 *
 * The server fans `/k8s/stream` out across every region and the SPA
 * snapshot now keeps both regions' objects (see
 * `useK8sCacheStream.region-split-5571.test.ts`). This file pins the
 * last leg: the operator has to be able to SEE it. Without a region
 * column, two rows with the identical namespace/name/age read as a
 * duplicate-render bug rather than as "this policy exists in both
 * regions", and a set that covers only one region is indistinguishable
 * from a complete estate — which is the exact failure #5571 describes
 * on the NetworkPolicies / CiliumNetworkPolicies pages.
 *
 * Snapshot keys here are built with the PRODUCTION `objectKey`
 * function, never hand-written, so this fixture cannot drift away from
 * the real key contract.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'
import { cleanup, render, screen, within } from '@testing-library/react'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import { objectKey } from '@/widgets/architecture-graph/useK8sCacheStream'
import { regionLabel } from './k8sColumns'

/** hw292's two real cluster-id conventions. */
const REGION_A = 'sovereign-hw292.omani.works'
const REGION_B = '1c56518035a83e03-me-east-215-b-1'

const cloudState: {
  k8sSnapshot: K8sSnapshot
  k8sStatus: string
  k8sRevision: number
  deploymentId: string
} = {
  k8sSnapshot: new Map(),
  k8sStatus: 'open',
  k8sRevision: 1,
  deploymentId: '1c56518035a83e03',
}

vi.mock('../CloudPage', () => ({ useCloud: () => cloudState }))

afterEach(cleanup)

/**
 * Body rows carry `role="link"` (they are click-navigable), which
 * OVERRIDES the implicit `row` role — so query the DOM directly rather
 * than by role, or only the <thead> row is ever found.
 */
function bodyRows(table: HTMLElement): HTMLElement[] {
  return [...table.querySelectorAll('tbody tr')] as HTMLElement[]
}

// Imported AFTER the mock so the page picks up the stubbed useCloud.
const { K8sListPage } = await import('./K8sListPage')

function np(ns: string, name: string, cluster: string): [string, K8sObject] {
  const obj: K8sObject = {
    apiVersion: 'networking.k8s.io/v1',
    kind: 'NetworkPolicy',
    metadata: { namespace: ns, name, uid: `uid-${cluster}-${ns}-${name}` },
    clusterId: cluster,
  }
  return [objectKey('networkpolicy', obj, cluster), obj]
}

const COLUMNS = [
  { header: 'Namespace', extract: (o: K8sObject) => o.metadata?.namespace ?? '—' },
  { header: 'Name', extract: (o: K8sObject) => o.metadata?.name ?? '—' },
]

function renderList(entries: Array<[string, K8sObject]>) {
  cloudState.k8sSnapshot = new Map(entries)
  cloudState.k8sRevision += 1
  return render(
    <K8sListPage
      kind="networkpolicy"
      title="Network Policies"
      tagline="Namespaced ingress/egress rules"
      columns={COLUMNS}
    />,
  )
}

describe('#5571 — Cloud list must attribute every row to its region', () => {
  it('GUARD: the same policy in two regions renders TWO rows carrying DIFFERENT regions', () => {
    renderList([
      // Real hw292 collision — present in both regions.
      np('gitea', 'gitea-default-deny', REGION_A),
      np('gitea', 'gitea-default-deny', REGION_B),
    ])

    const table = screen.getByTestId('cloud-networkpolicy-table')
    const rows = bodyRows(table)

    expect(
      rows.length,
      '#5571: gitea/gitea-default-deny exists in BOTH hw292 regions — the list must show ' +
        'both, not silently collapse them to one row.',
    ).toBe(2)

    // A "Region" header must exist…
    expect(
      within(table).getByText('Region'),
      '#5571: no Region column — a one-region set is indistinguishable from the whole estate.',
    ).toBeTruthy()

    // …and the two rows must carry DIFFERENT, correctly-attributed
    // regions. Asserting on the VALUE: a Region column that rendered
    // the same label (or an empty one) for both rows would satisfy a
    // header-only or non-empty-only check while still telling the
    // operator nothing.
    const regions = rows.map((r) => r.getAttribute('data-region'))
    expect(regions.slice().sort()).toEqual([REGION_B, REGION_A].sort())

    const cellText = rows.map((r) => [...r.querySelectorAll('td')].at(-1)?.textContent)
    expect(cellText.slice().sort()).toEqual(
      [regionLabel(REGION_A), regionLabel(REGION_B)].sort(),
    )
    expect(new Set(cellText).size, 'both rows rendered the SAME region label').toBe(2)
  })

  it('CONTROL: a single-region list still renders its rows and its region (passes pre- AND post-fix)', () => {
    // Shares the harness, the mock and the render helper with the guard
    // above but exercises no cross-region collision, so it holds on
    // both trees — if the suite were disabled or the mock neutered to
    // silence the guard, this would go red too.
    renderList([
      np('gitea', 'gitea-default-deny', REGION_A),
      np('alloy', 'alloy-default-deny', REGION_A),
    ])
    const table = screen.getByTestId('cloud-networkpolicy-table')
    const rows = bodyRows(table)
    expect(rows.length).toBe(2)
    expect(within(table).getByText('gitea-default-deny')).toBeTruthy()
    expect(within(table).getByText('alloy-default-deny')).toBeTruthy()
  })
})

describe('#5571 — regionLabel', () => {
  it('renders both real cluster-id conventions, and never blanks a known region', () => {
    // `<depID>-<region>` → the region.
    expect(regionLabel('1c56518035a83e03-me-east-215-b-1')).toBe('me-east-215-b-1')
    // chroot primary alias → "primary".
    expect(regionLabel('sovereign-hw292.omani.works')).toBe('primary')
    // Unrecognised id renders VERBATIM — never blank, because a blank
    // region cell is the "partial set reads as complete" failure this
    // column exists to prevent.
    expect(regionLabel('some-unknown-shape')).toBe('some-unknown-shape')
    // Only a genuinely absent region gets the em-dash.
    expect(regionLabel('')).toBe('—')
  })
})
