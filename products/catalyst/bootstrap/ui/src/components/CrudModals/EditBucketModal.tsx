/**
 * EditBucketModal — patch capacity quota + retention-days on an
 * existing bucket. Bucket name is immutable on most providers.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { BucketFormFields, type BucketFormValues } from './formFields'
import { updateBucket } from '@/lib/infrastructure-crud'
import type { BucketItem } from '@/lib/infrastructure.types'

export interface EditBucketModalProps {
  open: boolean
  deploymentId: string
  bucket: BucketItem
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditBucketModal({
  open,
  deploymentId,
  bucket,
  onClose,
  onSuccess,
}: EditBucketModalProps) {
  const [values, setValues] = useState<BucketFormValues>({
    name: bucket.name,
    capacity: bucket.capacity,
    retentionDays: bucket.retentionDays,
  })

  if (!open) return null

  const dirty =
    values.capacity !== bucket.capacity ||
    values.retentionDays !== bucket.retentionDays

  async function handleSubmit() {
    const patch: { capacity?: string; retentionDays?: string } = {}
    if (values.capacity !== bucket.capacity) patch.capacity = values.capacity.trim()
    if (values.retentionDays !== bucket.retentionDays)
      patch.retentionDays = values.retentionDays.trim()
    const ref = await updateBucket({ deploymentId, bucketId: bucket.id, ...patch })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-bucket"
      title="Edit bucket"
      subtitle={`Bucket ${bucket.name}`}
      primaryLabel="Save changes"
      canSubmit={dirty}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <BucketFormFields values={values} onChange={setValues} nameReadOnly />
    </CrudFormModal>
  )
}
