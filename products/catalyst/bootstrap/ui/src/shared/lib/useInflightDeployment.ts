/**
 * useInflightDeployment — query the deployments list for the signed-in
 * operator's most-recent in-flight or recently-completed deployment
 * (issue #747).
 *
 * Why this exists: when the operator refreshes /sovereign/wizard during
 * an active provisioning run (15+ minutes), we must redirect them back
 * to /sovereign/provision/<id> instead of presenting an empty Step 1.
 * The wizard route's effect reads this hook and hard-navigates when a
 * live deployment is detected.
 *
 * The hook returns three buckets so the wizard can decide between
 * "redirect" and "show banner":
 *
 *   - inflight    — deployment whose status is one of the live phases
 *                   ('provisioning', 'tofu-applying', 'flux-bootstrapping',
 *                   'phase1-watching', 'pending') OR 'ready' but not yet
 *                   adopted (handover hasn't fired). Wizard redirects to
 *                   /provision/<inflight.id>.
 *
 *   - completed   — deployments whose status is 'failed' / 'wiped' /
 *                   any non-live state. Wizard renders a banner with a
 *                   "View previous deployments" link.
 *
 *   - all         — every deployment the operator owns (already filtered
 *                   server-side by session email). Useful for the
 *                   /deployments list page.
 *
 * Adopted deployments are filtered server-side and never appear in any
 * bucket — once the customer's Sovereign owns the cluster, the wizard
 * redirect must NOT pull the operator back into Catalyst-Zero.
 *
 * Stale time of 30s mirrors useSession to keep the polling cost
 * negligible while still picking up the new deployment row created by
 * the StepReview launch in the same tab.
 */

import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'

/**
 * Slim deployment shape returned by GET /api/v1/deployments. Mirrors the
 * Go-side listEntry — keep in sync with handler/deployments.go's
 * ListDeployments response shape.
 */
export interface DeploymentListEntry {
  id: string
  status: string
  sovereignFQDN?: string
  region?: string
  startedAt?: string
  finishedAt?: string
  ownerEmail?: string
  adoptedAt?: string | null
  error?: string
  /**
   * #3611 — surface-only flag: true when a "ready" multi-region prov's
   * secondary region is significantly behind the primary. Lets the
   * deployments list badge it "secondary degraded" instead of a flat
   * green pill. The full per-region breakdown lives on the detail
   * response (GET /deployments/{id}); the list carries just this roll-up.
   */
  secondaryDegraded?: boolean
}

interface ListDeploymentsResponse {
  deployments: DeploymentListEntry[]
}

/**
 * Status values that indicate a deployment is mid-flight and the
 * wizard must redirect the operator back to /provision/<id>.
 *
 * Includes 'ready' on purpose: a deployment that finished Phase 1 but
 * has not yet been handed over to the customer is still owned by the
 * Catalyst-Zero operator — the next step is the handover button on the
 * /provision/<id> page, not a fresh wizard. Adopted deployments are
 * already excluded server-side, so 'ready' here can only mean
 * "not yet adopted".
 */
export const INFLIGHT_STATUSES: ReadonlySet<string> = new Set([
  'pending',
  'provisioning',
  'tofu-applying',
  'tofu-plan',
  'tofu-apply',
  'flux-bootstrapping',
  'cloud-init-waiting',
  'phase1-watching',
  'ready',
])

const QUERY_KEY = ['catalyst', 'deployments', 'list'] as const

async function fetchDeployments(ownerEmail: string | null): Promise<DeploymentListEntry[]> {
  // The session cookie is the security boundary; the ?owner= query
  // is a hint that mirrors session.email so the server-side filter
  // short-circuits without scanning rows it knows can't match.
  const url = new URL(`${API_BASE}/v1/deployments`, window.location.origin)
  if (ownerEmail) url.searchParams.set('owner', ownerEmail)
  const res = await fetch(url.toString(), {
    method: 'GET',
    credentials: 'include',
    headers: { Accept: 'application/json' },
  })
  if (res.status === 401) return []
  if (!res.ok) {
    // Treat errors as "no deployments" so the wizard renders rather
    // than locking the operator out behind a network blip.
    throw new Error(`deployments list: HTTP ${res.status}`)
  }
  const body = (await res.json()) as ListDeploymentsResponse
  return Array.isArray(body.deployments) ? body.deployments : []
}

export interface InflightDeploymentResult {
  inflight: DeploymentListEntry | null
  completed: DeploymentListEntry[]
  all: DeploymentListEntry[]
  loading: boolean
  isError: boolean
}

/**
 * Pick the most-recent in-flight deployment for the given list. Sort by
 * startedAt DESC and pick the first match. The server-side endpoint
 * already excludes adopted deployments so we only filter on status here.
 */
function pickInflight(list: DeploymentListEntry[]): DeploymentListEntry | null {
  const candidates = list
    .filter((d) => INFLIGHT_STATUSES.has(d.status))
    .filter((d) => !d.adoptedAt)
  if (candidates.length === 0) return null
  // Sort newest-first by startedAt; missing dates sort last so the
  // freshly-created row (with a real timestamp) wins over a row whose
  // StartedAt was zeroed for some reason.
  candidates.sort((a, b) => {
    const ta = a.startedAt ? Date.parse(a.startedAt) : 0
    const tb = b.startedAt ? Date.parse(b.startedAt) : 0
    return tb - ta
  })
  return candidates[0] ?? null
}

export function useInflightDeployment(opts: {
  ownerEmail: string | null
  enabled: boolean
}): InflightDeploymentResult {
  const { ownerEmail, enabled } = opts
  const q = useQuery({
    queryKey: [...QUERY_KEY, ownerEmail ?? ''],
    queryFn: () => fetchDeployments(ownerEmail),
    enabled,
    staleTime: 30_000,
    refetchOnWindowFocus: true,
    retry: 1,
    networkMode: 'always',
  })

  const all = q.data ?? []
  const inflight = pickInflight(all)
  const completed = all.filter((d) => d !== inflight)

  return {
    inflight,
    completed,
    all,
    loading: q.isLoading,
    isError: q.isError,
  }
}
