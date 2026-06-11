/**
 * VerifyPinPage.test.tsx — coverage for the post-PIN-verify redirect
 * destination, especially the Sovereign-mode default (BUG-015 / D23).
 *
 * On chroot Sovereign Console (DETECTED_MODE.mode === 'sovereign'),
 * a PIN-verify that returns {ok:true} with no `next=` param MUST land
 * the operator on `/dashboard` (the per-Sovereign landing page), NOT
 * on `/wizard` (the mothership new-prov wizard). Sending an operator
 * who just completed handover to the wizard re-prompts for org details
 * on an already-converged Sovereign — directly contradicting D0.
 *
 * Caught live on t129.omani.works (2026-05-16, BUG-015).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, cleanup, fireEvent, waitFor } from '@testing-library/react'

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

// Mode mock — overridden per test via `mockMode`. The module-level
// `DETECTED_MODE` export is read via a getter so each test sees the
// current value without needing module-cache shenanigans.
let mockMode: 'sovereign' | 'catalyst-zero' = 'sovereign'
vi.mock('@/shared/lib/detectMode', () => ({
  get DETECTED_MODE() {
    return { mode: mockMode, sovereignFQDN: 't129.omani.works' }
  },
}))

// PinInput6 is heavy + has hooks we don't need for this test — replace
// with a minimal seam that exposes onComplete via a hidden button so
// the test can fire it deterministically.
vi.mock('@/components/PinInput6', () => ({
  PinInput6: (props: {
    onChange?: (v: string) => void
    onComplete?: (v: string) => void
    disabled?: boolean
    testId?: string
  }) => (
    <button
      type="button"
      data-testid="pin-complete-stub"
      onClick={() => {
        props.onChange?.('123456')
        props.onComplete?.('123456')
      }}
    >
      stub-complete
    </button>
  ),
}))

import { VerifyPinPage } from './VerifyPinPage'

let replaceSpy: ReturnType<typeof vi.fn>

beforeEach(() => {
  navigateSpy.mockClear()
  searchState.current = { email: 'emrah.baysal@openova.io', requestId: 'req-1' }
  replaceSpy = vi.fn()
  Object.defineProperty(window, 'location', {
    configurable: true,
    writable: true,
    value: {
      ...window.location,
      host: 'console.t42.omani.works',
      hostname: 'console.t42.omani.works',
      pathname: '/',
      href: 'http://test/login/verify',
      replace: replaceSpy,
    },
  })
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('VerifyPinPage — post-PIN-verify default landing (BUG-015 / D23)', () => {
  it('redirects to /dashboard on Sovereign mode when no next= is given', async () => {
    mockMode = 'sovereign'
    const fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    })
    vi.stubGlobal('fetch', fetchSpy)

    const { getByTestId } = render(<VerifyPinPage />)
    fireEvent.click(getByTestId('pin-complete-stub'))

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalledTimes(1)
    })
    expect(replaceSpy).toHaveBeenCalledWith('/dashboard')
  })

  it('redirects to /wizard on Catalyst-Zero mode when no next= is given', async () => {
    mockMode = 'catalyst-zero'
    const fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    })
    vi.stubGlobal('fetch', fetchSpy)

    const { getByTestId } = render(<VerifyPinPage />)
    fireEvent.click(getByTestId('pin-complete-stub'))

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalledTimes(1)
    })
    expect(replaceSpy).toHaveBeenCalledWith('/wizard')
  })

  it('honours the explicit next= param over the mode-default (Sovereign)', async () => {
    mockMode = 'sovereign'
    searchState.current = {
      email: 'emrah.baysal@openova.io',
      requestId: 'req-1',
      next: '/apps',
    }
    const fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    })
    vi.stubGlobal('fetch', fetchSpy)

    const { getByTestId } = render(<VerifyPinPage />)
    fireEvent.click(getByTestId('pin-complete-stub'))

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalledTimes(1)
    })
    expect(replaceSpy).toHaveBeenCalledWith('/apps')
  })

  it('navigates verbatim to a same-Sovereign absolute OAuth next (#3271 walk)', async () => {
    // KC's identity-broker bounce after PIN-verify carries the OAuth
    // continuation as an absolute cross-host URL on api.<fqdn>. The page
    // must hard-navigate there verbatim — NOT drop the user on
    // /dashboard (the original #3271 bug) and NOT mangle the URL with
    // the relative-path basepath rewrite.
    mockMode = 'sovereign'
    const oauthNext =
      'https://api.t42.omani.works/oidc/auth?client_id=catalyst-pin' +
      '&redirect_uri=https://auth.t42.omani.works/realms/sovereign/broker/catalyst-pin/endpoint' +
      '&state=xyz&response_type=code'
    searchState.current = {
      email: 'emrah.baysal@openova.io',
      requestId: 'req-1',
      next: oauthNext,
    }
    const fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    })
    vi.stubGlobal('fetch', fetchSpy)

    const { getByTestId } = render(<VerifyPinPage />)
    fireEvent.click(getByTestId('pin-complete-stub'))

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalledTimes(1)
    })
    expect(replaceSpy).toHaveBeenCalledWith(oauthNext)
  })

  it('rejects an off-Sovereign absolute next and falls back to /dashboard', async () => {
    // Open-redirect defense: an attacker-supplied https://evil.com next
    // must be dropped — the operator lands on the Sovereign default.
    mockMode = 'sovereign'
    searchState.current = {
      email: 'emrah.baysal@openova.io',
      requestId: 'req-1',
      next: 'https://evil.com/phish',
    }
    const fetchSpy = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ ok: true }),
    })
    vi.stubGlobal('fetch', fetchSpy)

    const { getByTestId } = render(<VerifyPinPage />)
    fireEvent.click(getByTestId('pin-complete-stub'))

    await waitFor(() => {
      expect(replaceSpy).toHaveBeenCalledTimes(1)
    })
    expect(replaceSpy).toHaveBeenCalledWith('/dashboard')
  })
})
