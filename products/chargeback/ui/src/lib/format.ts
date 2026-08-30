export function num(v: number | string | null | undefined, digits = 4): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = typeof v === 'number' ? v : Number(v)
  if (Number.isNaN(n)) return String(v)
  return n.toLocaleString(undefined, { maximumFractionDigits: digits })
}

export function money(v: number | string | null | undefined, currency?: string): string {
  if (v === null || v === undefined || v === '') return '—'
  const n = typeof v === 'number' ? v : Number(v)
  if (Number.isNaN(n)) return String(v)
  return `${n.toLocaleString(undefined, { minimumFractionDigits: 3, maximumFractionDigits: 3 })} ${currency ?? ''}`.trim()
}

export function when(v: string | null | undefined): string {
  if (!v) return '—'
  const d = new Date(v)
  if (Number.isNaN(d.getTime())) return v
  return d.toISOString().replace('T', ' ').slice(0, 16) + 'Z'
}

export function day(v: string | null | undefined): string {
  if (!v) return '—'
  return v.length >= 10 ? v.slice(0, 10) : v
}

/** first day of the current month as YYYY-MM-DD */
export function monthStart(d = new Date()): string {
  return `${d.getUTCFullYear()}-${String(d.getUTCMonth() + 1).padStart(2, '0')}-01`
}

export function today(d = new Date()): string {
  return d.toISOString().slice(0, 10)
}

/** previous calendar month as YYYY-MM (the default statement period). */
export function lastMonth(d = new Date()): string {
  const y = d.getUTCFullYear()
  const m = d.getUTCMonth() // 0-based; previous month
  const py = m === 0 ? y - 1 : y
  const pm = m === 0 ? 12 : m
  return `${py}-${String(pm).padStart(2, '0')}`
}
