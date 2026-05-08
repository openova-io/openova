/**
 * PolicyModeToggle.test.tsx — unit tests for the U5 toggle widget
 * (slice U, #1096).
 *
 * What we assert:
 *   1. Renders current mode pill (Audit / Enforce).
 *   2. Click flips into a confirmation dialog with a diff message
 *      showing pass/fail counts and target mode.
 *   3. Cancel closes the dialog without firing the network call.
 *   4. Confirm fires PUT /api/v1/sovereigns/{id}/environments/{env}/policy
 *      with `modes` body and surfaces error on failure.
 *   5. RBAC-disabled mode disables the toggle button.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PolicyModeToggle } from './PolicyModeToggle'

afterEach(() => cleanup())

function renderWithQueryClient(ui: React.ReactElement) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

describe('PolicyModeToggle — render', () => {
  it('renders Audit pill for permissive mode', () => {
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="prod"
        policyName="probes-present"
        currentMode="permissive"
      />,
    )
    const btn = screen.getByTestId('policy-mode-toggle-button-probes-present')
    expect(btn.textContent).toContain('Audit')
    expect(btn.getAttribute('aria-pressed')).toBe('false')
  })

  it('renders Enforce pill for enforcing mode', () => {
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="prod"
        policyName="probes-present"
        currentMode="enforcing"
      />,
    )
    const btn = screen.getByTestId('policy-mode-toggle-button-probes-present')
    expect(btn.textContent).toContain('Enforce')
    expect(btn.getAttribute('aria-pressed')).toBe('true')
  })

  it('disabled prop renders button disabled', () => {
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="prod"
        policyName="probes-present"
        currentMode="permissive"
        disabled
      />,
    )
    const btn = screen.getByTestId('policy-mode-toggle-button-probes-present') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })
})

describe('PolicyModeToggle — confirm dialog', () => {
  it('clicking the toggle surfaces the confirmation dialog with diff copy', () => {
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="prod"
        policyName="probes-present"
        currentMode="permissive"
        passingCount={10}
        failingCount={3}
      />,
    )
    fireEvent.click(screen.getByTestId('policy-mode-toggle-button-probes-present'))
    const dialog = screen.getByTestId('policy-mode-confirm-probes-present')
    expect(dialog).toBeTruthy()
    expect(dialog.textContent).toContain('10')
    expect(dialog.textContent).toContain('3')
    expect(dialog.textContent).toMatch(/Enforce/i)
  })

  it('cancel closes the dialog without firing fetch', () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })))
    globalThis.fetch = fetchMock as never
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="prod"
        policyName="probes-present"
        currentMode="permissive"
      />,
    )
    fireEvent.click(screen.getByTestId('policy-mode-toggle-button-probes-present'))
    expect(screen.queryByTestId('policy-mode-confirm-probes-present')).toBeTruthy()
    fireEvent.click(screen.getByTestId('policy-mode-confirm-cancel-probes-present'))
    expect(screen.queryByTestId('policy-mode-confirm-probes-present')).toBeNull()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})

describe('PolicyModeToggle — confirm fires PUT', () => {
  let fetchMock: ReturnType<typeof vi.fn>

  beforeEach(() => {
    fetchMock = vi.fn(() => Promise.resolve(new Response(null, { status: 204 })))
    globalThis.fetch = fetchMock as never
  })

  it('confirm clicks PUT to /environments/<env>/policy with mode payload', async () => {
    const onModeChanged = vi.fn()
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="acme-prod"
        policyName="probes-present"
        currentMode="permissive"
        onModeChanged={onModeChanged}
      />,
    )
    fireEvent.click(screen.getByTestId('policy-mode-toggle-button-probes-present'))
    fireEvent.click(screen.getByTestId('policy-mode-confirm-apply-probes-present'))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1)
    })
    const [url, init] = fetchMock.mock.calls[0]!
    expect(String(url)).toContain('/environments/acme-prod/policy')
    expect(init.method).toBe('PUT')
    const body = JSON.parse(init.body as string)
    expect(body).toEqual({ modes: { 'probes-present': 'enforcing' } })

    await waitFor(() => {
      expect(onModeChanged).toHaveBeenCalledWith('enforcing')
    })
  })

  it('flipping enforcing→permissive sends mode=permissive', async () => {
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="acme-prod"
        policyName="probes-present"
        currentMode="enforcing"
      />,
    )
    fireEvent.click(screen.getByTestId('policy-mode-toggle-button-probes-present'))
    fireEvent.click(screen.getByTestId('policy-mode-confirm-apply-probes-present'))
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalled()
    })
    const init = fetchMock.mock.calls[0]![1] as RequestInit
    const body = JSON.parse(init.body as string)
    expect(body.modes['probes-present']).toBe('permissive')
  })

  it('surfaces error on non-2xx response', async () => {
    fetchMock = vi.fn(() => Promise.resolve(new Response('forbidden', { status: 403 })))
    globalThis.fetch = fetchMock as never
    renderWithQueryClient(
      <PolicyModeToggle
        sovereignId="alpha"
        environmentRef="acme-prod"
        policyName="probes-present"
        currentMode="permissive"
      />,
    )
    fireEvent.click(screen.getByTestId('policy-mode-toggle-button-probes-present'))
    fireEvent.click(screen.getByTestId('policy-mode-confirm-apply-probes-present'))
    await waitFor(() => {
      expect(
        screen.queryByTestId('policy-mode-confirm-error-probes-present'),
      ).toBeTruthy()
    })
  })
})
