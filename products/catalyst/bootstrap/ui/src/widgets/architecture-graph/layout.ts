/**
 * layout.ts — the constant-density, viewport-aware force tuning for the
 * unified Cloud-graph (#3958). Pure math, no DOM — lives in its own
 * module so GraphCanvas.tsx stays a component-only file
 * (react-refresh/only-export-components) and so the tuning is unit-test
 * lockable.
 *
 * The founder repeatedly rejected two failure modes:
 *   • center-crush  — small graphs collapse into an overlapping blob.
 *   • edge-fling    — charge repulsion blows nodes to the canvas edge,
 *                     centre empty.
 *
 * The fix is to make the spread a function of √N so DENSITY stays
 * constant: 4 nodes → a small centred cluster, 60 nodes → a
 * proportionally larger field at the SAME density. physicsFor() targets a
 * field whose RADII scale with √N (area ∝ N), then derives the per-tick
 * forces from that field:
 *   • The field is an ELLIPSE, not a short-dim circle: fieldRadiusX is
 *     anchored to width/2 and fieldRadiusY to height/2. On a wide-short
 *     canvas (~1100×600) this fills BOTH axes instead of collapsing into
 *     a min(W,H)/2 disc centred in a sea of empty left/right margin.
 *   • centerGravity is a WEAK anti-drift pull only (~0.02–0.05) — it
 *     parks the equilibrium against drift without crushing the cloud to
 *     the centre. Repulsion (charge) is the dominant spreading force.
 *   • linkDistance is CLAMPED to [minLink, maxLink]: minLink ≥
 *     2·maxNodeR + pad prevents overlap; maxLink caps edge-fling.
 *   • collide has a HARD floor so two bubbles can never overlap.
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

/** Reference for the constant-density spread — at N_REF nodes the field
 *  fills FIELD_FRAC of each viewport half-extent. */
const DENSITY_N_REF = 40
/** Target field radius as a fraction of a half-extent at N_REF nodes.
 *  ~0.85 fills the canvas generously (the elliptical cap clamps to
 *  FIELD_MAX_FRAC, so large graphs reach ~85% of BOTH axes). */
const FIELD_FRAC = 0.85
/** Upper clamp: the field never exceeds this fraction of a half-extent,
 *  so the cloud fills the canvas without spilling past the edge padding. */
const FIELD_MAX_FRAC = 0.88
/** Lower clamp: the field never drops below this fraction of a half-extent,
 *  so even a 1–4 node graph keeps "social distance" (a tidy cluster, not a
 *  single overlapping dot) while staying centred. */
const FIELD_MIN_FRAC = 0.18
/** Max node radius (degree-driven) used to floor link length + collide so
 *  bubbles never overlap. Mirrors radiusForDegree's clamp ceiling. */
const MAX_NODE_R = 20

export interface PhysicsParams {
  charge: number
  linkDistance: number
  minLink: number
  maxLink: number
  linkStrength: number
  collide: number
  alphaDecay: number
  centerGravity: number
  /** The target field radius on the X axis — anchored to width/2. The
   *  elliptical radial cap uses this so the cloud fills the canvas WIDTH
   *  on a wide-short viewport instead of collapsing into a min(W,H) disc. */
  fieldRadiusX: number
  /** The target field radius on the Y axis — anchored to height/2. */
  fieldRadiusY: number
  /** Scalar convenience = min(fieldRadiusX, fieldRadiusY). Retained for
   *  seeding-jitter and any caller that wants a single scalar; the radial
   *  CAP itself is elliptical (uses X and Y independently). */
  fieldRadius: number
}

export function physicsFor(nodeCount: number, width = 800, height = 480): PhysicsParams {
  const n = Math.max(1, nodeCount)
  const halfW = width / 2
  const halfH = height / 2
  // Constant-density spread factor: grows with √N (area ∝ N), anchored so
  // N_REF nodes fill FIELD_FRAC of each half-extent. The SAME factor is
  // applied to both axes so density stays uniform; the asymmetry between
  // fieldRadiusX and fieldRadiusY comes purely from the canvas aspect
  // ratio (width/2 vs height/2). On a wide-short canvas this yields a
  // genuinely WIDER field that fills the empty left/right margin.
  const densityFactor = FIELD_FRAC * Math.sqrt(n / DENSITY_N_REF)
  // Per-axis field radius. Clamp each independently to its own half-extent
  // so neither axis spills past the edge padding nor collapses to a dot.
  const fieldRadiusX = Math.max(
    halfW * FIELD_MIN_FRAC,
    Math.min(halfW * FIELD_MAX_FRAC, halfW * densityFactor),
  )
  const fieldRadiusY = Math.max(
    halfH * FIELD_MIN_FRAC,
    Math.min(halfH * FIELD_MAX_FRAC, halfH * densityFactor),
  )
  // Scalar = the SHORT axis (min) so seeding-jitter never seeds outside the
  // tighter dimension; the cap below is elliptical and uses X/Y directly.
  const fieldRadius = Math.min(fieldRadiusX, fieldRadiusY)

  // Hard collision floor: two MAX_NODE_R bubbles + a little air. This is
  // the no-overlap guarantee independent of N.
  const collide = MAX_NODE_R + 6

  // Link length clamp. minLink keeps connected bubbles from overlapping;
  // maxLink prevents a long chain from flinging an endpoint to the edge.
  const minLink = 2 * MAX_NODE_R + 10 // ≥ sum-of-radii + pad
  // maxLink scales with the field but never lets one edge span the whole
  // cloud — keeps the natural topology readable without edge-fling.
  const maxLink = Math.max(minLink + 8, Math.min(fieldRadius * 0.9, 160))
  // Nominal link distance scales mildly down with N so dense graphs pack
  // tighter (constant density) but always within [minLink, maxLink].
  const linkDistance = Math.max(minLink, Math.min(maxLink, 90 - Math.log2(n) * 6))

  // Charge (repulsion) is now the DOMINANT spreading force — strong enough
  // to push nodes OUT to fill the field instead of collapsing to the
  // centre. It softens as N grows so a huge graph stays bounded (the
  // elliptical cap catches the overshoot). centerGravity is ONLY anti-drift
  // now (~0.02–0.05) — it keeps the cloud from wandering off-centre without
  // crushing it inward; the field's shape is owned by charge + collide +
  // the elliptical cap, not by gravity.
  let charge: number
  let linkStrength: number
  let alphaDecay: number
  let centerGravity: number
  if (n <= 50) {
    charge = -240
    linkStrength = 0.5
    alphaDecay = 0.022
    centerGravity = 0.03
  } else if (n <= 200) {
    charge = -150
    linkStrength = 0.45
    alphaDecay = 0.026
    centerGravity = 0.035
  } else if (n <= 1000) {
    charge = -90
    linkStrength = 0.38
    alphaDecay = 0.03
    centerGravity = 0.04
  } else if (n <= 5000) {
    charge = -45
    linkStrength = 0.25
    alphaDecay = 0.04
    centerGravity = 0.045
  } else {
    charge = -24
    linkStrength = 0.15
    alphaDecay = 0.05
    centerGravity = 0.05
  }

  return {
    charge,
    linkDistance,
    minLink,
    maxLink,
    linkStrength,
    collide,
    alphaDecay,
    centerGravity,
    fieldRadiusX,
    fieldRadiusY,
    fieldRadius,
  }
}
