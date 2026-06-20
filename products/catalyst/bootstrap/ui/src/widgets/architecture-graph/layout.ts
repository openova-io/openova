/**
 * layout.ts — even / homogeneous force tuning for the unified Cloud-graph
 * (#3958 / #3970). Pure math, no DOM — lives in its own module so
 * GraphCanvas.tsx stays a component-only file
 * (react-refresh/only-export-components) and so the tuning is unit-test
 * lockable.
 *
 * #3970 corrects the layout. The founder rejected BOTH failure modes:
 *   • center-crush  — small graphs collapse into an overlapping blob.
 *   • edge-fling    — charge repulsion (with the old elliptical radial
 *                     cap) parks nodes on a boundary RING with an empty
 *                     middle.
 *
 * The target is EVEN node density across the WHOLE canvas — no dense
 * centre, no edge ring. The recipe:
 *   • COLLISION is the dominant force. `forceCollide(uniformGap)` with a
 *     hard strength spaces every node at least `uniformGap` apart, where
 *     uniformGap ≈ sqrt(canvasArea / N) · GAP_K — exactly the spacing a
 *     uniform packing of N nodes over the canvas needs. This is what
 *     makes the density homogeneous.
 *   • CHARGE is mild / near-zero (a tiny anti-overlap nudge only), so it
 *     never flings nodes outward to a ring.
 *   • GENTLE centering (forceX/forceY at a low strength) only keeps the
 *     cloud from drifting off-canvas — never strong enough to crush the
 *     middle.
 *   • EVEN SEEDING (phyllotaxis sunflower over the canvas) so nodes RELAX
 *     into a uniform fill instead of starting clustered and being pushed.
 *   • No radial cap — the elliptical-cap edge-ring generator is DELETED
 *     (#3970). The hard viewport-bound clamp remains as the absolute
 *     safety net only.
 */

/**
 * Floor radius for the node disc (#348 item 10). The actual drawn radius
 * is `max(NODE_R, radiusForDegree(node.degree))`.
 */
export const NODE_R = 14

/**
 * Bound padding inside the canvas — the bounded-physics safety-net force
 * never lets a node's centre come closer than r + BOUND_PADDING to a
 * viewport edge (#348 item 5).
 */
export const BOUND_PADDING = 20

/** Max node radius (degree-driven) used to floor the collide radius so
 *  even high-degree bubbles never overlap. Mirrors radiusForDegree's
 *  clamp ceiling. */
const MAX_NODE_R = 20

/**
 * Uniform-gap tuning. The even-density gap is the side of the square each
 * node would own in a uniform packing of N nodes over the usable canvas:
 *   gap = sqrt(usableArea / N) · GAP_K
 * GAP_K < 1 packs a little tighter than a perfect square lattice so the
 * cloud reads as a filled field, not a sparse grid; the gap is then
 * clamped to [GAP_MIN, GAP_MAX] so two nodes never overlap (floor) and a
 * tiny graph doesn't sprawl edge-to-edge (ceiling).
 */
const GAP_K = 0.92
/** Floor: two MAX_NODE_R bubbles + a little air — the no-overlap guarantee. */
const GAP_MIN = 2 * MAX_NODE_R + 6
/** Ceiling: a small graph keeps tidy social distance without spanning the
 *  whole canvas. */
const GAP_MAX = 120

export interface PhysicsParams {
  /** Charge (repulsion). Mild / near-zero in the even-density model so it
   *  no longer flings nodes outward — collision owns the spacing. */
  charge: number
  linkDistance: number
  minLink: number
  maxLink: number
  linkStrength: number
  /** The collide radius — the DOMINANT spacing force. Equal to half the
   *  uniformGap so two node CENTRES settle ~uniformGap apart. */
  collide: number
  /** The even-density target gap between adjacent node centres. */
  uniformGap: number
  alphaDecay: number
  /** Gentle anti-drift centering strength (forceX/forceY). Low enough to
   *  never crush the middle. */
  centerGravity: number
  /** Retained for the centering anchor + seeding extent. Anchored to
   *  width/2 (X) and height/2 (Y) so the cloud fills BOTH axes. */
  fieldRadiusX: number
  fieldRadiusY: number
  /** Scalar convenience = min(fieldRadiusX, fieldRadiusY). */
  fieldRadius: number
}

/** Field as a fraction of each half-extent — used only as the seeding
 *  extent + the gentle-centering anchor (no hard cap). */
const FIELD_FRAC = 0.86

