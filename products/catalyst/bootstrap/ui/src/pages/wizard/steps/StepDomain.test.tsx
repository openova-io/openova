/**
 * StepDomain.test.tsx — vitest coverage for the three-mode (pool /
 * byo-manual / byo-api) domain capture step. Closes #169.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react'
import { StepDomain } from './StepDomain'
import { useWizardStore } from '@/entities/deployment/store'
import {
  INITIAL_WIZARD_STATE,
  OPENOVA_NAMESERVERS,
  REGISTRAR_OPTIONS,
} from '@/entities/deployment/model'

beforeEach(() => {
  useWizardStore.setState({ ...INITIAL_WIZARD_STATE })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('StepDomain — mode toggle', () => {
  it('renders all three radio cards', () => {
    render(<StepDomain />)
    expect(screen.getByTestId('domain-mode-pool')).toBeTruthy()
    expect(screen.getByTestId('domain-mode-byo-manual')).toBeTruthy()
    expect(screen.getByTestId('domain-mode-byo-api')).toBeTruthy()
  })

  it('starts in pool mode by default', () => {
    render(<StepDomain />)
    expect(useWizardStore.getState().sovereignDomainMode).toBe('pool')
    expect(screen.getByTestId('pool-subdomain-input')).toBeTruthy()
  })

  it('switches to byo-manual and clears the pool subdomain', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignSubdomain: 'omantel-prod',
    })
    render(<StepDomain />)
    fireEvent.click(screen.getByTestId('domain-mode-byo-manual'))
    expect(useWizardStore.getState().sovereignDomainMode).toBe('byo-manual')
    expect(useWizardStore.getState().sovereignSubdomain).toBe('')
    expect(screen.getByTestId('byo-domain-input')).toBeTruthy()
    expect(screen.getByTestId('byo-ns-instructions')).toBeTruthy()
  })

  it('switches to byo-api and exposes registrar + token fields', () => {
    render(<StepDomain />)
    fireEvent.click(screen.getByTestId('domain-mode-byo-api'))
    expect(useWizardStore.getState().sovereignDomainMode).toBe('byo-api')
    expect(screen.getByTestId('byo-api-registrar-select')).toBeTruthy()
    expect(screen.getByTestId('byo-api-token-input')).toBeTruthy()
    expect(screen.getByTestId('byo-api-validate-button')).toBeTruthy()
  })

  it('drops registrar credentials when switching away from byo-api', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      registrarType: 'cloudflare',
      registrarToken: 'secret-token-do-not-leak',
      registrarTokenValidated: true,
    })
    render(<StepDomain />)
    fireEvent.click(screen.getByTestId('domain-mode-pool'))
    const s = useWizardStore.getState()
    expect(s.registrarType).toBeNull()
    expect(s.registrarToken).toBe('')
    expect(s.registrarTokenValidated).toBe(false)
  })
})

describe('StepDomain — pool mode', () => {
  it('writes the subdomain to the store as the user types', () => {
    render(<StepDomain />)
    const input = screen.getByTestId('pool-subdomain-input') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'Omantel-Prod' } })
    expect(useWizardStore.getState().sovereignSubdomain).toBe('omantel-prod')
  })

  it('renders the pool dropdown defaulted to omani-works', () => {
    render(<StepDomain />)
    const select = screen.getByTestId('pool-domain-select') as HTMLSelectElement
    expect(select.value).toBe('omani-works')
  })
})

describe('StepDomain — byo-manual mode', () => {
  beforeEach(() => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-manual',
    })
  })

  it('writes the typed domain to the store', () => {
    render(<StepDomain />)
    const input = screen.getByTestId('byo-domain-input') as HTMLInputElement
    fireEvent.change(input, { target: { value: 'acme.com' } })
    expect(useWizardStore.getState().sovereignByoDomain).toBe('acme.com')
  })

  it('renders all OpenOva nameservers verbatim', () => {
    render(<StepDomain />)
    for (const ns of OPENOVA_NAMESERVERS) {
      expect(screen.getByText(ns)).toBeTruthy()
    }
  })

  it('exposes a copy button for each nameserver', () => {
    render(<StepDomain />)
    for (let i = 0; i < OPENOVA_NAMESERVERS.length; i++) {
      expect(screen.getByTestId(`byo-ns-copy-${i}`)).toBeTruthy()
    }
  })
})

describe('StepDomain — byo-api mode', () => {
  beforeEach(() => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
    })
  })

  it('lists every supported registrar in the dropdown', () => {
    render(<StepDomain />)
    const select = screen.getByTestId('byo-api-registrar-select') as HTMLSelectElement
    const optionValues = Array.from(select.options).map(o => o.value).filter(Boolean)
    expect(optionValues.sort()).toEqual([...REGISTRAR_OPTIONS.map(r => r.id)].sort())
  })

  it('disables the Validate button until domain + registrar + token are present', () => {
    render(<StepDomain />)
    const btn = screen.getByTestId('byo-api-validate-button') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('byo-api-domain-input'), { target: { value: 'acme.com' } })
    expect(btn.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('byo-api-registrar-select'), { target: { value: 'cloudflare' } })
    expect(btn.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('byo-api-token-input'), { target: { value: 'cf-token-123' } })
    expect(btn.disabled).toBe(false)
  })

  it('POSTs to /api/v1/registrar/{r}/validate and flips the validated flag on success', async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true, status: 200, json: async () => ({ valid: true }),
    } as Response)
    vi.stubGlobal('fetch', fetchMock)

    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      sovereignByoDomain: 'acme.com',
      registrarType: 'cloudflare',
      registrarToken: 'cf-token-123',
    })
    render(<StepDomain />)
    fireEvent.click(screen.getByTestId('byo-api-validate-button'))

    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    const [url, init] = fetchMock.mock.calls[0]
    expect(String(url)).toMatch(/\/api\/v1\/registrar\/cloudflare\/validate$/)
    expect(init?.method).toBe('POST')
    const body = JSON.parse(init?.body as string)
    expect(body.domain).toBe('acme.com')
    expect(body.token).toBe('cf-token-123')

    await waitFor(() => {
      expect(useWizardStore.getState().registrarTokenValidated).toBe(true)
    })
    expect(screen.getByTestId('byo-api-validated-banner')).toBeTruthy()
  })

  it('surfaces an error and leaves validated=false on a 401 invalid-token response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        ok: false, status: 401,
        json: async () => ({ error: 'invalid-token', detail: 'token rejected' }),
      } as Response),
    )

    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      sovereignByoDomain: 'acme.com',
      registrarType: 'cloudflare',
      registrarToken: 'wrong-token',
    })
    render(<StepDomain />)
    fireEvent.click(screen.getByTestId('byo-api-validate-button'))

    await waitFor(() => {
      expect(screen.getByRole('alert').textContent).toMatch(/Token rejected/i)
    })
    expect(useWizardStore.getState().registrarTokenValidated).toBe(false)
  })

  it('invalidates a previously-validated token when the customer edits it', () => {
    useWizardStore.setState({
      ...INITIAL_WIZARD_STATE,
      sovereignDomainMode: 'byo-api',
      sovereignByoDomain: 'acme.com',
      registrarType: 'cloudflare',
      registrarToken: 'old-token',
      registrarTokenValidated: true,
    })
    render(<StepDomain />)
    fireEvent.change(screen.getByTestId('byo-api-token-input'), { target: { value: 'new-token' } })
    expect(useWizardStore.getState().registrarTokenValidated).toBe(false)
  })
})
