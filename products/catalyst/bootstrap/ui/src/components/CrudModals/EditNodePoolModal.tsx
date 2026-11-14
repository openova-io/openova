/**
 * EditNodePoolModal — combined SKU + replicas patch on an existing
 * node pool. Replaces the legacy ScalePoolModal + ChangeSKUModal pair
 * with one consolidated Edit surface.
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC patch via the
 * catalyst-api PATCH /infrastructure/pools/{id} endpoint. The third-
 * sibling Composition is responsible for triggering the rolling
 * replace; catalyst-api never calls hcloud's API directly.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { NodePoolFormFields, type NodePoolFormValues } from './formFields'
import { updateNodePool } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import type { NodePoolSpec } from '@/lib/infrastructure.types'

export interface EditNodePoolModalProps {
  open: boolean
  deploymentId: string
  pool: NodePoolSpec
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditNodePoolModal({
  open,
  deploymentId,
  pool,
  regionProvider,
  onClose,
  onSuccess,
}: EditNodePoolModalProps) {
  const [values, setValues] = useState<NodePoolFormValues>({
    sku: pool.sku,
    replicas: pool.replicas,
  })

  if (!open) return null

  const dirty = values.sku !== pool.sku || values.replicas !== pool.replicas
  const canSubmit = dirty

  async function handleSubmit() {
    const patch: { sku?: string; replicas?: number } = {}
    if (values.sku !== pool.sku) patch.sku = values.sku
    if (values.replicas !== pool.replicas) patch.replicas = values.replicas
    const ref = await updateNodePool({
      deploymentId,
      poolId: pool.id,
      ...patch,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-nodepool"
      title="Edit node pool"
      subtitle={`Pool ${pool.id} · ${pool.sku}`}
      primaryLabel="Save changes"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <NodePoolFormFields values={values} onChange={setValues} provider={regionProvider} />
    </CrudFormModal>
  )
}
