/**
 * InfrastructureStorage — Storage tab. Flat table [PVC · Bucket ·
 * Volume], reads off the shared infrastructure tree.
 *
 * Per founder spec (issue #228): "Storage — flat table [PVC · Bucket
 * · Volume]. Bulk: snapshot, expand, delete."
 */

import { useMemo, useState } from 'react'
import { useInfrastructure } from './InfrastructurePage'
import { DeleteCascadeConfirm } from '@/components/CrudModals'
import { ModalShell, FormRow, TextInput } from '@/components/CrudModals/_shared'
import { pvcAction } from '@/lib/infrastructure-crud'
import type { PVCItem } from '@/lib/infrastructure.types'

export function InfrastructureStorage() {
  const { deploymentId, data, isLoading } = useInfrastructure()

  const { pvcs, buckets, volumes } = useMemo(() => {
    if (!data) return { pvcs: [], buckets: [], volumes: [] }
    return {
      pvcs: data.storage?.pvcs ?? [],
      buckets: data.storage?.buckets ?? [],
      volumes: data.storage?.volumes ?? [],
    }
  }, [data])

  const [selected, setSelected] = useState<{ kind: string; id: string }[]>([])
  const [expandPvc, setExpandPvc] = useState<PVCItem | null>(null)
  const [deleteRow, setDeleteRow] = useState<{
    resource: 'pvcs' | 'volumes' | 'buckets'
    id: string
    label: string
  } | null>(null)

  const isEmpty = !isLoading && pvcs.length === 0 && buckets.length === 0 && volumes.length === 0

  function toggle(kind: string, id: string, checked: boolean) {
    setSelected((prev) =>
      checked
        ? [...prev, { kind, id }]
        : prev.filter((s) => !(s.kind === kind && s.id === id)),
    )
  }

  return (
    <div data-testid="infrastructure-storage">
      {isLoading && (
        <div className="flex h-48 items-center justify-center text-sm text-[var(--color-text-dim)]" data-testid="infrastructure-storage-loading">
          Loading storage resources…
        </div>
      )}

      {isEmpty && (
        <div className="infra-empty" data-testid="infrastructure-storage-empty">
          <p className="title">No storage resources yet.</p>
          <p className="sub">PVCs, buckets and volumes will appear here as the cluster reports them.</p>
        </div>
      )}

      {!isEmpty && (
        <>
          <div className="infra-bulk-actions" data-testid="infrastructure-storage-bulk">
            <span className="label">Bulk · {selected.length} selected</span>
            <button
              type="button"
              data-testid="infrastructure-storage-bulk-snapshot"
              disabled={selected.filter((s) => s.kind === 'pvc').length === 0}
              onClick={() => {
                const pick = selected.find((s) => s.kind === 'pvc')
                if (!pick) return
                void pvcAction({ deploymentId, pvcId: pick.id, action: 'snapshot' }).catch(() => {})
                setSelected([])
              }}
            >
              Snapshot
            </button>
            <button
              type="button"
              data-testid="infrastructure-storage-bulk-expand"
              disabled={selected.filter((s) => s.kind === 'pvc').length !== 1}
              onClick={() => {
                const pick = selected.find((s) => s.kind === 'pvc')
                if (!pick) return
                const target = pvcs.find((p) => p.id === pick.id)
                if (target) setExpandPvc(target)
              }}
            >
              Expand
            </button>
            <button
              type="button"
              data-testid="infrastructure-storage-bulk-delete"
              disabled={selected.length !== 1}
              onClick={() => {
                const pick = selected[0]
                if (!pick) return
                if (pick.kind === 'pvc') {
                  const t = pvcs.find((p) => p.id === pick.id)
                  if (t) setDeleteRow({ resource: 'pvcs', id: t.id, label: t.name })
                } else if (pick.kind === 'volume') {
                  const t = volumes.find((v) => v.id === pick.id)
                  if (t) setDeleteRow({ resource: 'volumes', id: t.id, label: t.name })
                }
              }}
            >
              Delete
            </button>
          </div>

          <section className="infra-section" data-testid="infrastructure-pvcs-section">
            <h2>
              Persistent Volume Claims <span className="count" data-testid="infrastructure-pvcs-count">{pvcs.length}</span>
            </h2>
            <FlatTable
              testId="infrastructure-pvcs-table"
              headers={['', 'Name', 'Namespace', 'Capacity', 'Used', 'Class', 'Status', '']}
            >
              {pvcs.map((p) => (
                <tr key={p.id} data-testid={`infrastructure-pvc-row-${p.id}`}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selected.some((s) => s.kind === 'pvc' && s.id === p.id)}
                      onChange={(e) => toggle('pvc', p.id, e.target.checked)}
                      data-testid={`infrastructure-pvc-row-${p.id}-select`}
                    />
                  </td>
                  <td>{p.name}</td>
                  <td>{p.namespace}</td>
                  <td>{p.capacity}</td>
                  <td>{p.used || '—'}</td>
                  <td>{p.storageClass}</td>
                  <td>
                    <StatusBadge status={p.status} />
                  </td>
                  <td style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                    <button
                      type="button"
                      onClick={() => setExpandPvc(p)}
                      data-testid={`infrastructure-pvc-row-${p.id}-expand`}
                      style={rowBtn}
                    >
                      Expand
                    </button>
                    <button
                      type="button"
                      onClick={() => void pvcAction({ deploymentId, pvcId: p.id, action: 'snapshot' }).catch(() => {})}
                      data-testid={`infrastructure-pvc-row-${p.id}-snapshot`}
                      style={rowBtn}
                    >
                      Snapshot
                    </button>
                  </td>
                </tr>
              ))}
            </FlatTable>
          </section>

          <section className="infra-section" data-testid="infrastructure-buckets-section">
            <h2>
              Object Buckets <span className="count" data-testid="infrastructure-buckets-count">{buckets.length}</span>
            </h2>
            <FlatTable
              testId="infrastructure-buckets-table"
              headers={['', 'Name', 'Endpoint', 'Capacity', 'Used', 'Retention']}
            >
              {buckets.map((b) => (
                <tr key={b.id} data-testid={`infrastructure-bucket-row-${b.id}`}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selected.some((s) => s.kind === 'bucket' && s.id === b.id)}
                      onChange={(e) => toggle('bucket', b.id, e.target.checked)}
                      data-testid={`infrastructure-bucket-row-${b.id}-select`}
                    />
                  </td>
                  <td>{b.name}</td>
                  <td style={{ fontFamily: 'monospace' }}>{b.endpoint}</td>
                  <td>{b.capacity}</td>
                  <td>{b.used || '—'}</td>
                  <td>{b.retentionDays || 'indefinite'}</td>
                </tr>
              ))}
            </FlatTable>
          </section>

          <section className="infra-section" data-testid="infrastructure-volumes-section">
            <h2>
              Block Volumes <span className="count" data-testid="infrastructure-volumes-count">{volumes.length}</span>
            </h2>
            <FlatTable
              testId="infrastructure-volumes-table"
              headers={['', 'Name', 'Capacity', 'Region', 'Attached', 'Status']}
            >
              {volumes.map((v) => (
                <tr key={v.id} data-testid={`infrastructure-volume-row-${v.id}`}>
                  <td>
                    <input
                      type="checkbox"
                      checked={selected.some((s) => s.kind === 'volume' && s.id === v.id)}
                      onChange={(e) => toggle('volume', v.id, e.target.checked)}
                      data-testid={`infrastructure-volume-row-${v.id}-select`}
                    />
                  </td>
                  <td>{v.name}</td>
                  <td>{v.capacity}</td>
                  <td>{v.region}</td>
                  <td>{v.attachedTo || 'detached'}</td>
                  <td>
                    <StatusBadge status={v.status} />
                  </td>
                </tr>
              ))}
            </FlatTable>
          </section>
        </>
      )}

      {expandPvc && (
        <ExpandPVCModal
          deploymentId={deploymentId}
          pvc={expandPvc}
          onClose={() => setExpandPvc(null)}
        />
      )}
      {deleteRow && (
        <DeleteCascadeConfirm
          open
          deploymentId={deploymentId}
          resource={deleteRow.resource}
          resourceId={deleteRow.id}
          resourceLabel={deleteRow.label}
          onClose={() => setDeleteRow(null)}
        />
      )}
    </div>
  )
}

