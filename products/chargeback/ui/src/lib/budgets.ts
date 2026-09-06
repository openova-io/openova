// Budget form parsing (DESIGN.md §2.7) — pure, unit-tested in budgets.test.ts.

export interface Parsed<T> {
  values: T[]
  error?: string
}

/**
 * "50, 80, 100" → [50, 80, 100]. Whole percentages, strictly ascending, at
 * least one; above 100 is allowed (alert on overrun) but nothing below 1.
 */
export function parseThresholds(text: string): Parsed<number> {
  const parts = text
    .split(/[,\s]+/)
    .map((p) => p.trim())
    .filter(Boolean)
  if (parts.length === 0) return { values: [], error: 'At least one threshold is required, e.g. 50, 80, 100' }
  const values: number[] = []
  for (const p of parts) {
    if (!/^\d+$/.test(p)) return { values: [], error: `"${p}" is not a whole percentage` }
    const n = Number(p)
    if (n < 1 || n > 1000) return { values: [], error: `${n} % is outside 1–1000` }
    if (values.length && n <= values[values.length - 1]) return { values: [], error: 'Thresholds must be strictly ascending' }
    values.push(n)
  }
  return { values }
}

const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/** "a@x.example, b@y.example" → both, lower-cased, de-duplicated; empty input is fine (no mail). */
export function parseEmails(text: string): Parsed<string> {
  const parts = text
    .split(/[,;\s]+/)
    .map((p) => p.trim().toLowerCase())
    .filter(Boolean)
  const values: string[] = []
  for (const p of parts) {
    if (!EMAIL_RE.test(p)) return { values: [], error: `"${p}" is not an email address` }
    if (!values.includes(p)) values.push(p)
  }
  return { values }
}
