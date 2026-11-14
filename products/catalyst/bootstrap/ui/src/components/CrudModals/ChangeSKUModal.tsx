/**
 * ChangeSKUModal — confirm dialog with diff + ETA warning for changing
 * a node pool's SKU. Per founder spec: "Confirm dialog with diff +
 * ETA warning."
 */

import { useState } from 'react'
import { ModalShell, FormRow } from './_shared'
import { changePoolSKU } from '@/lib/infrastructure-crud'
import type { NodePoolSpec } from '@/lib/infrastructure.types'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES } from '@/shared/constants/providerSizes'

export interface ChangeSKUModalProps {
  open: boolean
  deploymentId: string
  pool: NodePoolSpec
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function ChangeSKUModal({
  open,
  deploymentId,
  pool,
  regionProvider,
  onClose,
  onSuccess,
}: ChangeSKUModalProps) {
  const [newSku, setNewSku] = useState(pool.sku)
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  const skus = PROVIDER_NODE_SIZES[regionProvider] ?? []
  const oldDef = skus.find((s) => s.id === pool.sku)
  const newDef = skus.find((s) => s.id === newSku)

  async function handleSubmit() {
    if (newSku === pool.sku) return
    setSubmitting(true)
    try {
      const ref = await changePoolSKU({
        deploymentId,
        poolId: pool.id,
        newSku,
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('ChangePoolSKU failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="change-sku"
      open={open}
      title="Change SKU"
      subtitle={`Pool ${pool.id}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Change SKU',
        onClick: handleSubmit,
        loading: submitting,
        disabled: newSku === pool.sku,
        danger: true,
      }}
    >
      <FormRow label="Target SKU">
        <select
          data-testid="change-sku-modal-target"
          value={newSku}
          onChange={(e) => setNewSku(e.target.value)}
          style={{
            padding: '8px 10px',
            borderRadius: 6,
            border: '1px solid var(--color-border)',
            background: 'var(--color-bg)',
            color: 'var(--color-text)',
            fontSize: '0.85rem',
          }}
        >
          {skus.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label} · {s.vcpu} vCPU · {s.ram} GB
            </option>
          ))}
        </select>
      </FormRow>

      <div
        data-testid="change-sku-modal-diff"
        style={{
          background: 'var(--color-bg)',
          border: '1px solid var(--color-border)',
          borderRadius: 8,
          padding: 12,
          fontSize: '0.82rem',
          display: 'grid',
          gridTemplateColumns: '1fr auto 1fr',
          gap: 8,
          alignItems: 'center',
        }}
      >
        <div>
          <div style={{ fontWeight: 600, color: 'var(--color-text)' }}>{oldDef?.label ?? pool.sku}</div>
          <div style={{ color: 'var(--color-text-dim)', fontSize: '0.72rem' }}>
            {oldDef ? `${oldDef.vcpu} vCPU · ${oldDef.ram} GB` : 'current'}
          </div>
        </div>
        <div style={{ color: 'var(--color-text-dim)', fontWeight: 700 }}>→</div>
        <div>
          <div style={{ fontWeight: 600, color: 'var(--color-accent)' }}>{newDef?.label ?? newSku}</div>
          <div style={{ color: 'var(--color-text-dim)', fontSize: '0.72rem' }}>
            {newDef ? `${newDef.vcpu} vCPU · ${newDef.ram} GB` : 'new'}
          </div>
        </div>
      </div>

      <div
        data-testid="change-sku-modal-eta"
        style={{
          background: 'color-mix(in srgb, var(--color-warn) 8%, transparent)',
          border: '1px solid color-mix(in srgb, var(--color-warn) 40%, transparent)',
          borderRadius: 8,
          padding: 10,
          fontSize: '0.78rem',
          color: 'var(--color-warn)',
        }}
      >
        ⚠ Each node will be drained and recreated. Estimated wall time
        for this pool: ~{Math.max(1, pool.replicas * 4)} minutes. Workloads
        will reschedule rolling — disruption expected.
      </div>
    </ModalShell>
  )
}
