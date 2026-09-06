/**
 * Number formatting for the cost-analysis surfaces (#6867).
 *
 * Locale is pinned to en-US so a value renders identically in every browser
 * and in the node test run; the thin space before % and the true minus sign
 * (U+2212) are deliberate — a hyphen-minus reads as a dash in a table.
 */

const LOCALE = 'en-US'
export const MINUS = '−'
export const NA = '—'

/** Accepts a number or a numeric string (the API's Decimal fields arrive as JSON numbers, older callers pass strings). */
type Numeric = number | string | null | undefined

function num(v: Numeric): number | null {
  const n = typeof v === 'number' ? v : typeof v === 'string' && v.trim() !== '' ? Number(v) : NaN
  return Number.isFinite(n) ? n : null
}

function finite(v: unknown): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

function fixed(abs: number, digits: number): string {
  return abs.toLocaleString(LOCALE, { minimumFractionDigits: digits, maximumFractionDigits: digits })
}

/** Up to `digits` decimals, trailing zeros trimmed, thousands separated. */
export function formatNumber(v: Numeric, digits = 3): string {
  const nv = num(v)
  if (nv === null) return NA
  v = nv
  const s = Math.abs(v).toLocaleString(LOCALE, { maximumFractionDigits: digits })
  return v < 0 && s !== '0' ? MINUS + s : s
}

const UNITS: ReadonlyArray<[number, string]> = [
  [1e9, 'B'],
  [1e6, 'M'],
  [1e3, 'k'],
]

/**
 * formatCompact renders three significant digits with a k / M / B suffix:
 * 2780.012 → "2.78k", 1200000 → "1.2M", 104.16 → "104", 0.48 → "0.48".
 */
export function formatCompact(v: Numeric, significant = 3): string {
  const nv = num(v)
  if (nv === null) return NA
  v = nv
  const abs = Math.abs(v)
  if (abs === 0) return '0'
  const sig = (x: number) => {
    const decimals = Math.max(0, significant - 1 - Math.floor(Math.log10(x)))
    return x.toLocaleString(LOCALE, { maximumFractionDigits: Math.min(decimals, 12) })
  }
  let out = sig(abs)
  for (const [unit, suffix] of UNITS) {
    const x = abs / unit
    // 999.6k would round to "1,000k": promote it to the next unit instead.
    if (x >= 1 || (x >= 0.9995 && Number(sig(x)) === 1)) {
      out = sig(x) + suffix
      break
    }
  }
  return v < 0 ? MINUS + out : out
}

export interface MoneyOptions {
  compact?: boolean
  /** Fraction digits for the full form (default 3 — OMR has three). */
  digits?: number
}

/** formatMoney(2780.012, 'OMR') → "2,780.012 OMR"; compact → "2.78k OMR". */
export function formatMoney(v: Numeric, currency: string, opts: MoneyOptions = {}): string {
  const nv = num(v)
  if (nv === null) return NA
  v = nv
  const cur = currency ? ` ${currency}` : ''
  if (opts.compact) return formatCompact(v) + cur
  const digits = opts.digits ?? 3
  const s = fixed(Math.abs(v), digits)
  const zero = Number(s.replace(/,/g, '')) === 0
  return (v < 0 && !zero ? MINUS : '') + s + cur
}

export interface PctOptions {
  /** Prefix positive values with "+". */
  sign?: boolean
  /** Fraction digits; default 1, or 0 once |v| ≥ 100. */
  digits?: number
}

/** formatPct(5.8, {sign:true}) → "+5.8 %"; −3.1 → "−3.1 %"; null → "—". */
export function formatPct(v: number | null | undefined, opts: PctOptions = {}): string {
  if (!finite(v)) return NA
  const abs = Math.abs(v)
  const digits = opts.digits ?? (abs >= 100 ? 0 : 1)
  const s = fixed(abs, digits)
  const zero = Number(s.replace(/,/g, '')) === 0
  const prefix = zero ? '' : v < 0 ? MINUS : opts.sign ? '+' : ''
  return `${prefix}${s} %`
}

/** formatQty(84, 'vcpu-hour') → "84 vcpu-hour"; 1234.5678 GB → "1,234.568 GB". */
export function formatQty(v: Numeric, unit?: string | null): string {
  const nv = num(v)
  if (nv === null) return NA
  v = nv
  const n = formatNumber(v, 3)
  return unit ? `${n} ${unit}` : n
}

/**
 * deltaPct is the change from `prev` to `cur` in percent, or null when there
 * is nothing to compare against (prev = 0, or either side is not a number) —
 * the same rule the API applies to delta_pct.
 */
export function deltaPct(cur: number | null | undefined, prev: number | null | undefined): number | null {
  if (!finite(cur) || !finite(prev) || prev === 0) return null
  return ((cur - prev) / Math.abs(prev)) * 100
}

/** formatDelta(105.8, 100) → "+5.8 %"; formatDelta(5, 0) → "—". */
export function formatDelta(cur: number | null | undefined, prev: number | null | undefined): string {
  return formatPct(deltaPct(cur, prev), { sign: true })
}
