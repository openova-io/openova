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
  // TODO (follow-up slice): wire whoami → claims.tier so the UI
  // mirrors the server-side `sessions.playback` gate. For now we render
  // the buttons and let the server enforce.
  return (
    <div className="mx-auto max-w-6xl">
      <SessionsPage deploymentId={deploymentId} canReplay={true} />
    </div>
  )
}
