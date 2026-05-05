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

import { useEffect, useState, type ReactNode } from 'react'
import { Outlet, useRouter } from '@tanstack/react-router'
import { LogOut, Settings } from 'lucide-react'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE } from '@/shared/config/urls'
import {
  loadTokens,
  isTokenExpired,
  silentRefresh,
  initiateLogin,
  initiateLogout,
  parseJWTClaims,
  getRequiredActions,
} from '@/shared/lib/oidc'
import type { TokenSet } from '@/shared/lib/oidc'
import { RequiredActionsModal } from '@/components/RequiredActionsModal'
import { SovereignSidebar } from '@/pages/sovereign/SovereignSidebar'
import { ThemeToggle } from '@/components/ThemeToggle'
import { NotificationBell } from '@/shared/ui/notifications'
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuLabel,
} from '@/shared/ui/dropdown-menu'
import { Avatar, AvatarFallback } from '@/shared/ui/avatar'

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

interface SovereignConsoleLayoutProps {
  pageTitle?: string
  headerSlotRight?: ReactNode
}

export function SovereignConsoleLayout({
  pageTitle,
  headerSlotRight,
}: SovereignConsoleLayoutProps) {
  const [authState, setAuthState] = useState<AuthState>({ status: 'loading' })
  const sovereignFQDN = DETECTED_MODE.sovereignFQDN ?? ''
  const router = useRouter()

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

  async function handleLogout() {
    // Cookie-authenticated session: clear the server-side cookie via
    // the DELETE /api/v1/auth/session endpoint, then hard-reload to '/'
    // so the layout re-runs initAuth from a clean state. The OIDC
    // logout endpoint isn't relevant in this branch — there is no
    // Keycloak session to terminate.
    if (authState.status === 'cookie-authenticated') {
      try {
        await fetch(`${API_BASE}/v1/auth/session`, {
          method: 'DELETE',
          credentials: 'include',
        })
      } catch {
        // Network failures don't block client-side sign-out; the cookie
        // will be cleared on the next request that gets a 401 anyway.
      }
      window.location.replace('/')
      return
    }
    if (sovereignFQDN) initiateLogout(sovereignFQDN)
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

  // Two authenticated branches feed the same shell: the cookie path
  // (post-handover, no OIDC tokens) and the OIDC path (returning user
  // whose KC session is fresh). Normalise both to a (userName,
  // requiredActions) pair so the chrome below stays branch-agnostic.
  let userName: string
  let requiredActions: string[]
  if (authState.status === 'cookie-authenticated') {
    userName = authState.claims.email || 'User'
    // No id_token in the cookie path — required actions are gated by
    // the server-side middleware before the cookie is ever issued, so
    // there is nothing for the client to enforce here.
    requiredActions = []
  } else {
    const { tokens, requiredActions: ra } = authState
    const claims = parseJWTClaims(tokens.idToken)
    userName =
      (claims.name as string | undefined) ??
      (claims.preferred_username as string | undefined) ??
      (claims.email as string | undefined) ??
      'User'
    requiredActions = ra
  }
  const userInitials = userName
    .split(/\s+/)
    .map((w) => w[0]?.toUpperCase() ?? '')
    .slice(0, 2)
    .join('')

  return (
    <div
      className="flex min-h-screen bg-[var(--color-bg)] text-[var(--color-text)]"
      data-testid="sov-console-shell"
    >
      {/* Required-actions blocking modal */}
      {requiredActions.length > 0 ? (
        <RequiredActionsModal
          sovereignFQDN={sovereignFQDN}
          requiredActions={requiredActions}
          onComplete={handleRequiredActionsComplete}
        />
      ) : null}

      {/* Left sidebar */}
      <SovereignSidebar sovereignFQDN={sovereignFQDN} />

      {/* Main content area */}
      <div className="ml-56 flex flex-1 flex-col">
        {/* Top header band */}
        <header
          data-testid="sov-console-header"
          className="sticky top-0 z-40 flex h-14 items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-2)]/90 px-4 backdrop-blur"
        >
          <div className="flex min-w-0 flex-1 items-center gap-3">
            {pageTitle ? (
              <h1 className="truncate text-base font-semibold text-[var(--color-text-strong)]">
                {pageTitle}
              </h1>
            ) : null}
          </div>

          <div className="flex shrink-0 items-center gap-3">
            {headerSlotRight}
            <NotificationBell />
            <ThemeToggle />

            {/* User menu */}
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <button
                  className="flex items-center gap-2 rounded-lg px-2 py-1.5 text-sm transition-colors hover:bg-[var(--color-surface-hover)]"
                  data-testid="sov-user-menu-trigger"
                  aria-label="User menu"
                >
                  <Avatar className="h-7 w-7">
                    <AvatarFallback className="text-xs">{userInitials}</AvatarFallback>
                  </Avatar>
                  <span className="hidden max-w-[120px] truncate text-xs font-medium text-[var(--color-text)] sm:block">
                    {userName}
                  </span>
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent side="bottom" align="end" className="w-48">
                <DropdownMenuLabel className="text-xs text-[var(--color-text-dim)]">
                  {sovereignFQDN}
                </DropdownMenuLabel>
                <DropdownMenuItem
                  onClick={() => router.navigate({ to: '/settings' as never })}
                >
                  <Settings className="h-3.5 w-3.5" />
                  Settings
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  className="text-[var(--color-error)] focus:text-[var(--color-error)]"
                  onClick={handleLogout}
                  data-testid="sov-logout-btn"
                >
                  <LogOut className="h-3.5 w-3.5" />
                  Sign out
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>

        {/* Page content */}
        <main className="flex-1 p-8">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