export function physicsFor(nodeCount: number, width = 800, height = 480): PhysicsParams {
  const n = Math.max(1, nodeCount)
  const halfW = width / 2
  const halfH = height / 2

  // Per-axis seeding/centering field — just the usable extent on each
  // axis. No radial cap clamps to it; it only bounds the seed pattern and
  // anchors the gentle centering pull.
  const fieldRadiusX = halfW * FIELD_FRAC
  const fieldRadiusY = halfH * FIELD_FRAC
  const fieldRadius = Math.min(fieldRadiusX, fieldRadiusY)

  // EVEN-DENSITY gap. Usable canvas area (inside the bound padding) divided
  // by N gives the area each node owns; its square-root is the side. GAP_K
  // packs a touch tighter so the field reads as filled.
  const usableW = Math.max(1, width - 2 * (NODE_R + BOUND_PADDING))
  const usableH = Math.max(1, height - 2 * (NODE_R + BOUND_PADDING))
  const rawGap = Math.sqrt((usableW * usableH) / n) * GAP_K
  const uniformGap = Math.max(GAP_MIN, Math.min(GAP_MAX, rawGap))

  // Collision is the DOMINANT force: radius = half the gap so two node
  // centres can't come closer than ~uniformGap. This is what makes the
  // density homogeneous across the canvas.
  const collide = uniformGap / 2

  // Link length clamp. minLink keeps connected bubbles from overlapping;
  // maxLink prevents a long chain from flinging an endpoint away. Both
  // track the uniform gap so a connected pair sits roughly one cell apart.
  const minLink = 2 * MAX_NODE_R + 10 // ≥ sum-of-radii + pad
  const maxLink = Math.max(minLink + 8, Math.min(uniformGap * 1.6, 200))
  const linkDistance = Math.max(minLink, Math.min(maxLink, uniformGap))

  // Charge is MILD / near-zero now (#3970) — collision owns spacing, so a
  // strong negative charge would only re-introduce the edge-fling. A tiny
  // negative value just helps disconnected components ease apart. It
  // softens further as N grows so a huge graph never blows up.
  let charge: number
  let linkStrength: number
  let alphaDecay: number
  let centerGravity: number
  if (n <= 50) {
    charge = -18
    linkStrength = 0.4
    alphaDecay = 0.022
    centerGravity = 0.045
  } else if (n <= 200) {
    charge = -12
    linkStrength = 0.35
    alphaDecay = 0.026
    centerGravity = 0.05
  } else if (n <= 1000) {
    charge = -8
    linkStrength = 0.3
    alphaDecay = 0.03
    centerGravity = 0.055
  } else if (n <= 5000) {
    charge = -4
    linkStrength = 0.2
    alphaDecay = 0.04
    centerGravity = 0.06
  } else {
    charge = -2
    linkStrength = 0.12
    alphaDecay = 0.05
    centerGravity = 0.065
  }

  return {
    charge,
    linkDistance,
    minLink,
    maxLink,
    linkStrength,
    collide,
    uniformGap,
    alphaDecay,
    centerGravity,
    fieldRadiusX,
    fieldRadiusY,
    fieldRadius,
  }
}

/**
 * phyllotaxisSeed — an EVEN sunflower/phyllotaxis seed position for node
 * `i` of `count`, spread over the elliptical field (fieldRadiusX ×
 * fieldRadiusY) centred at (cx, cy). The golden-angle spiral places points
 * at uniform local density across the disc, so nodes START evenly spread
 * and merely RELAX under collision — no central jitter to be flung outward
 * (#3970 replaces the old tight-central-jitter seeding).
 *
 * `jitter` (0..1) breaks the perfect lattice slightly via the supplied RNG
 * so the relaxed layout looks organic rather than a visible spiral; pass a
 * deterministic RNG in tests for reproducibility.
 */
const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5))

export function phyllotaxisSeed(
  i: number,
  count: number,
  cx: number,
  cy: number,
  fieldRadiusX: number,
  fieldRadiusY: number,
  jitter = 0,
  rng: () => number = Math.random,
): { x: number; y: number } {
  const n = Math.max(1, count)
  // Normalized radius 0..1 — sqrt keeps the areal density uniform across
  // the disc (equal-area rings). +0.5 offset avoids stacking the first few
  // points at the exact centre.
  const t = Math.sqrt((i + 0.5) / n)
  const ang = i * GOLDEN_ANGLE
  let rx = t * fieldRadiusX
  let ry = t * fieldRadiusY
  if (jitter > 0) {
    rx += (rng() - 0.5) * jitter * fieldRadiusX * 0.12
    ry += (rng() - 0.5) * jitter * fieldRadiusY * 0.12
  }
  return { x: cx + Math.cos(ang) * rx, y: cy + Math.sin(ang) * ry }
}
