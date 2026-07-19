/**
 * CreateOrganizationPage.test.tsx — multi-domain Organization onboarding
 * coverage (issue #828, parent epic #825).
 *
 *   • Heading + form renders
 *   • Parent-domain dropdown lists every org-pool entry
 *   • NS-flip-pending entries are disabled in the dropdown
 *   • Console URL preview updates as fields change (free-subdomain)
 *   • Switching to BYO mode hides the dropdown + shows the BYO field
 *   • Empty pool surfaces the "no parents available" placeholder
 */

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { CreateOrganizationPage } from './CreateOrganizationPage'
import type { SovereignParentDomain } from './org.api'

afterEach(() => cleanup())

const POOL: SovereignParentDomain[] = [
  { name: 'omani.works', role: 'org-pool', flipStatus: 'ready' },
  { name: 'omani.trade', role: 'org-pool', flipStatus: 'ready' },
  { name: 'pending.example', role: 'org-pool', flipStatus: 'flipping' },
]

describe('CreateOrganizationPage', () => {
  it('renders heading + form', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    // Issue #3378: the page is the Organizations internal door now.
    expect(screen.getByTestId('create-org-title').textContent).toBe('Create organization')
    expect(screen.getByTestId('org-create-form')).toBeTruthy()
    expect(screen.getByTestId('org-create-submit')).toBeTruthy()
  })

  /* ── Organizations internal door (issue #3378 B1, DoD-4) ── */

  it('defaults to customer kind with real + vcluster derived defaults', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    expect(
      screen.getByTestId('create-org-kind-customer').getAttribute('aria-pressed'),
    ).toBe('true')
    expect(screen.getByTestId('create-org-billing-mode').getAttribute('data-mode')).toBe('real')
    expect(screen.getByTestId('create-org-isolation').getAttribute('data-isolation')).toBe('vcluster')
  })

  it('selecting Internal renders showback + namespace defaults', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    fireEvent.click(screen.getByTestId('create-org-kind-internal'))
    expect(
      screen.getByTestId('create-org-kind-internal').getAttribute('aria-pressed'),
    ).toBe('true')
    expect(screen.getByTestId('create-org-billing-mode').getAttribute('data-mode')).toBe('showback')
    expect(screen.getByTestId('create-org-isolation').getAttribute('data-isolation')).toBe('namespace')
  })

  it('the advanced override is visible and can change billing/isolation', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    fireEvent.click(screen.getByTestId('create-org-kind-internal'))
    // advanced panel hidden until toggled
    expect(screen.queryByTestId('create-org-advanced')).toBeNull()
    fireEvent.click(screen.getByTestId('create-org-advanced-toggle'))
    expect(screen.getByTestId('create-org-advanced')).toBeTruthy()
    // override billing to chargeback — the badge reflects it
    const billingSel = screen.getByTestId('create-org-billing-select') as HTMLSelectElement
    fireEvent.change(billingSel, { target: { value: 'chargeback' } })
    expect(screen.getByTestId('create-org-billing-mode').getAttribute('data-mode')).toBe('chargeback')
  })

  it('renders parent-domain dropdown with every org-pool entry', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    expect(
      screen.getByTestId('org-create-parent-option-omani.works'),
    ).toBeTruthy()
    expect(
      screen.getByTestId('org-create-parent-option-omani.trade'),
    ).toBeTruthy()
    expect(
      screen.getByTestId('org-create-parent-option-pending.example'),
    ).toBeTruthy()
  })

  it('disables NS-flip-pending entries in the parent dropdown', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const pending = screen.getByTestId(
      'org-create-parent-option-pending.example',
    ) as HTMLOptionElement
    expect(pending.disabled).toBe(true)
    const ready = screen.getByTestId(
      'org-create-parent-option-omani.works',
    ) as HTMLOptionElement
    expect(ready.disabled).toBe(false)
  })

  it('updates console URL preview as the operator types the slug', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const slug = screen.getByTestId('org-create-subdomain') as HTMLInputElement
    fireEvent.change(slug, { target: { value: 'acme' } })
    const preview = screen.getByTestId('org-create-url-preview')
    // The default-selected parent should be the first ns_flip_ready entry.
    expect(preview.textContent).toBe('console.acme.omani.works')
  })

  it('preview reflects parent dropdown changes', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    const slug = screen.getByTestId('org-create-subdomain') as HTMLInputElement
    fireEvent.change(slug, { target: { value: 'acme' } })
    const select = screen.getByTestId(
      'org-create-parent-select',
    ) as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'omani.trade' } })
    expect(
      screen.getByTestId('org-create-url-preview').textContent,
    ).toBe('console.acme.omani.trade')
  })

  it('switching to BYO mode hides the parent dropdown + shows the BYO field', () => {
    render(<CreateOrganizationPage initialParentDomains={POOL} disableFetch />)
    expect(screen.queryByTestId('org-create-parent-row')).toBeTruthy()

    const byoRadio = screen
      .getByTestId('org-create-mode-byo')
      .querySelector('input[type="radio"]') as HTMLInputElement
    fireEvent.click(byoRadio)

    expect(screen.queryByTestId('org-create-parent-row')).toBeNull()
    expect(screen.getByTestId('org-create-byo')).toBeTruthy()

    // Type the BYO domain — preview reflects it.
    const byo = screen.getByTestId('org-create-byo') as HTMLInputElement
    fireEvent.change(byo, { target: { value: 'acme.com' } })
    expect(
      screen.getByTestId('org-create-url-preview').textContent,
    ).toBe('console.acme.com')
  })

  it('empty pool renders the no-parents placeholder', () => {
    render(<CreateOrganizationPage initialParentDomains={[]} disableFetch />)
    const select = screen.getByTestId(
      'org-create-parent-select',
    ) as HTMLSelectElement
    expect(select.disabled).toBe(true)
    expect(select.textContent).toContain('No pool parents available')
  })
})
