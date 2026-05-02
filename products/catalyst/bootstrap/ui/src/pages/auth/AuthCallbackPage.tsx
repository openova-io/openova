/**
 * AuthCallbackPage — mode-aware OIDC / session-cookie callback handler.
 *
 * Keycloak (or the catalyst-api session gate) redirects to /auth/callback
 * after the user authenticates. This component dispatches to one of two
 * sub-handlers based on DETECTED_MODE:
 *
 *   • catalyst-zero mode  → CatalystZeroCallback
 *     The callback carries the PKCE authorization_code that the SERVER
 *     needs to exchange.  We hard-navigate the browser to the server-side
 *     endpoint so the API can set an HttpOnly session cookie and then
 *     redirect back to the wizard.
 *
 *   • sovereign mode      → SovereignCallback
 *     Standard client-side PKCE exchange via handleCallback() (oidc.ts).
 *     Tokens are stored in sessionStorage and the user is redirected to
 *     /console/dashboard inside the SPA.
 *
 * Related: GitHub issues #607 (sovereign mode), #608 (magic-link auth)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the Sovereign
 * FQDN is read from DETECTED_MODE, never inlined.
 */

import { useEffect, useState } from 'react'
import { useRouter } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { handleCallback } from '@/shared/lib/oidc'
import { API_BASE } from '@/shared/config/urls'

/**
 * Derive the path-only UI base prefix for hard-navigation fallback.
 *
 * On contabo-mkt the browser URL has a '/sovereign' prefix (Traefik
 * routes /sovereign/* → catalyst-ui but does NOT rewrite the Location
 * header on client-side navigations). Error redirects must therefore go
 * to '/sovereign/login' not '/login'.
 *
 * On Sovereign clusters the UI is served at the domain root (basepath '/')
 * so the prefix is empty.
 */
function uiBase(): string {
  if (typeof window === 'undefined') return ''
  return window.location.pathname.startsWith('/sovereign') ? '/sovereign' : ''
}

// ── Catalyst-Zero Callback ────────────────────────────────────────────────
//
// Hard-navigate to the server-side token-exchange endpoint.  The API sets
// an HttpOnly session cookie and then 302-redirects back to the wizard
// (or the originally-requested URL stored in the state param).
//
// This is a hard navigate (window.location.replace), NOT a SPA redirect,
// because the HttpOnly cookie must come from the server response headers —
// there is no client-side way to receive it.

function CatalystZeroCallback() {
  useEffect(() => {
    const search = new URLSearchParams(window.location.search)
    const error = search.get('error')

    if (error) {
      // Keycloak denied or the magic-link expired — redirect to login with
      // an error indicator so the UI can surface the right copy.
      window.location.replace(uiBase() + '/login?error=' + encodeURIComponent(error))
      return
    }

    const code = search.get('code')
    if (!code) {
      // No code and no error — unexpected. Redirect to login.
      window.location.replace(uiBase() + '/login?error=no_code')
      return
    }

    // Build the server-side callback URL. The PKCE verifier cookie was set
    // by the server when the operator submitted their email. Because this
    // is a same-origin redirect (console.openova.io → /sovereign/api/v1/...)
    // the cookie is carried automatically by the browser.
    const callbackURL = new URL(`${API_BASE}/v1/auth/callback`, window.location.href)
    search.forEach((value, key) => callbackURL.searchParams.set(key, value))

    // Hard navigation — must NOT use TanStack router redirect here.
    window.location.replace(callbackURL.toString())
  }, [])

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

// ── Sovereign Callback ────────────────────────────────────────────────────
//
// Client-side PKCE token exchange.  Keycloak redirects here with
// ?code=...&state=... after the user authenticates on the Sovereign realm.
// handleCallback() (oidc.ts) exchanges the code, stores tokens in
// sessionStorage, and this component navigates the SPA to /console/dashboard.

type SovereignPageState =
  | { status: 'exchanging' }
  | { status: 'error'; message: string }

function SovereignCallback() {
  const router = useRouter()
  const [state, setState] = useState<SovereignPageState>({ status: 'exchanging' })

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
          message:
            err instanceof Error ? err.message : 'Unknown error during OIDC token exchange.',
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
            <circle
              cx="12"
              cy="12"
              r="10"
              stroke="currentColor"
              strokeWidth="3"
              opacity="0.25"
            />
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

// ── Mode-aware dispatcher ─────────────────────────────────────────────────

export function AuthCallbackPage() {
  if (DETECTED_MODE.mode === 'sovereign') {
    return <SovereignCallback />
  }
  return <CatalystZeroCallback />
}
