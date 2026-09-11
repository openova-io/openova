import { describe, expect, it } from 'vitest'
import type { TaxRule } from '../api/types'
import {
  certificateState,
  certificateText,
  distinctRates,
  emptyTaxRuleForm,
  einvoiceXMLURL,
  hasArchivedXML,
  knownCategories,
  rateText,
  rulePhase,
  scopeText,
  taxBaseTotal,
  taxCategoryBody,
  taxLinesTotal,
  taxRuleBody,
  taxRuleFormFrom,
  validateTaxCategory,
  validateTaxRule,
  validityText,
  withKind,
} from './tax'

/**
 * The decisions the tax surfaces make, before any of them is rendered
 * (DESIGN.md §17): the percentage/fraction bargain, what a non-standard
 * kind does to a rate, when a rule is in force, and whether an exemption
 * certificate has lapsed.
 */

const standard: TaxRule = {
  id: 'tr1',
  name: 'Oman VAT standard',
  country: 'OM',
  region: '',
  category: '',
  rate: '0.0500',
  kind: 'standard',
  note: '',
  effective_from: '2026-01-01',
  effective_to: '',
}

describe('rates travel as fractions and read as percentages', () => {
  it('renders a fraction as a percentage, trailing zeros trimmed', () => {
    expect(rateText('0.0500')).toBe('5 %')
    expect(rateText('0.0550')).toBe('5.5 %')
    expect(rateText('0.0000')).toBe('0 %')
    expect(rateText(0.15)).toBe('15 %')
  })

  it('a typed percentage leaves as the FRACTION the API stores', () => {
    const f = { ...emptyTaxRuleForm('2026-01-01'), name: 'Oman VAT standard', country: 'om', rate: '5' }
    expect(taxRuleBody(f).rate).toBe('0.05')
    expect(taxRuleBody({ ...f, rate: '5.5' }).rate).toBe('0.055')
    expect(taxRuleBody({ ...f, rate: '' }).rate).toBe('0')
    // …and the country is upper-cased, which is the only form the API takes.
    expect(taxRuleBody(f).country).toBe('OM')
  })

  it('reads a saved rule back into the form as a percentage', () => {
    expect(taxRuleFormFrom(standard)).toEqual({
      name: 'Oman VAT standard',
      country: 'OM',
      region: '',
      category: '',
      rate: '5',
      kind: 'standard',
      note: '',
      effective_from: '2026-01-01',
      effective_to: '',
    })
  })
})

describe('a kind that charges nothing keeps its rate at 0', () => {
  it('switching the kind zeroes the rate, and switching back does not resurrect it', () => {
    const typed = { ...taxRuleFormFrom(standard), rate: '5' }
    const reverse = withKind(typed, 'reverse_charge')
    expect(reverse.rate).toBe('0')
    expect(withKind(typed, 'exempt').rate).toBe('0')
    expect(withKind(typed, 'zero_rated').rate).toBe('0')
    expect(withKind(reverse, 'standard').rate).toBe('0')
    // The body agrees even if the field were edited around the control.
    expect(taxRuleBody({ ...reverse, rate: '5' }).rate).toBe('0')
  })

  it('refuses a non-standard rule that carries a rate, in the API’s own words', () => {
    const f = { ...taxRuleFormFrom(standard), kind: 'exempt', rate: '5' }
    expect(validateTaxRule(f).rate).toMatch(/charges nothing, so its rate must be 0/)
    expect(validateTaxRule({ ...f, rate: '0' })).toEqual({})
  })

  it('validates the same rules the store applies', () => {
    expect(validateTaxRule(taxRuleFormFrom(standard))).toEqual({})
    expect(validateTaxRule({ ...taxRuleFormFrom(standard), name: ' ' }).name).toMatch(/required/)
    expect(validateTaxRule({ ...taxRuleFormFrom(standard), country: 'OMN' }).country).toMatch(/two-letter/)
    expect(validateTaxRule({ ...taxRuleFormFrom(standard), rate: '' }).rate).toMatch(/required/)
    expect(validateTaxRule({ ...taxRuleFormFrom(standard), rate: '120' }).rate).toMatch(/100/)
    expect(validateTaxRule({ ...taxRuleFormFrom(standard), effective_from: '2026-13-01' }).effective_from).toMatch(/YYYY-MM-DD/)
    expect(validateTaxRule({ ...taxRuleFormFrom(standard), effective_to: '2025-01-01' }).effective_to).toMatch(/after the start/)
  })
})

