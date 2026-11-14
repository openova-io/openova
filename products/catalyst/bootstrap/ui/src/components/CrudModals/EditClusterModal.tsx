/**
 * EditClusterModal — patch fields supported on a Cluster: rename,
 * version (k3s upgrade), control-plane SKU resize. Surfaces only the
 * fields the catalyst-api supports updating safely.
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC patch via the
 * catalyst-api PATCH endpoint at
 * /infrastructure/clusters/{id} — never a direct cloud-provider API
 * call.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import { ClusterFormFields, type ClusterFormValues } from './formFields'
import { updateCluster } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import type { ClusterSpec } from '@/lib/infrastructure.types'

export interface EditClusterModalProps {
  open: boolean
  deploymentId: string
  cluster: ClusterSpec
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditClusterModal({
  open,
  deploymentId,
  cluster,
  regionProvider,
  onClose,
  onSuccess,
}: EditClusterModalProps) {
  const [values, setValues] = useState<ClusterFormValues>({
    name: cluster.name,
    version: cluster.version,
    // catalyst-api doesn't expose CP SKU per-cluster on the read side
    // today (it's surfaced on the parent Region). Edit modal surfaces
    // it as a free-text field defaulting to "" so the operator can
    // explicitly opt into a CP resize. Empty string means "leave as-is".
    controlPlaneSku: '',
  })

  if (!open) return null

  const canSubmit =
    values.name.trim().length > 0 &&
    (values.name !== cluster.name ||
      values.version !== cluster.version ||
      values.controlPlaneSku.length > 0)

  async function handleSubmit() {
    const patch: {
      name?: string
      version?: string
      controlPlaneSku?: string
    } = {}
    if (values.name !== cluster.name) patch.name = values.name.trim()
    if (values.version !== cluster.version) patch.version = values.version.trim()
    if (values.controlPlaneSku.length > 0) patch.controlPlaneSku = values.controlPlaneSku
    const ref = await updateCluster({
      deploymentId,
      clusterId: cluster.id,
      ...patch,
    })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-cluster"
      title="Edit cluster"
      subtitle={`Cluster ${cluster.id}`}
      primaryLabel="Save changes"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <ClusterFormFields
        values={values}
        onChange={setValues}
        provider={regionProvider}
      />
    </CrudFormModal>
  )
}
