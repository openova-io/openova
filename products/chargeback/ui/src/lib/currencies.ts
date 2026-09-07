import type { CurrencyRate, CurrencyRates, UnconvertedCurrency } from '../api/types'
import { formatMoney, formatNumber } from './money'

/**
 * Currency conversion helpers (#6867 follow-up, DESIGN.md §3.10) — pure,
 * unit-tested in currencies.test.ts.
 *
 * The server converts; the page only validates what it sends, previews a
 * rate the way the server will apply it, and words the warning for usage
 * that carries no rate. per_base is units of the book currency per ONE
 * reporting unit, so a book amount is DIVIDED by it: 26 USD at 2.6 USD per
 * OMR is 10 OMR.
 */

export const CURRENCY_CODE = /^[A-Za-z]{3}$/
/** The server's Decimal shape: digits with an optional fraction, no sign, no exponent. */
const RATE_SHAPE = /^\d+(\.\d+)?$/

export interface RateDraft {
  code: string
  per_base: string
}
export type RateErrors = Partial<Record<keyof RateDraft, string>>

/** Tolerant reader of GET /currencies: never throws, never yields NaN. */
export function readRates(doc: CurrencyRates | null | undefined): { reporting: string; rates: CurrencyRate[] } {
  const reporting = typeof doc?.reporting_currency === 'string' ? doc.reporting_currency.toUpperCase() : ''
  const rates = Array.isArray(doc?.rates)
    ? doc.rates
        .filter((r) => r && typeof r.code === 'string')
        .map((r) => ({ ...r, code: r.code.toUpperCase(), per_base: Number(r.per_base), source: r.source || 'manual' }))
        .filter((r) => Number.isFinite(r.per_base) && r.per_base > 0)
    : []
  return { reporting, rates }
}

/** Mirrors the server's rules so a bad row is refused before it is sent. */
export function validateRate(d: RateDraft, reporting: string, existing: string[] = [], replacing?: string): RateErrors {
  const errs: RateErrors = {}
  const code = d.code.trim().toUpperCase()
  if (!CURRENCY_CODE.test(code)) errs.code = 'Three-letter code, e.g. USD'
  else if (reporting && code === reporting.toUpperCase()) errs.code = `${reporting} is the reporting currency — its rate is 1 by definition`
  else if (code !== replacing && existing.includes(code)) errs.code = `${code} already has a rate — edit that row`
  const per = d.per_base.trim()
  if (!RATE_SHAPE.test(per) || Number(per) <= 0) errs.per_base = `A number above 0: how many ${code || 'units'} one ${reporting || 'reporting unit'} buys, e.g. 2.6`
  return errs
}

/** The PUT body: the rate as the exact text typed, never a float round-trip. */
export function rateBody(d: RateDraft): { per_base: string } {
  return { per_base: d.per_base.trim() }
}

/** An amount in a book currency in the reporting currency, or null without a usable rate. */
export function toBase(amount: number, perBase: number): number | null {
  if (!Number.isFinite(amount) || !Number.isFinite(perBase) || perBase <= 0) return null
  return amount / perBase
}

/** "1 OMR = 2.6 USD · 1 USD = 0.385 OMR" */
export function describeRate(r: Pick<CurrencyRate, 'code' | 'per_base'>, reporting: string): string {
  const per = Number(r.per_base)
  const inv = toBase(1, per)
  return `1 ${reporting} = ${formatNumber(per, 6)} ${r.code} · 1 ${r.code} = ${inv === null ? '—' : formatNumber(inv, 6)} ${reporting}`
}

/**
 * The warning for usage no total includes. With an unconverted list it
 * names each currency, its records and its cost in that currency; an older
 * server that only flags mixed_currency gets the generic sentence.
 */
export function describeUnconverted(list: UnconvertedCurrency[] | null | undefined, reporting: string, mixed = false): string {
  const rows = (Array.isArray(list) ? list : []).filter((u) => u && typeof u.currency === 'string')
  if (rows.length === 0) return mixed ? `This selection mixes more than one currency; values are shown as ${reporting || 'one currency'}.` : ''
  const parts = rows.map((u) => {
    const n = Number(u.records)
    const records = Number.isFinite(n) ? `${n.toLocaleString('en-US')} record${n === 1 ? '' : 's'}` : 'records'
    return `${u.currency} (${records} · ${formatMoney(u.cost, u.currency)})`
  })
  const lead = reporting ? `No exchange rate to ${reporting} for ${parts.join(', ')}.` : `No exchange rate for ${parts.join(', ')}.`
  return `${lead} That usage is left out of every total until a rate is added.`
}
