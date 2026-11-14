/**
 * EditNetworkModal — rename an existing network. CIDR is immutable
 * post-create on every supported provider.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { NetworkFormFields, type NetworkFormValues } from './formFields'
import { updateNetwork } from '@/lib/infrastructure-crud'
import type { NetworkSpec } from '@/lib/infrastructure.types'

export interface EditNetworkModalProps {
  open: boolean
  deploymentId: string
  network: NetworkSpec
  /** Display name when the spec doesn't carry one — fall back to id. */
  fallbackLabel?: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditNetworkModal({
  open,
  deploymentId,
  network,
  fallbackLabel,
  onClose,
  onSuccess,
}: EditNetworkModalProps) {
  const [values, setValues] = useState<NetworkFormValues>({
    name: fallbackLabel ?? network.id,
    cidr: network.cidr,
  })

  if (!open) return null

  const dirty = values.name.trim() !== (fallbackLabel ?? network.id)

  async function handleSubmit() {
    const ref = await updateNetwork({
      deploymentId,
      networkId: network.id,
      name: values.name.trim(),
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-network"
      title="Edit network"
      subtitle={`Network ${network.id}`}
      primaryLabel="Save changes"
      canSubmit={dirty && values.name.trim().length > 0}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <NetworkFormFields values={values} onChange={setValues} cidrReadOnly />
    </CrudFormModal>
  )
}
