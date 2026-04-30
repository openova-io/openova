/**
 * pipelineLayout.test.ts — pure-function tests for the two-level
 * Sugiyama layout used by the Flow tab on JobsPage.
 *
 * Coverage (per founder spec):
 *   • Empty input is well-formed (no crash, no nodes).
 *   • Canonical 5-job fan-in example: 4 stages, 4 edges, zero crossings,
 *     edge `2→5` rendered as a 4-point bezier (skips empty stage-2 col).
 *   • Real otech bootstrap-kit (13 jobs): 5 stages, fan-in at
 *     external-dns (2 incoming edges), zero crossings.
 *   • Two-batch fixture: meta-DAG with 1 meta-edge connecting the LAST
 *     stage of phase-0-infra to the first stage of bootstrap-kit.
 *   • Collapsed batch: returns 1 batch supernode, no inner job nodes.
 *   • Default-collapse policy: all-succeeded batches collapse, others stay
 *     expanded.
 */

import { describe, it, expect } from 'vitest'
import {
  pipelineLayout,
  defaultCollapsedBatchIds,
  aggregateStatus,
  edgeToPath,
  routeEdge,
  sugiyama,
} from './pipelineLayout'
import type { Job } from './jobs.types'

/* ── Fixtures ────────────────────────────────────────────────────── */

/**
 * 5-job canonical fan-out / fan-in example from the founder brief:
 *   1, 2(←1), 3(←1), 4(←3), 5(←2,4)
 * Should produce 4 stages: {1}, {2,3}, {4}, {5}.
 * Edge 2→5 has span 2 (skips stage-2) → must be a bezier.
 */
const FIVE_JOB_FANIN: Job[] = [
  { id: '1', jobName: 'job-1', appId: 'app', batchId: 'B', dependsOn: [], status: 'pending', startedAt: null, finishedAt: null, durationMs: 0 },
  { id: '2', jobName: 'job-2', appId: 'app', batchId: 'B', dependsOn: ['1'], status: 'pending', startedAt: null, finishedAt: null, durationMs: 0 },
  { id: '3', jobName: 'job-3', appId: 'app', batchId: 'B', dependsOn: ['1'], status: 'pending', startedAt: null, finishedAt: null, durationMs: 0 },
  { id: '4', jobName: 'job-4', appId: 'app', batchId: 'B', dependsOn: ['3'], status: 'pending', startedAt: null, finishedAt: null, durationMs: 0 },
  { id: '5', jobName: 'job-5', appId: 'app', batchId: 'B', dependsOn: ['2', '4'], status: 'pending', startedAt: null, finishedAt: null, durationMs: 0 },
]

/**
 * Real otech bootstrap-kit shape (13 jobs):
 *   cilium (s0)
 *   cert-manager (s1, ←cilium)
 *   spire / sealed-secrets / flux / keycloak / powerdns (s2, ←cert-manager)
 *   crossplane (s3, ←flux), gitea (s3, ←keycloak), nats-jetstream (s3, ←spire),
 *     openbao (s3, ←spire), external-dns (s3, ←cert-manager + powerdns)
 *   catalyst-platform (s4, ←gitea)
 */
function makeBootstrapKit(batchId = 'bootstrap-kit'): Job[] {
  const j = (id: string, deps: string[]): Job => ({
    id,
    jobName: id,
    appId: id,
    batchId,
    dependsOn: deps,
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  })
  return [
    j('cilium', []),
    j('cert-manager', ['cilium']),
    j('spire', ['cert-manager']),
    j('sealed-secrets', ['cert-manager']),
    j('flux', ['cert-manager']),
    j('keycloak', ['cert-manager']),
    j('powerdns', ['cert-manager']),
    j('crossplane', ['flux']),
    j('gitea', ['keycloak']),
    j('nats-jetstream', ['spire']),
    j('openbao', ['spire']),
    j('external-dns', ['cert-manager', 'powerdns']),
    j('catalyst-platform', ['gitea']),
  ]
}

