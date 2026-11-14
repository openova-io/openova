/**
 * AddPVCModal — provision a Persistent Volume Claim into a namespace.
 *
 * Backend seam (ADR-0001 §9.2 row B3): catalyst-api writes the PVC
 * directly via dynamic client (core/v1/persistentvolumeclaims) on the
 * Sovereign cluster — NOT a Crossplane XRC. PVC is K8s-native.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { PVCFormFields, type PVCFormValues } from './formFields'
import { addPVC } from '@/lib/infrastructure-crud'

export interface AddPVCModalProps {
  open: boolean
  deploymentId: string
  /** Storage classes the operator can pick from. */
  storageClasses: readonly string[]
  /** Default namespace; falls back to "default". */
  defaultNamespace?: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddPVCModal({
  open,
  deploymentId,
  storageClasses,
  defaultNamespace,
  onClose,
  onSuccess,
}: AddPVCModalProps) {
  const initialClass = storageClasses[0] ?? 'default'
  const [values, setValues] = useState<PVCFormValues>({
    name: '',
    namespace: defaultNamespace ?? 'default',
    capacity: '10Gi',
    storageClass: initialClass,
  })

  if (!open) return null

  const canSubmit =
    values.name.trim().length > 0 &&
    values.namespace.trim().length > 0 &&
    values.capacity.trim().length > 0

  async function handleSubmit() {
    const ref = await addPVC({
      deploymentId,
      name: values.name.trim(),
      namespace: values.namespace.trim(),
      capacity: values.capacity.trim(),
      storageClass: values.storageClass,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="add-pvc"
      title="Add PVC"
      subtitle="Persistent volume claim"
      primaryLabel="Add PVC"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <PVCFormFields
        values={values}
        onChange={setValues}
        storageClasses={storageClasses}
      />
    </CrudFormModal>
  )
}
