/**
 * Allocation helpers (#6865) — pure, so they can be asserted without pulling
 * React or the router into a test.
 */

export const pct = (v: number) => `${(v * 100).toFixed(1)}%`

/**
 * n2 formats an hour count for display.
 *
 * DISPLAY ONLY. toFixed rounds the binary value, so 1.005 renders as 1.00 —
 * acceptable for hour counts, and never used for money: every currency figure
 * is computed server-side in exact big.Rat.
 *
 * Non-finite renders as a dash: an operator reading a cost table should see
 * "no value", not "NaN".
 */
export const n2 = (v: number) => (Number.isFinite(v) ? v.toFixed(2) : '—')

/**
 * reconciles reports whether a split accounts for the whole window.
 *
 * A split whose shares do not sum to 1 has lost cost between the collected
 * cloud total and the rows an operator is shown — and on screen it looks
 * identical to a complete one. The tolerance is for IEEE754: 0.1+0.2+0.7 is
 * not exactly 1, and an exact comparison would flag a correct split as broken.
 *
 * An empty result is vacuously fine: there is nothing to lose, and warning
 * about it would train the operator to ignore the warning that matters.
 */
export function reconciles(rowCount: number, shareTotal: number): boolean {
  if (rowCount === 0) return true
  return Math.abs(shareTotal - 1) < 1e-6
}
