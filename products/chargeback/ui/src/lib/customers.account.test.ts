import { describe, expect, it } from 'vitest'
import type { Customer } from '../api/types'
import { customerPatch, fieldLabel, settingsFrom, taxRatePercent } from './customers'

// The account-credit switches and the tax profile on the settings form
// (DESIGN.md §9.4–§9.5): booleans travel as booleans, the rate as a fraction.
const cust = (over: Partial<Customer>): Customer => ({ id: 'c-x', slug: 'x', name: 'X', admin_email: 'x@example.com', billing_mode: 'chargeback', charging: 'billed', payment_model: 'prepaid', payment_method: 'transfer', status: 'active', ...over })

describe('settingsFrom reads the tax profile and the switches', () => {
  it('defaults every switch to off and the override to empty', () => {
    const f = settingsFrom(cust({}))
    expect(f).toMatchObject({ auto_apply_credit: false, suspend_at_zero: false, tax_exempt: false, tax_exempt_reason: '', tax_rate: '', tax_registration_number: '' })
  })
  it('shows the override as a percentage', () => {
    expect(settingsFrom(cust({ tax_rate: '0.050000', tax_exempt: true, tax_exempt_reason: 'government', auto_apply_credit: true, suspend_at_zero: true, tax_registration_number: 'OM1' })).tax_rate).toBe('5')
    expect(taxRatePercent(0.055)).toBe('5.5')
    expect(taxRatePercent(null)).toBe('')
  })
})

describe('customerPatch with switches and tax', () => {
  const orig = cust({})
  it('sends a flipped switch as a boolean and nothing for an untouched one', () => {
    expect(customerPatch(orig, { ...settingsFrom(orig), auto_apply_credit: true })).toEqual({ auto_apply_credit: true })
    expect(customerPatch(orig, { ...settingsFrom(orig), suspend_at_zero: true, tax_exempt: true, tax_exempt_reason: 'embassy' })).toEqual({ suspend_at_zero: true, tax_exempt: true, tax_exempt_reason: 'embassy' })
    expect(customerPatch(orig, settingsFrom(orig))).toEqual({})
  })
  it('sends the tax rate as a fraction, and an emptied override as "" to clear it', () => {
    expect(customerPatch(orig, { ...settingsFrom(orig), tax_rate: '5' })).toEqual({ tax_rate: '0.05' })
    const withRate = cust({ tax_rate: '0.100000' })
    expect(customerPatch(withRate, { ...settingsFrom(withRate), tax_rate: '' })).toEqual({ tax_rate: '' })
    expect(customerPatch(withRate, { ...settingsFrom(withRate), tax_rate: '10' })).toEqual({})
  })
  it('switching the exemption off clears its reason', () => {
    const exempt = cust({ tax_exempt: true, tax_exempt_reason: 'embassy' })
    expect(customerPatch(exempt, { ...settingsFrom(exempt), tax_exempt: false })).toEqual({ tax_exempt: false, tax_exempt_reason: '' })
  })
  it('labels the new fields for the saved notice', () => {
    expect(fieldLabel('auto_apply_credit')).toBe('auto-apply credit')
    expect(fieldLabel('tax_registration_number')).toBe('tax registration number')
  })
})
