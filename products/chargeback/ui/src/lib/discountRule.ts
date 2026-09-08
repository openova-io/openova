import type { DiscountRule } from '../api/types'

/**
 * Discount combination rule (DESIGN.md §2.11) — the display catalogue and a
 * client-side mirror of rating.ApplyDiscounts for the live example on the
 * Discounts page. The statement run is the authority; this is float
 * arithmetic on a four-number example, rounded to 6 dp like the engine.
 */

export const DISCOUNT_RULES: ReadonlyArray<{ value: DiscountRule; label: string; summary: string }> = [
  {
    value: 'most-specific',
    label: 'Most specific wins',
    summary: 'Per line, the percent discount with the narrowest scope applies: a SKU discount beats a whole-bill one; at the same scope the higher percent wins.',
  },
  { value: 'highest', label: 'Highest wins', summary: 'Per line, the highest percent applies, whatever its scope.' },
  { value: 'stack', label: 'Stack', summary: 'Every applicable percent is added together against the list price: 10 % + 20 % = 30 % off.' },
  { value: 'compound', label: 'Compound', summary: 'Percents multiply, each off what the one before left: 10 % then 20 % = 28 % off.' },
]

export const DEFAULT_DISCOUNT_RULE: DiscountRule = 'most-specific'

export function isDiscountRule(v: unknown): v is DiscountRule {
  return typeof v === 'string' && DISCOUNT_RULES.some((r) => r.value === v)
}

/** "Most specific wins" for a known rule; the raw value for an unknown one; "" for none. */
export function discountRuleLabel(rule: string | null | undefined): string {
  if (!rule) return ''
  return DISCOUNT_RULES.find((r) => r.value === rule)?.label ?? rule
}

export interface RuleLine {
  sku: string
  amount: number
}

export interface RuleDiscount {
  id: string
  kind: 'percent' | 'fixed' | string
  value: number
  sku?: string | null
  stackable?: boolean
}

export interface RuleOutcome {
  /** Total taken off, percent and fixed, clamped at the gross. */
  total: number
  /** Percent taken off each input line, by index. */
  percentByLine: number[]
  /** What each discount took, by id (0 for a superseded one). */
  byDiscount: Record<string, number>
  /** Superseded discount id → the id that beat it (most-specific / highest). */
  superseded: Record<string, string>
}

const round6 = (v: number) => Math.round(v * 1e6) / 1e6
const scopeRank = (d: RuleDiscount) => ((d.sku ?? '').trim() ? 1 : 0)

function beats(rule: DiscountRule, a: RuleDiscount, b: RuleDiscount): boolean {
  const sa = scopeRank(a)
  const sb = scopeRank(b)
  if (rule === 'highest') return a.value !== b.value ? a.value > b.value : sa > sb
  return sa !== sb ? sa > sb : a.value > b.value
}

/**
 * The same decision rating.ApplyDiscounts makes, per line: which percents
 * apply and how they combine; fixed amounts come off the remainder, in
 * every rule, and the bill never goes below zero.
 */
export function combineDiscounts(lines: RuleLine[], discounts: RuleDiscount[], rule: DiscountRule): RuleOutcome {
  const byDiscount: Record<string, number> = {}
  const superseded: Record<string, string> = {}
  const supersededBase: Record<string, number> = {}
  const applied: Record<string, boolean> = {}
  const add = (id: string, amt: number) => {
    byDiscount[id] = (byDiscount[id] ?? 0) + amt
    if (amt > 0) applied[id] = true
  }
  const pcts = discounts.filter((d) => d.kind === 'percent' && Number.isFinite(d.value) && d.value > 0)
  const percentByLine = lines.map((line) => {
    const base = Math.max(0, line.amount)
    if (base <= 0) return 0
    const cands = pcts.filter((d) => !(d.sku ?? '').trim() || (d.sku ?? '').trim() === line.sku)
    let off = 0
    if (rule === 'stack') {
      for (const c of cands) {
        const amt = (base * c.value) / 100
        add(c.id, amt)
        off += amt
      }
    } else if (rule === 'compound') {
      const ordered = [...cands].sort((a, b) => scopeRank(b) - scopeRank(a) || b.value - a.value)
      let remaining = base
      for (const c of ordered) {
        const amt = (remaining * c.value) / 100
        add(c.id, amt)
        remaining -= amt
        off += amt
      }
    } else {
      let winner: RuleDiscount | null = null
      for (const c of cands) {
        if (c.stackable) continue
        if (!winner || beats(rule, c, winner)) winner = c
      }
      for (const c of cands) {
        if (c.stackable || c === winner) {
          const amt = (base * c.value) / 100
          add(c.id, amt)
          off += amt
        } else if (winner && (supersededBase[c.id] === undefined || base > supersededBase[c.id])) {
          superseded[c.id] = winner.id
          supersededBase[c.id] = base
        }
      }
    }
    return off
  })
  // A discount that won somewhere is not superseded; one that lost
  // everywhere is on the bill with 0, as the engine writes it.
  for (const id of Object.keys(superseded)) {
    if (applied[id]) delete superseded[id]
    else byDiscount[id] = 0
  }

  const gross = lines.reduce((n, l) => n + Math.max(0, l.amount), 0)
  let total = percentByLine.reduce((n, v) => n + v, 0)
  for (const d of discounts) {
    if (d.kind !== 'fixed' || !Number.isFinite(d.value) || d.value <= 0) continue
    const remaining = gross - total
    if (remaining <= 0) break
    const amt = Math.min(d.value, remaining)
    add(d.id, amt)
    total += amt
  }
  total = Math.min(total, gross)
  for (const k of Object.keys(byDiscount)) byDiscount[k] = round6(byDiscount[k])
  return { total: round6(total), percentByLine: percentByLine.map(round6), byDiscount, superseded }
}

/** The worked example on the Discounts page: list 100 — SKU A 50, SKU B 50; 10 % on everything, 20 % on A. */
export const RULE_EXAMPLE = {
  lines: [
    { sku: 'A', amount: 50 },
    { sku: 'B', amount: 50 },
  ] as RuleLine[],
  discounts: [
    { id: 'global', kind: 'percent', value: 10, sku: '' },
    { id: 'sku-a', kind: 'percent', value: 20, sku: 'A' },
  ] as RuleDiscount[],
}

export interface RuleExampleRow {
  rule: DiscountRule
  label: string
  /** Discount taken off SKU A (50, both discounts apply). */
  a: number
  /** Discount taken off SKU B (50, only the 10 % applies). */
  b: number
  total: number
}

/** One row per rule for the example above. */
export function ruleExampleRows(): RuleExampleRow[] {
  return DISCOUNT_RULES.map((r) => {
    const out = combineDiscounts(RULE_EXAMPLE.lines, RULE_EXAMPLE.discounts, r.value)
    return { rule: r.value, label: r.label, a: out.percentByLine[0], b: out.percentByLine[1], total: out.total }
  })
}
