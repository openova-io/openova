// Tests for #5489 — the Organizations directory asserted a vCluster that
// does not exist.
//
// On hw291 the parent row rendered `isolation: vcluster` while
// `kubectl get vclusters.vcluster.com -A` returned *No resources found*,
// `vcluster-system` was empty, and the topology API reported vclusters=0 in
// both regions. It was not a mislabelled API field: `GET /api/v1/sovereign/
// self` returns only {deploymentId, sovereignFQDN}, so there was no value to
// derive from — the literal was invented in the client.
//
// The sovereign root IS the cluster; it is not isolated inside one.
//
// Anti-theater: the first assertion is the one that fails against the pre-fix
// code. The rest are the control — a fix that returned undefined, or dropped
// the row, would satisfy "not vcluster" while breaking the directory, so the
// shape is pinned too.

import { describe, it, expect } from 'vitest'
import { parentRowFromSelf } from './organizations.api'

describe('#5489 parent row must not claim a vCluster', () => {
  const self = { deploymentId: '2c2d746b578c636b', sovereignFQDN: 'hw291.omantel.biz' }

  it('does not assert vcluster isolation for the sovereign root', () => {
    expect(parentRowFromSelf(self).isolation).not.toBe('vcluster')
  })

  it('reports the sovereign root as the cluster itself', () => {
    expect(parentRowFromSelf(self).isolation).toBe('cluster')
  })

  it('still renders a usable parent row', () => {
    const row = parentRowFromSelf(self)
    expect(row.isParent).toBe(true)
    // UAT row 25 — this assertion used to demand the FQDN here while the
    // sibling case below demanded 'sovereign'. Those two lines together
    // PINNED the divergence as if it were intent: the same row got a
    // different identity depending on whether GET /v1/sovereign/self had
    // succeeded, and that slug is both the directory's link target and the
    // detail page's lookup key, so one of the two always 404'd. The identity
    // is now a constant; the FQDN remains pinned on displayName and
    // consoleHost below, which is what "usable" actually means for this row.
    expect(row.slug).toBe('sovereign')
    expect(row.displayName).toBe('hw291.omantel.biz')
    expect(row.id).toBe('2c2d746b578c636b')
    expect(row.consoleHost).toBe('console.hw291.omantel.biz')
  })

  it('degrades safely when self-discovery has not resolved', () => {
    const row = parentRowFromSelf(null)
    expect(row.isolation).toBe('cluster')
    expect(row.id).toBe('__parent__')
    expect(row.slug).toBe('sovereign')
  })

  // The identity is the SAME in both states — the property the two cases
  // above only imply by each naming the same literal.
  it('carries one identity regardless of self-discovery (UAT row 25)', () => {
    expect(parentRowFromSelf(self).slug).toBe(parentRowFromSelf(null).slug)
  })
})
