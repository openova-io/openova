import { useEffect } from 'react'
import { useSearch } from '@tanstack/react-router'
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

/**
 * AuthCallbackPage — handles the Keycloak PKCE callback for Catalyst-Zero.
 *
 * Keycloak redirects to console.openova.io/sovereign/auth/callback?code=...
 * after the operator clicks the magic-link email. This page receives the
 * `code` query param, builds the server-side callback URL
 * (/api/v1/auth/callback?code=...) and hard-navigates there so the
 * server can:
 *   1. Read the PKCE verifier cookie (same-origin, carried automatically)
 *   2. Exchange the code for tokens
 *   3. Issue the HMAC-signed session cookie
 *   4. Redirect the browser to /sovereign/wizard (or /wizard on Sovereign clusters)
 *
 * A hard navigation (window.location.replace) is required so the
 * browser's cookie jar picks up the Set-Cookie header from the server's
 * 302 response — a client-side fetch or TanStack redirect would not work
 * here because cookies set on redirect responses are honoured by the
 * browser's cookie engine, not by XHR/fetch.
 */
export function AuthCallbackPage() {
  const search = useSearch({ strict: false }) as Record<string, string>

  useEffect(() => {
    const code = search['code']
    const state = search['state']
    const error = search['error']

    if (error) {
      // Keycloak denied or the magic-link expired — redirect to login with
      // an error indicator so the UI can surface the right copy.
      window.location.replace(uiBase() + '/login?error=' + encodeURIComponent(error))
      return
    }

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
    callbackURL.searchParams.set('code', code)
    if (state) callbackURL.searchParams.set('state', state)

    // Hard navigation — must NOT use TanStack router redirect here.
    window.location.replace(callbackURL.toString())
  }, [search])

  // Render nothing — we navigate away immediately.
  return null
}
