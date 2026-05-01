/**
 * EditLBModal — rename + listener-set rewrite on an existing
 * LoadBalancer.
 *
 * Backend seam (ADR-0001 §9.2 row B3): Crossplane XRC patch via
 * catalyst-api PATCH /infrastructure/loadbalancers/{id}.
 */

import { useState } from 'react'
import { CrudFormModal } from './crudModalScaffold'
import {
  LoadBalancerFormFields,
  parseLBPorts,
  type LoadBalancerFormValues,
} from './formFields'
import { updateLB } from '@/lib/infrastructure-crud'
import type { LoadBalancerSpec } from '@/lib/infrastructure.types'

export interface EditLBModalProps {
  open: boolean
  deploymentId: string
  lb: LoadBalancerSpec
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditLBModal({
  open,
  deploymentId,
  lb,
  onClose,
  onSuccess,
}: EditLBModalProps) {
  const [values, setValues] = useState<LoadBalancerFormValues>({
    name: lb.name,
    portsCsv: (lb.listeners ?? []).map((l) => String(l.port)).join(','),
  })

  if (!open) return null

  const newListeners = parseLBPorts(values.portsCsv)
  const dirty =
    values.name !== lb.name ||
    JSON.stringify(newListeners) !==
      JSON.stringify(
        (lb.listeners ?? []).map((l) => ({ port: l.port, protocol: l.protocol })),
      )
  const canSubmit = dirty && values.name.trim().length > 0 && newListeners.length > 0

  async function handleSubmit() {
    const patch: { name?: string; listeners?: { port: number; protocol: string }[] } = {}
    if (values.name !== lb.name) patch.name = values.name.trim()
    patch.listeners = newListeners
    const ref = await updateLB({ deploymentId, lbId: lb.id, ...patch })
    onSuccess?.(ref.jobId)
    onClose()
  }

  return (
    <CrudFormModal
      open={open}
      id="edit-lb"
      title="Edit load balancer"
      subtitle={`LB ${lb.id}`}
      primaryLabel="Save changes"
      canSubmit={canSubmit}
      onSubmit={handleSubmit}
      onClose={onClose}
    >
      <LoadBalancerFormFields values={values} onChange={setValues} />
    </CrudFormModal>
  )
}
