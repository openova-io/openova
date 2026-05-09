/**
 * LoginPage.test.tsx — coverage for the URL-driven error banner and
 * deep-link `next` propagation (issue #1089 cluster).
 *
 * The matrix asserts the operator sees a literal banner copy whenever
 * they land on /login with a known error code:
 *   - ?error=pin-expired       → "code expired"
 *   - ?error=attempts-exceeded → "wrong attempts"
 *   - ?error=flow_changed      → "flow has changed"
 *
 * The banner must be visible REGARDLESS of input state (the form
 * starts in 'idle' on a fresh mount). Previously the banner was wired
 * into the Input's inline error which only renders when state ===
 * 'error', so the URL-driven copy never surfaced (TC-R-033, TC-R-061).
 *
 * Also asserts that the `next` search param is preserved end-to-end so
 * deep-linked entries (#1089) round-trip through PIN verify back to the
 * original page.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

const navigateSpy = vi.fn<(opts: unknown) => void>()
const searchState: { current: Record<string, string | undefined> } = { current: {} }

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = (await importOriginal()) as object
  return {
    ...actual,
    useNavigate: () => navigateSpy,
    useSearch: () => searchState.current,
  }
})

vi.mock('@/app/layouts/AuthLayout', () => ({
  AuthShell: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}))

import { LoginPage } from './LoginPage'

beforeEach(() => {
  navigateSpy.mockClear()
  searchState.current = {}
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('LoginPage — URL-driven error banner', () => {
  it('renders the "code expired" banner on ?error=pin-expired (#1089 / TC-R-061)', () => {
    searchState.current = { error: 'pin-expired' }
    render(<LoginPage />)
    const banner = screen.getByTestId('login-error-banner')
    expect(banner.textContent).toMatch(/code expired/i)
  })

  it('renders the "wrong attempts" banner on ?error=attempts-exceeded (TC-R-060)', () => {
    searchState.current = { error: 'attempts-exceeded' }
    render(<LoginPage />)
    const banner = screen.getByTestId('login-error-banner')
    expect(banner.textContent).toMatch(/wrong attempts/i)
  })

  it('renders the "flow has changed" banner on ?error=flow_changed (TC-R-033)', () => {
    searchState.current = { error: 'flow_changed' }
    render(<LoginPage />)
    const banner = screen.getByTestId('login-error-banner')
    expect(banner.textContent).toMatch(/flow has changed/i)
  })

  it('does not render the banner when ?error is absent', () => {
    render(<LoginPage />)
    expect(screen.queryByTestId('login-error-banner')).toBeNull()
  })
})

describe('LoginPage — `next` redirect hint (TC-004 / qa-loop iter-6)', () => {
  it('renders a body-text hint with the post-sign-in destination when a safe ?next= is present', () => {
    searchState.current = { next: '/dashboard' }
    render(<LoginPage />)
    const hint = screen.getByTestId('login-next-hint')
    // TC-004 (iter-6): the rendered hint MUST contain the destination
    // path (/dashboard) AND must NOT contain the literal words
    // "login" or "verify" — the routing matrix asserts both
    // must_contain and must_not_contain on document.body.innerText.
    expect(hint.textContent).toContain('/dashboard')
    expect(hint.textContent?.toLowerCase()).not.toContain('login')
    expect(hint.textContent?.toLowerCase()).not.toContain('verify')
  })

  it('omits the hint entirely when ?next is absent (no decorative noise on direct sign-in)', () => {
    render(<LoginPage />)
    expect(screen.queryByTestId('login-next-hint')).toBeNull()
  })

  it('omits the hint when ?next= would open-redirect off-origin (TC-010)', () => {
    // Even if a malicious deep-link survived the route-level
    // validateSearch (e.g. a future caller bypasses it), the
    // component-level sanitizeNextParam guard MUST suppress the
    // hint so the attacker-controlled hostname never paints.
    searchState.current = { next: 'https://evil.example.com/phish' }
    render(<LoginPage />)
    expect(screen.queryByTestId('login-next-hint')).toBeNull()
  })
})

describe('LoginPage — deep-link `next` propagation (#1089)', () => {
  it('forwards a deep-linked `next` param into /login/verify after PIN issue', async () => {
    searchState.current = { next: '/jobs/timeline' }
    const fetchSpy = vi.spyOn(global, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ ok: true, requestId: 'req-abc' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    render(<LoginPage />)

    const email = screen.getByTestId('login-email') as HTMLInputElement
    fireEvent.change(email, { target: { value: 'sarah@omantel.om' } })

    // The form's onSubmit handler does the work; clicking the submit
    // button issues a 'submit' event on the surrounding <form>.
    const form = email.closest('form')!
    fireEvent.submit(form)

    await waitFor(() => {
      expect(navigateSpy).toHaveBeenCalled()
    })

    const navArg = navigateSpy.mock.calls[0]![0] as { to: string; search: Record<string, string> }
    expect(navArg.to).toBe('/login/verify')
    expect(navArg.search.email).toBe('sarah@omantel.om')
    expect(navArg.search.requestId).toBe('req-abc')
    expect(navArg.search.next).toBe('/jobs/timeline')

    expect(fetchSpy).toHaveBeenCalled()
  })
})
