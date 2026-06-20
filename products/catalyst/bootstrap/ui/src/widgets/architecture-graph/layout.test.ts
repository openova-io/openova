/**
 * layout.test.ts — locks the #3958 / fix/cloud-graph-spread-layout
 * contract on `physicsFor` and the settled force-directed layout:
 *
 *   1. ELLIPTICAL field geometry: on a wide-short canvas the X-field
 *      (anchored to width/2) is materially larger than the Y-field
 *      (anchored to height/2), so the cloud fills BOTH axes instead of
 *      collapsing into a min(W,H)/2 disc with empty left/right margin.
 *   2. Constant-density clamp invariants hold across N = 4 / 64 / 400.
 *   3. The settled layout (driven headlessly through the real d3-force
 *      sim with production charge/collide/gravity + the elliptical cap)
 *      fills ≥75% of BOTH the canvas width and height for N = 64, while a
 *      4-node graph stays a tidy CENTRED cluster (not flung to corners).
 *
 * Approach note (per the brief): jsdom has no rAF loop, but d3-force's
 * `simulation.tick()` is fully synchronous, so we drive the sim to a
 * settled state deterministically (seeded RNG + fixed tick budget) and
 * assert the bounding-box coverage directly. The cap used here is the
 * SAME elliptical boundary the canvas installs (mirrored inline so the
 * test owns no DOM); the geometry source of truth — `physicsFor` — is
 * imported directly.
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
import { physicsFor, NODE_R, BOUND_PADDING } from './layout'

/* ── geometry contract (pure, no sim) ───────────────────────────── */

describe('physicsFor — elliptical, viewport-filling field', () => {
  const W = 1100
  const H = 600

  it('X-field tracks width/2 and Y-field tracks height/2 — they differ when W≠H', () => {
    for (const n of [4, 64, 400]) {
      const p = physicsFor(n, W, H)
      // The wide axis must be materially larger than the short axis.
      expect(p.fieldRadiusX).toBeGreaterThan(p.fieldRadiusY)
      // …and by roughly the canvas aspect ratio (same density factor on
      // both axes ⇒ the ratio is the half-extent ratio when neither axis
      // is clamped). At the very least it must exceed a short-dim circle.
      const shortDimCircleR = Math.min(W, H) / 2 // what the OLD code used
      // For a mid-size graph the X field reaches past the short-dim radius
      // territory the old circle was stuck inside.
      if (n >= 64) {
        expect(p.fieldRadiusX).toBeGreaterThan(shortDimCircleR * 0.5)
      }
    }
  })

  it('a square canvas yields equal X/Y fields (no spurious asymmetry)', () => {
    const p = physicsFor(64, 800, 800)
    expect(p.fieldRadiusX).toBeCloseTo(p.fieldRadiusY, 6)
  })

  it('each axis is clamped inside its own half-extent (never spills the edge)', () => {
    for (const n of [4, 64, 400, 4000]) {
      const p = physicsFor(n, W, H)
      expect(p.fieldRadiusX).toBeLessThanOrEqual(W / 2)
      expect(p.fieldRadiusY).toBeLessThanOrEqual(H / 2)
      // …and never collapses to a dot, even for a 4-node graph.
      expect(p.fieldRadiusX).toBeGreaterThan(W / 2 * 0.1)
      expect(p.fieldRadiusY).toBeGreaterThan(H / 2 * 0.1)
    }
  })

  it('the field grows with sqrt(N) (constant density) on BOTH axes', () => {
    const p4 = physicsFor(4, W, H)
    const p64 = physicsFor(64, W, H)
    const p400 = physicsFor(400, W, H)
    expect(p64.fieldRadiusX).toBeGreaterThan(p4.fieldRadiusX)
    expect(p400.fieldRadiusX).toBeGreaterThanOrEqual(p64.fieldRadiusX)
    expect(p64.fieldRadiusY).toBeGreaterThan(p4.fieldRadiusY)
    expect(p400.fieldRadiusY).toBeGreaterThanOrEqual(p64.fieldRadiusY)
  })

  it('scalar fieldRadius is the SHORT axis (min of X/Y)', () => {
    const p = physicsFor(64, W, H)
    expect(p.fieldRadius).toBe(Math.min(p.fieldRadiusX, p.fieldRadiusY))
  })

  it('centerGravity is now weak (anti-drift only, not center-crush)', () => {
    for (const n of [4, 64, 400, 8000]) {
      const p = physicsFor(n, W, H)
      expect(p.centerGravity).toBeGreaterThan(0)
      expect(p.centerGravity).toBeLessThanOrEqual(0.05)
    }
  })

  it('charge is a dominant spreading force (strongly negative for small N)', () => {
    // The whole point of the fix: repulsion must be strong enough to push
    // nodes out to fill the field instead of collapsing to the centre.
    expect(physicsFor(64, W, H).charge).toBeLessThanOrEqual(-150)
    // …and softens (less negative) as N grows so a huge graph stays bounded.
    expect(physicsFor(8000, W, H).charge).toBeGreaterThan(physicsFor(64, W, H).charge)
  })
})

