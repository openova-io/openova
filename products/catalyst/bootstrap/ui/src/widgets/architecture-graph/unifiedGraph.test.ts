/**
 * unifiedGraph.test.ts — locks the #3958 unified Cloud-graph contract:
 *   • the three visual channels (category/family/status) are exhaustive
 *     and self-consistent,
 *   • the reconciler adapter folds reconcilers into typed graph nodes,
 *   • the preset lenses hide the right node types,
 *   • physicsFor keeps a constant-density, viewport-aware field.
 */

import { describe, it, expect } from 'vitest'
import {
  ALL_CATEGORIES,
  ALL_FAMILIES,
  ALL_NODE_TYPES,
  CATEGORY_LABEL,
  FAMILY_BORDER,
  FAMILY_LABEL,
  NODE_CATEGORY,
  NODE_FAMILY,
  STATUS_FILL,
  familyForApiGroup,
  type ArchStatus,
} from './types'
import { reconcilersToGraph } from './reconcilerAdapter'
import { PRESETS, presetHiddenTypes } from './presets'
import { shapeForCategory } from './shapes'
import { physicsFor } from './layout'
import type { ReconciliationNode } from '@/lib/reconciliation.api'

describe('three visual channels are exhaustive', () => {
  it('every node type maps to a category and a family', () => {
    for (const t of ALL_NODE_TYPES) {
      expect(ALL_CATEGORIES).toContain(NODE_CATEGORY[t])
      expect(ALL_FAMILIES).toContain(NODE_FAMILY[t])
    }
  })
  it('every category + family has a label and the family a border colour', () => {
    for (const c of ALL_CATEGORIES) expect(CATEGORY_LABEL[c]).toBeTruthy()
    for (const f of ALL_FAMILIES) {
      expect(FAMILY_LABEL[f]).toBeTruthy()
      expect(FAMILY_BORDER[f]).toMatch(/^#[0-9a-f]{6}$/i)
    }
  })
  it('the five status fills collapse to exactly five distinct colours', () => {
    const statuses: ArchStatus[] = ['healthy', 'reconciling', 'drifted', 'degraded', 'failed', 'suspended', 'unknown']
    for (const s of statuses) expect(STATUS_FILL[s]).toMatch(/^#[0-9a-f]{6}$/i)
    // degraded folds onto failed (red); suspended folds onto unknown (grey).
    expect(STATUS_FILL.degraded).toBe(STATUS_FILL.failed)
    expect(STATUS_FILL.suspended).toBe(STATUS_FILL.unknown)
    expect(new Set(Object.values(STATUS_FILL)).size).toBe(5)
  })
})

describe('familyForApiGroup', () => {
  const cases: [string, string][] = [
    ['helm.toolkit.fluxcd.io/v2', 'flux'],
    ['kustomize.toolkit.fluxcd.io/v1', 'flux'],
    ['cert-manager.io/v1', 'certManager'],
    ['postgresql.cnpg.io/v1', 'cnpg'],
    ['external-secrets.io/v1beta1', 'externalSecrets'],
    ['cilium.io/v2', 'cilium'],
    ['catalyst.openova.io/v1', 'catalyst'],
    ['apps', 'coreK8s'],
  ]
  for (const [group, fam] of cases) {
    it(`${group} → ${fam}`, () => {
      expect(familyForApiGroup(group)).toBe(fam)
    })
  }
  it('returns undefined for an unknown group', () => {
    expect(familyForApiGroup('whatever.unknown.io')).toBeUndefined()
    expect(familyForApiGroup(undefined)).toBeUndefined()
  })
})

describe('reconcilersToGraph', () => {
  const nodes: ReconciliationNode[] = [
    { id: 'hr/a', label: 'a', kind: 'HelmRelease', state: 'Reconciled' },
    { id: 'hr/b', label: 'b', kind: 'HelmRelease', state: 'Degraded', dependsOn: ['hr/a'] },
    { id: 'cert/c', label: 'c', kind: 'Certificate', state: 'Reconciling' },
    { id: 'cnpg/d', label: 'd', kind: 'Cluster', state: 'Drifted' },
    { id: 'es/e', label: 'e', kind: 'ExternalSecret', state: 'Suspended', dependsOn: ['missing'] },
  ]

  it('returns empty for null/empty input', () => {
    expect(reconcilersToGraph(null)).toEqual({ nodes: [], edges: [] })
    expect(reconcilersToGraph([])).toEqual({ nodes: [], edges: [] })
  })

  it('namespaces ids, maps kind→type and state→status', () => {
    const g = reconcilersToGraph(nodes)
    const byId = new Map(g.nodes.map((n) => [n.id, n]))
    expect(byId.get('recon:hr/a')?.type).toBe('HelmRelease')
    expect(byId.get('recon:hr/a')?.status).toBe('healthy')
    expect(byId.get('recon:hr/b')?.status).toBe('degraded')
    expect(byId.get('recon:cert/c')?.type).toBe('Certificate')
    expect(byId.get('recon:cert/c')?.status).toBe('reconciling')
    // CNPG Cluster kind remaps to Database (Data/Storage hexagon).
    expect(byId.get('recon:cnpg/d')?.type).toBe('Database')
    expect(NODE_CATEGORY['Database']).toBe('data')
    expect(NODE_FAMILY['Database']).toBe('cnpg')
    expect(byId.get('recon:es/e')?.status).toBe('suspended')
  })

  it('emits dependsOn edges only when both endpoints are present', () => {
    const g = reconcilersToGraph(nodes)
    // hr/a → hr/b present; es/e → missing dropped.
    expect(g.edges).toHaveLength(1)
    expect(g.edges[0]).toMatchObject({
      source: 'recon:hr/a',
      target: 'recon:hr/b',
      type: 'depends-on',
    })
  })
})

describe('preset lenses', () => {
  it('the All preset hides nothing', () => {
    expect(presetHiddenTypes(PRESETS.all).size).toBe(0)
  })
  it('Reconciliation keeps only Control/Reconciler shapes', () => {
    const hidden = presetHiddenTypes(PRESETS.reconciliation)
    // HelmRelease (control) survives; Pod (compute) is hidden.
    expect(hidden.has('HelmRelease')).toBe(false)
    expect(hidden.has('Kustomization')).toBe(false)
    expect(hidden.has('Pod')).toBe(true)
    expect(hidden.has('Service')).toBe(true)
  })
  it('the Flux family lens keeps only Flux-family types', () => {
    const hidden = presetHiddenTypes(PRESETS.flux)
    expect(hidden.has('HelmRelease')).toBe(false)
    expect(hidden.has('Kustomization')).toBe(false)
    expect(hidden.has('Certificate')).toBe(true) // cert-manager family
    expect(hidden.has('Pod')).toBe(true)
  })
  it('Networking keeps the network category', () => {
    const hidden = presetHiddenTypes(PRESETS.networking)
    expect(hidden.has('Service')).toBe(false)
    expect(hidden.has('Gateway')).toBe(false)
    expect(hidden.has('Pod')).toBe(true)
  })
})

describe('shapeForCategory', () => {
  it('compute is a circle, others are polygons', () => {
    expect(shapeForCategory('compute', 10).el).toBe('circle')
    for (const c of ['control', 'network', 'config', 'data', 'scope'] as const) {
      const g = shapeForCategory(c, 10)
      expect(g.el).toBe('polygon')
      expect(g.points && g.points.length).toBeGreaterThan(0)
    }
  })
})

describe('physicsFor — constant-density centered field', () => {
  const W = 1000
  const H = 600

  it('field radius grows with sqrt(N) (constant density)', () => {
    const p4 = physicsFor(4, W, H)
    const p40 = physicsFor(40, W, H)
    const p160 = physicsFor(160, W, H)
    // monotonic increase
    expect(p40.fieldRadius).toBeGreaterThan(p4.fieldRadius)
    expect(p160.fieldRadius).toBeGreaterThan(p40.fieldRadius)
    // never exceeds the viewport half-extent (no edge overflow)
    expect(p160.fieldRadius).toBeLessThanOrEqual(Math.min(W, H) / 2)
  })

  it('a tiny graph stays a centred cluster, not flung to edges', () => {
    const p = physicsFor(4, W, H)
    // 4-node field is a small fraction of the viewport.
    expect(p.fieldRadius).toBeLessThan(Math.min(W, H) / 2 * 0.5)
  })

  it('link distance is clamped to [minLink, maxLink] with no overlap', () => {
    for (const n of [1, 4, 40, 300, 3000]) {
      const p = physicsFor(n, W, H)
      expect(p.minLink).toBeGreaterThanOrEqual(2 * 20 + 10) // sum-of-radii + pad
      expect(p.linkDistance).toBeGreaterThanOrEqual(p.minLink)
      expect(p.linkDistance).toBeLessThanOrEqual(p.maxLink)
      expect(p.maxLink).toBeLessThanOrEqual(160)
      // hard collision floor present
      expect(p.collide).toBeGreaterThanOrEqual(20)
    }
  })

  it('keeps a gentle inward gravity at every scale', () => {
    for (const n of [4, 40, 1000, 8000]) {
      const p = physicsFor(n, W, H)
      expect(p.centerGravity).toBeGreaterThan(0)
      expect(p.centerGravity).toBeLessThan(0.3)
    }
  })
})
