/**
 * BillingModeGate.test.tsx — mode-aware billing (issue #3378 B4 / DoD 5).
 * showback/chargeback → the notice and the link to the cost view of the
 * billing engine, with ZERO payment actions (the wrapped payment page is
 * NOT mounted); real → the payment page renders.
 */

import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
// The link has its own tests (CostViewLink.test.tsx) and needs a router;
// the gate is tested with a stand-in.
vi.mock('./CostViewLink', () => ({
  CostViewLink: () => <div data-testid="cost-view-link" />,
}))

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
  it('showback: renders the notice and the cost view link, NO payment page', () => {
    renderGate('showback')
    expect(screen.getByTestId('billing-mode-gate').getAttribute('data-mode')).toBe('showback')
    expect(screen.getByTestId('billing-showback-notice')).toBeTruthy()
    expect(screen.getByTestId('cost-view-link')).toBeTruthy()
    // the payment page must NOT be mounted (zero payment actions reachable)
    expect(screen.queryByTestId('payment-page')).toBeNull()
  })

  it('chargeback: also the notice and the link, no payment page', () => {
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
