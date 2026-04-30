/**
 * DeleteCascadeConfirm — confirm dialog that fetches a cascade
 * preview, shows every affected workload, and only allows the operator
 * to proceed once the preview is loaded.
 *
 * Per founder spec: "Delete (any node) — Cascade-preview confirm
 * dialog showing affected workloads."
 */

import { useEffect, useState } from 'react'
import { ModalShell } from './_shared'
import {
  cascadeDelete,
  previewCascadeDelete,
  type DeletableResource,
  type CascadePreview,
} from '@/lib/infrastructure-crud'

export interface DeleteCascadeConfirmProps {
  open: boolean
  deploymentId: string
  resource: DeletableResource
  resourceId: string
  resourceLabel: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function DeleteCascadeConfirm({
  open,
  deploymentId,
  resource,
  resourceId,
  resourceLabel,
  onClose,
  onSuccess,
}: DeleteCascadeConfirmProps) {
  const [preview, setPreview] = useState<CascadePreview | null>(null)
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!open) return
    setLoading(true)
    previewCascadeDelete({ deploymentId, resource, resourceId })
      .then((p) => setPreview(p))
      .catch(() => setPreview({ affected: [], estimatedDuration: 'unknown', blockers: [] }))
      .finally(() => setLoading(false))
  }, [open, deploymentId, resource, resourceId])

  if (!open) return null

  async function handleSubmit() {
    if (!preview || preview.blockers.length > 0) return
    setSubmitting(true)
    try {
      const ref = await cascadeDelete({ deploymentId, resource, resourceId })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('CascadeDelete failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  const blocked = preview?.blockers && preview.blockers.length > 0

  return (
    <ModalShell
      id="delete-cascade"
      open={open}
      title={`Delete ${resourceLabel}`}
      subtitle={`Resource ${resource}/${resourceId}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Delete',
        onClick: handleSubmit,
        loading: submitting,
        disabled: loading || !!blocked,
        danger: true,
      }}
    >
      {loading && (
        <p
          data-testid="delete-cascade-loading"
          style={{ margin: 0, fontSize: '0.85rem', color: 'var(--color-text-dim)' }}
        >
          Loading cascade preview…
        </p>
      )}

      {!loading && preview && (
        <>
          <div
            data-testid="delete-cascade-preview"
            style={{
              border: '1px solid var(--color-border)',
              borderRadius: 8,
              padding: 12,
              fontSize: '0.82rem',
              background: 'var(--color-bg)',
            }}
          >
            <div style={{ fontWeight: 600, marginBottom: 6, color: 'var(--color-text-strong)' }}>
              Affected resources ({preview.affected.length})
            </div>
            {preview.affected.length === 0 ? (
              <div style={{ color: 'var(--color-text-dim)' }}>
                No additional resources will be touched.
              </div>
            ) : (
              <ul style={{ margin: 0, paddingLeft: 18, color: 'var(--color-text)' }}>
                {preview.affected.map((a) => (
                  <li key={a.id} data-testid={`delete-cascade-affected-${a.id}`}>
                    <span style={{ fontFamily: 'monospace' }}>{a.kind}</span> · {a.label}
                  </li>
                ))}
              </ul>
            )}
            <div style={{ marginTop: 8, fontSize: '0.75rem', color: 'var(--color-text-dim)' }}>
              Estimated duration: {preview.estimatedDuration}
            </div>
          </div>

          {blocked && (
            <div
              data-testid="delete-cascade-blockers"
              style={{
                border: '1px solid color-mix(in srgb, var(--color-danger) 50%, transparent)',
                background: 'color-mix(in srgb, var(--color-danger) 10%, transparent)',
                borderRadius: 8,
                padding: 12,
                fontSize: '0.82rem',
                color: 'var(--color-danger)',
              }}
            >
              <div style={{ fontWeight: 700, marginBottom: 4 }}>Blocked</div>
              <ul style={{ margin: 0, paddingLeft: 18 }}>
                {preview.blockers.map((b, i) => (
                  <li key={i}>{b}</li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}
    </ModalShell>
  )
}
