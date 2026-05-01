/**
 * CloudStoragePage — Sovereign Cloud / Storage landing page (P3 of
 * issue #309). Replaces the previous flat dump in CloudStorage.tsx.
 *
 * Renders a tile grid for the four resource types in the Storage
 * category: PVCs, Storage Classes, Buckets, Volumes.
 */

import { useMemo } from 'react'
import { Link } from '@tanstack/react-router'
import { useCloud } from '../CloudPage'
import { CLOUD_LIST_CSS } from '../cloud-list/cloudListCss'

interface StorageTile {
  id: 'pvcs' | 'storage-classes' | 'buckets' | 'volumes'
  label: string
  tagline: string
  hasData: boolean
}

const STORAGE_TILES: readonly StorageTile[] = [
  {
    id: 'pvcs',
    label: 'PVCs',
    tagline: 'Persistent volume claims across all namespaces.',
    hasData: true,
  },
  {
    id: 'storage-classes',
    label: 'Storage Classes',
    tagline: 'Awaiting storage-class informer (#321).',
    hasData: false,
  },
  {
    id: 'buckets',
    label: 'Buckets',
    tagline: 'S3-compatible buckets (SeaweedFS / provider).',
    hasData: true,
  },
  {
    id: 'volumes',
    label: 'Volumes',
    tagline: 'Cloud block volumes attached to nodes.',
    hasData: true,
  },
]

export function CloudStoragePage() {
  const { deploymentId, data, isLoading } = useCloud()

  const counts = useMemo(() => {
    const out: Record<StorageTile['id'], number | null> = {
      pvcs: 0,
      'storage-classes': null,
      buckets: 0,
      volumes: 0,
    }
    if (!data) return out
    out.pvcs = data.storage?.pvcs?.length ?? 0
    out.buckets = data.storage?.buckets?.length ?? 0
    out.volumes = data.storage?.volumes?.length ?? 0
    return out
  }, [data])

  return (
    <div data-testid="cloud-storage-page">
      <style>{CLOUD_LIST_CSS}</style>
      <header className="mb-3">
        <h1
          className="text-2xl font-bold text-[var(--color-text-strong)]"
          data-testid="cloud-storage-page-title"
        >
          Storage
        </h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          PVCs, storage classes, buckets and block volumes for this Sovereign.
        </p>
      </header>

      {isLoading ? (
        <div
          className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
          data-testid="cloud-storage-page-loading"
        >
          Loading storage resources…
        </div>
      ) : (
        <div className="cloud-list-tile-grid" data-testid="cloud-storage-page-tiles">
          {STORAGE_TILES.map((tile) => {
            const c = counts[tile.id]
            return (
              <Link
                key={tile.id}
                to={`/provision/$deploymentId/cloud/storage/${tile.id}` as never}
                params={{ deploymentId } as never}
                className="cloud-list-tile"
                data-testid={`cloud-storage-page-tile-${tile.id}`}
              >
                <div className="cloud-list-tile-name">
                  <span>{tile.label}</span>
                  <span
                    className="cloud-list-tile-count"
                    data-testid={`cloud-storage-page-tile-${tile.id}-count`}
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
