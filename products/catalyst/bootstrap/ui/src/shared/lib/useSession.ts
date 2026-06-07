/**
 * useSession — small React Query hook around GET /api/v1/whoami (issue #689).
 *
 * The wizard surface is anonymous-first; the ProfileMenu in the header
 * needs to know whether the visitor has a session cookie so it can
 * either render a "Sign in" button (anonymous) or the operator's
 * email-initial avatar with a sign-out menu.
 *
 * Returns:
 *   - signedIn       — boolean, true when /whoami returned 200
 *   - email          — string | null, the authenticated email (200 path) or null
 *   - sub            — string | null, the Keycloak user id
 *   - loading        — boolean, true on first fetch
 *   - refetch        — () => void, force a re-poll (post-PIN-verify)
 *   - signOut        — async () => void, DELETE /auth/session + invalidate cache
 *
 * The query key is shared so any component reading the session sees the
 * same cached value.  Sovereign mode (console.<sov-fqdn>) is handled
 * separately via OIDC and doesn't share this hook — the catalyst-zero
 * detection is intentionally NOT in the hook because the hook is also
 * used by ProfileMenu in non-Catalyst-Zero contexts when present.
 */

import { useCallback } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { API_BASE, path as basePath } from '@/shared/config/urls'

export interface SessionState {
  signedIn: boolean
  email: string | null
  sub: string | null
  loading: boolean
  refetch: () => void
  signOut: () => Promise<void>
}

interface WhoamiResponse {
  email: string
  sub: string
  verified: boolean
}

const SESSION_QUERY_KEY = ['catalyst', 'session', 'whoami'] as const

async function fetchWhoami(): Promise<WhoamiResponse | null> {
  const res = await fetch(`${API_BASE}/v1/whoami`, {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (res.status === 401) return null
  if (!res.ok) {
    // Treat 5xx as "session unknown — render the anonymous chrome but
    // don't lock anyone out". Throwing here would put the query in an
    // error state which the ProfileMenu would render as "signed out",
    // mirroring the desired behaviour.
    throw new Error(`whoami: HTTP ${res.status}`)
  }
  return (await res.json()) as WhoamiResponse
}

export function useSession(): SessionState {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: SESSION_QUERY_KEY,
    queryFn: fetchWhoami,
    // Re-fetch on window focus — if the operator signs in via the PIN
    // modal in another tab, the wizard tab picks it up on focus.
    refetchOnWindowFocus: true,
    // Don't retry hard on 5xx — one attempt is enough; the user can
    // refresh manually.
    retry: 1,
    staleTime: 30_000,
    // Treat null (401 from API) as a fully-resolved value, not an error.
    // We only enter the error branch on 5xx / network failures.
    networkMode: 'always',
  })

  const refetch = useCallback(() => {
    void qc.invalidateQueries({ queryKey: SESSION_QUERY_KEY })
  }, [qc])

  const signOut = useCallback(async () => {
    // Two-hop sign-out (caught live 2026-05-04: clearing only local
    // cookies left the operator silently re-authenticated as the same
    // identity on next page load because the Keycloak SSO session was
    // still alive and the OIDC PKCE auth-guard re-fetched a session
    // for it).
    //
    //   Hop 1 — DELETE /api/v1/auth/session: server clears the
    //           catalyst_session + catalyst_refresh cookies (with the
    //           EXACT same Domain/Path/SameSite they were set with —
    //           a Domain mismatch silently no-ops the clear) and
    //           returns the Keycloak end_session_endpoint URL.
    //
    //   Hop 2 — window.location.href = keycloakLogoutURL: hard-
    //           navigate to KC so it drops its SSO session, then KC
    //           redirects back to /login. Hard navigation (not the
    //           SPA router) is required because we need the browser
    //           to send the freshly-cleared cookie state on the next
    //           page load and to bypass any in-flight TanStack Query
    //           cache.
    let keycloakLogoutURL = ''
    try {
      const res = await fetch(`${API_BASE}/v1/auth/session`, {
        method: 'DELETE',
        credentials: 'include',
      })
      if (res.ok) {
        const body = (await res.json().catch(() => ({}))) as {
          keycloakLogoutURL?: string
        }
        keycloakLogoutURL = body.keycloakLogoutURL ?? ''
      }
    } catch {
      // Network errors don't block client-side sign-out — fall through
      // to the local clear + hard navigate to /login.
    }
    qc.setQueryData(SESSION_QUERY_KEY, null)
    qc.clear()
    if (keycloakLogoutURL) {
      window.location.href = keycloakLogoutURL
    } else {
      // Issue #3086: bare `/login` dropped the `/sovereign` basepath on
      // Catalyst-Zero (nginx serves the SPA only under `/sovereign/*`) →
      // 404 instead of the login screen. basePath('login') resolves to
      // `/sovereign/login` on Catalyst-Zero and `/login` on Sovereign
      // clusters — same basepath-aware target the route guard uses.
      window.location.href = basePath('login')
    }
  }, [qc])

  const data = q.data ?? null
  return {
    signedIn: data !== null,
    email: data?.email ?? null,
    sub: data?.sub ?? null,
    loading: q.isLoading,
    refetch,
    signOut,
  }
}