/* ──────────────────────────────────────────────────────────────────
 * Empty input
 * ────────────────────────────────────────────────────────────────── */

describe('pipelineLayout — empty', () => {
  it('returns no nodes / edges / batches and a non-zero canvas', () => {
    const r = pipelineLayout([])
    expect(r.nodes).toEqual([])
    expect(r.edges).toEqual([])
    expect(r.batches).toEqual([])
    expect(r.width).toBeGreaterThan(0)
    expect(r.height).toBeGreaterThan(0)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Canonical 5-job fan-in
 * ────────────────────────────────────────────────────────────────── */

describe('pipelineLayout — canonical 5-job fan-in', () => {
  const r = pipelineLayout(FIVE_JOB_FANIN)
  const byId = new Map(r.nodes.map((n) => [n.id, n]))

  it('produces 5 job nodes (one batch, all expanded by default)', () => {
    expect(r.nodes.length).toBe(5)
    expect(r.nodes.every((n) => n.kind === 'job')).toBe(true)
  })

  it('produces 5 within-batch edges (1→2, 1→3, 2→5, 3→4, 4→5)', () => {
    const within = r.edges.filter((e) => e.kind === 'within-batch')
    expect(within.length).toBe(5)
    const pairs = within.map((e) => `${e.fromId}→${e.toId}`).sort()
    expect(pairs).toEqual(['1→2', '1→3', '2→5', '3→4', '4→5'].sort())
  })

  it('arranges jobs into 4 stages: {1}, {2,3}, {4}, {5}', () => {
    expect(byId.get('1')!.stage).toBe(0)
    expect(byId.get('2')!.stage).toBe(1)
    expect(byId.get('3')!.stage).toBe(1)
    expect(byId.get('4')!.stage).toBe(2)
    expect(byId.get('5')!.stage).toBe(3)
  })

  it('edge 2→5 is a 4-point bezier (skips empty stage-2)', () => {
    const e25 = r.edges.find((e) => e.fromId === '2' && e.toId === '5')
    expect(e25, 'edge 2→5 must be present in the within-batch edge set').toBeTruthy()
    expect(e25!.points.length).toBe(4)
  })

  it('edges with span=1 are 2-point straight lines', () => {
    for (const e of r.edges.filter((e) => e.kind === 'within-batch')) {
      const from = byId.get(e.fromId)!
      const to = byId.get(e.toId)!
      const span = (to.stage ?? 0) - (from.stage ?? 0)
      if (span === 1) {
        expect(e.points.length, `edge ${e.fromId}→${e.toId} (span=1) must be a 2-point line`).toBe(2)
      }
    }
  })

  it('zero crossings within the single batch', () => {
    expect(countCrossings(r.edges.filter((e) => e.kind === 'within-batch'), r.nodes)).toBe(0)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Real otech bootstrap-kit
 * ────────────────────────────────────────────────────────────────── */

describe('pipelineLayout — bootstrap-kit (13 jobs, 5 stages)', () => {
  const jobs = makeBootstrapKit()
  const r = pipelineLayout(jobs)
  const byId = new Map(r.nodes.map((n) => [n.id, n]))

  it('produces 13 job nodes', () => {
    expect(r.nodes.length).toBe(13)
  })

  it('produces 5 distinct inner stages', () => {
    const stages = new Set(r.nodes.map((n) => n.stage))
    expect(stages.size).toBe(5)
  })

  it('cilium is at stage 0; catalyst-platform is at stage 4', () => {
    expect(byId.get('cilium')!.stage).toBe(0)
    expect(byId.get('catalyst-platform')!.stage).toBe(4)
  })

  it('external-dns has fan-in (2 incoming edges)', () => {
    const incoming = r.edges.filter(
      (e) => e.kind === 'within-batch' && e.toId === 'external-dns',
    )
    expect(incoming.length).toBe(2)
    const sources = incoming.map((e) => e.fromId).sort()
    expect(sources).toEqual(['cert-manager', 'powerdns'])
  })

  it('zero edge crossings across the inner DAG', () => {
    const within = r.edges.filter((e) => e.kind === 'within-batch')
    expect(countCrossings(within, r.nodes)).toBe(0)
  })

  it('every dependency in the dataset is reflected by an emitted edge', () => {
    let depCount = 0
    for (const j of jobs) depCount += j.dependsOn.length
    const within = r.edges.filter((e) => e.kind === 'within-batch')
    expect(within.length).toBe(depCount)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Two-batch fixture
 * ────────────────────────────────────────────────────────────────── */

describe('pipelineLayout — two-batch fixture (meta-DAG)', () => {
  // phase-0-infra: 4 jobs in series → produces a 4-stage chain.
  // bootstrap-kit: 3 jobs (cilium → cert-manager → flux), with one
  //                cross-batch dep cilium ← phase-0-infra's last job.
  const phase0 = (id: string, deps: string[]): Job => ({
    id,
    jobName: id,
    appId: 'infrastructure',
    batchId: 'phase-0-infra',
    dependsOn: deps,
    status: 'succeeded',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  })
  const bk = (id: string, deps: string[]): Job => ({
    id,
    jobName: id,
    appId: id,
    batchId: 'bootstrap-kit',
    dependsOn: deps,
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  })
  const jobs: Job[] = [
    phase0('tofu-init', []),
    phase0('tofu-plan', ['tofu-init']),
    phase0('tofu-apply', ['tofu-plan']),
    phase0('tofu-output', ['tofu-apply']),
    bk('cilium', ['tofu-output']),
    bk('cert-manager', ['cilium']),
    bk('flux', ['cert-manager']),
  ]

  const r = pipelineLayout(jobs)

  it('produces 2 batch lanes', () => {
    expect(r.batches.length).toBe(2)
  })

  it('phase-0-infra is at meta-stage 0; bootstrap-kit at meta-stage 1', () => {
    const p0 = r.batches.find((b) => b.batchId === 'phase-0-infra')!
    const bkLane = r.batches.find((b) => b.batchId === 'bootstrap-kit')!
    expect(p0.metaStage).toBe(0)
    expect(bkLane.metaStage).toBe(1)
  })

  it('bootstrap-kit lane sits to the right of phase-0-infra', () => {
    const p0 = r.batches.find((b) => b.batchId === 'phase-0-infra')!
    const bkLane = r.batches.find((b) => b.batchId === 'bootstrap-kit')!
    expect(bkLane.x).toBeGreaterThan(p0.x + p0.width)
  })

  it('emits exactly 1 cross-batch job edge tofu-output → cilium', () => {
    const cross = r.edges.filter((e) => e.kind === 'cross-batch-job')
    expect(cross.length).toBe(1)
    expect(cross[0]!.fromId).toBe('tofu-output')
    expect(cross[0]!.toId).toBe('cilium')
  })

  it('the cross-batch source is the LAST stage of phase-0-infra', () => {
    const tofuOutput = r.nodes.find((n) => n.id === 'tofu-output')!
    const phase0Jobs = r.nodes.filter((n) => n.batchId === 'phase-0-infra')
    const maxStage = Math.max(...phase0Jobs.map((n) => n.stage ?? 0))
    expect(tofuOutput.stage).toBe(maxStage)
  })

  it('cross-batch edge is rendered as a bezier (4 points)', () => {
    const cross = r.edges.find((e) => e.kind === 'cross-batch-job')!
    expect(cross.points.length).toBe(4)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Collapsed batches
 * ────────────────────────────────────────────────────────────────── */

describe('pipelineLayout — collapsed batches', () => {
  const phase0Job = (id: string, deps: string[]): Job => ({
    id,
    jobName: id,
    appId: 'infrastructure',
    batchId: 'phase-0-infra',
    dependsOn: deps,
    status: 'succeeded',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  })
  const bkJob = (id: string, deps: string[]): Job => ({
    id,
    jobName: id,
    appId: id,
    batchId: 'bootstrap-kit',
    dependsOn: deps,
    status: 'pending',
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  })
  const jobs: Job[] = [
    phase0Job('tofu-init', []),
    phase0Job('tofu-plan', ['tofu-init']),
    phase0Job('tofu-apply', ['tofu-plan']),
    phase0Job('tofu-output', ['tofu-apply']),
    bkJob('cilium', ['tofu-output']),
    bkJob('cert-manager', ['cilium']),
    bkJob('flux', ['cert-manager']),
  ]

  it('collapsed batch returns 1 batch supernode (not 4 job nodes)', () => {
    const r = pipelineLayout(jobs, {
      collapsedBatchIds: new Set(['phase-0-infra']),
    })
    const phase0Nodes = r.nodes.filter((n) => n.batchId === 'phase-0-infra')
    expect(phase0Nodes.length).toBe(1)
    expect(phase0Nodes[0]!.kind).toBe('batch')
    expect(phase0Nodes[0]!.id).toBe('phase-0-infra')
  })

  it('cross-batch edge from a collapsed source is a meta arrow', () => {
    const r = pipelineLayout(jobs, {
      collapsedBatchIds: new Set(['phase-0-infra']),
    })
    const meta = r.edges.filter((e) => e.kind === 'meta')
    expect(meta.length).toBe(1)
    expect(meta[0]!.fromId).toBe('phase-0-infra')
    expect(meta[0]!.toId).toBe('bootstrap-kit')
  })

  it('jobs in the expanded batch are still rendered individually', () => {
    const r = pipelineLayout(jobs, {
      collapsedBatchIds: new Set(['phase-0-infra']),
    })
    const bkNodes = r.nodes.filter((n) => n.batchId === 'bootstrap-kit')
    expect(bkNodes.length).toBe(3)
    expect(bkNodes.every((n) => n.kind === 'job')).toBe(true)
  })

  it('default collapse policy collapses all-succeeded batches only', () => {
    const collapsed = defaultCollapsedBatchIds(jobs)
    expect(collapsed.has('phase-0-infra')).toBe(true)
    expect(collapsed.has('bootstrap-kit')).toBe(false)
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Helpers — pure functions that the layout exports
 * ────────────────────────────────────────────────────────────────── */

describe('aggregateStatus', () => {
  const j = (status: Job['status']): Job => ({
    id: 'x',
    jobName: 'x',
    appId: 'a',
    batchId: 'B',
    dependsOn: [],
    status,
    startedAt: null,
    finishedAt: null,
    durationMs: 0,
  })

  it('all-succeeded → "succeeded"', () => {
    expect(aggregateStatus([j('succeeded'), j('succeeded')])).toBe('succeeded')
  })
  it('any failure with running/pending peers → "mixed"', () => {
    expect(aggregateStatus([j('failed'), j('running')])).toBe('mixed')
  })
  it('failed + succeeded (no in-flight peers) → "failed"', () => {
    // Per founder spec: any failure surfaces as red on the lane;
    // mixed is reserved for batches that still have in-flight work.
    expect(aggregateStatus([j('failed'), j('succeeded')])).toBe('failed')
  })
  it('only running → "running"', () => {
    expect(aggregateStatus([j('running'), j('running')])).toBe('running')
  })
  it('empty → "pending"', () => {
    expect(aggregateStatus([])).toBe('pending')
  })
})

describe('routeEdge', () => {
  it('span ≤ 1 → 2-point line', () => {
    const pts = routeEdge({ x: 0, y: 0, width: 10, height: 10 }, { x: 30, y: 0, width: 10, height: 10 }, 1)
    expect(pts.length).toBe(2)
  })
  it('span > 1 → 4-point bezier', () => {
    const pts = routeEdge({ x: 0, y: 0, width: 10, height: 10 }, { x: 100, y: 50, width: 10, height: 10 }, 3)
    expect(pts.length).toBe(4)
  })
})

describe('edgeToPath', () => {
  it('2 points → straight L command', () => {
    expect(edgeToPath([{ x: 0, y: 0 }, { x: 10, y: 5 }])).toBe('M 0 0 L 10 5')
  })
  it('4 points → cubic bezier C command', () => {
    expect(
      edgeToPath([
        { x: 0, y: 0 },
        { x: 10, y: 0 },
        { x: 20, y: 5 },
        { x: 30, y: 5 },
      ]),
    ).toBe('M 0 0 C 10 0, 20 5, 30 5')
  })
})

describe('sugiyama', () => {
  it('handles a single root with no edges', () => {
    const r = sugiyama(['a'], [])
    expect(r.layer.get('a')).toBe(0)
    expect(r.position.get('a')).toBe(0)
    expect(r.layerCount).toBe(1)
  })
  it('produces deterministic layering for an isolated DAG', () => {
    const r1 = sugiyama(['a', 'b', 'c'], [{ from: 'a', to: 'b' }, { from: 'a', to: 'c' }])
    const r2 = sugiyama(['c', 'b', 'a'], [{ from: 'a', to: 'b' }, { from: 'a', to: 'c' }])
    expect(r1.layer.get('a')).toBe(r2.layer.get('a'))
    expect(r1.layer.get('b')).toBe(r2.layer.get('b'))
    expect(r1.layer.get('c')).toBe(r2.layer.get('c'))
  })
})

/* ──────────────────────────────────────────────────────────────────
 * Test helper — line-segment crossing count.
 *
 * For each pair of edges in the same batch, we treat them as
 * straight segments from source-node-right-mid to target-node-left-mid
 * (the polyline anchor) and count how many pairs intersect strictly
 * between their endpoints. Bezier control points are ignored — the
 * crossing test is a logical check on the underlying graph topology,
 * not the rendered curve.
 * ────────────────────────────────────────────────────────────────── */

interface Pt {
  x: number
  y: number
}
interface Seg {
  a: Pt
  b: Pt
}

function makeSegment(
  edge: { fromId: string; toId: string },
  nodes: Array<{ id: string; x: number; y: number; width: number; height: number }>,
): Seg | null {
  const from = nodes.find((n) => n.id === edge.fromId)
  const to = nodes.find((n) => n.id === edge.toId)
  if (!from || !to) return null
  return {
    a: { x: from.x + from.width, y: from.y + from.height / 2 },
    b: { x: to.x, y: to.y + to.height / 2 },
  }
}

function ccw(a: Pt, b: Pt, c: Pt): number {
  return (b.x - a.x) * (c.y - a.y) - (b.y - a.y) * (c.x - a.x)
}

function segmentsCross(s1: Seg, s2: Seg): boolean {
  // Ignore segments that share an endpoint — these aren't crossings,
  // they're shared sources / fan-ins. Numerical tolerance to dodge
  // float wobble.
  const eps = 1e-6
  const sharesEndpoint = (p: Pt, q: Pt) => Math.abs(p.x - q.x) < eps && Math.abs(p.y - q.y) < eps
  if (
    sharesEndpoint(s1.a, s2.a) ||
    sharesEndpoint(s1.a, s2.b) ||
    sharesEndpoint(s1.b, s2.a) ||
    sharesEndpoint(s1.b, s2.b)
  ) {
    return false
  }
  const d1 = ccw(s2.a, s2.b, s1.a)
  const d2 = ccw(s2.a, s2.b, s1.b)
  const d3 = ccw(s1.a, s1.b, s2.a)
  const d4 = ccw(s1.a, s1.b, s2.b)
  if (((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))) {
    return true
  }
  return false
}

function countCrossings(
  edges: Array<{ fromId: string; toId: string }>,
  nodes: Array<{ id: string; x: number; y: number; width: number; height: number }>,
): number {
  const segs: Seg[] = []
  for (const e of edges) {
    const s = makeSegment(e, nodes)
    if (s) segs.push(s)
  }
  let n = 0
  for (let i = 0; i < segs.length; i++) {
    for (let j = i + 1; j < segs.length; j++) {
      if (segmentsCross(segs[i]!, segs[j]!)) n++
    }
  }
  return n
}
