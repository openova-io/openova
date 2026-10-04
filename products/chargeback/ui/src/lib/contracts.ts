import type { Contract, ContractItem, PriceItem, PriceTier } from '../api/types'
import { toNumber } from './num'

// Contracts and commercial terms in the console (DESIGN.md §15). Pure
// functions only: what a term is, when a renewal is due, and — the one that
// matters most — the WORDS under a tiered or allowance-bearing price-book
// item, which must say exactly what the engine charges.

export const CONTRACT_STATUSES = ['draft', 'active', 'expired', 'cancelled'] as const

export const TIER_MODES: ReadonlyArray<{ value: 'graduated' | 'all_units'; label: string; help: string }> = [
  { value: 'graduated', label: 'Graduated — each band at its own price', help: 'The first band prices the units inside it, the next band the units above it, and so on. The industry default.' },
  { value: 'all_units', label: 'All units — the whole volume at the band it reaches', help: 'Every unit rates at the price of the band the total reaches, so crossing a bound reprices the lot.' },
]

/** A date string trimmed to YYYY-MM-DD, or '' when there is none. */
function dayOf(v: string | null | undefined): string {
  return v && v.length >= 10 ? v.slice(0, 10) : ''
}

/**
 * The end of a term of `months` from `startsOn`: the day BEFORE the
 * anniversary, so a term never overlaps its own renewal. Twelve months from
 * 2026-01-01 ends 2026-12-31.
 *
 * This is a PREVIEW — the server derives the stored date — so it must agree
 * with Go's AddDate to the day, including where it rolls a date the
 * anniversary month does not have forward: 31 January plus one month is 3
 * March, so the term ends 2 March.
 */
export function termEnd(startsOn: string, months: number): string {
  const d = dayOf(startsOn)
  if (!/^\d{4}-\d{2}-\d{2}$/.test(d) || !Number.isFinite(months) || months <= 0) return ''
  const [y, m, day] = d.split('-').map(Number)
  // Step the month, then one day back. Date handles the month lengths and
  // the leap day; the calculation is in UTC so a browser's zone cannot move
  // a contract's end date.
  const t = Date.UTC(y, m - 1 + months, day)
  const end = new Date(t - 86400000)
  return end.toISOString().slice(0, 10)
}

/** ends_on + 1 day — when the next term starts if the contract renews. */
export function renewalDate(c: Pick<Contract, 'ends_on'>): string {
  const d = dayOf(c.ends_on)
  if (!d) return ''
  return new Date(Date.parse(d + 'T00:00:00Z') + 86400000).toISOString().slice(0, 10)
}

/** The first day the contract appears in the renewals-due list. */
export function noticeFrom(c: Pick<Contract, 'ends_on' | 'renewal_notice_days'>): string {
  const d = dayOf(c.ends_on)
  if (!d) return ''
  const days = Number(c.renewal_notice_days) || 0
  return new Date(Date.parse(d + 'T00:00:00Z') - days * 86400000).toISOString().slice(0, 10)
}

/**
 * Is the contract inside its renewal notice window on `on`? Active, not yet
 * past its end date, and within `renewal_notice_days` of it — the same three
 * clauses the server's renewals-due query uses.
 */
export function renewalDue(c: Contract, on: string): boolean {
  if (c.status !== 'active') return false
  const end = dayOf(c.ends_on)
  const day = dayOf(on)
  if (!end || !day) return false
  return day <= end && noticeFrom(c) <= day
}

/** Days from `on` to the end of the term; negative once the term has passed. */
export function daysToEnd(c: Pick<Contract, 'ends_on'>, on: string): number | null {
  const end = dayOf(c.ends_on)
  const day = dayOf(on)
  if (!end || !day) return null
  return Math.round((Date.parse(end + 'T00:00:00Z') - Date.parse(day + 'T00:00:00Z')) / 86400000)
}

