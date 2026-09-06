/**
 * Axis helpers shared by every chart. Pure functions, tested in scale.test.ts.
 */

const STEP_CANDIDATES = [1, 2, 2.5, 5, 10]

/** Rounds away floating-point residue so 3 × 0.1 renders as 0.3, not 0.30000000000000004. */
function snap(v: number, step: number): number {
  const decimals = Math.max(0, 1 - Math.floor(Math.log10(step)))
  return Number(v.toFixed(Math.min(decimals, 12)))
}

/**
 * niceStep picks the tick spacing (1 / 2 / 2.5 / 5 × 10ⁿ) whose resulting
 * tick count is closest to `count`; ties go to the finer step so the data
 * fills more of the plot instead of leaving a third of it empty.
 */
export function niceStep(max: number, count = 5): number {
  const want = Math.max(2, Math.floor(count))
  const raw = max / (want - 1)
  const mag = Math.pow(10, Math.floor(Math.log10(raw)))
  let best = mag
  let bestScore = Infinity
  for (const c of STEP_CANDIDATES) {
    const step = c * mag
    const n = Math.ceil(max / step - 1e-9) + 1
    const score = Math.abs(n - want)
    if (score < bestScore) {
      best = step
      bestScore = score
    }
  }
  return best
}

/**
 * niceTicks returns ascending "nice" tick values from 0 to at least `max`.
 * A non-positive or non-finite max yields [0, 1]: a flat-zero series that WAS
 * measured still deserves a baseline rather than a divide-by-zero.
 */
export function niceTicks(max: number, count = 5): number[] {
  if (!Number.isFinite(max) || max <= 0) return [0, 1]
  const step = niceStep(max, count)
  const n = Math.ceil(max / step - 1e-9)
  const ticks: number[] = []
  for (let i = 0; i <= n; i++) ticks.push(snap(i * step, step))
  return ticks
}

/** The top tick niceTicks would produce for `max`. */
export function niceMax(max: number, count = 5): number {
  const t = niceTicks(max, count)
  return t[t.length - 1]
}

/** Ticks spanning [min, max] when min may be negative; 0 is always a tick. */
export function linearTicks(min: number, max: number, count = 5): number[] {
  const lo = Number.isFinite(min) ? Math.min(min, 0) : 0
  const hi = Number.isFinite(max) ? Math.max(max, 0) : 0
  if (lo === 0) return niceTicks(hi, count)
  const step = niceStep(Math.max(hi, -lo), count)
  const from = Math.floor(lo / step + 1e-9)
  const to = Math.ceil(hi / step - 1e-9)
  const ticks: number[] = []
  for (let i = from; i <= to; i++) ticks.push(snap(i * step, step))
  return ticks
}

/**
 * xLabelEvery returns the label stride that keeps bucket labels at least
 * `labelWidth` px apart: 1 = every bucket, 3 = every third.
 */
export function xLabelEvery(n: number, width: number, labelWidth = 48): number {
  if (!Number.isFinite(n) || n <= 0) return 1
  return Math.max(1, Math.ceil((n * labelWidth) / Math.max(width, 1)))
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec']

/** shortBucket abbreviates an API bucket for an axis: 2026-09-01 → "1 Sep", 2026-09 → "Sep 2026". */
export function shortBucket(b: string): string {
  const day = /^(\d{4})-(\d{2})-(\d{2})$/.exec(b)
  if (day) return `${Number(day[3])} ${MONTHS[Number(day[2]) - 1] ?? day[2]}`
  const month = /^(\d{4})-(\d{2})$/.exec(b)
  if (month) return `${MONTHS[Number(month[2]) - 1] ?? month[2]} ${month[1]}`
  return b
}

/** Approximate rendered width of 12-px system-ui text. */
export const CHAR_PX = 6.7

/** fitLabel truncates with an ellipsis when the text would exceed `maxPx`. */
export function fitLabel(text: string, maxPx: number, charPx = CHAR_PX): string {
  if (text.length * charPx <= maxPx) return text
  const keep = Math.floor(maxPx / charPx) - 1
  if (keep < 1) return '…'
  return text.slice(0, keep) + '…'
}
