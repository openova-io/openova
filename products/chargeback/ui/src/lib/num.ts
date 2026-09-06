/** Wire values arrive as JSON numbers or numeric strings; sum them as numbers. */
export function toNumber(v: number | string | null | undefined): number {
  if (v === null || v === undefined || v === '') return 0
  const n = typeof v === 'number' ? v : Number(v)
  return Number.isFinite(n) ? n : 0
}

/** Round to `dp` decimals without the binary noise of `x * 8760`. */
export function round(v: number, dp: number): number {
  const f = 10 ** dp
  return Math.round(v * f) / f
}
