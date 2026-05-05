import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { API_BASE } from '@/shared/config/urls'

interface SovereignSelf {
  deploymentId: string
  sovereignFQDN: string
}

const SELF_QUERY_KEY = ['sovereign', 'self'] as const

/**
 * useResolvedDeploymentId — resolves the active deployment id from either
 * URL params (mothership tenant operator at /provision/$deploymentId/*)
 * or the runtime self-discovery endpoint (Sovereign operator at clean
 * URLs like /dashboard, /cloud, /jobs).
 *
 * Order of resolution:
 *   1. URL `:deploymentId` param (when route is /provision/$deploymentId/...)
 *   2. /api/v1/sovereign/self (Sovereign self-discovery)
 *
 * Returns: { deploymentId: string | null, isLoading: boolean }
 */
export function useResolvedDeploymentId(): {
  deploymentId: string | null
  isLoading: boolean
} {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const enabled = !params.deploymentId
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

  return {
    deploymentId: params.deploymentId ?? q.data?.deploymentId ?? null,
    isLoading: enabled && q.isLoading,
  }
}
