import { describe, expect, it } from 'vitest'
import { budgetBody, currentPeriod, customerBody, discountBody, emptyBudgetForm, emptyCustomerForm, emptyDiscountForm, emptyPaymentForm, isDay, parseEmails, parseThresholds, paymentBody, slugify, validateBudget, validateCustomer, validateDiscount, validatePayment, validateSettings, validateSource } from './forms'

describe('slugify / isDay / currentPeriod', () => {
  it('slugifies names', () => {
    expect(slugify('Acme Trading LLC')).toBe('acme-trading-llc')
    expect(slugify('  Ünïcode & Co. ')).toBe('unicode-co')
    expect(slugify('---')).toBe('')
  })
  it('isDay rejects impossible calendar dates, not just the shape', () => {
    expect(isDay('2026-02-28')).toBe(true)
    expect(isDay('2026-02-30')).toBe(false)
    expect(isDay('2026-9-1')).toBe(false)
  })
  it('currentPeriod is YYYY-MM in UTC', () => {
    expect(currentPeriod(new Date('2026-09-07T23:30:00Z'))).toBe('2026-09')
    expect(currentPeriod(new Date('2026-01-01T00:00:00Z'))).toBe('2026-01')
  })
})

describe('validateCustomer / customerBody', () => {
  const ok = { ...emptyCustomerForm(), slug: 'acme', name: 'Acme', admin_email: 'ops@acme.om' }
  it('a complete form has no errors', () => {
    expect(validateCustomer(ok)).toEqual({})
  })
  it('names every missing or malformed field', () => {
    const e = validateCustomer({ ...emptyCustomerForm(), slug: 'Acme Inc', admin_email: 'nope', start_date: '2026-13-01' })
    expect(Object.keys(e).sort()).toEqual(['admin_email', 'name', 'slug', 'start_date'])
  })
  it('a slug may not start or end with a hyphen', () => {
    expect(validateCustomer({ ...ok, slug: '-acme' }).slug).toBeTruthy()
    expect(validateCustomer({ ...ok, slug: 'acme-' }).slug).toBeTruthy()
    expect(validateCustomer({ ...ok, slug: 'a' })).toEqual({})
  })
  it('an organization kind validates the org slug when given', () => {
    expect(validateCustomer({ ...ok, kind: 'organization', org_slug: 'Bad Slug' }).org_slug).toBeTruthy()
    expect(validateCustomer({ ...ok, kind: 'organization', org_slug: '' })).toEqual({})
  })
  it('body normalises case, nulls blanks and keeps org_slug only for organizations', () => {
    expect(customerBody({ ...ok, slug: ' ACME ', admin_email: 'Ops@Acme.om', org_slug: 'acme' })).toMatchObject({ slug: 'acme', admin_email: 'ops@acme.om', price_book_id: null, start_date: null, kind: 'external', org_slug: null })
    expect(customerBody({ ...ok, kind: 'organization', org_slug: 'Acme' }).org_slug).toBe('acme')
  })
})