describe('a rule is in force, scheduled or ended', () => {
  it('reads its own validity window, with the end EXCLUDED as the store reads it', () => {
    const closed = { ...standard, effective_to: '2027-01-01' }
    expect(rulePhase(standard, '2026-06-01')).toBe('in force')
    expect(rulePhase(standard, '2025-12-31')).toBe('scheduled')
    expect(rulePhase(closed, '2026-12-31')).toBe('in force')
    // The day the end names is the first day it no longer applies.
    expect(rulePhase(closed, '2027-01-01')).toBe('ended')
    expect(validityText(standard)).toBe('from 2026-01-01')
    expect(validityText(closed)).toBe('2026-01-01 → 2027-01-01')
  })

  it('says what supply it governs', () => {
    expect(scopeText(standard)).toBe('OM · every region · every category')
    expect(scopeText({ country: 'AE', region: 'Dubai', category: 'storage' })).toBe('AE · Dubai · storage')
  })

  it('offers the categories that are actually in use', () => {
    expect(knownCategories([{ sku: 'evs.*', category: 'storage' }], [{ ...standard, category: 'compute' }])).toEqual(['compute', 'storage'])
  })
})

describe('placing a SKU in a category', () => {
  it('takes an exact SKU or a prefix ending in *', () => {
    expect(validateTaxCategory({ sku: 'k8s.vcpu', category: 'compute', note: '' })).toEqual({})
    expect(validateTaxCategory({ sku: 'evs.*', category: 'storage', note: '' })).toEqual({})
    expect(validateTaxCategory({ sku: 'evs.*.gb', category: 'storage', note: '' }).sku).toMatch(/only END the pattern/)
    expect(validateTaxCategory({ sku: '', category: 'storage', note: '' }).sku).toMatch(/required/)
    expect(validateTaxCategory({ sku: 'evs.*', category: '', note: '' }).category).toMatch(/delete the row/)
    expect(taxCategoryBody({ sku: ' evs.* ', category: ' storage ', note: ' the whole family ' })).toEqual({ sku: 'evs.*', category: 'storage', note: 'the whole family' })
  })
})

describe('the exemption certificate lapses, and the fallback is the standard rate', () => {
  const number = 'CERT-9001'
  it('separates expired from valid from never-recorded', () => {
    expect(certificateState({ tax_exempt: true, tax_exemption_number: number, tax_exemption_expires_on: '2026-01-31T00:00:00Z' }, '2026-09-11')).toBe('expired')
    expect(certificateState({ tax_exempt: true, tax_exemption_number: number, tax_exemption_expires_on: '2027-12-31T00:00:00Z' }, '2026-09-11')).toBe('valid')
    expect(certificateState({ tax_exempt: true, tax_exemption_number: number }, '2026-09-11')).toBe('open-ended')
    expect(certificateState({ tax_exempt: false }, '2026-09-11')).toBe('none')
  })

  it('holds THROUGH the day it names and lapses the day after — the same boundary store.TaxEngine.Resolve uses', () => {
    const cert = { tax_exempt: true, tax_exemption_number: 'EX-1', tax_exemption_expires_on: '2026-08-31' }
    // A badge that disagreed with the engine would tell an operator the
    // exemption had lapsed on an invoice the engine still exempted.
    expect(certificateState(cert, '2026-08-30')).toBe('valid')
    expect(certificateState(cert, '2026-08-31')).toBe('valid')
    expect(certificateState(cert, '2026-09-01')).toBe('expired')
    // The wire may carry the plain day or the RFC 3339 instant; both read
    // the same way.
    expect(certificateState({ ...cert, tax_exemption_expires_on: '2026-08-31T00:00:00Z' }, '2026-08-31')).toBe('valid')
  })

  it('says what an expired one means for the next invoice', () => {
    expect(certificateText('expired', '2026-01-31')).toContain('expired on 2026-01-31')
    expect(certificateText('expired', '2026-01-31')).toContain('standard rate')
    expect(certificateText('valid', '2027-12-31')).toBe('in force through 2027-12-31')
  })
})

describe('the per-rule tax summary of an invoice', () => {
  const lines = [
    { rule_id: 'tr1', rule_name: 'Oman VAT standard', kind: 'standard', category: 'compute', rate: '0.0500', base: '800.000', tax: '40.000' },
    { rule_id: 'tr2', rule_name: 'Storage zero-rated', kind: 'zero_rated', category: 'storage', rate: '0.0000', base: '200.000', tax: '0.000' },
  ]
  it('adds up to the tax on the invoice, over the base it covers', () => {
    expect(taxLinesTotal(lines)).toBe(40)
    expect(taxBaseTotal(lines)).toBe(1000)
    expect(distinctRates(lines)).toBe(2)
    expect(taxLinesTotal(null)).toBe(0)
  })
})

describe('the e-invoice a reader can take away', () => {
  it('points the download at the archival XML, and offers it only once one exists', () => {
    expect(einvoiceXMLURL('st1')).toBe('/api/v1/statements/st1/einvoice.xml')
    expect(hasArchivedXML({ profile: 'zatca', state: 'archived', built_at: '2026-09-01T00:00:00Z' })).toBe(true)
    expect(hasArchivedXML({ profile: 'zatca', state: 'not_submitted', built_at: '2026-09-01T00:00:00Z' })).toBe(true)
    expect(hasArchivedXML({ profile: 'zatca', state: 'built', built_at: '2026-09-01T00:00:00Z' })).toBe(false)
    expect(hasArchivedXML(null)).toBe(false)
  })
})
