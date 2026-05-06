/**
 * SovereignConsoleLayout — root layout for Sovereign-mode console routes.
 *
 * This layout is mounted when catalyst-ui detects it is running on a
 * Sovereign's `console.<sov-fqdn>` hostname (mode = 'sovereign'). It:
 *
 *   1. Calls GET /api/v1/whoami (with credentials:'include') to detect a
 *      `catalyst_session` cookie minted by the server-side /auth/handover
 *      handler. If 200, the operator is authenticated via the cookie —
 *      render the console without ever touching Keycloak.
 *   2. If 401, fall back to the legacy OIDC flow:
 *      - read tokens from sessionStorage,
 *      - if missing/expired, attempt silentRefresh,
 *      - if that fails, initiateLogin (PKCE redirect to Keycloak).
 *   3. After login, if the id_token contains required actions
 *      (UPDATE_PASSWORD / configure-passkey / CONFIGURE_TOTP), shows the
 *      RequiredActionsModal before rendering the console.
 *   4. Once authenticated + no required actions, renders the
 *      SovereignSidebar + main <Outlet />.
 *
 * Why /whoami first: the wizard's handover button lands the operator at
 *   GET /auth/handover?token=<jwt>
 * which the Sovereign-side catalyst-api validates, mints a
 * `catalyst_session` HttpOnly Secure SameSite=Lax cookie for, and 302s to
 * /console/dashboard. The browser arrives with a fresh cookie but no
 * sessionStorage tokens — the layout MUST recognise that cookie before
 * bouncing to Keycloak's hosted login (issue: live regression on
 * otech49 + otech52 today, operator landed on a username/password
 * screen instead of the dashboard).
 *
 * The SovereignSidebar uses `/console/*` routes (no deploymentId param) —
 * in Sovereign mode the sovereign context is implicit from the hostname.
 *
 * Layout contract:
 *   - Left rail: SovereignSidebar (w-56 fixed, same shape as Sidebar.tsx)
 *   - Main content: ml-56 flex-1
 *   - Top header band: 56px sticky with page title + NotificationBell +
 *     ThemeToggle + UserMenu (shows user name from id_token claims)
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the Keycloak
 * issuer URL, account URL, and Sovereign FQDN are always derived from
 * runtime state, never inlined.
 *
 * Related: GitHub issue #607 (initial OIDC gate),
 *          Phase-8b followup (session-cookie precedence).
 */

import { useEffect, useState } from 'react'
import { Outlet } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE } from '@/shared/config/urls'
import {
  loadTokens,
  isTokenExpired,
  silentRefresh,
  initiateLogin,
  getRequiredActions,
} from '@/shared/lib/oidc'
import type { TokenSet } from '@/shared/lib/oidc'
import { RequiredActionsModal } from '@/components/RequiredActionsModal'

/**
 * Shape returned by GET /api/v1/whoami when the catalyst_session cookie
 * is present and valid (HMAC-verified server-side by the session
 * middleware in catalyst-api/internal/auth).
 */
interface WhoamiClaims {
  email: string
  sub: string
  verified: boolean
}

/**
 * Cookie-authenticated mode — no OIDC tokens involved. The user arrived
 * via /auth/handover and the server-side middleware will continue to
 * gate /api/v1/* on every subsequent request via the same cookie.
 */
type AuthState =
  | { status: 'loading' }
  | { status: 'unauthenticated' }
  | { status: 'authenticated'; tokens: TokenSet; requiredActions: string[] }
  | { status: 'cookie-authenticated'; claims: WhoamiClaims }

/**
 * Probe GET /api/v1/whoami to detect a server-side catalyst_session
 * cookie.
 *
 * Returns the claims on 200, null on 401 (no/invalid cookie), and null on
 * any other error so the caller falls through to the OIDC path —
 * 5xx/network failures must NOT redirect the operator to Keycloak with a
 * fresh PKCE flow when the cookie might still be valid; the next
 * request will retry. Surfacing the failure as "no cookie" is the safe
 * default because the OIDC fallback is itself idempotent.
 */
