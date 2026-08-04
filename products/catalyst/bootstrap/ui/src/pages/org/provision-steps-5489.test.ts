// Tests for #5489 — the post-create timeline rendered a `vCluster` step for
// namespace-isolated Organizations. The API returned `steps.vcluster:
// "done"` for a tier that never provisions a vCluster (proven live on hw291
// — zero vclusters.vcluster.com resources), and the SPA's fixed step list
// painted the dot regardless. The API now omits the key for namespace-tier
// Orgs; provisionStepItems renders only the steps the payload carries.
//
// Anti-theater: the omission assertion fails against the pre-fix fixed list;
// the full-payload control pins that a vcluster-tier timeline still renders
// all six steps in order — a fix that dropped the step everywhere would pass
// the first half while erasing a real step from real vcluster-tier Orgs.

import { describe, it, expect } from 'vitest'
import { provisionStepItems } from './CreateOrganizationPage'
import type { OrgProvisionSteps } from './org.api'

const REST: Omit<OrgProvisionSteps, 'vcluster'> = {
  bp_charts: 'done',
  dns: 'done',
  certs: 'done',
  keycloak_clients: 'done',
  registry: 'done',
}

describe('#5489 provision timeline must not paint a vCluster step over nothing', () => {
  it('omits the vCluster step when the payload carries none (namespace tier)', () => {
    const items = provisionStepItems({ ...REST })
    expect(items.map((i) => i.key)).not.toContain('vcluster')
    // Control within the case: the remaining timeline survives, in order.
    expect(items.map((i) => i.key)).toEqual([
      'bp_charts',
      'dns',
      'certs',
      'keycloak_clients',
      'registry',
    ])
  })

  it('keeps the vCluster step for a payload that carries one (vcluster tier)', () => {
    const items = provisionStepItems({ vcluster: 'done', ...REST })
    expect(items[0]).toEqual({ key: 'vcluster', label: 'vCluster', state: 'done' })
    expect(items).toHaveLength(6)
  })

  it('carries per-step state through unchanged (failed boundary still renders red)', () => {
    const items = provisionStepItems({
      vcluster: 'failed',
      bp_charts: 'pending',
      dns: 'pending',
      certs: 'pending',
      keycloak_clients: 'pending',
      registry: 'pending',
    })
    expect(items[0]?.state).toBe('failed')
    expect(items[1]?.state).toBe('pending')
  })
})
