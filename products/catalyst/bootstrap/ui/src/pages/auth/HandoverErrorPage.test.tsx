/**
 * HandoverErrorPage.test.tsx — TC-004 (qa-loop iter-1 cluster
 * `auth-handover-flow-text`) coverage.
 *
 * The 2026-05-09 routing matrix asserts on `document.body.innerText`
 * (NOT URL or HTTP status). For `/auth/handover-error?reason=missing_token`
 * the rendered body MUST contain BOTH "Handover incomplete" and the
 * literal word "missing". The previous copy used "did not include" and
 * silently failed the body-text assertion, even though the BE 302 +
 * SPA route both behaved correctly.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { HandoverErrorPage } from './HandoverErrorPage'

afterEach(() => cleanup())

describe('HandoverErrorPage — body-text contract (TC-004)', () => {
  it('renders "Handover incomplete" and the literal "missing" token for ?reason=missing_token', () => {
    render(<HandoverErrorPage search="?reason=missing_token" />)
    const page = screen.getByTestId('handover-error-page')
    // Both tokens are matrix-asserted on document.body.innerText.
    expect(page.textContent).toContain('Handover incomplete')
    expect(page.textContent).toContain('missing')
  })

  it('renders the expired-link copy with the literal "expired" token', () => {
    render(<HandoverErrorPage search="?reason=expired" />)
    const page = screen.getByTestId('handover-error-page')
    expect(page.textContent).toContain('Handover incomplete')
    expect(page.textContent).toContain('expired')
  })

  it('renders the single-use copy with the literal "already used" token for ?reason=replayed', () => {
    render(<HandoverErrorPage search="?reason=replayed" />)
    const page = screen.getByTestId('handover-error-page')
    expect(page.textContent).toContain('Handover incomplete')
    expect(page.textContent).toContain('already been used')
  })

  it('falls back to a generic copy when ?reason is unrecognised', () => {
    render(<HandoverErrorPage search="?reason=mystery" />)
    const page = screen.getByTestId('handover-error-page')
    expect(page.textContent).toContain('Handover incomplete')
    expect(page.textContent).toContain('We could not complete the handover')
  })

  it('still renders the heading + Continue link with NO query string', () => {
    render(<HandoverErrorPage search="" />)
    const page = screen.getByTestId('handover-error-page')
    expect(page.textContent).toContain('Handover incomplete')
    expect(page.textContent).toContain('Continue to console')
  })

  it('renders a "Try again" link pointing back to /login (TC-005, iter-6)', () => {
    // qa-loop iter-6 cluster `auth-handover-edge-cases`: the routing
    // matrix asserts the literal token "Try again" appears in
    // document.body.innerText so the operator has an obvious recovery
    // path back to /login when the handover token was bad.
    render(<HandoverErrorPage search="?reason=expired" />)
    const tryAgain = screen.getByTestId('handover-error-try-again') as HTMLAnchorElement
    expect(tryAgain.textContent).toContain('Try again')
    expect(tryAgain.getAttribute('href')).toBe('/login')
  })
})
