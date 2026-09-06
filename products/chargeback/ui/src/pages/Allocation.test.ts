import { describe, expect, it } from 'vitest'
import { n2, pct, reconciles } from '../lib/allocation'

describe('allocation reconciliation', () => {
  // THE property. A split that quietly loses cost looks exactly like a
  // complete one on screen, so the page must be able to say it is wrong.
  it('accepts a split whose shares sum to one', () => {
    expect(reconciles(2, 1)).toBe(true)
  })
  it('rejects a split that lost cost', () => {
    expect(reconciles(2, 0.85)).toBe(false)
  })
  it('rejects a split that double-counted', () => {
    expect(reconciles(1, 1.2)).toBe(false)
  })
  // A correct split can miss 1 by one ULP. 0.7+0.2+0.1 sums to
  // 0.9999999999999999, so an exact === would flag it broken and cry wolf on
  // every window. (0.1+0.2+0.7 happens to land on exactly 1 — an earlier
  // version of this test used that order and passed even with the tolerance
  // removed, proving nothing.)
  it('tolerates float representation error', () => {
    const sum = 0.7 + 0.2 + 0.1
    expect(sum).not.toBe(1)
    expect(reconciles(3, sum)).toBe(true)
  })
  // Nothing to lose in an empty window; warning would train the operator to
  // ignore the warning that matters.
  it('is vacuously fine with no rows', () => {
    expect(reconciles(0, 0)).toBe(true)
  })
  it('formats shares and hours', () => {
    expect(pct(0.5)).toBe('50.0%')
    expect(pct(1)).toBe('100.0%')
    expect(n2(1.234)).toBe('1.23')
    // NaN must render as a dash, not "NaN" — an operator reading a cost table
    // should see "no value", not a JavaScript artefact.
    expect(n2(NaN)).toBe('—')
    expect(n2(Infinity)).toBe('—')
  })

  // Documenting a real limit rather than asserting a wrong expectation:
  // toFixed rounds the BINARY value, and 1.005 is fractionally below the
  // midpoint, so it yields 1.00 not 1.01. That is fine here because these are
  // DISPLAY helpers for hour counts — every money figure is computed
  // server-side in exact big.Rat and never passes through toFixed.
  it('rounds the binary value, which is acceptable for display-only hours', () => {
    expect(n2(1.005)).toBe('1.00')
  })
})