async function probeSessionCookie(): Promise<WhoamiClaims | null> {
  try {
    const res = await fetch(`${API_BASE}/v1/whoami`, {
      method: 'GET',
      credentials: 'include',
      headers: { Accept: 'application/json' },
    })
    if (res.status !== 200) return null
    return (await res.json()) as WhoamiClaims
  } catch {
    return null
  }
}

export function SovereignConsoleLayout() {
  const [authState, setAuthState] = useState<AuthState>({ status: 'loading' })
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''

  useEffect(() => {
    async function initAuth() {
      if (!sovereignFQDN) {
        // Should not happen in sovereign mode, but guard defensively.
        setAuthState({ status: 'unauthenticated' })
        return
      }

      // Phase-8b followup: the wizard handover lands the operator at
      // /auth/handover?token=<jwt>, which mints a catalyst_session
      // cookie and 302s here. The cookie predates any OIDC PKCE flow,
      // so we MUST probe /whoami before considering Keycloak. If the
      // cookie is valid, render the console — never redirect to
      // Keycloak's hosted login when the operator already has a
      // server-issued session.
      const cookieClaims = await probeSessionCookie()
      if (cookieClaims) {
        setAuthState({ status: 'cookie-authenticated', claims: cookieClaims })
        return
      }

      // Legacy OIDC fallback — for operators who arrived via direct
      // navigation to /console/* without going through the wizard
      // handover (e.g. a returning user whose cookie expired). The PKCE
      // flow remains the canonical re-auth path.
      const existing = loadTokens()
      if (!existing) {
        setAuthState({ status: 'unauthenticated' })
        await initiateLogin(sovereignFQDN)
        return
      }

      if (isTokenExpired(existing)) {
        const refreshed = await silentRefresh(sovereignFQDN)
        if (!refreshed) {
          setAuthState({ status: 'unauthenticated' })
          await initiateLogin(sovereignFQDN)
          return
        }
        const actions = getRequiredActions(refreshed.idToken)
        setAuthState({ status: 'authenticated', tokens: refreshed, requiredActions: actions })
        return
      }

      const actions = getRequiredActions(existing.idToken)
      setAuthState({ status: 'authenticated', tokens: existing, requiredActions: actions })
    }

    void initAuth()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sovereignFQDN])

  function handleRequiredActionsComplete(tokens: TokenSet) {
    setAuthState({ status: 'authenticated', tokens, requiredActions: [] })
  }

  // Loading — blank page while we check sessionStorage + maybe redirect to KC
  if (authState.status === 'loading' || authState.status === 'unauthenticated') {
    return (
      <div
        className="flex h-screen items-center justify-center bg-[var(--color-bg)] text-[var(--color-text-dim)]"
        data-testid="sov-auth-loading"
      >
        <div className="flex flex-col items-center gap-3">
          <svg
            className="h-8 w-8 animate-spin text-[var(--color-accent)]"
            viewBox="0 0 24 24"
            fill="none"
            aria-label="Authenticating"
          >
            <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25" />
            <path
              fill="currentColor"
              opacity="0.8"
              d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
            />
          </svg>
          <span className="text-sm">Authenticating…</span>
        </div>
      </div>
    )
  }

  // Chroot byte-identical chrome: this layout is auth-gate + the
  // required-actions modal only. The page's PortalShell brings the
  // sidebar + header (PortalShell renders SovereignSidebar on chroot
  // for the clean-root URLs). Rendering both this layout's chrome AND
  // PortalShell's chrome at once produced a visible "frame in frame" —
  // two stacked headers, two sidebars-worth of nav. Caught on
  // omantel.biz 2026-05-06.
  const requiredActions =
    authState.status === 'cookie-authenticated' ? [] : authState.requiredActions
  return (
    <>
      {requiredActions.length > 0 ? (
        <RequiredActionsModal
          sovereignFQDN={sovereignFQDN}
          requiredActions={requiredActions}
          onComplete={handleRequiredActionsComplete}
        />
      ) : null}
      <Outlet />
    </>
  )
}
