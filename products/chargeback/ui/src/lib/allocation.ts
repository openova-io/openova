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

// ── Basis preview (DESIGN.md §2.8) ───────────────────────────────────────

export interface Weights {
  vcpu: number
  mem_gib: number
  pvc_gb: number
}

export interface Hours {
  vcpu_hours: number
  mem_gib_hours: number
  pvc_gb_hours: number
}

export interface BasisTerm {
  label: string
  hours: number
  weight: number
  product: number
}

/** The weighted consumption an Organization's share is taken from. */
export function basis(w: Weights, h: Hours): number {
  return h.vcpu_hours * w.vcpu + h.mem_gib_hours * w.mem_gib + h.pvc_gb_hours * w.pvc_gb
}

/** Sum of the three hour counters over rows (the window's whole consumption). */
export function sumHours(rows: Hours[]): Hours {
  return rows.reduce(
    (acc, r) => ({ vcpu_hours: acc.vcpu_hours + r.vcpu_hours, mem_gib_hours: acc.mem_gib_hours + r.mem_gib_hours, pvc_gb_hours: acc.pvc_gb_hours + r.pvc_gb_hours }),
    { vcpu_hours: 0, mem_gib_hours: 0, pvc_gb_hours: 0 },
  )
}

/** Term-by-term breakdown of `basis` for the live preview under the weight inputs. */
export function basisPreview(w: Weights, h: Hours): { terms: BasisTerm[]; total: number } {
  const terms: BasisTerm[] = [
    { label: 'vCPU-h', hours: h.vcpu_hours, weight: w.vcpu, product: h.vcpu_hours * w.vcpu },
    { label: 'GiB-h', hours: h.mem_gib_hours, weight: w.mem_gib, product: h.mem_gib_hours * w.mem_gib },
    { label: 'GB-h', hours: h.pvc_gb_hours, weight: w.pvc_gb, product: h.pvc_gb_hours * w.pvc_gb },
  ]
  return { terms, total: basis(w, h) }
}
