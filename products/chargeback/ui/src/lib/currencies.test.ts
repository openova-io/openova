import { describe, expect, it } from 'vitest'
import type { CurrencyRates } from '../api/types'
import { describeRate, describeUnconverted, rateBody, readRates, toBase, validateRate } from './currencies'

describe('currency rates', () => {
  it('reads the /currencies document tolerantly', () => {
    const doc = { reporting_currency: 'omr', rates: [{ code: 'usd', per_base: '2.6', source: '', updated_at: 'x' }, { code: 'EUR', per_base: 0 }, { code: 7 }, null] } as unknown as CurrencyRates
    const r = readRates(doc)
    expect(r.reporting).toBe('OMR')
    expect(r.rates).toEqual([{ code: 'USD', per_base: 2.6, source: 'manual', updated_at: 'x' }])
    expect(readRates(null)).toEqual({ reporting: '', rates: [] })
    expect(readRates({} as CurrencyRates)).toEqual({ reporting: '', rates: [] })
  })
  it('divides by per_base — 26 USD at 2.6 per OMR is 10 OMR, not 67.6', () => {
    expect(toBase(26, 2.6)).toBeCloseTo(10, 9)
    expect(toBase(26, 2.6)).not.toBeCloseTo(67.6, 3)
    expect(toBase(67.2, 0.42)).toBeCloseTo(160, 9)
    expect(toBase(1, 0)).toBeNull()
    expect(toBase(1, -1)).toBeNull()
    expect(toBase(Number.NaN, 2)).toBeNull()
    expect(describeRate({ code: 'USD', per_base: 2.6 }, 'OMR')).toBe('1 OMR = 2.6 USD · 1 USD = 0.384615 OMR')
  })
  it('validates a draft the way the server does', () => {
    expect(validateRate({ code: 'usd', per_base: '2.6' }, 'OMR')).toEqual({})
    expect(validateRate({ code: 'USD', per_base: '0.0001' }, 'OMR', ['USD'], 'USD')).toEqual({})
    expect(validateRate({ code: 'OMR', per_base: '1' }, 'OMR').code).toMatch(/reporting currency/)
    expect(validateRate({ code: 'omr', per_base: '1' }, 'OMR').code).toMatch(/reporting currency/)
    expect(validateRate({ code: 'US', per_base: '1' }, 'OMR').code).toMatch(/Three-letter/)
    expect(validateRate({ code: 'US1', per_base: '1' }, 'OMR').code).toMatch(/Three-letter/)
    expect(validateRate({ code: 'USD', per_base: '1' }, 'OMR', ['USD']).code).toMatch(/already has a rate/)
    for (const bad of ['', '0', '-2.6', 'abc', '2.', '.5', '1e3', '2,6']) expect(validateRate({ code: 'USD', per_base: bad }, 'OMR').per_base).toMatch(/above 0/)
    expect(rateBody({ code: 'USD', per_base: ' 2.6000000001 ' })).toEqual({ per_base: '2.6000000001' })
  })
  it('words the unconverted warning from the list, falling back to the mixed flag', () => {
    expect(describeUnconverted([{ currency: 'USD', records: 168, cost: 84 }], 'OMR')).toBe('No exchange rate to OMR for USD (168 records · 84.000 USD). That usage is left out of every total until a rate is added.')
    expect(describeUnconverted([{ currency: 'EUR', records: 1, cost: 0.4 }, { currency: 'USD', records: 2, cost: 1 }], 'OMR')).toContain('EUR (1 record · 0.400 EUR), USD (2 records · 1.000 USD)')
    expect(describeUnconverted([], 'OMR')).toBe('')
    expect(describeUnconverted(undefined, 'OMR')).toBe('')
    expect(describeUnconverted(undefined, 'OMR', true)).toBe('This selection mixes more than one currency; values are shown as OMR.')
    expect(describeUnconverted([{ currency: 'USD', records: Number.NaN, cost: 1 }], '')).toBe('No exchange rate for USD (records · 1.000 USD). That usage is left out of every total until a rate is added.')
  })
})
