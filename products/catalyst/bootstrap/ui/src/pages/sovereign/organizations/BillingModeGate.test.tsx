/**
 * BillingModeGate.test.tsx — mode-aware billing (issue #3378 B4 / DoD 5).
 * showback/chargeback → the consumption view with ZERO payment actions
 * (the wrapped payment page is NOT mounted); real → the payment page
 * renders.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BillingModeGate } from './BillingModeGate'

function renderGate(mode: 'real' | 'showback' | 'chargeback') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <BillingModeGate modeOverride={mode}>
        <div data-testid="payment-page">PAYMENT FLOWS</div>
      </BillingModeGate>
    </QueryClientProvider>,
  )
}

afterEach(() => cleanup())

describe('BillingModeGate — mode-aware billing (DoD 5)', () => {
  it('showback: renders the consumption view, NO payment page', () => {
    renderGate('showback')
    expect(screen.getByTestId('billing-mode-gate').getAttribute('data-mode')).toBe('showback')
    expect(screen.getByTestId('billing-showback-notice')).toBeTruthy()
    expect(screen.getByTestId('showback-panel')).toBeTruthy()
    // the payment page must NOT be mounted (zero payment actions reachable)
    expect(screen.queryByTestId('payment-page')).toBeNull()
  })

  it('chargeback: also consumption-only, no payment page', () => {
    renderGate('chargeback')
    expect(screen.getByTestId('billing-mode-gate').getAttribute('data-mode')).toBe('chargeback')
    expect(screen.queryByTestId('payment-page')).toBeNull()
  })

  it('real: renders the payment page (full flow)', () => {
    renderGate('real')
    expect(screen.getByTestId('payment-page')).toBeTruthy()
    expect(screen.queryByTestId('billing-mode-gate')).toBeNull()
  })
})
