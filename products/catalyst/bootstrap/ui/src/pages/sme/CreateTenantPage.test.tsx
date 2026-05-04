/**
 * CreateTenantPage.test.tsx — multi-domain SME tenant onboarding
 * coverage (issue #828, parent epic #825).
 *
 *   • Heading + form renders
 *   • Parent-domain dropdown lists every sme-pool entry
 *   • NS-flip-pending entries are disabled in the dropdown
 *   • Console URL preview updates as fields change (free-subdomain)
 *   • Switching to BYO mode hides the dropdown + shows the BYO field
 *   • Empty pool surfaces the "no parents available" placeholder
 */

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CreateTenantPage } from './CreateTenantPage'
import type { SovereignParentDomain } from './sme.api'

afterEach(() => cleanup())

const POOL: SovereignParentDomain[] = [
  { name: 'omani.works', role: 'sme-pool', flipStatus: 'ready' },
  { name: 'omani.trade', role: 'sme-pool', flipStatus: 'ready' },
  { name: 'pending.example', role: 'sme-pool', flipStatus: 'flipping' },
]

describe('CreateTenantPage', () => {
  it('renders heading + form', () => {
    render(<CreateTenantPage initialParentDomains={POOL} disableFetch />)
    expect(screen.getByText('Onboard SME Tenant')).toBeTruthy()
    expect(screen.getByTestId('sme-create-tenant-form')).toBeTruthy()
    expect(screen.getByTestId('sme-create-tenant-submit')).toBeTruthy()
  })

  it('renders parent-domain dropdown with every sme-pool entry', () => {
    render(<CreateTenantPage initialParentDomains={POOL} disableFetch />)
    expect(
      screen.getByTestId('sme-create-tenant-parent-option-omani.works'),
    ).toBeTruthy()
    expect(
      screen.getByTestId('sme-create-tenant-parent-option-omani.trade'),
    ).toBeTruthy()
    expect(
      screen.getByTestId('sme-create-tenant-parent-option-pending.example'),
    ).toBeTruthy()
  })

  it('disables NS-flip-pending entries in the parent dropdown', () => {
    render(<CreateTenantPage initialParentDomains={POOL} disableFetch />)
    const pending = screen.getByTestId(
      'sme-create-tenant-parent-option-pending.example',
    ) as HTMLOptionElement
    expect(pending.disabled).toBe(true)
    const ready = screen.getByTestId(
      'sme-create-tenant-parent-option-omani.works',
    ) as HTMLOptionElement
    expect(ready.disabled).toBe(false)
  })

  it('updates console URL preview as the operator types the slug', () => {
    render(<CreateTenantPage initialParentDomains={POOL} disableFetch />)
    const slug = screen.getByTestId('sme-create-tenant-subdomain') as HTMLInputElement
    fireEvent.change(slug, { target: { value: 'acme' } })
    const preview = screen.getByTestId('sme-create-tenant-url-preview')
    // The default-selected parent should be the first ns_flip_ready entry.
    expect(preview.textContent).toBe('console.acme.omani.works')
  })

  it('preview reflects parent dropdown changes', () => {
    render(<CreateTenantPage initialParentDomains={POOL} disableFetch />)
    const slug = screen.getByTestId('sme-create-tenant-subdomain') as HTMLInputElement
    fireEvent.change(slug, { target: { value: 'acme' } })
    const select = screen.getByTestId(
      'sme-create-tenant-parent-select',
    ) as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'omani.trade' } })
    expect(
      screen.getByTestId('sme-create-tenant-url-preview').textContent,
    ).toBe('console.acme.omani.trade')
  })

  it('switching to BYO mode hides the parent dropdown + shows the BYO field', () => {
    render(<CreateTenantPage initialParentDomains={POOL} disableFetch />)
    expect(screen.queryByTestId('sme-create-tenant-parent-row')).toBeTruthy()

    const byoRadio = screen
      .getByTestId('sme-create-tenant-mode-byo')
      .querySelector('input[type="radio"]') as HTMLInputElement
    fireEvent.click(byoRadio)

    expect(screen.queryByTestId('sme-create-tenant-parent-row')).toBeNull()
    expect(screen.getByTestId('sme-create-tenant-byo')).toBeTruthy()

    // Type the BYO domain — preview reflects it.
    const byo = screen.getByTestId('sme-create-tenant-byo') as HTMLInputElement
    fireEvent.change(byo, { target: { value: 'acme.com' } })
    expect(
      screen.getByTestId('sme-create-tenant-url-preview').textContent,
    ).toBe('console.acme.com')
  })

  it('empty pool renders the no-parents placeholder', () => {
    render(<CreateTenantPage initialParentDomains={[]} disableFetch />)
    const select = screen.getByTestId(
      'sme-create-tenant-parent-select',
    ) as HTMLSelectElement
    expect(select.disabled).toBe(true)
    expect(select.textContent).toContain('No sme-pool parents available')
  })
})
