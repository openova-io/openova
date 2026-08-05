/**
 * modes.test.ts — the matrix-derived mode-description tests (#3905),
 * preserved from the deleted `TopologyEditor.test.tsx` (#5609).
 *
 * WHY THIS FILE EXISTS AS PURE-FUNCTION TESTS: the old file rendered a
 * `TopologyEditor` component that **production never mounted** (superseded by
 * `PlacementEditor` in #3969, which deliberately has no mode picker). Those
 * render tests asserted radio-button state on a surface no operator could
 * reach, so they inflated apparent coverage of the mode picker. Only the
 * assertions below exercise logic production actually calls —
 * `describeModeForComponent` is imported by the create dialog
 * (`NewInstanceDialog`), which is the ONE production mode picker.
 *
 * The picker's *gating* behaviour is covered on the real rendered surface in
 * `pages/sovereign/AppDetail/InstancesSection.topology-gate.test.tsx`.
 */
import { describe, it, expect } from 'vitest'

import { describeModeForComponent, describeMode, canonicalizeMode } from './modes'
import { TOPOLOGY_BY_ID } from '@/shared/constants/catalog.generated'

describe('#3905 — matrix-derived mode descriptions', () => {
  // The exact bug the founder flagged: the card derived openbao's
  // active-passive as `raft · async` warm-standby from the topology matrix,
  // while the edit dropdown printed a GENERIC "…(backup-restore)" string for
  // every component. The picker now reads the SAME matrix, so its
  // active-passive helper text must mirror the matrix contract and never say
  // "backup-restore".
  const openbaoTopology = TOPOLOGY_BY_ID['bp-openbao']

  it('describeModeForComponent for openbao active-passive exactly matches the matrix variant', () => {
    expect(openbaoTopology).toBeTruthy()
    const text = describeModeForComponent('active-passive', openbaoTopology).toLowerCase()
    const v = openbaoTopology.perTopology['active-passive']
    // Every field the picker shows is sourced from the matrix, not hardcoded.
    expect(v?.replication?.backend).toBe('raft')
    expect(v?.replication?.mode).toBe('async')
    expect(v?.switchover?.mechanism).toBe('raft-transition')
    expect(text).toContain(`${v!.replication!.backend} · ${v!.replication!.mode}`.toLowerCase())
    expect(text).toContain('raft-transition switchover')
    expect(text).not.toContain('backup-restore')
  })

  it('falls back to the generic one-liner when no matrix is supplied', () => {
    // An app with no spec.topology block → no `topology` arg → generic copy.
    const text = describeModeForComponent('active-passive', undefined).toLowerCase()
    expect(text).toContain('promotes on failover')
    // The generic fallback is mechanism-neutral; it must not claim backup-restore.
    expect(text).not.toContain('backup-restore')
  })

  it('singleton uses the generic one-liner even with a matrix (no DR contract to derive)', () => {
    const text = describeModeForComponent('singleton', openbaoTopology).toLowerCase()
    expect(text).toContain('one cluster')
  })
})

describe('#3375 DoD-1 — legacy spellings fold onto the canonical token', () => {
  it('canonicalizeMode folds the legacy editor dialect', () => {
    expect(canonicalizeMode('single-region')).toBe('singleton')
    expect(canonicalizeMode('active-hotstandby')).toBe('active-hot-standby')
    // active-passive has no legacy alias — it round-trips unchanged.
    expect(canonicalizeMode('active-passive')).toBe('active-passive')
  })

  it('describeMode describes every canonical class, including active-passive', () => {
    for (const m of ['singleton', 'active-active', 'active-hot-standby', 'active-passive']) {
      expect(describeMode(m)).not.toBe('')
    }
  })
})
