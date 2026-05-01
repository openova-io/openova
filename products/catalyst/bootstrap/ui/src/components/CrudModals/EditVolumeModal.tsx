/**
 * EditVolumeModal — resize an existing volume + attach/detach.
 * Volume name is immutable on most cloud providers.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { VolumeFormFields, type VolumeFormValues } from './formFields'
import { updateVolume } from '@/lib/infrastructure-crud'
import type { VolumeItem } from '@/lib/infrastructure.types'

export interface EditVolumeModalProps {
  open: boolean
  deploymentId: string
  volume: VolumeItem
  /** Worker nodes in the same region — feeds the attach-to picker. */
  nodeIds: readonly string[]
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditVolumeModal({
  open,
  deploymentId,
  volume,
  nodeIds,
  onClose,
  onSuccess,
}: EditVolumeModalProps) {
  const [values, setValues] = useState<VolumeFormValues>({
    name: volume.name,
    capacity: volume.capacity,
    attachedTo: volume.attachedTo,
  })

  if (!open) return null

  const dirty =
    values.capacity !== volume.capacity || values.attachedTo !== volume.attachedTo

  async function handleSubmit() {
    const patch: { capacity?: string; attachedTo?: string } = {}
    if (values.capacity !== volume.capacity) patch.capacity = values.capacity.trim()
    if (values.attachedTo !== volume.attachedTo)
      patch.attachedTo = values.attachedTo.trim()
    const ref = await updateVolume({ deploymentId, volumeId: volume.id, ...patch })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-volume"
      title="Edit volume"
      subtitle={`Volume ${volume.name}`}
      primaryLabel="Save changes"
      canSubmit={dirty}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <VolumeFormFields
        values={values}
        onChange={setValues}
        nodeIds={nodeIds}
        nameReadOnly
      />
    </CrudFormModal>
  )
}
