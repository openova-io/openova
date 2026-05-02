/**
 * SovereignConsoleLayout — root layout for Sovereign-mode console routes.
 *
 * This layout is mounted when catalyst-ui detects it is running on a
 * Sovereign's `console.<sov-fqdn>` hostname (mode = 'sovereign'). It:
 *
 *   1. Reads the current OIDC session from sessionStorage.
 *   2. If no valid session exists, initiates the Keycloak PKCE login flow.
 *   3. After login, if the id_token contains required actions
 *      (UPDATE_PASSWORD / configure-passkey / CONFIGURE_TOTP), shows the
 *      RequiredActionsModal before rendering the console.
 *   4. Once authenticated + no required actions, renders the
 *      SovereignSidebar + main <Outlet />.
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
 * Related: GitHub issue #607
 */

import { useEffect, useState, type ReactNode } from 'react'
import { Outlet, useRouter } from '@tanstack/react-router'
import { LogOut, Settings } from 'lucide-react'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
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

type AuthState =
  | { status: 'loading' }
  | { status: 'unauthenticated' }
  | { status: 'authenticated'; tokens: TokenSet; requiredActions: string[] }

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

  function handleLogout() {
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

  const { tokens, requiredActions } = authState
  const claims = parseJWTClaims(tokens.idToken)
  const userName =
    (claims.name as string | undefined) ??
    (claims.preferred_username as string | undefined) ??
    (claims.email as string | undefined) ??
    'User'
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
                  onClick={() => router.navigate({ to: '/console/settings' as never })}
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
