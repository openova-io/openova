/**
 * AddVolumeModal — provision a cloud block volume into a region.
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC POST to
 * /infrastructure/volumes.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { VolumeFormFields, type VolumeFormValues } from './formFields'
import { addVolume } from '@/lib/infrastructure-crud'
import { FormRow, SelectInput } from './_shared'

export interface AddVolumeModalProps {
  open: boolean
  deploymentId: string
  /** Available region ids — feeds the region selector. */
  regionIds: readonly string[]
  /** Available worker node ids in the picked region (for attach). */
  nodeIds: readonly string[]
  defaultRegionId?: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddVolumeModal({
  open,
  deploymentId,
  regionIds,
  nodeIds,
  defaultRegionId,
  onClose,
  onSuccess,
}: AddVolumeModalProps) {
  const [regionId, setRegionId] = useState<string>(
    defaultRegionId ?? regionIds[0] ?? '',
  )
  const [values, setValues] = useState<VolumeFormValues>({
    name: '',
    capacity: '50Gi',
    attachedTo: '',
  })

  if (!open) return null

  const canSubmit =
    values.name.trim().length > 0 &&
    values.capacity.trim().length > 0 &&
    regionId.trim().length > 0

  async function handleSubmit() {
    const ref = await addVolume({
      deploymentId,
      regionId,
      name: values.name.trim(),
      capacity: values.capacity.trim(),
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="add-volume"
      title="Add volume"
      subtitle="Block storage volume"
      primaryLabel="Add volume"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <FormRow label="Region">
        <SelectInput
          value={regionId}
          onChange={setRegionId}
          options={regionIds.map((r) => ({ value: r, label: r }))}
          testId="add-volume-modal-region"
        />
      </FormRow>
      <VolumeFormFields values={values} onChange={setValues} nodeIds={nodeIds} />
    </CrudFormModal>
  )
}
