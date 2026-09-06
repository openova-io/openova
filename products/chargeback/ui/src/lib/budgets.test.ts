import { describe, expect, it } from 'vitest'
import { parseEmails, parseThresholds } from './budgets'

describe('parseThresholds', () => {
  it('parses a comma list with stray spaces into ascending whole percentages', () => {
    expect(parseThresholds(' 50, 80 ,100 ')).toEqual({ values: [50, 80, 100] })
    expect(parseThresholds('90')).toEqual({ values: [90] })
    expect(parseThresholds('80 100 120')).toEqual({ values: [80, 100, 120] })
  })
  it('rejects an empty list', () => {
    expect(parseThresholds('')).toMatchObject({ values: [], error: expect.stringContaining('At least one') })
  })
  it('rejects non-integers and out-of-range values', () => {
    expect(parseThresholds('50,eighty').error).toContain('eighty')
    expect(parseThresholds('50.5').error).toContain('50.5')
    expect(parseThresholds('0,50').error).toContain('outside')
    expect(parseThresholds('50,-1').error).toBeTruthy()
  })
  // Descending or repeated thresholds would alert out of order or twice;
  // the evaluator records one crossing per threshold, so they must be unique.
  it('rejects descending and duplicate thresholds', () => {
    expect(parseThresholds('80,50').error).toContain('ascending')
    expect(parseThresholds('50,50').error).toContain('ascending')
  })
})

describe('parseEmails', () => {
  it('splits on commas, semicolons and whitespace, lower-cases and de-duplicates', () => {
    expect(parseEmails('Ops@Acme.example, fin@acme.example; ops@acme.example')).toEqual({ values: ['ops@acme.example', 'fin@acme.example'] })
  })
  it('accepts an empty list (no notification)', () => {
    expect(parseEmails('')).toEqual({ values: [] })
  })
  it('names the bad address', () => {
    expect(parseEmails('ok@x.example, not-an-address').error).toContain('not-an-address')
  })
})
