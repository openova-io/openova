import { describe, expect, it } from 'vitest'
import { deltaPct, formatCompact, formatDelta, formatMoney, formatNumber, formatPct, formatQty } from './money'

describe('formatMoney', () => {
  it('renders three decimals with thousands separators and the currency', () => {
    expect(formatMoney(2780.012, 'OMR')).toBe('2,780.012 OMR')
    expect(formatMoney(0, 'OMR')).toBe('0.000 OMR')
    expect(formatMoney(2780.012, '')).toBe('2,780.012')
  })
  it('compacts to three significant digits', () => {
    expect(formatMoney(2780.012, 'OMR', { compact: true })).toBe('2.78k OMR')
    expect(formatMoney(1200000, 'OMR', { compact: true })).toBe('1.2M OMR')
    expect(formatMoney(1234567, 'OMR', { compact: true })).toBe('1.23M OMR')
    expect(formatMoney(104.16, 'OMR', { compact: true })).toBe('104 OMR')
    expect(formatMoney(14.88, 'OMR', { compact: true })).toBe('14.9 OMR')
    expect(formatMoney(0.48, 'OMR', { compact: true })).toBe('0.48 OMR')
    expect(formatMoney(0, 'OMR', { compact: true })).toBe('0 OMR')
  })
  it('honours digits', () => {
    expect(formatMoney(2780.012, 'OMR', { digits: 2 })).toBe('2,780.01 OMR')
    expect(formatMoney(2780.5, 'USD', { digits: 0 })).toBe('2,781 USD')
  })
  it('uses a true minus and never a minus on a rounded zero', () => {
    expect(formatMoney(-12.5, 'OMR')).toBe('−12.500 OMR')
    expect(formatMoney(-2780, 'OMR', { compact: true })).toBe('−2.78k OMR')
    expect(formatMoney(-0.0001, 'OMR')).toBe('0.000 OMR')
    expect(formatMoney(0.0001, 'OMR')).toBe('0.000 OMR')
  })
  it('renders null, undefined, NaN and Infinity as a dash', () => {
    expect(formatMoney(null, 'OMR')).toBe('—')
    expect(formatMoney(undefined, 'OMR')).toBe('—')
    expect(formatMoney(NaN, 'OMR')).toBe('—')
    expect(formatMoney(Infinity, 'OMR')).toBe('—')
  })
})

describe('formatCompact', () => {
  it('promotes a value that would round to 1,000 of its unit', () => {
    expect(formatCompact(999.6)).toBe('1k')
    expect(formatCompact(999999)).toBe('1M')
    expect(formatCompact(999.4)).toBe('999')
  })
  it('covers k, M and B', () => {
    expect(formatCompact(12345)).toBe('12.3k')
    expect(formatCompact(123456)).toBe('123k')
    expect(formatCompact(1e9)).toBe('1B')
    expect(formatCompact(2.5e9)).toBe('2.5B')
  })
  it('keeps precision on tiny values', () => {
    expect(formatCompact(0.0005)).toBe('0.0005')
    expect(formatCompact(0.00123)).toBe('0.00123')
  })
})

describe('formatPct', () => {
  it('signs, uses a true minus, and dashes null', () => {
    expect(formatPct(5.8, { sign: true })).toBe('+5.8 %')
    expect(formatPct(5.8)).toBe('5.8 %')
    expect(formatPct(-3.1)).toBe('−3.1 %')
    expect(formatPct(-3.1, { sign: true })).toBe('−3.1 %')
    expect(formatPct(null)).toBe('—')
    expect(formatPct(undefined)).toBe('—')
    expect(formatPct(NaN)).toBe('—')
    expect(formatPct(Infinity)).toBe('—')
  })
  it('drops the decimal from three-digit percentages', () => {
    expect(formatPct(148)).toBe('148 %')
    expect(formatPct(148, { sign: true })).toBe('+148 %')
    expect(formatPct(-250.4)).toBe('−250 %')
    expect(formatPct(1234.5)).toBe('1,235 %')
  })
  it('never signs a zero, including one that rounds to zero', () => {
    expect(formatPct(0, { sign: true })).toBe('0.0 %')
    expect(formatPct(0.04, { sign: true })).toBe('0.0 %')
    expect(formatPct(-0.04)).toBe('0.0 %')
  })
  it('honours digits', () => {
    expect(formatPct(5.8, { digits: 2 })).toBe('5.80 %')
    expect(formatPct(80.65, { digits: 0 })).toBe('81 %')
  })
})

describe('formatQty', () => {
  it('appends the unit and trims trailing zeros', () => {
    expect(formatQty(84, 'vcpu-hour')).toBe('84 vcpu-hour')
    expect(formatQty(1234.5678, 'GB')).toBe('1,234.568 GB')
    expect(formatQty(2.5, 'GB')).toBe('2.5 GB')
    expect(formatQty(84, '')).toBe('84')
    expect(formatQty(84, null)).toBe('84')
    expect(formatQty(-1, 'GB')).toBe('−1 GB')
  })
  it('dashes a missing quantity', () => {
    expect(formatQty(null, 'GB')).toBe('—')
    expect(formatQty(NaN, 'GB')).toBe('—')
  })
})

describe('formatNumber', () => {
  it('trims and separates', () => {
    expect(formatNumber(1234.5678)).toBe('1,234.568')
    expect(formatNumber(1234.5678, 1)).toBe('1,234.6')
    expect(formatNumber(-0.0001)).toBe('0')
    expect(formatNumber(null)).toBe('—')
  })
})

describe('deltaPct / formatDelta', () => {
  it('mirrors the API rule: null when there is no previous', () => {
    expect(deltaPct(105.8, 100)).toBeCloseTo(5.8)
    expect(deltaPct(5, 0)).toBeNull()
    expect(deltaPct(0, 0)).toBeNull()
    expect(deltaPct(NaN, 5)).toBeNull()
    expect(deltaPct(5, null)).toBeNull()
    expect(deltaPct(42, 84)).toBe(-50)
    expect(deltaPct(104.16, 42)).toBeCloseTo(148)
  })
  it('formats with a sign', () => {
    expect(formatDelta(105.8, 100)).toBe('+5.8 %')
    expect(formatDelta(42, 84)).toBe('−50.0 %')
    expect(formatDelta(104.16, 42)).toBe('+148 %')
    expect(formatDelta(5, 0)).toBe('—')
    expect(formatDelta(7, 7)).toBe('0.0 %')
  })
})
