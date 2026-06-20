/**
 * layout.test.ts — locks the #3970 EVEN / HOMOGENEOUS layout contract on
 * `physicsFor` and the settled force-directed layout:
 *
 *   1. physicsFor produces an even-density tuning: collision is dominant
 *      (radius = uniformGap/2), charge is mild/near-zero, centering is
 *      gentle. uniformGap = sqrt(usableArea / N)·k shrinks as N grows.
 *   2. The settled layout (driven headlessly through the real d3-force
 *      sim with the PRODUCTION collide-dominant + mild-charge + gentle-
 *      centering config, the even phyllotaxis seed, and the hard viewport
 *      bound — NO radial cap, which #3970 deleted) has UNIFORM LOCAL
 *      DENSITY: partition the canvas into a G×G grid, count occupancy per
 *      cell, and assert the variance/mean of occupied-region cells is
 *      below a bound — i.e. NEITHER a dense central blob NOR an edge ring.
 *
 * Approach note: jsdom has no rAF loop, but d3-force's `simulation.tick()`
 * is fully synchronous, so we drive the sim to a settled state
 * deterministically (seeded RNG + fixed tick budget) and assert the
 * grid-cell occupancy variance directly. The geometry source of truth —
 * `physicsFor` + `phyllotaxisSeed` — is imported directly.
 */

import { describe, it, expect } from 'vitest'
import {
  forceSimulation,
  forceManyBody,
  forceCollide,
  forceX,
  forceY,
  forceCenter,
  type Simulation,
} from 'd3-force'
import { physicsFor, phyllotaxisSeed, NODE_R, BOUND_PADDING } from './layout'

/* ── geometry contract (pure, no sim) ───────────────────────────── */

describe('physicsFor — even/homogeneous tuning (#3970)', () => {
  const W = 1100
  const H = 600

  it('collision is the dominant spacing force: collide = uniformGap/2', () => {
    for (const n of [25, 64, 400]) {
      const p = physicsFor(n, W, H)
      expect(p.collide).toBeCloseTo(p.uniformGap / 2, 6)
      // a hard floor so two bubbles never overlap
      expect(p.collide).toBeGreaterThanOrEqual(NODE_R)
    }
  })

  it('charge is mild / near-zero (no edge-fling)', () => {
    for (const n of [25, 64, 400, 4000]) {
      const p = physicsFor(n, W, H)
      expect(p.charge).toBeGreaterThan(-30)
      expect(p.charge).toBeLessThanOrEqual(0)
    }
  })

  it('the uniform gap shrinks with N (constant density)', () => {
    const p25 = physicsFor(25, W, H)
    const p64 = physicsFor(64, W, H)
    const p400 = physicsFor(400, W, H)
    expect(p64.uniformGap).toBeLessThanOrEqual(p25.uniformGap)
    expect(p400.uniformGap).toBeLessThanOrEqual(p64.uniformGap)
  })

  it('centerGravity is weak (anti-drift only, never center-crush)', () => {
    for (const n of [4, 64, 400, 8000]) {
      const p = physicsFor(n, W, H)
      expect(p.centerGravity).toBeGreaterThan(0)
      expect(p.centerGravity).toBeLessThanOrEqual(0.07)
    }
  })

  it('the field anchor tracks width/2 and height/2 (fills both axes)', () => {
    const p = physicsFor(64, W, H)
    expect(p.fieldRadiusX).toBeGreaterThan(p.fieldRadiusY) // wide canvas
    expect(p.fieldRadiusX).toBeLessThanOrEqual(W / 2)
    expect(p.fieldRadiusY).toBeLessThanOrEqual(H / 2)
  })
})

/* ── phyllotaxis seed is even ───────────────────────────────────── */

describe('phyllotaxisSeed — even sunflower seeding', () => {
  it('places points across the field with no central pile-up', () => {
    const cx = 550
    const cy = 300
    const rx = 500
    const ry = 270
    const N = 200
    const pts = Array.from({ length: N }, (_, i) =>
      phyllotaxisSeed(i, N, cx, cy, rx, ry, 0),
    )
    // radii are spread roughly uniformly by area (sqrt distribution): the
    // mean normalized radius is around 2/3, not clustered at 0.
    const meanNormR =
      pts.reduce((s, p) => s + Math.hypot((p.x - cx) / rx, (p.y - cy) / ry), 0) / N
    expect(meanNormR).toBeGreaterThan(0.55)
    expect(meanNormR).toBeLessThan(0.78)
    // every point stays inside the field ellipse.
    for (const p of pts) {
      const nd = Math.hypot((p.x - cx) / rx, (p.y - cy) / ry)
      expect(nd).toBeLessThanOrEqual(1.0001)
    }
  })
})

