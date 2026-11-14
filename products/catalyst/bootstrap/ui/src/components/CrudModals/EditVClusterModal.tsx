/**
 * EditVClusterModal — rename + change isolation mode on a vCluster.
 *
 * Backend seam (ADR-0001 §9.2 row B3): catalyst-api writes the
 * vCluster CR directly via dynamic client (vcluster.io/v1alpha1/
 * vclusters), NOT a Crossplane XRC. Crossplane stays out of K8s-to-
 * K8s composition.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { VClusterFormFields, type VClusterFormValues } from './formFields'
import { updateVCluster } from '@/lib/infrastructure-crud'
import type { VClusterSpec } from '@/lib/infrastructure.types'

export interface EditVClusterModalProps {
  open: boolean
  deploymentId: string
  vcluster: VClusterSpec
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditVClusterModal({
  open,
  deploymentId,
  vcluster,
  onClose,
  onSuccess,
}: EditVClusterModalProps) {
  const [values, setValues] = useState<VClusterFormValues>({
    name: vcluster.name,
    isolationMode: vcluster.isolationMode,
  })

  if (!open) return null

  const dirty =
    values.name !== vcluster.name || values.isolationMode !== vcluster.isolationMode
  const canSubmit = dirty && values.name.trim().length > 0

  async function handleSubmit() {
    const patch: { name?: string; isolationMode?: VClusterFormValues['isolationMode'] } = {}
    if (values.name !== vcluster.name) patch.name = values.name.trim()
    if (values.isolationMode !== vcluster.isolationMode)
      patch.isolationMode = values.isolationMode
    const ref = await updateVCluster({
      deploymentId,
      vclusterId: vcluster.id,
      ...patch,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-vcluster"
      title="Edit vCluster"
      subtitle={`vCluster ${vcluster.id}`}
      primaryLabel="Save changes"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <VClusterFormFields values={values} onChange={setValues} />
    </CrudFormModal>
  )
}