/** "12 months · 2026-01-01 → 2026-12-31", the term in one line. */
export function termText(c: Pick<Contract, 'term_months' | 'starts_on' | 'ends_on'>): string {
  const months = Number(c.term_months) || 0
  const term = months ? `${months} month${months === 1 ? '' : 's'} · ` : ''
  return `${term}${dayOf(c.starts_on) || '?'} → ${dayOf(c.ends_on) || '?'}`
}

/**
 * HOURS_PER_MONTH is the product's own convention for a time-integrated
 * quantity: a monthly price is an hourly price × 730 (store.PlanBook,
 * "hourly = annual / 8760 = monthly / 730"), so a quantity of unit-hours per
 * period divided by 730 is "this many, always on".
 */
export const HOURS_PER_MONTH = 730

/**
 * THE HUMAN READING of a time-integrated quantity, under the raw figure the
 * engine uses: "744,000 gb-hour" is what the rating engine takes off the top,
 * "≈ 1,019 GB always on" is what a person reads. The raw quantity stays —
 * it is the contract — and this is the muted line beneath it.
 *
 *   gb-hour / gib-hour → "≈ 1,019 GB always on"  (÷ 730, whole GB or GiB)
 *   mbps-hour          → "≈ 10 Mbps always on"
 *   instance-hour      → "≈ 8 servers all month" (one decimal when not whole)
 *   plan-hour          → "≈ 1 plan all month"
 *   vcpu-hour          → "≈ 4 vCPU always on"
 *   anything else      → ''  (a unit that is not per hour has no such reading)
 */
export function quantityReading(quantity: number | string | null | undefined, unit: string | null | undefined): string {
  const q = toNumber(quantity)
  const u = (unit ?? '').trim().toLowerCase()
  if (!(q > 0) || !u) return ''
  const per = q / HOURS_PER_MONTH
  switch (u) {
    case 'gb-hour':
      return `≈ ${wholeNumber(per)} GB always on`
    case 'gib-hour':
      return `≈ ${wholeNumber(per)} GiB always on`
    case 'mbps-hour':
      return `≈ ${wholeNumber(per)} Mbps always on`
    case 'instance-hour': {
      const n = oneDecimal(per)
      return `≈ ${n} ${n === '1' ? 'server' : 'servers'} all month`
    }
    case 'plan-hour': {
      const n = oneDecimal(per)
      return `≈ ${n} ${n === '1' ? 'plan' : 'plans'} all month`
    }
    case 'vcpu-hour':
      return `≈ ${oneDecimal(per)} vCPU always on`
    default:
      return ''
  }
}

/** 1019.18 → "1,019"; a whole number of the unit, grouped. */
function wholeNumber(v: number): string {
  return Math.round(v).toLocaleString('en-US', { maximumFractionDigits: 0 })
}

/** 8.15 → "8.2", 8.02 → "8", 1 → "1": one decimal only when the figure is not whole at that precision. */
function oneDecimal(v: number): string {
  const r = Math.round(v * 10) / 10
  return r.toLocaleString('en-US', { minimumFractionDigits: 0, maximumFractionDigits: 1 })
}

/**
 * The monthly floor a contract is trued up to and where it comes from: the
 * header minimum, the spend commitment's amount, or — when the contract
 * carries both — the LARGER of the two (store.Contract.MonthlyFloor). Two
 * promises of "at least this much" do not add up; the higher one is the
 * promise, and the page says so.
 */
export function contractFloor(c: Pick<Contract, 'minimum_commitment' | 'items'>): { amount: number; source: 'none' | 'minimum' | 'spend' | 'both'; minimum: number; spend: number } {
  const minimum = Math.max(0, toNumber(c.minimum_commitment))
  const spendLine = (c.items ?? []).find((it) => it.kind === 'spend')
  const spend = spendLine ? Math.max(0, toNumber(spendLine.amount)) : 0
  if (minimum <= 0 && spend <= 0) return { amount: 0, source: 'none', minimum, spend }
  if (minimum > 0 && spend > 0) return { amount: Math.max(minimum, spend), source: 'both', minimum, spend }
  return minimum > 0 ? { amount: minimum, source: 'minimum', minimum, spend } : { amount: spend, source: 'spend', minimum, spend }
}

