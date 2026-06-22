/**
 * useConsoleScope — resolves whether the current Sovereign Console session
 * is an Org-scoped customer session vs a Sovereign-admin/operator session
 * (#4110).
 *
 * THE SECURITY CONTEXT: the same catalyst-ui bundle is served on BOTH the
 * Sovereign's own front door (console.<sov-fqdn>, e.g. console.omantel.biz)
 * AND every Org pool-domain console (console.<org>.<pool-zone>, e.g.
 * console.demo.omani.homes). detectMode() can't tell them apart (both strip
 * a leading `console.`), so the SPA used to render the FULL sovereign-admin
 * console — provisioning wizard, deployment-stream, every Org, cloud view,
 * sovereign DNS/parent-domain/BSS settings — on an Org console too. A
 * customer thus saw god-mode (live privilege escalation on omantel.biz).
 *
 * The authoritative scope comes from the BACKEND: GET /api/v1/whoami now
 * returns `orgScoped: true` + `org: "<slug>"` when the session was minted on
 * an Org console host (catalyst-api stamps the PIN session Org-scoped from
 * the request host — auth.go HandlePinVerify + org_scope.go). The SPA reads
 * that flag to render the scoped chrome and hide the sovereign-admin
 * surfaces. The backend ALSO 403s those surfaces, so this is defense-in-
 * depth (hide AND enforce), not UI-only hiding.
 *
 * Returns:
 *   - orgScoped — true when the session is confined to one Organization.
 *   - org       — the Organization slug (e.g. "demo") when orgScoped.
 *   - loading   — true on first fetch (treat as NOT-yet-known; callers that
 *                 must fail safe should render the scoped/minimal view while
 *                 loading rather than flashing the admin nav).
 */

import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'

interface WhoamiScopeResponse {
  orgScoped?: boolean
  org?: string
}

export interface ConsoleScope {
  orgScoped: boolean
  org: string | null
  loading: boolean
}

const CONSOLE_SCOPE_QUERY_KEY = ['catalyst', 'console-scope', 'whoami'] as const

async function fetchConsoleScope(): Promise<WhoamiScopeResponse | null> {
  const res = await fetch(`${API_BASE}/v1/whoami`, {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (res.status === 401) return null
  if (!res.ok) throw new Error(`whoami: HTTP ${res.status}`)
  return (await res.json()) as WhoamiScopeResponse
}

export function useConsoleScope(): ConsoleScope {
  const q = useQuery({
    queryKey: CONSOLE_SCOPE_QUERY_KEY,
    queryFn: fetchConsoleScope,
    refetchOnWindowFocus: false,
    retry: 1,
    staleTime: 60_000,
    networkMode: 'always',
  })
  const data = q.data ?? null
  return {
    orgScoped: data?.orgScoped === true,
    org: data?.org ?? null,
    loading: q.isLoading,
  }
}
