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
    // #5611 — the storage-class informer landed (k8scache registry kind
    // `storageclass`), so this tile counts live objects instead of
    // rendering the not-collected marker "—".
    id: 'storage-classes',
    label: 'Storage Classes',
    tagline: 'Provisioner + reclaim policy presets backing every PVC.',
    hasData: true,
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
  const { deploymentId, data, isLoading, k8sSnapshot, k8sRevision } = useCloud()

  const counts = useMemo(() => {
    const out: Record<StorageTile['id'], number | null> = {
      pvcs: 0,
      // #5611 — null until the SSE stream delivers initialState; a
      // storage-class count of 0 before the stream connects would be a
      // false zero, so the tile keeps rendering "—" until then.
      'storage-classes': null,
      buckets: 0,
      volumes: 0,
    }
    // Storage classes are cluster-scoped K8s objects streamed through
    // the k8scache SSE, not projected onto the topology response, so
    // this count comes from the snapshot — the same source the
    // /cloud?view=list chip tallies. Snapshot keys are
    // `storageclass:<name>@<cluster>` (#5571), so both regions' copies
    // of `evs-ssd` are counted, matching the PersistentVolumes chip.
    if (k8sSnapshot && k8sSnapshot.size > 0) {
      let sc = 0
      for (const key of k8sSnapshot.keys()) {
        if (key.split(':', 1)[0] === 'storageclass') sc += 1
      }
      out['storage-classes'] = sc
    }
    if (!data) return out
    out.pvcs = data.storage?.pvcs?.length ?? 0
    out.buckets = data.storage?.buckets?.length ?? 0
    out.volumes = data.storage?.volumes?.length ?? 0
    return out
    // k8sRevision looks "unnecessary" to the linter but is load-bearing:
    // k8sSnapshot is a MUTABLE Map whose identity does not change as SSE
    // deltas are folded in, so the revision counter is the only signal
    // that its contents moved. Same dependency CloudPage's kindCounts
    // carries for the same reason.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, k8sSnapshot, k8sRevision])

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
