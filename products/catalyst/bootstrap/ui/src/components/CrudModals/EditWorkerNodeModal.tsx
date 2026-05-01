/**
 * EditWorkerNodeModal — resize machine type, edit taints + labels.
 * Name + role are immutable on existing nodes (changing either
 * triggers a destroy+recreate, which is the Add path).
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { WorkerNodeFormFields, type WorkerNodeFormValues } from './formFields'
import { updateWorkerNode } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import type { NodeSpec } from '@/lib/infrastructure.types'

export interface EditWorkerNodeModalProps {
  open: boolean
  deploymentId: string
  node: NodeSpec
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditWorkerNodeModal({
  open,
  deploymentId,
  node,
  regionProvider,
  onClose,
  onSuccess,
}: EditWorkerNodeModalProps) {
  const [values, setValues] = useState<WorkerNodeFormValues>({
    name: node.name,
    sku: node.sku,
    role: (node.role === 'control-plane' ? 'control-plane' : 'worker') as
      | 'control-plane'
      | 'worker',
    taints: '',
    labels: '',
  })

  if (!open) return null

  const dirty =
    values.sku !== node.sku ||
    values.taints.length > 0 ||
    values.labels.length > 0

  async function handleSubmit() {
    const patch: { sku?: string; taints?: string; labels?: string } = {}
    if (values.sku !== node.sku) patch.sku = values.sku
    if (values.taints.length > 0) patch.taints = values.taints
    if (values.labels.length > 0) patch.labels = values.labels
    const ref = await updateWorkerNode({
      deploymentId,
      nodeId: node.id,
      ...patch,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-worker-node"
      title="Edit worker node"
      subtitle={`Node ${node.id}`}
      primaryLabel="Save changes"
      canSubmit={dirty}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <WorkerNodeFormFields
        values={values}
        onChange={setValues}
        provider={regionProvider}
        nameReadOnly
        roleReadOnly
      />
    </CrudFormModal>
  )
}
