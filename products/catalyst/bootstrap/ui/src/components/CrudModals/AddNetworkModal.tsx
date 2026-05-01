/**
 * AddNetworkModal — provision a VPC / DRG (cloud network) inside an
 * existing region.
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC POST to
 * /infrastructure/networks.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { NetworkFormFields, type NetworkFormValues } from './formFields'
import { addNetwork } from '@/lib/infrastructure-crud'
import { FormRow, SelectInput } from './_shared'

export interface AddNetworkModalProps {
  open: boolean
  deploymentId: string
  /** Available region ids to attach the network to. */
  regionIds: readonly string[]
  /** Default region id (when called from a region context). */
  defaultRegionId?: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddNetworkModal({
  open,
  deploymentId,
  regionIds,
  defaultRegionId,
  onClose,
  onSuccess,
}: AddNetworkModalProps) {
  const [regionId, setRegionId] = useState<string>(
    defaultRegionId ?? regionIds[0] ?? '',
  )
  const [values, setValues] = useState<NetworkFormValues>({
    name: '',
    cidr: '10.0.0.0/16',
  })

  if (!open) return null

  const canSubmit =
    values.name.trim().length > 0 &&
    values.cidr.trim().length > 0 &&
    regionId.trim().length > 0

  async function handleSubmit() {
    const ref = await addNetwork({
      deploymentId,
      regionId,
      name: values.name.trim(),
      cidr: values.cidr.trim(),
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="add-network"
      title="Add network"
      subtitle="VPC / DRG inside a Sovereign region"
      primaryLabel="Add network"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <FormRow label="Region">
        <SelectInput
          value={regionId}
          onChange={setRegionId}
          options={regionIds.map((r) => ({ value: r, label: r }))}
          testId="add-network-modal-region"
        />
      </FormRow>
      <NetworkFormFields values={values} onChange={setValues} />
    </CrudFormModal>
  )
}
