/**
 * AuthCallbackPage — handles the OIDC authorization_code redirect.
 *
 * Keycloak redirects to /auth/callback?code=...&state=...  after the
 * user authenticates. This page:
 *
 *   1. Reads `code` + `state` from the URL search params.
 *   2. Calls `handleCallback()` to exchange the code for tokens via the
 *      PKCE token endpoint.
 *   3. On success: navigates to /console/dashboard.
 *   4. On error: shows the error message so the operator can debug.
 *
 * This page is intentionally minimal — it renders for < 1s in the
 * normal flow. All token storage and validation lives in oidc.ts.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the Sovereign
 * FQDN is read from DETECTED_MODE, never inlined.
 *
 * Related: GitHub issue #607
 */

import { useEffect, useState } from 'react'
import { useRouter } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { handleCallback } from '@/shared/lib/oidc'

type PageState =
  | { status: 'exchanging' }
  | { status: 'error'; message: string }

export function AuthCallbackPage() {
  const router = useRouter()
  const [state, setState] = useState<PageState>({ status: 'exchanging' })

  useEffect(() => {
    async function exchange() {
      const sovereignFQDN = DETECTED_MODE.sovereignFQDN
      if (!sovereignFQDN) {
        setState({
          status: 'error',
          message: 'OIDC callback reached in catalyst-zero mode — unexpected.',
        })
        return
      }

      try {
        const params = new URLSearchParams(window.location.search)
        await handleCallback(sovereignFQDN, params)
        // Navigate to the console dashboard — replace so the callback
        // URL doesn't appear in browser history.
        router.navigate({ to: '/console/dashboard' as never, replace: true })
      } catch (err) {
        setState({
          status: 'error',
          message: err instanceof Error ? err.message : 'Unknown error during OIDC token exchange.',
        })
      }
    }

    void exchange()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  if (state.status === 'exchanging') {
    return (
      <div
        className="flex h-screen items-center justify-center bg-[var(--color-bg)]"
        data-testid="auth-callback-loading"
      >
        <div className="flex flex-col items-center gap-3 text-[var(--color-text-dim)]">
          <svg
            className="h-8 w-8 animate-spin text-[var(--color-accent)]"
            viewBox="0 0 24 24"
            fill="none"
            aria-label="Completing sign-in"
          >
            <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25" />
            <path
              fill="currentColor"
              opacity="0.8"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
            />
          </svg>
          <span className="text-sm">Completing sign-in…</span>
        </div>
      </div>
    )
  }

  return (
    <div
      className="flex h-screen items-center justify-center bg-[var(--color-bg)] p-8"
      data-testid="auth-callback-error"
    >
      <div className="w-full max-w-md rounded-xl border border-[var(--color-error)]/30 bg-[var(--color-error)]/10 p-8">
        <h1 className="mb-2 text-lg font-semibold text-[var(--color-error)]">Sign-in failed</h1>
        <p className="text-sm text-[var(--color-text-dim)]">{state.message}</p>
        <a
          href="/"
          className="mt-6 inline-block text-sm text-[var(--color-accent)] hover:underline"
        >
          Return to home
        </a>
      </div>
    </div>
  )
}
