/**
 * AddNodePoolModal — 1-step modal: SKU selector + replica count slider.
 */

import { useState } from 'react'
import { ModalShell, FormRow, NumberSlider } from './_shared'
import { addNodePool } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES, defaultNodeSizeId } from '@/shared/constants/providerSizes'

export interface AddNodePoolModalProps {
  open: boolean
  deploymentId: string
  clusterId: string
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddNodePoolModal({
  open,
  deploymentId,
  clusterId,
  regionProvider,
  onClose,
  onSuccess,
}: AddNodePoolModalProps) {
  const [sku, setSku] = useState(defaultNodeSizeId(regionProvider))
  const [replicas, setReplicas] = useState(3)
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  const skus = PROVIDER_NODE_SIZES[regionProvider] ?? []

  async function handleSubmit() {
    setSubmitting(true)
    try {
      const ref = await addNodePool({ deploymentId, clusterId, sku, replicas })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('AddNodePool failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="add-nodepool"
      open={open}
      title="Add node pool"
      subtitle={`Cluster ${clusterId}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Add node pool',
        onClick: handleSubmit,
        loading: submitting,
      }}
    >
      <FormRow label="SKU">
        <select
          data-testid="add-nodepool-modal-sku"
          value={sku}
          onChange={(e) => setSku(e.target.value)}
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
      <FormRow label="Replicas" hint="0 = pause pool, 1-50 = active.">
        <NumberSlider
          value={replicas}
          onChange={setReplicas}
          min={0}
          max={50}
          testId="add-nodepool-modal-replicas"
        />
      </FormRow>
    </ModalShell>
  )
}
