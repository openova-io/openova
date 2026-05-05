import { useParams } from '@tanstack/react-router'

/**
 * useLinkPrefix — returns '/provision/$deploymentId' (mother mode on contabo)
 * or '' (self mode on Sovereign / clean URLs). Internal Link `to=` callers
 * use this to build paths that work on both surfaces:
 *
 *   const prefix = useLinkPrefix(deploymentId)
 *   <Link to={`${prefix}/jobs/${jobId}`} ...>
 *
 * Mother mode is detected by the presence of `deploymentId` URL param
 * (route `/provision/$deploymentId/...`). When absent, the URL is clean
 * and the prefix is empty.
 */
export function useLinkPrefix(deploymentId?: string): string {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  // If deploymentId is in URL, we're in mother mode — preserve the prefix.
  if (params.deploymentId) return `/provision/${params.deploymentId}`
  // Self mode (Sovereign clean URLs) — no prefix.
  if (deploymentId) return '' // explicit self
  return ''
}
