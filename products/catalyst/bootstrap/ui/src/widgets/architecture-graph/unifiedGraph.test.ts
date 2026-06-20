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
import { LENSES, DEFAULT_LENS_ID, lensChips } from './presets'
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

describe('lens chip-sets (#3970 — a lens IS a named chip-set)', () => {
  it('the default lens is Cloud — the cloud-provider scope/compute/network nodes', () => {
    expect(DEFAULT_LENS_ID).toBe('cloud')
    const chips = lensChips(LENSES.cloud)
    // Cloud-family scope/compute/network nodes are members…
    expect(chips.has('Cloud')).toBe(true)
    expect(chips.has('Region')).toBe(true)
    expect(chips.has('Cluster')).toBe(true)
    expect(chips.has('NodePool')).toBe(true)
    expect(chips.has('LoadBalancer')).toBe(true)
    // …and non-cloud-family / non-domain types are NOT in the strip.
    expect(chips.has('Pod')).toBe(false) // coreK8s family
    expect(chips.has('HelmRelease')).toBe(false) // flux family
    expect(chips.has('Database')).toBe(false) // cnpg family
  })
  it('Reconciliation = exactly the Control/Reconciler chips', () => {
    const chips = lensChips(LENSES.reconciliation)
    // HelmRelease (control) is a member; Pod / Service are NOT in the strip.
    expect(chips.has('HelmRelease')).toBe(true)
    expect(chips.has('Kustomization')).toBe(true)
    expect(chips.has('Application')).toBe(true)
    expect(chips.has('Environment')).toBe(true)
    expect(chips.has('Pod')).toBe(false)
    expect(chips.has('Service')).toBe(false)
  })
  it('the Flux family lens = exactly the Flux-family chips', () => {
    const chips = lensChips(LENSES.flux)
    expect(chips.has('HelmRelease')).toBe(true)
    expect(chips.has('Kustomization')).toBe(true)
    expect(chips.has('Certificate')).toBe(false) // cert-manager family
    expect(chips.has('Pod')).toBe(false)
  })
  it('Networking = exactly the network-category chips', () => {
    const chips = lensChips(LENSES.networking)
    expect(chips.has('Service')).toBe(true)
    expect(chips.has('Gateway')).toBe(true)
    expect(chips.has('Pod')).toBe(false)
  })
  it('every lens membership is non-empty except the runtime-only Crossplane family', () => {
    for (const id of Object.keys(LENSES) as (keyof typeof LENSES)[]) {
      const size = lensChips(LENSES[id]).size
      if (id === 'crossplane') {
        // Crossplane is a runtime-only family — statically empty in
        // NODE_FAMILY; populated from live apiVersion at runtime.
        expect(size).toBe(0)
      } else {
        expect(size).toBeGreaterThan(0)
      }
    }
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

describe('physicsFor — even / homogeneous density (#3970)', () => {
  const W = 1000
  const H = 600

  it('the uniform gap SHRINKS as N grows (constant density: more nodes ⇒ tighter packing)', () => {
    const p4 = physicsFor(4, W, H)
    const p40 = physicsFor(40, W, H)
    const p160 = physicsFor(160, W, H)
    // gap = sqrt(area / N): monotonically smaller as N grows (clamped).
    expect(p40.uniformGap).toBeLessThanOrEqual(p4.uniformGap)
    expect(p160.uniformGap).toBeLessThanOrEqual(p40.uniformGap)
    // collide radius is half the gap — the dominant spacing force.
    expect(p40.collide).toBeCloseTo(p40.uniformGap / 2, 6)
  })

  it('collision (not charge) is the dominant force — charge is mild/near-zero', () => {
    for (const n of [4, 64, 400]) {
      const p = physicsFor(n, W, H)
      // mild charge — never the strong negative that flings to a ring.
      expect(p.charge).toBeGreaterThan(-30)
      expect(p.charge).toBeLessThanOrEqual(0)
      // collide floor keeps nodes apart (no overlap).
      expect(p.collide).toBeGreaterThanOrEqual(20)
    }
  })

  it('link distance is clamped to [minLink, maxLink] with no overlap', () => {
    for (const n of [1, 4, 40, 300, 3000]) {
      const p = physicsFor(n, W, H)
      expect(p.minLink).toBeGreaterThanOrEqual(2 * 20 + 10) // sum-of-radii + pad
      expect(p.linkDistance).toBeGreaterThanOrEqual(p.minLink)
      expect(p.linkDistance).toBeLessThanOrEqual(p.maxLink)
      expect(p.maxLink).toBeLessThanOrEqual(200)
    }
  })

  it('keeps a gentle inward gravity at every scale (anti-drift, never center-crush)', () => {
    for (const n of [4, 40, 1000, 8000]) {
      const p = physicsFor(n, W, H)
      expect(p.centerGravity).toBeGreaterThan(0)
      expect(p.centerGravity).toBeLessThanOrEqual(0.07)
    }
  })
})