/* ── settled-layout contract (headless d3-force) ────────────────── */

/**
 * A small deterministic PRNG so the settled layout is reproducible in CI
 * (d3-force seeds initial positions from Math.random via its phyllotaxis
 * fallback only when x/y are unset — we set them ourselves, so the only
 * randomness is our jitter seed here).
 */
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
 * Mirror of GraphCanvas.makeForceRadialCap — the elliptical soft cap. Kept
 * inline so this test owns no DOM/component import; the geometry it caps to
 * comes straight from physicsFor (the source of truth under test).
 */
function ellipticalCap(cx: number, cy: number, rx: number, ry: number) {
  let nodes: SimNode[] = []
  const force = (alpha: number) => {
    if (rx <= 0 || ry <= 0) return
    for (const n of nodes) {
      if (n.fx !== null || n.fy !== null) continue
      const dx = n.x - cx
      const dy = n.y - cy
      const nx = dx / rx
      const ny = dy / ry
      const norm2 = nx * nx + ny * ny
      if (norm2 <= 1 || norm2 === 0) continue
      const overshoot = Math.sqrt(norm2) - 1
      const k = Math.min(0.25, overshoot) * alpha
      n.vx -= dx * k
      n.vy -= dy * k
    }
  }
  ;(force as unknown as { initialize: (n: SimNode[]) => void }).initialize = (next) => {
    nodes = next
  }
  return force as ((alpha: number) => void) & { initialize: (n: SimNode[]) => void }
}

/**
 * Mirror of GraphCanvas.makeForceBound — the HARD viewport-edge clamp +
 * velocity-reflection safety net. Production installs this alongside the
 * soft elliptical cap; we mirror it so the settle test is faithful (the
 * hard bound is what guarantees no node ever leaves the canvas).
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

/** Build N seeded nodes clustered near the centroid (as the canvas seeds). */
function seedNodes(count: number, cx: number, cy: number, seedR: number, rng: () => number): SimNode[] {
  const out: SimNode[] = []
  for (let i = 0; i < count; i++) {
    const ang = rng() * 2 * Math.PI
    const rad = Math.sqrt(rng()) * seedR
    out.push({
      x: cx + Math.cos(ang) * rad,
      y: cy + Math.sin(ang) * rad,
      vx: 0,
      vy: 0,
      fx: null,
      fy: null,
      degree: 0,
    })
  }
  return out
}

