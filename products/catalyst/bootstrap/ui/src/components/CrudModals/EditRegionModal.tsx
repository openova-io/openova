/**
 * EditRegionModal — resize default node pool + change CP/Worker SKU
 * for an existing region. Provider + providerRegion are immutable
 * (cloud-side identity).
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC patch via the
 * catalyst-api PATCH /infrastructure/regions/{id} endpoint.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { RegionFormFields, type RegionFormValues } from './formFields'
import { updateRegion } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import type { RegionSpec } from '@/lib/infrastructure.types'

export interface EditRegionModalProps {
  open: boolean
  deploymentId: string
  region: RegionSpec
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditRegionModal({
  open,
  deploymentId,
  region,
  onClose,
  onSuccess,
}: EditRegionModalProps) {
  const [values, setValues] = useState<RegionFormValues>({
    skuCp: region.skuCp,
    skuWorker: region.skuWorker,
    workerCount: region.workerCount,
  })

  if (!open) return null

  const dirty =
    values.skuCp !== region.skuCp ||
    values.skuWorker !== region.skuWorker ||
    values.workerCount !== region.workerCount

  async function handleSubmit() {
    const patch: { skuCp?: string; skuWorker?: string; workerCount?: number } = {}
    if (values.skuCp !== region.skuCp) patch.skuCp = values.skuCp
    if (values.skuWorker !== region.skuWorker) patch.skuWorker = values.skuWorker
    if (values.workerCount !== region.workerCount) patch.workerCount = values.workerCount
    const ref = await updateRegion({
      deploymentId,
      regionId: region.id,
      ...patch,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-region"
      title="Edit region"
      subtitle={`${region.provider}:${region.providerRegion}`}
      primaryLabel="Save changes"
      canSubmit={dirty}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <RegionFormFields
        values={values}
        onChange={setValues}
        provider={region.provider as CloudProvider}
        readOnlyCpSku
      />
    </CrudFormModal>
  )
}
