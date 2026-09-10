import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * The customer directory's Balance column (DESIGN.md §9): the customer
 * document's accounting-signed `balance` — owed in red, credit in green,
 * settled in words, and a dash (never 0) when the list did not carry it.
 */

const customers = {
  customers: [
    { id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'ops@acme.example', billing_mode: 'chargeback', charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer', status: 'active', balance: 250.5, available_credit: 0 },
    { id: 'c2', slug: 'globex', name: 'Globex', admin_email: 'fin@globex.example', billing_mode: 'chargeback', charging: 'billed', payment_model: 'prepaid', payment_method: 'transfer', status: 'active', balance: -100, available_credit: 100 },
    { id: 'c3', slug: 'initech', name: 'Initech', admin_email: 'cfo@initech.example', billing_mode: 'chargeback', charging: 'billed', payment_model: 'postpaid', payment_method: 'transfer', status: 'active', balance: 0, available_credit: 0 },
    { id: 'c4', slug: 'legacy', name: 'Legacy Co', admin_email: 'it@legacy.example', billing_mode: 'showback', status: 'pending' },
  ],
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/customers' ? customers : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

import { Customers } from './Customers'

describe('Customers renders the balance column', () => {
  it('signs and colours the balance, and dashes a document without one', () => {
    const html = renderToString(createElement(MemoryRouter, { initialEntries: ['/customers'] }, createElement(Customers))).replace(/<!-- -->/g, '')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('>Balance<')
    expect(html).toMatch(/class="bad">250\.500<\/span><span class="sub">owes</)
    expect(html).toMatch(/class="ok">100\.000<\/span><span class="sub">in credit</)
    expect(html).toContain('>settled<')
    expect(html).toContain('title="The list did not carry a balance — open the Account tab"')
  })
})
