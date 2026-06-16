import { useEffect, useState } from 'react'
import { useSearch, useRouter } from '@tanstack/react-router'
import { isCatalystZeroURL } from '@/shared/config/urls'

/**
 * AuthCallbackPage — handles the OIDC / Keycloak authorization_code callback.
 *
 * After issue #688 (6-digit PIN auth) replaced the magic-link flow on
 * Catalyst-Zero, this page only matters for Sovereign clusters that
 * still use the PKCE round-trip with their own Keycloak realm. On
 * Catalyst-Zero it's a safety-net 302 to /login since cached Keycloak
 * redirect URIs may still resolve here.
 *
 * Sovereign (console.<sov-fqdn>):
 *   Keycloak redirects to /auth/callback?code=...&state=... after the
 *   operator authenticates. The page calls handleCallback() to exchange
 *   the code for tokens via the PKCE token endpoint (client-side), then
 *   navigates to /console/dashboard.
 *
 * Catalyst-Zero (console.openova.io):
 *   The PIN flow set the catalyst_session cookie on /auth/pin/verify;
 *   this page is never visited in the normal happy path. If a stale
 *   bookmark lands here, redirect to /login with an explanatory error.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the Sovereign
 * FQDN and mode are detected at runtime, never inlined.
 */

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
  // G110-followup #2706: host + path check (not path-only).
  return isCatalystZeroURL() ? '/sovereign' : ''
}

// Detect Catalyst-Zero at module-init time (same logic as router.tsx).
const isCatalystZero =
  typeof window !== 'undefined' && window.location.hostname === 'console.openova.io'

// ── Sovereign OIDC state type ──────────────────────────────────────────────

type SovereignState =
  | { status: 'exchanging' }
  | { status: 'error'; message: string }

// ── Sovereign OIDC callback component ─────────────────────────────────────

function SovereignCallbackPage() {
  const router = useRouter()
  const [state, setState] = useState<SovereignState>({ status: 'exchanging' })

  useEffect(() => {
    async function exchange() {
      try {
        // Lazy-import oidc so Catalyst-Zero builds don't pay for unused code.
        const { handleCallback } = await import('@/shared/lib/oidc')
        const { DETECTED_MODE } = await import('@/shared/lib/detectMode')

        const sovereignFQDN = DETECTED_MODE.sovereignFQDN
        if (!sovereignFQDN) {
          setState({
            status: 'error',
            message: 'OIDC callback reached in catalyst-zero mode — unexpected.',
          })
          return
        }

        const params = new URLSearchParams(window.location.search)
        await handleCallback(sovereignFQDN, params)
        // #3374 Layer-B: clear the silent-SSO one-shot guard now that a
        // real session exists, so a LATER genuine expiry re-arms the
        // silent round-trip (router.tsx attemptSilentSovereignSSO) rather
        // than going straight to the PIN fallback.
        try {
          sessionStorage.removeItem('catalyst:silent-sso-attempted')
        } catch {
          /* private browsing may throw */
        }
        // Navigate to the console dashboard — replace so the callback
        // URL doesn't appear in browser history.
        router.navigate({ to: '/dashboard' as never, replace: true })
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

// ── Catalyst-Zero callback component (post-#688 safety net) ───────────────
//
// PIN auth (#688) doesn't use a callback URL — the verify endpoint sets
// the cookie inline. This component only exists as a safety net in case
// Keycloak still has /sovereign/auth/callback in its allowed redirect
// list and sends the user here. In that case redirect to /login with an
// error code so the operator sees a clean screen.
function CatalystZeroCallbackPage() {
  const search = useSearch({ strict: false }) as Record<string, string>

  useEffect(() => {
    const error = search['error'] ?? 'flow_changed'
    window.location.replace(uiBase() + '/login?error=' + encodeURIComponent(error))
  }, [search])

  return null
}

// ── Mode-dispatching export ────────────────────────────────────────────────

export function AuthCallbackPage() {
  if (isCatalystZero) {
    return <CatalystZeroCallbackPage />
  }
  return <SovereignCallbackPage />
}
