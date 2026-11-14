/**
 * AddBucketModal — provision a SeaweedFS / cloud-provider object
 * bucket.
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC POST to
 * /infrastructure/buckets — buckets are cloud objects.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { BucketFormFields, type BucketFormValues } from './formFields'
import { addBucket } from '@/lib/infrastructure-crud'

export interface AddBucketModalProps {
  open: boolean
  deploymentId: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddBucketModal({ open, deploymentId, onClose, onSuccess }: AddBucketModalProps) {
  const [values, setValues] = useState<BucketFormValues>({
    name: '',
    capacity: '100Gi',
    retentionDays: '',
  })

  if (!open) return null

  const canSubmit =
    values.name.trim().length > 0 && values.capacity.trim().length > 0

  async function handleSubmit() {
    const ref = await addBucket({
      deploymentId,
      name: values.name.trim(),
      capacity: values.capacity.trim(),
      retentionDays: values.retentionDays.trim(),
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="add-bucket"
      title="Add bucket"
      subtitle="Object storage"
      primaryLabel="Add bucket"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <BucketFormFields values={values} onChange={setValues} />
    </CrudFormModal>
  )
}