describe('validateSettings', () => {
  const base = { name: 'A', admin_email: 'a@b.co', charging: 'informational', payment_model: '', payment_method: '', gateway_name: '', po_reference: '', payment_terms_days: '', external_account_id: '', start_date: '', status: 'active', org_slug: '' }
  it('accepts an informational customer with no payment model or method', () => {
    expect(validateSettings(base)).toEqual({})
  })
  it('a billed customer must say when and how it pays (DESIGN.md §8)', () => {
    expect(Object.keys(validateSettings({ ...base, charging: 'billed' })).sort()).toEqual(['payment_method', 'payment_model'])
    expect(validateSettings({ ...base, charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer' })).toEqual({})
    expect(validateSettings({ ...base, charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway' }).gateway_name).toMatch(/which gateway/)
    expect(validateSettings({ ...base, charging: 'billed', payment_model: 'prepaid', payment_method: 'gateway', gateway_name: 'stripe' })).toEqual({})
  })
  it('rejects an unknown status, an unknown charging and impossible terms', () => {
    const e = validateSettings({ ...base, name: '', charging: 'free', status: 'deleted', payment_terms_days: '400' })
    expect(Object.keys(e).sort()).toEqual(['charging', 'name', 'payment_terms_days', 'status'])
    expect(validateSettings({ ...base, payment_terms_days: 'thirty' }).payment_terms_days).toMatch(/whole number/)
    expect(validateSettings({ ...base, payment_terms_days: '0' })).toEqual({})
  })
})

describe('validatePayment / paymentBody', () => {
  const ok = { amount: '500', paid_at: '2026-07-02', reference: 'TRF-77' }
  it('accepts a payment up to the outstanding balance', () => {
    expect(validatePayment(ok, 1200)).toEqual({})
    expect(validatePayment({ ...ok, amount: '1200' }, 1200)).toEqual({})
  })
  it('refuses more than the balance, zero, and a bad date', () => {
    expect(validatePayment({ ...ok, amount: '1200.01' }, 1200).amount).toMatch(/credit note/)
    expect(validatePayment({ ...ok, amount: '0' }, 1200).amount).toMatch(/above zero/)
    expect(validatePayment({ ...ok, amount: 'lots' }, 1200).amount).toMatch(/plain number/)
    expect(validatePayment({ ...ok, paid_at: '' }, 1200).paid_at).toMatch(/required/)
    expect(validatePayment({ ...ok, paid_at: '02-07-2026' }, 1200).paid_at).toMatch(/YYYY-MM-DD/)
  })
  it('the body is exactly what the API decodes', () => {
    expect(paymentBody({ amount: ' 500 ', paid_at: '2026-07-02', reference: ' TRF-77 ' })).toEqual({ amount: '500', paid_at: '2026-07-02', reference: 'TRF-77' })
  })
  it('the form opens on the full outstanding balance', () => {
    expect(emptyPaymentForm(1200, '2026-07-02')).toEqual({ amount: '1200.000', paid_at: '2026-07-02', reference: '' })
    expect(emptyPaymentForm(0, '2026-07-02').amount).toBe('')
  })
})

describe('validateDiscount / discountBody', () => {
  const ok = { ...emptyDiscountForm(), name: 'Launch', value: '10' }
  it('accepts a percent within 0..100 and a fixed amount of any size', () => {
    expect(validateDiscount(ok)).toEqual({})
    expect(validateDiscount({ ...ok, kind: 'fixed', value: '2500.5' })).toEqual({})
  })
  it('rejects a percent above 100, a negative value and a non-number', () => {
    expect(validateDiscount({ ...ok, value: '120' }).value).toMatch(/exceed 100/)
    expect(validateDiscount({ ...ok, value: '-1' }).value).toMatch(/negative/)
    expect(validateDiscount({ ...ok, value: '10%' }).value).toMatch(/plain number/)
    expect(validateDiscount({ ...ok, value: '' }).value).toMatch(/required/)
  })
  it('a window must end after it starts; a single bound is fine', () => {
    expect(validateDiscount({ ...ok, starts_at: '2026-09-01', ends_at: '2026-09-01' }).ends_at).toMatch(/after start/)
    expect(validateDiscount({ ...ok, starts_at: '2026-09-01', ends_at: '2026-10-01' })).toEqual({})
    expect(validateDiscount({ ...ok, ends_at: '2026-10-01' })).toEqual({})
  })
  it('body carries the value as decimal text and the customer scope', () => {
    expect(discountBody({ ...ok, value: ' 12.5 ', sku: ' ecs.s6 ' }, 'c-1')).toEqual({ customer_id: 'c-1', name: 'Launch', kind: 'percent', value: '12.5', sku: 'ecs.s6', starts_at: '', ends_at: '', active: true })
    expect(discountBody(ok, null).customer_id).toBeNull()
  })
})

describe('parseThresholds', () => {
  it('accepts commas, spaces and semicolons; sorts and de-duplicates', () => {
    expect(parseThresholds('100, 50;80 50').values).toEqual([50, 80, 100])
  })
  it('names the offending token', () => {
    expect(parseThresholds('50, eighty').error).toContain('"eighty"')
    expect(parseThresholds('50, 0').error).toContain('0 is out of range')
    expect(parseThresholds('50.5').error).toContain('"50.5"')
    expect(parseThresholds('').error).toMatch(/at least one/)
  })
})

describe('parseEmails', () => {
  it('lowercases, de-duplicates, allows empty', () => {
    expect(parseEmails('A@x.com, a@x.com\nb@y.com').values).toEqual(['a@x.com', 'b@y.com'])
    expect(parseEmails('   ')).toEqual({ values: [] })
  })
  it('rejects the first bad address by name', () => {
    expect(parseEmails('a@x.com, not-mail').error).toContain('"not-mail"')
  })
})

describe('validateBudget / budgetBody', () => {
  const ok = { ...emptyBudgetForm('OMR'), name: 'September', amount: '3000' }
  it('a complete form has no errors and produces the wire body', () => {
    expect(validateBudget(ok)).toEqual({})
    expect(budgetBody({ ...ok, currency: 'omr', notify_emails: 'Fin@acme.om' }, 'c-1')).toEqual({
      name: 'September',
      customer_id: 'c-1',
      amount: '3000',
      currency: 'OMR',
      period: 'monthly',
      thresholds: [50, 80, 100],
      notify_emails: ['fin@acme.om'],
      active: true,
    })
  })
  it('rejects a zero or negative amount, a bad currency, bad thresholds and bad emails', () => {
    const e = validateBudget({ ...ok, amount: '0', currency: 'OM', thresholds: '50, x', notify_emails: 'nope' })
    expect(Object.keys(e).sort()).toEqual(['amount', 'currency', 'notify_emails', 'thresholds'])
  })
})

describe('validateSource', () => {
  const f = { kind: 'huawei-project', region: '', project_id: '', domain_id: '', scope_token: '' }
  it('requires region and project for a Huawei project only when those fields are editable', () => {
    expect(Object.keys(validateSource(f, ['region', 'project_id', 'scope_token'])).sort()).toEqual(['project_id', 'region'])
    expect(validateSource(f, ['scope_token'])).toEqual({})
  })
  it('a scope token cannot contain whitespace', () => {
    expect(validateSource({ ...f, scope_token: 'dep 123' }, ['scope_token']).scope_token).toBeTruthy()
    expect(validateSource({ ...f, scope_token: 'dep-123' }, ['scope_token'])).toEqual({})
  })
  it('a file source needs neither region nor project', () => {
    expect(validateSource({ ...f, kind: 'file' }, ['region', 'project_id'])).toEqual({})
  })
})
