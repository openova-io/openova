// Money / percentage formatting (#6867). The chart lane ships the full
// version with tests; this file carries the same exported signatures so the
// frame compiles on its own — on merge, theirs wins.

export function formatMoney(v: number | string | null | undefined, currency?: string, opts?: { compact?: boolean; digits?: number }): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = typeof v === 'number' ? v : Number(v)
  if (!Number.isFinite(n)) return '—'
  const digits = opts?.digits ?? 3
  let text: string
  if (opts?.compact && Math.abs(n) >= 1000) {
    const abs = Math.abs(n)
    const [div, suffix] = abs >= 1e9 ? [1e9, 'B'] : abs >= 1e6 ? [1e6, 'M'] : [1e3, 'k']
    text = `${(n / div).toLocaleString('en-GB', { maximumFractionDigits: 2, minimumFractionDigits: 0 })}${suffix}`
  } else {
    text = n.toLocaleString('en-GB', { minimumFractionDigits: digits, maximumFractionDigits: digits })
  }
  return currency ? `${text} ${currency}` : text
}

export function formatPct(v: number | null | undefined, opts?: { sign?: boolean; digits?: number }): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—'
  const digits = opts?.digits ?? 1
  const abs = Math.abs(v).toLocaleString('en-GB', { minimumFractionDigits: digits, maximumFractionDigits: digits })
  const sign = v < 0 ? '−' : opts?.sign && v > 0 ? '+' : ''
  return `${sign}${abs} %`
}

export function formatQty(v: number | string | null | undefined, unit?: string): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = typeof v === 'number' ? v : Number(v)
  if (!Number.isFinite(n)) return '—'
  const text = n.toLocaleString('en-GB', { maximumFractionDigits: 2 })
  return unit ? `${text} ${unit}` : text
}

export function formatDelta(cur: number, prev: number): number | null {
  if (!prev) return null
  return ((cur - prev) / prev) * 100
}