/** Does the line count as a commitment on the page? A committed quantity or a committed amount. */
export function isCommitmentKind(kind: string): boolean {
  return kind === 'commitment' || kind === 'spend'
}

/**
 * The unit price a committed-use line results in, given the SKU's list
 * price: the committed price when one is written, else list less the
 * percentage. null when there is nothing to compute from yet — the figure the
 * dialog shows live under the rate field.
 */
export function committedUnitPrice(listPrice: number | null | undefined, it: Pick<ContractItem, 'committed_price' | 'discount_pct'>): number | null {
  const price = it.committed_price
  if (price !== null && price !== undefined && String(price).trim() !== '') return toNumber(price)
  const pct = it.discount_pct
  if (listPrice === null || listPrice === undefined || !Number.isFinite(listPrice)) return null
  if (pct === null || pct === undefined || String(pct).trim() === '') return null
  return round8(listPrice * (1 - toNumber(pct) / 100))
}

function round8(v: number): number {
  return Math.round(v * 1e8) / 1e8
}

/** What a contract line is, in words, for the list and the detail page. */
export function contractItemText(it: ContractItem, currency: string): string {
  const qty = trimZeros(String(toNumber(it.quantity)))
  const unit = it.unit ? ` ${it.unit}` : ''
  if (it.kind === 'spend') {
    const amount = trimZeros(String(toNumber(it.amount)))
    const pct = trimZeros(String(toNumber(it.discount_pct)))
    return `${amount} ${currency} committed each period, whatever is used, for ${pct} % off everything on the bill; a period below it carries a true-up line.`
  }
  if (it.kind === 'allowance') {
    const roll = it.rollover ? 'carried into the next period if unused' : 'lapses at the end of each period'
    return `${qty}${unit} of ${it.sku} included each period; ${roll}.`
  }
  const rate =
    it.committed_price !== null && it.committed_price !== undefined && it.committed_price !== ''
      ? `${trimZeros(String(toNumber(it.committed_price)))} ${currency} per ${it.unit || 'unit'}`
      : it.discount_pct !== null && it.discount_pct !== undefined && it.discount_pct !== ''
        ? `${trimZeros(String(toNumber(it.discount_pct)))} % off list`
        : 'no rate — the line is incomplete'
  return `${qty}${unit} of ${it.sku} committed each period at ${rate}; anything above it at list.`
}

/**
 * The bands of an item, normalised the way the engine reads them: `from` is
 * where the band starts, `upTo` null the unbounded last band. A ladder that
 * stops is completed by carrying its last price upward, exactly as
 * `rating.bandsOf` does.
 */
export interface Band {
  from: number
  upTo: number | null
  price: number
}

export function bandsOf(item: { tiers?: PriceTier[] | null }): Band[] {
  const tiers = (item.tiers ?? []) as PriceTier[]
  if (!tiers.length) return []
  const out: Band[] = []
  let prev = 0
  let open = false
  for (const t of tiers) {
    const price = toNumber(t.price)
    const bounded = t.up_to !== null && t.up_to !== undefined && t.up_to !== ''
    if (open) break
    if (!bounded) {
      open = true
      out.push({ from: prev, upTo: null, price })
      continue
    }
    const upTo = toNumber(t.up_to)
    out.push({ from: prev, upTo, price })
    prev = upTo
  }
  if (!open && out.length) out.push({ from: prev, upTo: null, price: out[out.length - 1].price })
  return out
}

/**
 * The ladder's problem in words, or '' when it is sound — the same three
 * rules the engine enforces, so the editor refuses what the bill would
 * refuse: bands ascend, the unbounded band is last, no band is negative.
 */
