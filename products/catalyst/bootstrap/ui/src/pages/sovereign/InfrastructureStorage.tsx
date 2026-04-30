/**
 * InfrastructureStorage — Storage tab of the Infrastructure surface.
 * Three card sections: Persistent Volume Claims + Object Buckets +
 * Block Volumes.
 *
 * Per founder spec: "storage (pvcs, buckets etc)".
 */

import { useParams } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import {
  getStorage,
  type BucketItem,
  type PVCItem,
  type StorageResponse,
  type VolumeItem,
} from '@/lib/infrastructure.types'

const STALE_MS = 30_000

interface InfrastructureStorageProps {
  initialDataOverride?: StorageResponse
}

export function InfrastructureStorage({
  initialDataOverride,
}: InfrastructureStorageProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/infrastructure/storage' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId

  const query = useQuery<StorageResponse>({
    queryKey: ['infra-storage', deploymentId],
    queryFn: () => getStorage(deploymentId),
    staleTime: STALE_MS,
    enabled: !initialDataOverride,
  })

  const data = initialDataOverride ?? query.data
  const isLoading = !initialDataOverride && query.isLoading && !data
  const pvcs = data?.pvcs ?? []
  const buckets = data?.buckets ?? []
  const volumes = data?.volumes ?? []
  const isEmpty =
    !isLoading && pvcs.length === 0 && buckets.length === 0 && volumes.length === 0

  return (
    <div data-testid="infrastructure-storage">
      {isLoading && (
        <div
          className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]"
          data-testid="infrastructure-storage-loading"
        >
          Loading storage resources…
        </div>
      )}

      {isEmpty && !query.isError && (
        <div className="infra-empty" data-testid="infrastructure-storage-empty">
          <p className="title">No storage resources yet.</p>
          <p className="sub">
            PVCs, S3 buckets and block volumes will appear here once the
            Sovereign cluster reports them.
          </p>
        </div>
      )}

      {!isEmpty && (
        <>
          <section className="infra-section" data-testid="infrastructure-pvcs-section">
            <h2>
              Persistent Volume Claims{' '}
              <span className="count" data-testid="infrastructure-pvcs-count">{pvcs.length}</span>
            </h2>
            {pvcs.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">No PVCs reported.</p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-pvcs-grid">
                {pvcs.map((p) => <PVCCard key={p.id} pvc={p} />)}
              </div>
            )}
          </section>

          <section className="infra-section" data-testid="infrastructure-buckets-section">
            <h2>
              Object Buckets{' '}
              <span className="count" data-testid="infrastructure-buckets-count">{buckets.length}</span>
            </h2>
            {buckets.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">No buckets reported.</p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-buckets-grid">
                {buckets.map((b) => <BucketCard key={b.id} bucket={b} />)}
              </div>
            )}
          </section>

          <section className="infra-section" data-testid="infrastructure-volumes-section">
            <h2>
              Block Volumes{' '}
              <span className="count" data-testid="infrastructure-volumes-count">{volumes.length}</span>
            </h2>
            {volumes.length === 0 ? (
              <p className="text-xs text-[var(--color-text-dim)]">
                No block volumes reported.
              </p>
            ) : (
              <div className="infra-grid" data-testid="infrastructure-volumes-grid">
                {volumes.map((v) => <VolumeCard key={v.id} volume={v} />)}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  )
}

function PVCCard({ pvc }: { pvc: PVCItem }) {
  return (
    <div
      className="infra-card"
      data-status={pvc.status}
      data-testid={`infrastructure-pvc-card-${pvc.id}`}
    >
      <span className="infra-card-status" data-status={pvc.status}>
        {pvc.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{pvc.name}</span>
        <span className="infra-card-kind">pvc</span>
      </div>
      <div className="infra-card-row"><span>Namespace</span><span className="v">{pvc.namespace}</span></div>
      <div className="infra-card-row"><span>Capacity</span><span className="v">{pvc.capacity}</span></div>
      <div className="infra-card-row"><span>Used</span><span className="v">{pvc.used || '—'}</span></div>
      <div className="infra-card-row"><span>Class</span><span className="v">{pvc.storageClass}</span></div>
    </div>
  )
}

function BucketCard({ bucket }: { bucket: BucketItem }) {
  return (
    <div
      className="infra-card"
      data-status="healthy"
      data-testid={`infrastructure-bucket-card-${bucket.id}`}
    >
      <div className="infra-card-head">
        <span className="infra-card-name">{bucket.name}</span>
        <span className="infra-card-kind">bucket</span>
      </div>
      <div className="infra-card-row"><span>Endpoint</span><span className="v">{bucket.endpoint}</span></div>
      <div className="infra-card-row"><span>Capacity</span><span className="v">{bucket.capacity}</span></div>
      <div className="infra-card-row"><span>Used</span><span className="v">{bucket.used || '—'}</span></div>
      <div className="infra-card-row"><span>Retention</span><span className="v">{bucket.retentionDays || 'indefinite'}</span></div>
    </div>
  )
}

function VolumeCard({ volume }: { volume: VolumeItem }) {
  return (
    <div
      className="infra-card"
      data-status={volume.status}
      data-testid={`infrastructure-volume-card-${volume.id}`}
    >
      <span className="infra-card-status" data-status={volume.status}>
        {volume.status}
      </span>
      <div className="infra-card-head">
        <span className="infra-card-name">{volume.name}</span>
        <span className="infra-card-kind">volume</span>
      </div>
      <div className="infra-card-row"><span>Capacity</span><span className="v">{volume.capacity}</span></div>
      <div className="infra-card-row"><span>Region</span><span className="v">{volume.region}</span></div>
      <div className="infra-card-row"><span>Attached to</span><span className="v">{volume.attachedTo || 'detached'}</span></div>
    </div>
  )
}
