/**
 * ScalePoolModal — 1-step modal with a count slider for scaling an
 * existing node pool's replica count.
 */

import { useState } from 'react'
import { ModalShell, FormRow, NumberSlider } from './_shared'
import { scalePool } from '@/lib/infrastructure-crud'
import type { NodePoolSpec } from '@/lib/infrastructure.types'

export interface ScalePoolModalProps {
  open: boolean
  deploymentId: string
  pool: NodePoolSpec
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function ScalePoolModal({
  open,
  deploymentId,
  pool,
  onClose,
  onSuccess,
}: ScalePoolModalProps) {
  const [replicas, setReplicas] = useState(pool.replicas)
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  const delta = replicas - pool.replicas

  async function handleSubmit() {
    if (replicas === pool.replicas) {
      onClose()
      return
    }
    setSubmitting(true)
    try {
      const ref = await scalePool({
        deploymentId,
        poolId: pool.id,
        replicas,
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('ScalePool failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="scale-pool"
      open={open}
      title="Scale node pool"
      subtitle={`Pool ${pool.id} · ${pool.sku}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: delta === 0 ? 'No change' : delta > 0 ? `Scale up (+${delta})` : `Scale down (${delta})`,
        onClick: handleSubmit,
        loading: submitting,
        disabled: delta === 0,
      }}
    >
      <FormRow
        label="Replicas"
        hint={`Currently ${pool.replicas}. Drag to scale up or down.`}
      >
        <NumberSlider
          value={replicas}
          onChange={setReplicas}
          min={0}
          max={50}
          testId="scale-pool-modal-replicas"
        />
      </FormRow>
    </ModalShell>
  )
}
