/**
 * AddWorkerNodeModal — provision a single worker node into an existing
 * cluster. Most of the time operators add nodes via NodePool replicas;
 * this surface exists for the one-off case where a node-shape can't
 * fit into an existing pool (different taints / labels / role).
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC POST to
 * /infrastructure/clusters/{id}/nodes. Crossplane reconciles to the
 * cloud's instance API.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { WorkerNodeFormFields, type WorkerNodeFormValues } from './formFields'
import { addWorkerNode } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import { defaultNodeSizeId } from '@/shared/constants/providerSizes'

export interface AddWorkerNodeModalProps {
  open: boolean
  deploymentId: string
  clusterId: string
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddWorkerNodeModal({
  open,
  deploymentId,
  clusterId,
  regionProvider,
  onClose,
  onSuccess,
}: AddWorkerNodeModalProps) {
  const [values, setValues] = useState<WorkerNodeFormValues>({
    name: '',
    sku: defaultNodeSizeId(regionProvider),
    role: 'worker',
    taints: '',
    labels: '',
  })

  if (!open) return null

  const canSubmit = values.name.trim().length > 0

  async function handleSubmit() {
    const ref = await addWorkerNode({
      deploymentId,
      clusterId,
      name: values.name.trim(),
      sku: values.sku,
      role: values.role,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="add-worker-node"
      title="Add worker node"
      subtitle={`Cluster ${clusterId}`}
      primaryLabel="Add worker node"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <WorkerNodeFormFields
        values={values}
        onChange={setValues}
        provider={regionProvider}
      />
    </CrudFormModal>
  )
}
