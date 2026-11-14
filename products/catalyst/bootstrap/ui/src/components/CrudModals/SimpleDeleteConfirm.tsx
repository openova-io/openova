/**
 * SimpleDeleteConfirm — non-cascade delete confirm for resources whose
 * removal does NOT propagate (PVC, Volume, Bucket, WorkerNode,
 * Network, LoadBalancer, NodePool). Cascade-aware deletes
 * (Region/Cluster/vCluster) keep DeleteCascadeConfirm.tsx.
 *
 * Backend seam: catalyst-api DELETE /infrastructure/{kind}/{id} —
 * routes through DeleteXRC for cloud kinds, dynamic-client delete for
 * K8s-native CRs (PVC). Either way the catalyst-api never calls a
 * cloud API directly (ADR-0001 §9.2 row B3).
 */

import { useState } from 'react'
import { DeleteConfirmShell } from './_shared'
import { cascadeDelete, type DeletableResource } from '@/lib/infrastructure-crud'

export interface SimpleDeleteConfirmProps {
  open: boolean
  deploymentId: string
  resource: DeletableResource
  resourceId: string
  resourceLabel: string
  resourceKind: string
  /** Optional warning override; falls back to a generic line. */
  warning?: React.ReactNode
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function SimpleDeleteConfirm({
  open,
  deploymentId,
  resource,
  resourceId,
  resourceLabel,
  resourceKind,
  warning,
  onClose,
  onSuccess,
}: SimpleDeleteConfirmProps) {
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  async function handleConfirm() {
    setSubmitting(true)
    try {
      const ref = await cascadeDelete({ deploymentId, resource, resourceId })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error(`SimpleDeleteConfirm ${resource}/${resourceId} failed`, err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <DeleteConfirmShell
      open={open}
      id={resource}
      resourceKind={resourceKind}
      resourceLabel={resourceLabel}
      warning={warning}
      onClose={onClose}
      onConfirm={handleConfirm}
      loading={submitting}
    />
  )
}
