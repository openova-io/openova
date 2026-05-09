/**
 * SessionsRoute — TanStack Router adapter for SessionsPage. Resolves
 * the deployment id from route params (mothership) or via
 * useResolvedDeploymentId (chroot Sovereign), and the canReplay flag
 * from the eventual whoami integration. Today we leave canReplay=true
 * and let the server gate enforce — a 403 surfaces in the row's
 * onReplay handler.
 */

import { useParams } from '@tanstack/react-router'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

import { SessionsPage } from './SessionsPage'

export function SessionsRoute() {
  const params = useParams({ strict: false }) as { deploymentId?: string }
  const { deploymentId: chrootDepId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? chrootDepId ?? ''
  // Render-then-enforce: replay buttons mount unconditionally and the
  // server-side `sessions.playback` RBAC gate is the authoritative
  // check. The client-side claims.tier mirror is a UX-only nicety;
  // the server gate stays in place and is the source of truth.
  return (
    <div className="mx-auto max-w-6xl">
      <SessionsPage deploymentId={deploymentId} canReplay={true} />
    </div>
  )
}
