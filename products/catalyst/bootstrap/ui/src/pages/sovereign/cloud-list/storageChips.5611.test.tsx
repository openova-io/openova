/**
 * storageChips.5611.test.tsx — #5611.
 *
 * The issue: `/cloud?view=list&kind=volumes` reported **`Volumes 0`**
 * and rendered "No volumes yet" on hw292, a Sovereign carrying 50+ EVS
 * block volumes; the sibling **`Storage Classes —`** chip rendered the
 * not-collected marker while `kubectl get storageclass` returned 2 per
 * region. Meanwhile the **PersistentVolumes** chip on the SAME row read
 * the correct union across both regions — so the data was always
 * reachable and only the storage collectors were wrong.
 *
 * What these guards assert, and why in this shape:
 *
 *  1. **On the VALUE, never the key.** A test asserting the chip
 *     "renders" or that `counts['storage-classes']` is *defined* passes
 *     on the pre-fix tree — `—` is rendered and `null` is defined. Zero
 *     is a value. Every assertion below pins a NUMBER.
 *
 *  2. **Through production code.** The counts come from
 *     `deriveKindCounts`, the function CloudPage itself calls. Handing
 *     `CloudKindChips` a literal `counts` object would assert on a value
 *     production never produces.
 *
 *  3. **Two regions.** The snapshot is built with the production
 *     `objectKey`, whose `@cluster` suffix (#5571) is what keeps both
 *     regions' copies of the identically-named `evs-ssd` StorageClass
 *     distinct. A single-region fixture would pass at half the count.
 *
 *  4. **Control.** PersistentVolumes — a kind that already collected
 *     correctly — must still read its own real number, and must not
 *     move. That is what rules out a "fix" that double-counts.
 *
 * Live hw292 numbers this fixture is scaled down from (both kubeconfigs
 * sampled 2026-08-06): region-a 57 PVs / 58 PVCs / 2 SCs, region-b 48
 * PVs / 48 PVCs / 2 SCs.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import type { K8sObject, K8sSnapshot } from '@/widgets/architecture-graph/useK8sCacheStream'
import { objectKey } from '@/widgets/architecture-graph/useK8sCacheStream'
import { deriveKindCounts } from './kindCounts'
import { CloudKindChips } from './CloudKindChips'
import { KINDS } from './kinds'

afterEach(cleanup)

/** hw292's two real cluster ids. */
const REGION_A = '1c56518035a83e03'
const REGION_B = '1c56518035a83e03-me-east-215-b-1'

function obj(
  kind: string,
  registryKind: string,
  name: string,
  cluster: string,
): [string, K8sObject] {
  const o: K8sObject = {
    kind,
    metadata: { name, uid: `uid-${cluster}-${name}` },
    clusterId: cluster,
  }
  return [objectKey(registryKind, o, cluster), o]
}

/**
 * A two-region snapshot in hw292's real shape: BOTH regions serve a
 * StorageClass named `evs-ssd` and one named `seaweedfs-storage` — the
 * names collide exactly, which is why the region-suffixed key matters.
 */
function twoRegionSnapshot(): K8sSnapshot {
  const entries: Array<[string, K8sObject]> = []
  for (const region of [REGION_A, REGION_B]) {
    entries.push(obj('StorageClass', 'storageclass', 'evs-ssd', region))
    entries.push(obj('StorageClass', 'storageclass', 'seaweedfs-storage', region))
  }
  // Control kind: PersistentVolumes, 3 in region-a + 2 in region-b.
  for (const name of ['pv-a-1', 'pv-a-2', 'pv-a-3']) {
    entries.push(obj('PersistentVolume', 'persistentvolume', name, REGION_A))
  }
  for (const name of ['pv-b-1', 'pv-b-2']) {
    entries.push(obj('PersistentVolume', 'persistentvolume', name, REGION_B))
  }
  return new Map(entries)
}

/** Read a chip's rendered count text out of the `+ More` popover. */
function overflowChipCount(kindId: string): string {
  fireEvent.click(screen.getByTestId('cloud-kind-chip-more'))
  const item = screen.getByTestId(`cloud-kind-chip-more-item-${kindId}`)
  const badge = item.querySelector('.cloud-kind-chip-count')
  return badge?.textContent ?? ''
}

