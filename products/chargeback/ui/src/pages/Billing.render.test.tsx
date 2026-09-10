import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'

/**
 * The Billing settings page rendered: the saved document fills every
 * control, the rate reads as a percentage, and the reminder schedule is
 * spelled out under its field.
 */

const settings = {
  discount_rule: 'most-specific',
  invoice_prefix: 'INV',
  credit_note_prefix: 'CN',
  commercial_provider: 'internal',
  tax_rate: '0.050000',
  tax_registration_number: 'OM100200300',
  legal_name: 'Sovereign LLC',
  address: 'Muscat',
  reminder_days: [-3, 0, 7, 14, 30],
  escalation_days: 45,
  escalation_action: 'notify',
  updated_at: '2026-09-01T00:00:00Z',
}

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({ data: path === '/billing-settings' ? settings : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
}))

import { Billing } from './Billing'

describe('Billing renders the settings', () => {
  it('fills every control from the document', () => {
    const html = renderToString(createElement(MemoryRouter, null, createElement(Billing))).replace(/<!-- -->/g, '')
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('value="INV"')
    expect(html).toContain('value="CN"')
    expect(html).toMatch(/aria-label="Tax rate"[^>]*value="5"|value="5"[^>]*aria-label="Tax rate"/)
    expect(html).toContain('value="OM100200300"')
    expect(html).toContain('value="Sovereign LLC"')
    expect(html).toContain('value="-3, 0, 7, 14, 30"')
    expect(html).toContain('Reads: 3 days before · on the due date · 7, 14, 30 days after.')
    expect(html).toContain('value="45"')
    expect(html).toContain('Notify the operator')
    expect(html).toContain('Suspend at the platform')
    expect(html).toContain('>Numbering<')
    expect(html).toContain('>Tax<')
    expect(html).toContain('>Collections<')
    expect(html).toContain('No changes')
  })
})