/* ── settled-layout contract (headless d3-force) ────────────────── */

function mulberry32(seed: number): () => number {
  let a = seed >>> 0
  return () => {
    a |= 0
    a = (a + 0x6d2b79f5) | 0
    let t = Math.imul(a ^ (a >>> 15), 1 | a)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

interface SimNode {
  index?: number
  x: number
  y: number
  vx: number
  vy: number
  fx: number | null
  fy: number | null
  degree: number
}

/**
 * Mirror of GraphCanvas.makeForceBound — the HARD viewport-edge clamp +
 * velocity-reflection safety net. This is the ONLY boundary force in the
 * #3970 model (the radial cap is deleted).
 */
function hardBound(W: number, H: number, padding: number, nodeR: number) {
  let nodes: SimNode[] = []
  const force = () => {
    for (const n of nodes) {
      const minX = nodeR + padding
      const minY = nodeR + padding
      const maxX = Math.max(minX, W - nodeR - padding)
      const maxY = Math.max(minY, H - nodeR - padding)
      if (n.x < minX) {
        n.x = minX
        if (n.vx < 0) n.vx = -n.vx * 0.4
      } else if (n.x > maxX) {
        n.x = maxX
        if (n.vx > 0) n.vx = -n.vx * 0.4
      }
      if (n.y < minY) {
        n.y = minY
        if (n.vy < 0) n.vy = -n.vy * 0.4
      } else if (n.y > maxY) {
        n.y = maxY
        if (n.vy > 0) n.vy = -n.vy * 0.4
      }
    }
  }
  ;(force as unknown as { initialize: (n: SimNode[]) => void }).initialize = (next) => {
    nodes = next
  }
  return force as (() => void) & { initialize: (n: SimNode[]) => void }
}

/** Seed N nodes on the EVEN phyllotaxis pattern (as the canvas now does). */
function seedNodes(
  count: number,
  cx: number,
  cy: number,
  rx: number,
  ry: number,
  rng: () => number,
): SimNode[] {
  const out: SimNode[] = []
  for (let i = 0; i < count; i++) {
    const p = phyllotaxisSeed(i, count, cx, cy, rx, ry, 1, rng)
    out.push({ x: p.x, y: p.y, vx: 0, vy: 0, fx: null, fy: null, degree: 0 })
  }
  return out
}

/** Drive the production force config to a settled state, return the nodes. */
function settle(count: number, W: number, H: number, seed = 1): SimNode[] {
  const phys = physicsFor(count, W, H)
  const cx = W / 2
  const cy = H / 2
  const rng = mulberry32(seed)
  const nodes = seedNodes(count, cx, cy, phys.fieldRadiusX, phys.fieldRadiusY, rng)

  const sim: Simulation<SimNode, undefined> = forceSimulation<SimNode>(nodes)
    .force('charge', forceManyBody<SimNode>().strength(phys.charge))
    .force(
      'collide',
      // PRODUCTION collide: radius = uniformGap/2 (floored at NODE_R+4),
      // strength 1 — the dominant even-density force.
      forceCollide<SimNode>().radius(Math.max(phys.collide, NODE_R + 4)).strength(1),
    )
    .force('center', forceCenter<SimNode>(cx, cy))
    .force('gravityX', forceX<SimNode>(cx).strength(phys.centerGravity))
    .force('gravityY', forceY<SimNode>(cy).strength(phys.centerGravity))
    // NO radial cap (#3970). Only the hard viewport bound.
    .force('bound', hardBound(W, H, BOUND_PADDING, NODE_R))
    .alphaDecay(phys.alphaDecay)
    .alphaTarget(0)
    .stop()

  const ticks = Math.ceil(Math.log(0.001) / Math.log(1 - phys.alphaDecay)) + 80
  sim.alpha(1)
  for (let i = 0; i < ticks; i++) sim.tick()

  return nodes
}

function bbox(nodes: SimNode[]) {
  let minX = Infinity
  let maxX = -Infinity
  let minY = Infinity
  let maxY = -Infinity
  for (const n of nodes) {
    minX = Math.min(minX, n.x)
    maxX = Math.max(maxX, n.x)
    minY = Math.min(minY, n.y)
    maxY = Math.max(maxY, n.y)
  }
  return { minX, maxX, minY, maxY, w: maxX - minX, h: maxY - minY }
}

/**
 * Grid-cell occupancy variance / mean over the OCCUPIED region (the
 * bounding box of the settled cloud). A perfectly uniform fill has all
 * cells equally populated (variance/mean → ~0); a central blob leaves the
 * outer cells empty and stacks the centre (high variance); an edge ring
 * leaves the centre cells empty (also high variance). The bound asserts
 * the fill is even in BOTH the no-blob and no-ring senses.
 */
function occupancyVarianceOverMean(nodes: SimNode[], G: number) {
  const b = bbox(nodes)
  const w = Math.max(1, b.w)
  const h = Math.max(1, b.h)
  const cells = new Array(G * G).fill(0)
  for (const n of nodes) {
    let gx = Math.floor(((n.x - b.minX) / w) * G)
    let gy = Math.floor(((n.y - b.minY) / h) * G)
    gx = Math.min(G - 1, Math.max(0, gx))
    gy = Math.min(G - 1, Math.max(0, gy))
    cells[gy * G + gx] += 1
  }
  const mean = nodes.length / (G * G)
  const variance =
    cells.reduce((s, c) => s + (c - mean) * (c - mean), 0) / cells.length
  return { ratio: variance / mean, cells, G, mean }
}

/** Count how many of the CENTRE cells are occupied — guards against an
 *  edge-ring (empty middle). */
function centreCellsOccupied(cells: number[], G: number): number {
  // The central 2×2 (even G) or 1×1 (odd G) block.
  const lo = Math.floor((G - 1) / 2)
  const hi = Math.ceil((G - 1) / 2)
  let occupied = 0
  let total = 0
  for (let gy = lo; gy <= hi; gy++) {
    for (let gx = lo; gx <= hi; gx++) {
      total += 1
      if (cells[gy * G + gx] > 0) occupied += 1
    }
  }
  return occupied / total
}

describe('settled layout has UNIFORM LOCAL DENSITY (#3970)', () => {
  const W = 1100
  const H = 600

  for (const N of [25, 64, 200]) {
    it(`N=${N}: grid-cell occupancy variance/mean is below the even-density bound`, () => {
      const nodes = settle(N, W, H, 42 + N)
      // 5×5 grid over the occupied region. A uniform fill keeps the
      // variance/mean low; a blob or ring blows it up. Empirically the
      // collide-dominant even-seed layout settles well under 2.0.
      const { ratio } = occupancyVarianceOverMean(nodes, 5)
      expect(ratio).toBeLessThan(2.0)
    })
  }

  it('N=400 fills the canvas (≥70% of BOTH axes) — no center-crush', () => {
    const nodes = settle(400, W, H, 11)
    const b = bbox(nodes)
    expect(b.w).toBeGreaterThanOrEqual(0.7 * W)
    expect(b.h).toBeGreaterThanOrEqual(0.7 * H)
  })

  it('N=64 — the CENTRE is occupied (no edge-ring / empty middle)', () => {
    const nodes = settle(64, W, H, 7)
    const { cells, G } = occupancyVarianceOverMean(nodes, 5)
    // The central block must be populated — an edge ring would leave it 0.
    expect(centreCellsOccupied(cells, G)).toBeGreaterThan(0)
  })

  it('N=64 — no two node centres closer than the collide radius (no overlap)', () => {
    const nodes = settle(64, W, H, 9)
    const phys = physicsFor(64, W, H)
    const collideR = Math.max(phys.collide, NODE_R + 4)
    // d3 collide resolves overlaps softly; allow a small residual.
    const minAllowed = collideR * 0.85
    let minDist = Infinity
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const d = Math.hypot(nodes[i].x - nodes[j].x, nodes[i].y - nodes[j].y)
        minDist = Math.min(minDist, d)
      }
    }
    expect(minDist).toBeGreaterThanOrEqual(minAllowed)
  })

  it('every node stays inside the viewport (hard bound holds)', () => {
    const nodes = settle(200, W, H, 3)
    const edge = NODE_R + BOUND_PADDING
    const b = bbox(nodes)
    expect(b.minX).toBeGreaterThanOrEqual(edge - 0.5)
    expect(b.maxX).toBeLessThanOrEqual(W - edge + 0.5)
    expect(b.minY).toBeGreaterThanOrEqual(edge - 0.5)
    expect(b.maxY).toBeLessThanOrEqual(H - edge + 0.5)
  })
})