describe('#5611 — storage kinds must report their real count, not a false zero or "—"', () => {
  it('GUARD: Storage Classes counts BOTH regions (4), not "—" and not one region (2)', () => {
    const counts = deriveKindCounts(undefined, twoRegionSnapshot())

    // Pre-fix this is `null` (no KIND_TO_REGISTRY mapping → never
    // overridden from the snapshot), which the chip renders as "—".
    expect(counts['storage-classes']).toBe(4)

    render(
      <CloudKindChips activeKind="clusters" counts={counts} onChange={() => {}} />,
    )
    // The rendered badge is the operator-visible claim. `—` and `2` both
    // fail here; only the cross-region union passes.
    expect(overflowChipCount('storage-classes')).toBe('4')
  })

  it('GUARD: the Storage Classes chip is wired to a collected kind (hasData)', () => {
    // hasData=false forces the chip to render "—" regardless of the
    // count, so the flag is load-bearing for the assertion above.
    const entry = KINDS.find((k) => k.id === 'storage-classes')
    expect(entry?.hasData).toBe(true)
  })

  // NOTE on vacuity: this one is GREEN on both trees by construction —
  // it feeds the fixture the union the Go loader now produces and checks
  // the UI does not lose it. The defect guard for the loader itself is
  // TestBuildStorage_UnionsBothRegions_5611 (Go), which reads 3 instead
  // of 5 on the pre-fix tree. Stated plainly rather than labelled GUARD.
  it('COHERENCE: Volumes and PersistentVolumes must never disagree', () => {
    // The topology response carries the per-region union the Go loader
    // now produces (storageSources fan-out): 3 region-a + 2 region-b.
    const data = {
      cloud: [],
      topology: { pattern: 'active-passive', regions: [] },
      storage: {
        pvcs: [],
        buckets: [],
        volumes: [
          { id: 'pv-a-1', name: 'pv-a-1', capacity: '50Gi', region: 'me-east-215-a', attachedTo: '', status: 'healthy' },
          { id: 'pv-a-2', name: 'pv-a-2', capacity: '50Gi', region: 'me-east-215-a', attachedTo: '', status: 'healthy' },
          { id: 'pv-a-3', name: 'pv-a-3', capacity: '50Gi', region: 'me-east-215-a', attachedTo: '', status: 'healthy' },
          { id: 'pv-b-1', name: 'pv-b-1', capacity: '50Gi', region: 'me-east-215-b', attachedTo: '', status: 'healthy' },
          { id: 'pv-b-2', name: 'pv-b-2', capacity: '50Gi', region: 'me-east-215-b', attachedTo: '', status: 'healthy' },
        ],
      },
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any

    const counts = deriveKindCounts(data, twoRegionSnapshot())

    // The coherence property #5611 is really about: Volumes and
    // PersistentVolumes project the SAME underlying objects, so they
    // must never disagree. Pre-fix the loader read region-a only, so
    // this read 3 while the PV chip read 5.
    expect(counts['volumes']).toBe(5)
    expect(counts['volumes']).toBe(counts['persistentvolumes'])
  })

  it('CONTROL: PersistentVolumes still reports its own real number (no double count)', () => {
    const counts = deriveKindCounts(undefined, twoRegionSnapshot())
    // 3 region-a + 2 region-b = 5. Green on the pre-fix tree too — this
    // is the control that proves the fix did not inflate every kind.
    expect(counts['persistentvolumes']).toBe(5)
  })

  it('CONTROL: an uncollected kind still reports "—", so "—" was never unreachable', () => {
    // DNS Zones is still hasData:false (PowerDNS, not K8s). If this
    // rendered a number the "—" assertions above would be vacuous.
    const counts = deriveKindCounts(undefined, twoRegionSnapshot())
    render(
      <CloudKindChips activeKind="clusters" counts={counts} onChange={() => {}} />,
    )
    expect(overflowChipCount('dns-zones')).toBe('—')
  })
})
