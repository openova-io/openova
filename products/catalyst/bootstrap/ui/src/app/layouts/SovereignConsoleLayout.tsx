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
 *   2. If 401, fall back to the OpenOva PIN-login page (`/login`) with
 *      the original deep-link preserved as `?next=<path+search>`. We do
 *      NOT redirect to Keycloak's hosted login — operators sign in via
 *      the OpenOva 6-digit PIN flow (issue #688 + #1089). Keycloak
 *      remains the IdP behind catalyst-api but its hosted UI must never
 *      surface to the operator.
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
 * sending the operator to /login (issue: live regression on
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
 *          Phase-8b followup (session-cookie precedence),
 *          issue #1089 (chroot anon must redirect to /login, never KC).
 */

import { useEffect, useState } from 'react'
import { Outlet } from '@tanstack/react-router'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { API_BASE, BASE, isCatalystZeroURL } from '@/shared/config/urls'
import { currentPathRelativeToBasepath } from '@/shared/lib/basepathRelative'
import {
  loadTokens,
  isTokenExpired,
  silentRefresh,
  getRequiredActions,
} from '@/shared/lib/oidc'
import type { TokenSet } from '@/shared/lib/oidc'
import { RequiredActionsModal } from '@/components/RequiredActionsModal'

/**
 * Build the basepath-aware URL prefix so the redirect target works both
 * on chroot Sovereigns (served at domain root) and on the mothership
 * (served at /sovereign/* with a strip-prefix middleware that the
 * browser URL still reflects).
 */
function uiBase(): string {
  // G110-followup #2706: host + path check (not path-only) — on a
  // Sovereign cluster the canonical URL has no /sovereign prefix even
  // if the operator landed on a stale /sovereign bookmark.
  return isCatalystZeroURL() ? '/sovereign' : ''
}

/**
 * Redirect to the OpenOva PIN-login page, preserving the original
 * deep-link as `?next=<path+search>` so post-verify the operator lands
 * back on the page they tried to visit. Hard navigation (location.replace)
 * is used so any stale OIDC sessionStorage state is naturally discarded
 * by the next page load.
 *
 * Per #1089: NEVER redirect to Keycloak's hosted login UI here. The
 * IdP-hosted flow is for catalyst-api server-side use only; the
 * operator-facing surface is the OpenOva PIN page.
 */
function redirectToLogin(): void {
  if (typeof window === 'undefined') return
  const next = window.location.pathname + window.location.search
  const target = `${uiBase()}/login?next=${encodeURIComponent(next)}`
  window.location.replace(target)
}

/**
 * Shape returned by GET /api/v1/whoami when the catalyst_session cookie
 * is present and valid (HMAC-verified server-side by the session
 * middleware in catalyst-api/internal/auth).
 */
interface WhoamiClaims {
  email: string
  sub: string
  verified: boolean
  // #4110 — Org-console scope. When orgScoped is true the session is a
  // customer confined to one Organization (minted on console.<org>.<pool>);
  // the layout redirects away from any Sovereign-admin path.
  orgScoped?: boolean
  org?: string
}

/**
 * #4110 — Sovereign-admin path prefixes an Org-scoped customer session must
 * never reach. A deep-link / stale bookmark to any of these on an Org
 * console is redirected to /apps (the Org's own estate landing). The
 * catalyst-api ALSO 403s the underlying data calls, so this redirect is the
 * UX half of the hide-AND-enforce contract — never the only guard.
 *
 * Allowed for an Org session (NOT in this list): /apps, /catalog, /sandbox,
 * /users, /settings, /login, /auth/*.
 */
const SOVEREIGN_ONLY_PATH_RES: readonly RegExp[] = [
  /^\/dashboard(\/|$)/,
  /^\/cloud(\/|$)/,
  /^\/infrastructure(\/|$)/,
  /^\/jobs(\/|$)/,
  /^\/organizations(\/|$)/,
  /^\/(sre|sec)\/compliance(\/|$)/,
  /^\/compliance(\/|$)/,
  /^\/provision(\/|$)/,
  /^\/deployments(\/|$)/,
  /^\/decommission(\/|$)/,
  /^\/sovereignty(\/|$)/,
]

function pathIsSovereignOnly(p: string): boolean {
  return SOVEREIGN_ONLY_PATH_RES.some((re) => re.test(p))
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
      // Catalyst-Zero (mothership) mode: the same SovereignConsoleLayout
      // mounts at console.openova.io/sovereign/* under the basepath strip.
      // There's no Sovereign FQDN and no Keycloak — auth is the mothership
      // PIN-cookie flow (/login → /login/verify → catalyst_session).
      //
      // On a 200 from /whoami, render the console. On 401, hard-redirect
      // to /sovereign/login?next=<currentPath> so VerifyPinPage routes
      // back to the deep-link after the 6-digit code lands. The OIDC
      // fallback below is sovereign-cluster-only and must NOT fire here
      // (no Keycloak issuer is configured for the mothership host).
      //
      // Issue #1090 (cluster B): /sovereign/{dashboard,jobs/timeline,
      // cloud,users,settings,notifications,apps} previously hung on the
      // "Authenticating…" spinner forever because the early-return on
      // !sovereignFQDN never navigated.
      if (!sovereignFQDN) {
        const cookieClaims = await probeSessionCookie()
        if (cookieClaims) {
          // Mark the rootRoute auth gate (#1090 cluster A2) as satisfied.
          try { sessionStorage.setItem('catalyst:authed', '1') } catch { /* private browsing */ }
          setAuthState({ status: 'cookie-authenticated', claims: cookieClaims })
          return
        }
        if (typeof window !== 'undefined') {
          // `next` is the POST-basepath route path so VerifyPinPage's
          // navigate({ to: next }) resolves cleanly through the router
          // (basepath is added back automatically). The hard-redirect
          // target itself uses BASE so the browser lands at the
          // /sovereign/login URL — only the `next` value is stripped.
          const here = currentPathRelativeToBasepath()
          const target = `${BASE}login?next=${encodeURIComponent(here)}`
          window.location.replace(target)
        }
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
        try { sessionStorage.setItem('catalyst:authed', '1') } catch { /* private browsing */ }
        setAuthState({ status: 'cookie-authenticated', claims: cookieClaims })
        return
      }

      // No session cookie — try existing OIDC tokens (set by the legacy
      // PKCE flow on otech<=46 builds before #688), refreshing silently
      // if expired. If there's no usable token AND silentRefresh fails,
      // bounce to the PIN-login page with deep-link preservation
      // (#1089). The previous behaviour here was to call initiateLogin()
      // which redirected to Keycloak's hosted login — that surface is
      // forbidden for operator-facing flows.
      const existing = loadTokens()
      if (!existing) {
        // #5460 (hw290 row-29): this is the PIN-cookie EXPIRY path — the
        // cookie probe 401'd and there are no OIDC tokens. Two duties the
        // old bail-out skipped:
        //  1. Revoke the stale `catalyst:authed` marker. It is what let the
        //     rootBeforeLoad gate short-circuit past its own whoami probe
        //     and silent-SSO leg, landing here instead — and while it
        //     survives, every marker-consuming surface renders
        //     authenticated chrome against a dead session.
        //  2. Attempt the SAME loop-guarded silent prompt=none round-trip
        //     the gate would have run: a live Keycloak SSO session re-mints
        //     the console session with no PIN form (the row-29 contract).
        //     The one-shot guard in attemptSilentSovereignSSO makes the
        //     genuinely-logged-out case land on /login exactly once.
        //
        // Duty 2 is scoped to the EXPIRY case the commit title names — a
        // stale `catalyst:authed` marker is the evidence that this tab
        // HELD a live console session, so re-minting it silently is
        // meaningful. A visitor with no marker never authenticated here:
        // rootBeforeLoad's marker fast-path (`if (hasCatalystSession())
        // return`) did NOT fire for them, so the gate already ran its own
        // whoami probe and its own silent leg. Repeating it from the
        // layout is not a second chance, it is a duplicate — and it broke
        // the #1089 contract, which is that an anonymous chroot visitor
        // lands on the OpenOva PIN page and the Keycloak surface is never
        // touched. It also burned the one-shot guard on the way, so the
        // FIRST anonymous page load left the console on the
        // "Authenticating…" spinner and only the second reached /login.
        let hadSessionMarker = false
        try {
          hadSessionMarker = sessionStorage.getItem('catalyst:authed') === '1'
          sessionStorage.removeItem('catalyst:authed')
        } catch { /* private browsing */ }
        if (hadSessionMarker) {
          const { attemptSilentSovereignSSO, SILENT_SSO_NAV_GRACE_MS, SILENT_SSO_GUARD_KEY } =
            await import('../router')
          if (await attemptSilentSovereignSSO()) {
            // Navigation handed to Keycloak (window.location.replace inside
            // initiateLogin) — keep the spinner while the document unloads.
            // Safety net, mirroring rootBeforeLoad: if the document
            // navigation never commits (auth host unreachable), the console
            // must not hang on the spinner forever. Re-arm the one-shot
            // guard and fall through to the explicit /login.
            await new Promise((resolve) => setTimeout(resolve, SILENT_SSO_NAV_GRACE_MS))
            try { sessionStorage.removeItem(SILENT_SSO_GUARD_KEY) } catch { /* private browsing */ }
          }
        }
        setAuthState({ status: 'unauthenticated' })
        redirectToLogin()
        return
      }

      if (isTokenExpired(existing)) {
        const refreshed = await silentRefresh(sovereignFQDN)
        if (!refreshed) {
          setAuthState({ status: 'unauthenticated' })
          redirectToLogin()
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
  }, [sovereignFQDN])

  // #4110 — Org-console scope redirect. When the resolved session is an
  // Org-scoped customer session and the operator has deep-linked / bookmarked
  // a Sovereign-admin path (the provisioning wizard, deployment-stream,
  // cloud view, the cross-org directory, sovereign settings groups), bounce
  // them to /apps (their own estate). The catalyst-api 403s the data behind
  // those pages too — this is the UX redirect, not the security boundary.
  useEffect(() => {
    if (authState.status !== 'cookie-authenticated') return
    if (!authState.claims.orgScoped) return
    if (typeof window === 'undefined') return
    const here = currentPathRelativeToBasepath()
    if (pathIsSovereignOnly(here)) {
      window.location.replace(`${uiBase()}/apps`)
    }
  }, [authState])

  function handleRequiredActionsComplete(tokens: TokenSet) {
    setAuthState({ status: 'authenticated', tokens, requiredActions: [] })
  }

  // Loading — blank page while we probe /whoami and (if needed)
  // redirect to /login. The `unauthenticated` branch keeps the same
  // shell while window.location.replace executes.
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
