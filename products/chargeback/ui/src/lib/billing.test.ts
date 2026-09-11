import { describe, expect, it } from 'vitest'
import type { BillingSettings } from '../api/types'
import { billingBody, billingFormFrom, fractionToPercent, percentToFraction, taxRateText, validateBilling } from './billing'

const saved: BillingSettings = {
  discount_rule: 'most-specific',
  invoice_prefix: 'INV',
  credit_note_prefix: 'CN',
  commercial_provider: 'internal',
  tax_rate: '0.050000',
  tax_registration_number: 'OM100',
  tax_country: 'OM',
  legal_name: 'Sovereign LLC',
  address: 'Muscat',
  reminder_days: [-3, 0, 7, 14, 30],
  escalation_days: 45,
  escalation_action: 'notify',
  updated_at: '2026-09-01T00:00:00Z',
}

describe('rates travel as fractions and read as percentages', () => {
  it('converts both ways without binary noise', () => {
    expect(fractionToPercent('0.050000')).toBe('5')
    expect(fractionToPercent(0.055)).toBe('5.5')
    expect(fractionToPercent(0)).toBe('0')
    expect(fractionToPercent(undefined)).toBe('')
    expect(percentToFraction('5')).toBe('0.05')
    expect(percentToFraction('5.5')).toBe('0.055')
    expect(percentToFraction('7')).toBe('0.07')
    expect(percentToFraction(' ')).toBe('')
    expect(taxRateText('0.05')).toBe('5 %')
    expect(taxRateText(0)).toBe('no tax')
  })
})

describe('the billing settings form', () => {
  it('reads the saved document into the form, days as a comma list', () => {
    expect(billingFormFrom(saved)).toEqual({
      invoice_prefix: 'INV',
      credit_note_prefix: 'CN',
      tax_rate: '5',
      tax_registration_number: 'OM100',
      tax_country: 'OM',
      legal_name: 'Sovereign LLC',
      address: 'Muscat',
      reminder_days: '-3, 0, 7, 14, 30',
      escalation_days: '45',
      escalation_action: 'notify',
    })
    expect(billingFormFrom(null).reminder_days).toBe('')
  })
  it('validates the same rules the server applies', () => {
    const f = billingFormFrom(saved)
    expect(validateBilling(f)).toEqual({})
    expect(validateBilling({ ...f, invoice_prefix: 'inv' }).invoice_prefix).toMatch(/upper-case/)
    expect(validateBilling({ ...f, credit_note_prefix: 'INV' }).credit_note_prefix).toMatch(/differ/)
    expect(validateBilling({ ...f, tax_rate: '5%' }).tax_rate).toMatch(/percentage/)
    expect(validateBilling({ ...f, tax_rate: '120' }).tax_rate).toMatch(/100/)
    expect(validateBilling({ ...f, reminder_days: '7, soon' }).reminder_days).toMatch(/whole number/)
    expect(validateBilling({ ...f, escalation_days: '-1' }).escalation_days).toMatch(/whole number/)
    expect(validateBilling({ ...f, escalation_action: 'call' }).escalation_action).toMatch(/notify or suspend/)
    // DESIGN.md §17 — the Sovereign's registration country. Empty is a
    // configured absence (no cross-border determination); anything that is
    // not two letters is a mistake.
    expect(validateBilling({ ...f, tax_country: '' })).toEqual({})
    expect(validateBilling({ ...f, tax_country: 'OMN' }).tax_country).toMatch(/two-letter/)
  })
  it('sends the required discount rule plus only what changed, with the rate as a fraction and the days as integers', () => {
    const f = billingFormFrom(saved)
    expect(billingBody(saved, f)).toBeNull()
    expect(billingBody(saved, { ...f, tax_rate: '7.5', reminder_days: '0, 7, 7, 30', credit_note_prefix: 'CRN' })).toEqual({ discount_rule: 'most-specific', credit_note_prefix: 'CRN', tax_rate: '0.075', reminder_days: [0, 7, 30] })
    expect(billingBody(saved, { ...f, escalation_days: '', escalation_action: 'suspend' })).toEqual({ discount_rule: 'most-specific', escalation_days: 0, escalation_action: 'suspend' })
  })
  it('sends the registration country the §17 rules resolve against', () => {
    const f = billingFormFrom(saved)
    expect(billingBody(saved, { ...f, tax_country: 'AE' })).toEqual({ discount_rule: 'most-specific', tax_country: 'AE' })
    // Clearing it is a real change: it turns cross-border determination off.
    expect(billingBody(saved, { ...f, tax_country: '' })).toEqual({ discount_rule: 'most-specific', tax_country: '' })
  })
  it('an emptied tax rate is sent as 0 — the Sovereign default is a rate, never unset', () => {
    expect(billingBody(saved, { ...billingFormFrom(saved), tax_rate: '' })).toEqual({ discount_rule: 'most-specific', tax_rate: '0' })
  })
})