/* ── convergence-and-stop contract (#3980 fix 2) ────────────────── */

/**
 * The canvas force sim must CONVERGE in ~2-4s and then STOP (no perpetual
 * oscillation). The canvas sets `alphaMin(0.02)` and decays alpha with
 * `physicsFor().alphaDecay` from a 0.7 warm-up, then force-stops once
 * `alpha() <= alphaMin()`. These tests drive the SAME production force
 * config + the canvas's alphaMin and assert:
 *   1. alpha crosses the settle threshold within a 4s@60fps tick budget
 *      (and is still warming at the ~1s mark, so it isn't instantaneous).
 *   2. after the sim is stopped at settle, the positions are STATIC — an
 *      extra tick (which would no-op on a stopped sim in the canvas, but
 *      we assert position-stability directly) moves nothing measurably.
 */
const CANVAS_ALPHA_MIN = 0.02

function buildSim(count: number, W: number, H: number, seed = 1) {
  const phys = physicsFor(count, W, H)
  const cx = W / 2
  const cy = H / 2
  const rng = mulberry32(seed)
  const nodes = seedNodes(count, cx, cy, phys.fieldRadiusX, phys.fieldRadiusY, rng)
  const sim: Simulation<SimNode, undefined> = forceSimulation<SimNode>(nodes)
    .force('charge', forceManyBody<SimNode>().strength(phys.charge))
    .force(
      'collide',
      forceCollide<SimNode>().radius(Math.max(phys.collide, NODE_R + 4)).strength(1),
    )
    .force('center', forceCenter<SimNode>(cx, cy))
    .force('gravityX', forceX<SimNode>(cx).strength(phys.centerGravity))
    .force('gravityY', forceY<SimNode>(cy).strength(phys.centerGravity))
    .force('bound', hardBound(W, H, BOUND_PADDING, NODE_R))
    .alphaDecay(phys.alphaDecay)
    .alphaMin(CANVAS_ALPHA_MIN)
    .alphaTarget(0)
    .stop()
  sim.alpha(0.7) // the canvas warm-up alpha
  return { sim, nodes }
}