/** Drive the production force config to a settled state, return the nodes. */
function settle(count: number, W: number, H: number, seed = 1): SimNode[] {
  const phys = physicsFor(count, W, H)
  const cx = W / 2
  const cy = H / 2
  const rng = mulberry32(seed)
  const seedR = Math.min(60, phys.fieldRadius * 0.5)
  const nodes = seedNodes(count, cx, cy, seedR, rng)

  const sim: Simulation<SimNode, undefined> = forceSimulation<SimNode>(nodes)
    .force('charge', forceManyBody<SimNode>().strength(phys.charge))
    .force(
      'collide',
      forceCollide<SimNode>().radius(NODE_R + 4).strength(0.9),
    )
    .force('center', forceCenter<SimNode>(cx, cy))
    .force('gravityX', forceX<SimNode>(cx).strength(phys.centerGravity))
    .force('gravityY', forceY<SimNode>(cy).strength(phys.centerGravity))
    .force('field', ellipticalCap(cx, cy, phys.fieldRadiusX, phys.fieldRadiusY))
    .force('bound', hardBound(W, H, BOUND_PADDING, NODE_R))
    .alphaDecay(phys.alphaDecay)
    .alphaTarget(0)
    .stop()

  // No links in this isolation test — we measure the spread of the field
  // itself (the founder complaint is about the empty margin, not topology).
  // Tick to convergence: enough iterations for alpha to decay below the
  // default 0.001 stop threshold given phys.alphaDecay.
  const ticks = Math.ceil(Math.log(0.001) / Math.log(1 - phys.alphaDecay)) + 50
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

describe('settled layout fills the canvas (headless d3-force)', () => {
  const W = 1100
  const H = 600

  it('N=64 fills ≥75% of BOTH width and height', () => {
    const nodes = settle(64, W, H, 42)
    const b = bbox(nodes)
    expect(b.w).toBeGreaterThanOrEqual(0.75 * W)
    expect(b.h).toBeGreaterThanOrEqual(0.75 * H)
  })

  it('N=64 — no two node centres closer than the collide radius', () => {
    const nodes = settle(64, W, H, 7)
    const collideR = NODE_R + 4 // forceCollide radius used above
    // d3 collide resolves overlaps softly; allow a small tolerance for the
    // residual sub-radius penetration at the settle threshold.
    const minAllowed = collideR * 0.9
    let minDist = Infinity
    for (let i = 0; i < nodes.length; i++) {
      for (let j = i + 1; j < nodes.length; j++) {
        const d = Math.hypot(nodes[i].x - nodes[j].x, nodes[i].y - nodes[j].y)
        minDist = Math.min(minDist, d)
      }
    }
    expect(minDist).toBeGreaterThanOrEqual(minAllowed)
  })

  it('N=4 stays a tidy CENTRED cluster (not flung to the corners)', () => {
    const nodes = settle(4, W, H, 3)
    const b = bbox(nodes)
    const cx = W / 2
    const cy = H / 2
    // The cluster's bounding box centre is near the canvas centre.
    expect(Math.abs((b.minX + b.maxX) / 2 - cx)).toBeLessThan(0.18 * W)
    expect(Math.abs((b.minY + b.maxY) / 2 - cy)).toBeLessThan(0.18 * H)
    // …and it does NOT sprawl to the edges (a tidy small cluster).
    expect(b.w).toBeLessThan(0.6 * W)
    expect(b.h).toBeLessThan(0.6 * H)
  })

  it('N=400 fills the canvas (no dense central blob) and stays inside the viewport', () => {
    const nodes = settle(400, W, H, 11)
    const b = bbox(nodes)
    // Wide spread on both axes — fills the canvas, NOT a central blob…
    expect(b.w).toBeGreaterThanOrEqual(0.75 * W)
    expect(b.h).toBeGreaterThanOrEqual(0.75 * H)
    // …and the hard bound guarantees no node ever leaves the viewport.
    const edge = NODE_R + BOUND_PADDING
    expect(b.minX).toBeGreaterThanOrEqual(edge - 0.5)
    expect(b.maxX).toBeLessThanOrEqual(W - edge + 0.5)
    expect(b.minY).toBeGreaterThanOrEqual(edge - 0.5)
    expect(b.maxY).toBeLessThanOrEqual(H - edge + 0.5)
  })
})
