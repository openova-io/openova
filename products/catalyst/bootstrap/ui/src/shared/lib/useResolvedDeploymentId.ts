import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'
import { DETECTED_MODE } from '@/shared/lib/detectMode'

interface SovereignSelf {
  deploymentId: string
  sovereignFQDN: string
}

const SELF_QUERY_KEY = ['sovereign', 'self'] as const

/**
 * useResolvedDeploymentId — resolves the active deployment id from either
 * the runtime self-discovery endpoint (Sovereign operator) or URL params
 * (mothership deployment operator at /provision/$deploymentId/*).
 *
 * # The two ids of a Sovereign (issue #4193)
 *
 * A chrooted Sovereign has TWO ids for the same cluster:
 *   • the OpenTofu deployment-record id (e.g. `4635277cae4ffed9`) — baked
 *     into cloud-list row links as `/provision/<id>/cloud/resource/…`, so
 *     it lands in `params.deploymentId`; and
 *   • the console-internal id (e.g. `7bb723da8da06047`) — returned by
 *     `/api/v1/sovereign/self` and the id the per-deployment stores (the
 *     reconcilers store, k8s cache, jobs store) are keyed on.
 *
 * On a Sovereign host the console-internal id is the ONLY one the backend
 * `/deployments/{id}/…` routes resolve — a request keyed on the tofu id
 * 404s (e.g. the #3996 Reconcile-tab logs/reconcile/suspend actions). So
 * the resolution order is **mode-dependent**:
 *
 *   • Sovereign host  → `/sovereign/self` console id wins, even when a
 *     `/provision/<tofu-id>` URL put a (wrong) id into params.
 *   • Mothership host → URL `:deploymentId` param wins (the operator is
 *     genuinely scoped to that deployment record), `/sovereign/self` 404s.
 *
 * The self-discovery query therefore runs on a Sovereign host **regardless
 * of the URL param** so the console id is always available to override it.
 *
 * Returns: { deploymentId: string | null, isLoading: boolean }
 */
export function useResolvedDeploymentId(): {
  deploymentId: string | null
  isLoading: boolean
} {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const isSovereign = DETECTED_MODE.mode === 'sovereign'

  // On a Sovereign host the console id must override any URL param, so the
  // self-discovery query has to run even when a param is present. On the
  // mothership the param is authoritative and /sovereign/self 404s, so we
  // only fire the query when there's no param (the original behaviour).
  const enabled = isSovereign || !params.deploymentId
  const q = useQuery<SovereignSelf | null>({
    queryKey: SELF_QUERY_KEY,
    queryFn: async () => {
      const res = await fetch(`${API_BASE}/v1/sovereign/self`, {
        credentials: 'include',
      })
      if (res.status === 404) return null
      if (!res.ok) return null
      return (await res.json()) as SovereignSelf
    },
    enabled,
    staleTime: Infinity,
    retry: false,
  })

  // Sovereign: prefer the self-resolved console id, fall back to the URL
  // param only until /sovereign/self lands. Mothership: the URL param is
  // authoritative.
  const deploymentId = isSovereign
    ? (q.data?.deploymentId ?? params.deploymentId ?? null)
    : (params.deploymentId ?? q.data?.deploymentId ?? null)

  return {
    deploymentId,
    isLoading: enabled && q.isLoading,
  }
}
