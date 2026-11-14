/**
 * EditPVCModal — expand the capacity of an existing PVC.
 * Kubernetes only supports expansion (no shrink, no rename).
 *
 * Backend seam: dynamic-client patch on core/v1/persistentvolumeclaims.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { PVCFormFields, type PVCFormValues } from './formFields'
import { updatePVC } from '@/lib/infrastructure-crud'
import type { PVCItem } from '@/lib/infrastructure.types'

export interface EditPVCModalProps {
  open: boolean
  deploymentId: string
  pvc: PVCItem
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditPVCModal({
  open,
  deploymentId,
  pvc,
  onClose,
  onSuccess,
}: EditPVCModalProps) {
  const [values, setValues] = useState<PVCFormValues>({
    name: pvc.name,
    namespace: pvc.namespace,
    capacity: pvc.capacity,
    storageClass: pvc.storageClass,
  })

  if (!open) return null

  const dirty = values.capacity !== pvc.capacity
  const canSubmit = dirty && values.capacity.trim().length > 0

  async function handleSubmit() {
    const ref = await updatePVC({
      deploymentId,
      pvcId: pvc.id,
      capacity: values.capacity.trim(),
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-pvc"
      title="Edit PVC"
      subtitle={`PVC ${pvc.namespace}/${pvc.name}`}
      primaryLabel="Save changes"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <PVCFormFields
        values={values}
        onChange={setValues}
        storageClasses={[pvc.storageClass]}
        expandOnly
      />
    </CrudFormModal>
  )
}