function ExpandPVCModal({
  deploymentId,
  pvc,
  onClose,
}: {
  deploymentId: string
  pvc: PVCItem
  onClose: () => void
}) {
  const [capacity, setCapacity] = useState(pvc.capacity)
  const [submitting, setSubmitting] = useState(false)
  return (
    <ModalShell
      id="expand-pvc"
      open
      title="Expand PVC"
      subtitle={`PVC ${pvc.name}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Expand',
        onClick: async () => {
          setSubmitting(true)
          try {
            await pvcAction({ deploymentId, pvcId: pvc.id, action: 'expand', newCapacity: capacity })
            onClose()
          } catch (err) {
            console.error('expand pvc failed', err)
          } finally {
            setSubmitting(false)
          }
        },
        loading: submitting,
        disabled: !capacity.trim() || capacity === pvc.capacity,
      }}
    >
      <FormRow label="Current capacity">
        <TextInput value={pvc.capacity} onChange={() => {}} testId="expand-pvc-modal-current" />
      </FormRow>
      <FormRow label="New capacity" hint="Format like 20Gi, 500Gi.">
        <TextInput value={capacity} onChange={setCapacity} testId="expand-pvc-modal-capacity" />
      </FormRow>
    </ModalShell>
  )
}

function FlatTable({ testId, headers, children }: { testId: string; headers: string[]; children: React.ReactNode }) {
  return (
    <table data-testid={testId} style={{ width: '100%', borderCollapse: 'separate', borderSpacing: 0, fontSize: '0.82rem' }}>
      <thead>
        <tr>
          {headers.map((h, i) => (
            <th key={i} style={{ textAlign: 'left', fontWeight: 600, fontSize: '0.72rem', textTransform: 'uppercase', letterSpacing: '0.05em', color: 'var(--color-text-dim)', padding: '6px 8px', borderBottom: '1px solid var(--color-border)' }}>
              {h}
            </th>
          ))}
        </tr>
      </thead>
      <tbody style={{ verticalAlign: 'middle' }}>{children}</tbody>
      <style>{`
        tbody tr td { padding: 8px; border-bottom: 1px solid var(--color-border); color: var(--color-text); }
        tbody tr:hover { background: var(--color-bg-2); }
      `}</style>
    </table>
  )
}

function StatusBadge({ status }: { status: 'healthy' | 'degraded' | 'failed' | 'unknown' }) {
  return (
    <span data-status={status} style={{ display: 'inline-block', fontSize: '0.65rem', textTransform: 'uppercase', letterSpacing: '0.06em', fontWeight: 700, padding: '0.1rem 0.45rem', borderRadius: 999, background: status === 'healthy' ? 'color-mix(in srgb, var(--color-success) 18%, transparent)' : status === 'degraded' ? 'color-mix(in srgb, var(--color-warn) 18%, transparent)' : status === 'failed' ? 'color-mix(in srgb, var(--color-danger) 18%, transparent)' : 'color-mix(in srgb, var(--color-text-dim) 18%, transparent)', color: status === 'healthy' ? 'var(--color-success)' : status === 'degraded' ? 'var(--color-warn)' : status === 'failed' ? 'var(--color-danger)' : 'var(--color-text-dim)' }}>
      {status}
    </span>
  )
}

const rowBtn: React.CSSProperties = {
  border: '1px solid var(--color-border)',
  background: 'transparent',
  color: 'var(--color-text)',
  padding: '3px 8px',
  borderRadius: 5,
  fontSize: '0.72rem',
  cursor: 'pointer',
}
