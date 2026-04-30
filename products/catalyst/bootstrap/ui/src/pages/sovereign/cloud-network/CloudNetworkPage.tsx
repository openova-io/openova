/**
 * CloudNetworkPage — Sovereign Cloud / Network landing page (P3 of
 * issue #309). Replaces the previous flat dump in CloudNetwork.tsx.
 *
 * Renders a tile grid for the four resource types in the Network
 * category: Services, Ingresses, Load Balancers, DNS Zones. The
 * informer-fed tiles show a count; placeholder tiles show "—".
 */

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useCloud } from '../CloudPage'
import { CLOUD_LIST_CSS } from '../cloud-list/cloudListCss'

interface NetworkTile {
  id: 'services' | 'ingresses' | 'load-balancers' | 'dns-zones'
  label: string
  tagline: string
  /** Whether the tree carries data for this resource yet. */
  hasData: boolean
}

const NETWORK_TILES: readonly NetworkTile[] = [
  {
    id: 'services',
    label: 'Services',
    tagline: 'Awaiting service informer (#321).',
    hasData: false,
  },
  {
    id: 'ingresses',
    label: 'Ingresses',
    tagline: 'Awaiting ingress informer (#321).',
    hasData: false,
  },
  {
    id: 'load-balancers',
    label: 'Load Balancers',
    tagline: 'Cloud-provisioned LBs fronting clusters.',
    hasData: true,
  },
  {
    id: 'dns-zones',
    label: 'DNS Zones',
    tagline: 'Awaiting external-dns informer (#321).',
    hasData: false,
  },
]

export function CloudNetworkPage() {
  const { deploymentId, data, isLoading } = useCloud()

  const counts = useMemo(() => {
    const out: Record<NetworkTile['id'], number | null> = {
      services: null,
      ingresses: null,
      'load-balancers': 0,
      'dns-zones': null,
    }
    if (!data) return out
    let lbCount = 0
    for (const region of data.topology.regions ?? []) {
      for (const cluster of region.clusters ?? []) {
        lbCount += cluster.loadBalancers?.length ?? 0
      }
    }
    out['load-balancers'] = lbCount
    return out
  }, [data])

  return (
    <div data-testid="cloud-network-page">
      <style>{CLOUD_LIST_CSS}</style>
      <header className="mb-3">
        <h1
          className="text-2xl font-bold text-[var(--color-text-strong)]"
          data-testid="cloud-network-page-title"
        >
          Network
        </h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Services, ingresses, load balancers and DNS zones for this Sovereign.
        </p>
      </header>

      {isLoading ? (
        <div
          className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
          data-testid="cloud-network-page-loading"
        >
          Loading network resources…
        </div>
      ) : (
        <div className="cloud-list-tile-grid" data-testid="cloud-network-page-tiles">
          {NETWORK_TILES.map((tile) => {
            const c = counts[tile.id]
            return (
              <Link
                key={tile.id}
                to={`/provision/$deploymentId/cloud/network/${tile.id}` as never}
                params={{ deploymentId } as never}
                className="cloud-list-tile"
                data-testid={`cloud-network-page-tile-${tile.id}`}
              >
                <div className="cloud-list-tile-name">
                  <span>{tile.label}</span>
                  <span
                    className="cloud-list-tile-count"
                    data-testid={`cloud-network-page-tile-${tile.id}-count`}
                  >
                    {tile.hasData && c !== null ? c : '—'}
                  </span>
                </div>
                <p className="cloud-list-tile-tagline">{tile.tagline}</p>
              </Link>
            )
          })}
        </div>
      )}
    </div>
  )
}
