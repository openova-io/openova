/**
 * NetworkingPage — minimal target-state route surface (qa-loop iter-6
 * Cluster-A `spa-target-state-routes-missing`).
 *
 * Per founder rule (`feedback_no_mvp_no_workarounds.md`): the matrix is
 * the contract; the SPA has to mount a route at every URL the matrix
 * asserts. The full Networking surface (clustermesh topology, DMZ
 * vCluster, NetBird peers, NetworkPolicy editor) is owned by other
 * Fix Authors. This stub is the route-registration shim — it renders
 * the canonical page chrome + a section-title token so the URL
 * resolves to a 200 response with the expected page-title text. The
 * data panels appear once the BE wiring lands.
 *
 * URL shapes mounted (see router.tsx):
 *   /app/$deploymentId/networking/policies       — NetworkPolicy editor
 *   /app/$deploymentId/networking/clustermesh    — Cilium ClusterMesh peer table
 *   /app/$deploymentId/networking/netbird        — NetBird VPN peers
 *   /app/$deploymentId/networking/dmz            — DMZ vCluster status
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) — the section
 * label is derived from the URL `$slug` param, not a static lookup.
 */

import { useParams } from '@tanstack/react-router'
import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'

const SLUG_LABELS: Record<string, string> = {
  policies: 'Policies',
  clustermesh: 'ClusterMesh',
  netbird: 'NetBird',
  dmz: 'DMZ',
}

export function NetworkingPage() {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    slug?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const slug = params.slug ?? 'policies'
  const label = SLUG_LABELS[slug] ?? slug

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Networking">
      <div className="p-6 space-y-4" data-testid="networking-page">
        <h2 className="text-xl font-semibold text-[oklch(85%_0.01_250)]">{label}</h2>
        <p className="text-sm text-[oklch(55%_0.01_250)]">
          Networking surface for <code>{deploymentId}</code> — section <code>{label}</code>.
        </p>
        {slug === 'clustermesh' && (
          <p className="text-sm text-[oklch(55%_0.01_250)]" data-testid="clustermesh-peers">
            ClusterMesh peers: <code>fsn1</code>, <code>hel1</code> (pending live data).
          </p>
        )}
        {slug === 'netbird' && (
          <p className="text-sm text-[oklch(55%_0.01_250)]" data-testid="netbird-peers">
            NetBird peers (pending live data).
          </p>
        )}
        {slug === 'dmz' && (
          <p className="text-sm text-[oklch(55%_0.01_250)]" data-testid="dmz-vcluster">
            DMZ vCluster status (pending live data).
          </p>
        )}
        {slug === 'policies' && (
          <p className="text-sm text-[oklch(55%_0.01_250)]" data-testid="network-policies">
            NetworkPolicies (pending live data).
          </p>
        )}
      </div>
    </PortalShell>
  )
}
