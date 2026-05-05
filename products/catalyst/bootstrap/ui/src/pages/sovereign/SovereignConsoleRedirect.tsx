/**
 * SovereignConsoleRedirect — runs ON Sovereign clusters and redirects
 * `/console/<page>` → `/provision/<self-deployment-id>/<page>` so the
 * canonical mothership-side React components render with this Sovereign's
 * own deployment record.
 *
 * The self deployment id is fetched from `GET /api/v1/sovereign/self`
 * which the catalyst-api Pod on a Sovereign returns with the deployment id
 * stamped at handover (env var `CATALYST_SELF_DEPLOYMENT_ID`).
 *
 * On the mothership (console.openova.io) `/api/v1/sovereign/self` returns
 * 404 — these routes are not expected to be hit there. Defensive fallback:
 * redirect to `/wizard`.
 *
 * Pixel-byte-byte identical UI contract: the same Dashboard / AppsPage /
 * JobsPage / CloudPage / UserAccessListPage / SettingsPage components
 * render on Sovereign and mothership. Only the host part of the URL
 * differs — and ONLY because `iteration 2` has not yet dropped the
 * `/sovereign/provision/$id/` URL prefix on Sovereign installs.
 */
import { useEffect } from 'react'
import { useRouter } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

interface Props {
  /** Sub-path under /provision/$id/ to land on. Empty = the AppsPage index. */
  to?: '' | 'dashboard' | 'jobs' | 'cloud' | 'users' | 'settings' | 'notifications'
}

export function SovereignConsoleRedirect({ to = 'dashboard' }: Props = {}) {
  const router = useRouter()
  const { deploymentId, isLoading } = useResolvedDeploymentId()

  useEffect(() => {
    if (isLoading) return
    if (!deploymentId) {
      // Not on a Sovereign (or self-discovery failed) — fall back to wizard.
      router.navigate({ to: '/wizard', replace: true } as never)
      return
    }
    const path = to === ''
      ? `/provision/${deploymentId}`
      : `/provision/${deploymentId}/${to}`
    router.navigate({ to: path as never, replace: true })
  }, [deploymentId, isLoading, router, to])

  return (
    <div className="flex h-screen items-center justify-center">
      <div className="h-8 w-8 animate-spin rounded-full border-2 border-blue-500 border-t-transparent" />
    </div>
  )
}
