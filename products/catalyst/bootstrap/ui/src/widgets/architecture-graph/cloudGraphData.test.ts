/**
 * cloudGraphData.test.ts — locks the #3970 graph+list parity contract:
 * the ONE dataset (cloud + k8s + reconcilers) used by BOTH renderings.
 *
 *   • buildCloudGraph folds the reconciler set into the SAME GraphNode[]
 *     the cloud tree produces — so a reconciler that appears in the graph
 *     appears in the list (DoD #6/#9).
 *   • lensChips drives which of those rows a lens shows — and applies
 *     identically to both renderings (DoD #10).
 */

import { describe, it, expect } from 'vitest'
import { buildCloudGraph } from './cloudGraphData'
import { LENSES, lensChips } from './presets'
import type { HierarchicalInfrastructure } from '@/lib/infrastructure.types'
import type { ReconciliationNode } from '@/lib/reconciliation.api'
import type { ArchNodeType } from './types'

const cloud: HierarchicalInfrastructure = {
  cloud: [
    { id: 'c1', name: 'Hetzner', provider: 'hetzner', regionCount: 1, quotaUsed: 0, quotaLimit: 0 },
  ],
  topology: {
    pattern: 'solo',
    regions: [
      {
        id: 'r1',
        name: 'fsn',
        provider: 'hetzner',
        providerRegion: 'fsn1',
        skuCp: 'cpx32',
        skuWorker: 'cpx32',
        workerCount: 1,
        status: 'healthy',
        clusters: [
          {
            id: 'cl1',
            name: 'cl1',
            version: 'v1.31.4+k3s1',
            status: 'healthy',
            nodeCount: 1,
            vclusters: [],
            loadBalancers: [],
            nodePools: [],
            nodes: [],
          },
        ],
        networks: [],
      },
    ],
  },
  storage: { pvcs: [], buckets: [], volumes: [] },
}

const reconcilers: ReconciliationNode[] = [
  { id: 'hr/keycloak', label: 'bp-keycloak', kind: 'HelmRelease', state: 'Reconciled' },
  { id: 'ks/bootstrap', label: 'catalyst-bootstrap', kind: 'Kustomization', state: 'Reconciled' },
  { id: 'cert/wild', label: 'wildcard-cert', kind: 'Certificate', state: 'Reconciled' },
  { id: 'es/creds', label: 'keycloak-pg-creds', kind: 'ExternalSecret', state: 'Reconciling' },
  { id: 'cnpg/sharedpg', label: 'shared-pg-c', kind: 'Cluster', state: 'Reconciled' },
  { id: 'env/acme', label: 'acme-prod', kind: 'Environment', state: 'Reconciled' },
  { id: 'org/acme', label: 'acme', kind: 'Organization', state: 'Reconciled' },
]

describe('buildCloudGraph — ONE dataset (cloud + reconcilers)', () => {
  it('the merged dataset contains BOTH the cloud nodes AND every reconciler kind', () => {
    const { nodes } = buildCloudGraph(cloud, null, reconcilers)
    const types = new Set(nodes.map((n) => n.type))
    // cloud side
    expect(types.has('Cloud')).toBe(true)
    expect(types.has('Cluster')).toBe(true)
    // reconciler side — the kinds the OLD cloud-list/kinds.ts had NONE of.
    expect(types.has('HelmRelease')).toBe(true)
    expect(types.has('Kustomization')).toBe(true)
    expect(types.has('Certificate')).toBe(true)
    expect(types.has('ExternalSecret')).toBe(true)
    expect(types.has('Database')).toBe(true) // CNPG Cluster → Database
    expect(types.has('Environment')).toBe(true)
    expect(types.has('Organization')).toBe(true)
  })

  it('a reconciler row carries its status (FILL) so the list STATUS column works', () => {
    const { nodes } = buildCloudGraph(cloud, null, reconcilers)
    const es = nodes.find((n) => n.label === 'keycloak-pg-creds')
    expect(es?.type).toBe('ExternalSecret')
    expect(es?.status).toBe('reconciling')
  })

  it('with no reconcilers the dataset is just the cloud nodes', () => {
    const { nodes } = buildCloudGraph(cloud, null, null)
    const types = new Set(nodes.map((n) => n.type))
    expect(types.has('Cloud')).toBe(true)
    expect(types.has('HelmRelease')).toBe(false)
  })
})

describe('lens filtering applies identically to the list dataset (DoD #10)', () => {
  const { nodes } = buildCloudGraph(cloud, null, reconcilers)
  function rowsFor(lensId: keyof typeof LENSES): Set<ArchNodeType> {
    const chips = lensChips(LENSES[lensId])
    return new Set(nodes.filter((n) => chips.has(n.type)).map((n) => n.type))
  }

  it('Reconciliation lens shows the control reconcilers (HelmRelease/Kustomization/Env/Org)', () => {
    const r = rowsFor('reconciliation')
    expect(r.has('HelmRelease')).toBe(true)
    expect(r.has('Kustomization')).toBe(true)
    expect(r.has('Environment')).toBe(true)
    expect(r.has('Organization')).toBe(true)
    // NOT the cloud scope nodes or the CNPG database (data category).
    expect(r.has('Cloud')).toBe(false)
    expect(r.has('Database')).toBe(false)
  })

  it('the cert-manager family lens shows ONLY Certificate rows', () => {
    const r = rowsFor('certManager')
    expect(r.has('Certificate')).toBe(true)
    expect(r.has('HelmRelease')).toBe(false)
    expect(r.has('Cloud')).toBe(false)
  })

  it('the CNPG family lens shows the Database (CNPG Cluster) row', () => {
    const r = rowsFor('cnpg')
    expect(r.has('Database')).toBe(true)
    expect(r.has('Certificate')).toBe(false)
  })

  it('the Cloud lens shows the cloud nodes, NOT the reconcilers', () => {
    const r = rowsFor('cloud')
    expect(r.has('Cloud')).toBe(true)
    expect(r.has('Cluster')).toBe(true)
    expect(r.has('HelmRelease')).toBe(false)
    expect(r.has('Database')).toBe(false)
  })
})
