import { describe, expect, it } from 'vitest'
import { activeMethods, canDispute, disputeState, expiryLabel, invoiceDownloadURL, isDisputed, isExpired, methodLabel, needsCompletion, openDispute } from './selfservice'
import type { Dispute, PaymentMethod } from '../api/types'

const method = (over: Partial<PaymentMethod> = {}): PaymentMethod => ({
  id: 'pm1',
  customer_id: 'c1',
  gateway: 'omantel',
  status: 'active',
  brand: 'visa',
  last4: '4242',
  exp_month: 11,
  exp_year: 2030,
  saved: true,
  ...over,
})

describe('invoiceDownloadURL', () => {
  it('is the statement route with a .pdf suffix', () => {
    expect(invoiceDownloadURL('st1')).toBe('/api/v1/statements/st1.pdf')
  })
})

describe('methodLabel', () => {
  it('names the brand and the last four', () => {
    expect(methodLabel(method())).toBe('Visa ···· 4242')
  })

  it('falls back to the customer label, then the gateway, and never invents digits', () => {
    expect(methodLabel(method({ brand: '', last4: '', label: 'Company card' }))).toBe('Company card')
    expect(methodLabel(method({ brand: '', last4: '', label: '' }))).toBe('Held by omantel')
    expect(methodLabel(method({ brand: '', last4: '', label: '', gateway: '' }))).toBe('Payment method')
  })
})

describe('expiryLabel and isExpired', () => {
  it('pads the month and leaves an unreported expiry blank', () => {
    expect(expiryLabel(method())).toBe('11 / 2030')
    expect(expiryLabel(method({ exp_month: 3, exp_year: 2027 }))).toBe('03 / 2027')
    expect(expiryLabel(method({ exp_month: undefined, exp_year: undefined }))).toBe('')
  })

  it('treats the expiry month itself as still good', () => {
    const now = new Date(Date.UTC(2027, 2, 20)) // March 2027
    expect(isExpired(method({ exp_month: 3, exp_year: 2027 }), now)).toBe(false)
    expect(isExpired(method({ exp_month: 2, exp_year: 2027 }), now)).toBe(true)
    // Nothing reported is not an expired card.
    expect(isExpired(method({ exp_month: undefined, exp_year: undefined }), now)).toBe(false)
  })
})

describe('the methods a customer sees', () => {
  it('drops removed ones and marks the ones still to finish', () => {
    const list = [method(), method({ id: 'pm2', status: 'removed' }), method({ id: 'pm3', status: 'pending', setup_url: 'https://pay.example/x', saved: false })]
    expect(activeMethods(list).map((m) => m.id)).toEqual(['pm1', 'pm3'])
    expect(needsCompletion(list[0])).toBe(false)
    expect(needsCompletion(list[2])).toBe(true)
    // A pending method with no page to send the payer to is not "finishable".
    expect(needsCompletion({ status: 'pending', setup_url: '' })).toBe(false)
  })
})

describe('canDispute', () => {
  it('refuses a draft, a cancelled invoice and one already disputed', () => {
    expect(canDispute({ status: 'sent' })).toBe(true)
    expect(canDispute({ status: 'paid' })).toBe(true)
    expect(canDispute({ status: 'draft' })).toBe(false)
    expect(canDispute({ status: 'cancelled' })).toBe(false)
    expect(canDispute({ status: 'sent', disputed_at: '2026-09-01T00:00:00Z' })).toBe(false)
  })

  it('isDisputed reads the flag the server sets', () => {
    expect(isDisputed({ disputed_at: '2026-09-01T00:00:00Z' })).toBe(true)
    expect(isDisputed({ disputed_at: null })).toBe(false)
  })
})

describe('disputes', () => {
  const d = (over: Partial<Dispute> = {}): Dispute => ({
    id: 'd1',
    statement_id: 'st1',
    customer_id: 'c1',
    reason: 'the storage line is not ours',
    amount: 100,
    status: 'open',
    opened_at: '2026-09-01T00:00:00Z',
    ...over,
  })

  it('finds the open one among resolved ones', () => {
    expect(openDispute([d({ id: 'a', status: 'rejected' }), d({ id: 'b' })])?.id).toBe('b')
    expect(openDispute([d({ id: 'a', status: 'upheld' })])).toBeNull()
  })

  it('says what each state means for collections', () => {
    expect(disputeState(d())).toContain('not chased')
    expect(disputeState(d({ status: 'upheld' }))).toContain('credit note')
    expect(disputeState(d({ status: 'rejected' }))).toContain('collections have resumed')
  })
})