export function tierProblem(tiers: PriceTier[]): string {
  let prev: number | null = 0
  for (let i = 0; i < tiers.length; i++) {
    const t = tiers[i]
    const price = toNumber(t.price)
    if (price < 0) return `Band ${i + 1} is priced below zero.`
    const bounded = t.up_to !== null && t.up_to !== undefined && String(t.up_to) !== ''
    if (prev === null) return `Band ${i + 1} comes after the band with no upper bound; that band must be last.`
    if (!bounded) {
      prev = null
      continue
    }
    const upTo = toNumber(t.up_to)
    if (!(upTo > prev)) return `Band ${i + 1} ends at ${trimZeros(String(upTo))}, which is not above the band before it (${trimZeros(String(prev))}).`
    prev = upTo
  }
  return ''
}

/**
 * THE EXPLAINED PRICE: what this item actually charges, in a sentence, under
 * the row that edits it. It mirrors `rating.ExplainItem` clause for clause —
 * an editor that explained the price differently from the way it is charged
 * would be worse than no explanation at all.
 */
export function explainItem(item: PriceItem, currency: string): string {
  const parts: string[] = []
  const allowance = toNumber(item.allowance)
  if (allowance > 0) {
    const roll = item.allowance_rollover ? 'unused allowance carries into the next period, once' : 'unused allowance lapses at the end of the period'
    parts.push(`the first ${trimZeros(String(allowance))} ${item.unit} each period are included; ${roll}`)
  }
  const bands = bandsOf(item)
  if (bands.length) {
    const words = bands.map((b) => {
      const rate = `${trimZeros(String(b.price))} ${currency} per ${item.unit}`
      if (b.upTo === null && b.from === 0) return `every unit at ${rate}`
      if (b.upTo === null) return `above ${trimZeros(String(b.from))} at ${rate}`
      if (b.from === 0) return `up to ${trimZeros(String(b.upTo))} at ${rate}`
      return `${trimZeros(String(b.from))}-${trimZeros(String(b.upTo))} at ${rate}`
    })
    parts.push(
      item.tier_mode === 'all_units'
        ? `the WHOLE billable quantity rates at the band it reaches: ${words.join(', ')}`
        : `each band rates at its own price: ${words.join(', ')}`,
    )
  } else {
    parts.push(`the rest at ${trimZeros(String(toNumber(item.unit_price)))} ${currency} per ${item.unit}`)
  }
  const s = parts.join('; ')
  return s.charAt(0).toUpperCase() + s.slice(1) + '.'
}

/** Does this item carry any shape at all? Drives the "priced by" badge. */
export function hasShape(item: { tiers?: PriceTier[] | null; allowance?: number | string | null }): boolean {
  return Boolean((item.tiers ?? []).length) || toNumber(item.allowance) > 0
}

/**
 * Price a quantity exactly as the engine does — allowance, then the bands,
 * then the flat price — so the editor can show what a sample volume costs
 * before anyone is invoiced for it. Returns null when the item has no shape
 * and nothing needs explaining.
 */
export function priceQuantity(item: PriceItem, quantity: number): number | null {
  if (!Number.isFinite(quantity) || quantity < 0) return null
  const allowance = Math.max(0, toNumber(item.allowance))
  const billable = Math.max(0, quantity - allowance)
  const bands = bandsOf(item)
  if (!bands.length) return round6(billable * toNumber(item.unit_price))
  if (item.tier_mode === 'all_units') {
    const band = bands.find((b) => b.upTo === null || billable <= b.upTo) ?? bands[bands.length - 1]
    return round6(billable * band.price)
  }
  let total = 0
  for (const b of bands) {
    const to = b.upTo === null ? billable : Math.min(b.upTo, billable)
    if (to <= b.from) continue
    total += (to - b.from) * b.price
  }
  return round6(total)
}

function round6(v: number): number {
  return Math.round(v * 1e6) / 1e6
}

export function trimZeros(s: string): string {
  if (!s.includes('.')) return s
  return s.replace(/0+$/, '').replace(/\.$/, '')
}
