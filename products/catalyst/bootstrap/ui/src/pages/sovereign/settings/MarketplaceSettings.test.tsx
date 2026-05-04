/**
 * MarketplaceSettings.test.tsx — wiring lock-in for the Marketplace
 * settings card (issue #710 wave 3b).
 *
 * Coverage:
 *   • Page renders heading + toggle + brand fields card.
 *   • Toggle flips enabled/disabled.
 *   • Save button POSTs to /api/v1/sovereigns/{id}/marketplace with
 *     `credentials: 'include'` and the expected payload.
 *   • Hex-colour validation surfaces an inline error and disables
 *     the Save button until corrected.
 *   • Reconciling status renders the commit SHA short prefix.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

// Mock DETECTED_MODE so deploymentId resolves without a window.location
vi.mock('@/shared/lib/detectMode', () => ({
  DETECTED_MODE: { mode: 'sovereign', sovereignFQDN: 'omantel.omani.works' },
}))

// Mock API_BASE so the assertion doesn't depend on the runtime base.
vi.mock('@/shared/config/urls', () => ({
  BASE: '/',
  API_BASE: '/api',
}))

import { MarketplaceSettings } from './MarketplaceSettings'

describe('MarketplaceSettings', () => {
  beforeEach(() => {
    // jsdom fetch is undefined — install a manual mock per test.
    globalThis.fetch = vi.fn() as never
  })

  afterEach(() => {
    cleanup()
    vi.restoreAllMocks()
  })

  it('renders heading, toggle, brand fields, and save button', () => {
    render(<MarketplaceSettings />)
    expect(screen.getByTestId('marketplace-settings-page')).toBeTruthy()
    expect(screen.getByTestId('marketplace-settings-card')).toBeTruthy()
    expect(screen.getByTestId('marketplace-settings-toggle')).toBeTruthy()
    expect(screen.getByTestId('marketplace-settings-brand-name')).toBeTruthy()
    expect(screen.getByTestId('marketplace-settings-brand-tagline')).toBeTruthy()
    expect(screen.getByTestId('marketplace-settings-brand-color-picker')).toBeTruthy()
    expect(screen.getByTestId('marketplace-settings-save')).toBeTruthy()
  })

  it('flips the toggle on click', () => {
    render(<MarketplaceSettings />)
    const toggle = screen.getByTestId('marketplace-settings-toggle')
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    fireEvent.click(toggle)
    expect(toggle.getAttribute('aria-checked')).toBe('true')
  })

  it('rejects an invalid primary colour and disables Save', () => {
    render(<MarketplaceSettings />)
    fireEvent.click(screen.getByTestId('marketplace-settings-toggle'))
    const colorText = screen.getByTestId('marketplace-settings-brand-color-text') as HTMLInputElement
    fireEvent.change(colorText, { target: { value: 'bad' } })
    expect(screen.getByTestId('marketplace-settings-brand-color-error')).toBeTruthy()
    const save = screen.getByTestId('marketplace-settings-save') as HTMLButtonElement
    expect(save.disabled).toBe(true)
  })

  it('POSTs to /v1/sovereigns/{id}/marketplace with credentials include', async () => {
    const fetchMock = vi.fn(async () =>
      new Response(
        JSON.stringify({
          deploymentId: 'omantel.omani.works',
          sovereignFQDN: 'omantel.omani.works',
          enabled: true,
          commitSha: 'abc1234567890',
          appliedAt: '2026-05-03T12:00:00Z',
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )
    globalThis.fetch = fetchMock as never

    render(<MarketplaceSettings />)
    fireEvent.click(screen.getByTestId('marketplace-settings-toggle'))
    fireEvent.change(screen.getByTestId('marketplace-settings-brand-name'), {
      target: { value: 'Otech Cloud' },
    })
    fireEvent.click(screen.getByTestId('marketplace-settings-save'))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalled()
    })
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/v1/sovereigns/omantel.omani.works/marketplace')
    expect(init.method).toBe('POST')
    expect(init.credentials).toBe('include')
    const body = JSON.parse(init.body as string)
    expect(body.enabled).toBe(true)
    expect(body.brand.name).toBe('Otech Cloud')

    // After 200 response, the reconciling status surfaces with the
    // short-form commit SHA.
    await waitFor(() => {
      expect(screen.getByTestId('marketplace-settings-status-reconciling')).toBeTruthy()
    })
    expect(screen.getByTestId('marketplace-settings-status-reconciling').textContent).toContain(
      'abc1234',
    )
  })
})