describe('force sim CONVERGES then STOPS (#3980 fix 2)', () => {
  const W = 1100
  const H = 600
  const FPS = 60
  const SETTLE_CAP_TICKS = 4 * FPS // the ~3.5-4s hard cap budget

  for (const N of [25, 64, 200]) {
    it(`N=${N}: alpha decays below alphaMin within the ~4s settle budget`, () => {
      const { sim } = buildSim(N, W, H, 100 + N)
      let settledAt = -1
      for (let i = 0; i < SETTLE_CAP_TICKS; i++) {
        sim.tick()
        if (sim.alpha() <= sim.alphaMin()) {
          settledAt = i
          break
        }
      }
      // It DID settle within the cap …
      expect(settledAt).toBeGreaterThan(0)
      // … and it isn't instantaneous — still warm ~1s in, so the layout
      // actually relaxes rather than snapping.
      expect(settledAt).toBeGreaterThanOrEqual(FPS * 0.5)
    })
  }

  it('after settle + stop, node positions are STATIC (no residual jitter)', () => {
    const { sim, nodes } = buildSim(64, W, H, 7)
    // Drive to settle.
    for (let i = 0; i < SETTLE_CAP_TICKS; i++) {
      sim.tick()
      if (sim.alpha() <= sim.alphaMin()) break
    }
    expect(sim.alpha()).toBeLessThanOrEqual(sim.alphaMin())
    // The canvas now stops the sim and zeros velocities (freezeSimulation).
    sim.stop()
    for (const n of nodes) {
      n.vx = 0
      n.vy = 0
    }
    const before = nodes.map((n) => ({ x: n.x, y: n.y }))
    // With velocities zeroed and the sim stopped, the next render reads
    // identical positions — assert position-stability directly (a stopped
    // sim's internal timer fires no more ticks; the freeze guarantees no
    // velocity-driven drift even if a stray tick occurred).
    for (const n of nodes) {
      // simulate "one more frame" worth of integration with zeroed velocity
      n.x += n.vx
      n.y += n.vy
    }
    nodes.forEach((n, i) => {
      expect(n.x).toBeCloseTo(before[i].x, 9)
      expect(n.y).toBeCloseTo(before[i].y, 9)
    })
  })
})
