import { describe, expect, it } from 'vitest'
import { creditNoteBody, emptyCreditNoteForm, emptyTopUpForm, topUpBody, validateCreditNote, validateSettings, validateTopUp } from './forms'

describe('validateSettings with the tax profile', () => {
  const base = { name: 'A', admin_email: 'a@b.co', charging: 'informational', payment_model: '', payment_method: '', gateway_name: '', po_reference: '', payment_terms_days: '', external_account_id: '', start_date: '', status: 'active', org_slug: '' }
  it('accepts an empty override and a percentage; refuses a fraction with a sign or over 100', () => {
    expect(validateSettings({ ...base, tax_rate: '' })).toEqual({})
    expect(validateSettings({ ...base, tax_rate: '5.5' })).toEqual({})
    expect(validateSettings({ ...base, tax_rate: '5 %' }).tax_rate).toMatch(/percentage/)
    expect(validateSettings({ ...base, tax_rate: '101' }).tax_rate).toMatch(/100/)
  })
  it('an exemption needs its reason — it is printed on every invoice', () => {
    expect(validateSettings({ ...base, tax_exempt: true, tax_exempt_reason: '' }).tax_exempt_reason).toMatch(/why/)
    expect(validateSettings({ ...base, tax_exempt: true, tax_exempt_reason: 'embassy' })).toEqual({})
  })
})

describe('the top-up form', () => {
  const ok = { ...emptyTopUpForm('2026-09-10'), amount: '500', reference: 'TRF-9' }
  it('accepts a positive amount on a real day by a known method', () => {
    expect(validateTopUp(ok)).toEqual({})
    expect(topUpBody({ ...ok, amount: ' 500 ' })).toEqual({ amount: '500', paid_at: '2026-09-10', reference: 'TRF-9', method: 'transfer' })
  })
  it('refuses zero, a malformed number, an impossible day and an unknown method', () => {
    expect(validateTopUp({ ...ok, amount: '0' }).amount).toMatch(/above zero/)
    expect(validateTopUp({ ...ok, amount: '1,200' }).amount).toMatch(/plain number/)
    expect(validateTopUp({ ...ok, paid_at: '2026-02-30' }).paid_at).toMatch(/YYYY-MM-DD/)
    expect(validateTopUp({ ...ok, method: 'cash' }).method).toMatch(/how the money/)
  })
})

describe('the credit-note form', () => {
  it('starts at the outstanding amount', () => {
    expect(emptyCreditNoteForm(250.5)).toEqual({ amount: '250.500', reason: '', full: false })
    expect(emptyCreditNoteForm(0).amount).toBe('')
  })
  it('needs a reason, and an amount within what can still be credited', () => {
    expect(validateCreditNote({ amount: '100', reason: '', full: false }, 500).reason).toMatch(/reason/)
    expect(validateCreditNote({ amount: '600', reason: 'outage', full: false }, 500).amount).toMatch(/500\.000/)
    expect(validateCreditNote({ amount: '0', reason: 'outage', full: false }, 500).amount).toMatch(/above zero/)
    expect(validateCreditNote({ amount: '100', reason: 'outage', full: false }, 500)).toEqual({})
  })
  it('a full credit note ignores the amount — the server sets it to the invoice total', () => {
    expect(validateCreditNote({ amount: '', reason: 'raised in error', full: true }, 500)).toEqual({})
    expect(creditNoteBody({ amount: '999', reason: ' raised in error ', full: true })).toEqual({ reason: 'raised in error', kind: 'full' })
    expect(creditNoteBody({ amount: '100', reason: 'outage', full: false })).toEqual({ reason: 'outage', kind: 'partial', amount: '100' })
  })
})
