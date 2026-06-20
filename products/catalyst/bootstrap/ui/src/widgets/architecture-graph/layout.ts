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
 * field RADIUS that scales with √N (area ∝ N), then derives the per-tick
 * forces from that field:
 *   • centerGravity parks the equilibrium inside the field (~70% of the
 *     viewport, never the literal edges).
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
 *  fills FIELD_FRAC of the viewport's smaller half-extent. */
const DENSITY_N_REF = 40
/** Target field radius as a fraction of min(W,H)/2 at N_REF nodes.
 *  ~0.7 keeps the cloud at ~70% of the viewport, never the edges. */
const FIELD_FRAC = 0.7
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
  /** The target field radius — the canvas uses it for the soft radial
   *  containment so the cloud parks at ~70% of the viewport. */
  fieldRadius: number
}

export function physicsFor(nodeCount: number, width = 800, height = 480): PhysicsParams {
  const n = Math.max(1, nodeCount)
  const halfExtent = Math.min(width, height) / 2
  // Constant-density target field radius: grows with √N (area ∝ N),
  // anchored so N_REF nodes fill FIELD_FRAC of the half-extent. Clamped
  // so 1-4 nodes stay a tight centred cluster (not a single dot) and a
  // huge graph never exceeds the viewport.
  const rawField = FIELD_FRAC * halfExtent * Math.sqrt(n / DENSITY_N_REF)
  const fieldRadius = Math.max(
    // floor: at least enough room for a few non-overlapping bubbles.
    Math.min(halfExtent * 0.35, 3 * (MAX_NODE_R + BOUND_PADDING)),
    Math.min(halfExtent * 0.92, rawField),
  )

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

  // Charge (repulsion) softens as N grows so the field stays bounded;
  // gravity is the complementary inward pull that parks the equilibrium
  // inside fieldRadius. Both scale gently with N.
  let charge: number
  let linkStrength: number
  let alphaDecay: number
  let centerGravity: number
  if (n <= 50) {
    charge = -70
    linkStrength = 0.5
    alphaDecay = 0.022
    centerGravity = 0.12
  } else if (n <= 200) {
    charge = -55
    linkStrength = 0.45
    alphaDecay = 0.026
    centerGravity = 0.11
  } else if (n <= 1000) {
    charge = -40
    linkStrength = 0.38
    alphaDecay = 0.03
    centerGravity = 0.1
  } else if (n <= 5000) {
    charge = -22
    linkStrength = 0.25
    alphaDecay = 0.04
    centerGravity = 0.09
  } else {
    charge = -13
    linkStrength = 0.15
    alphaDecay = 0.05
    centerGravity = 0.08
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
    fieldRadius,
  }
}
