/**
 * NodeActionConfirm — 1-click confirm dialog for cordon / drain /
 * replace actions on a worker node.
 */

import { useState } from 'react'
import { ModalShell } from './_shared'
import { nodeAction, type NodeAction } from '@/lib/infrastructure-crud'
import type { NodeSpec } from '@/lib/infrastructure.types'

export interface NodeActionConfirmProps {
  open: boolean
  deploymentId: string
  node: NodeSpec
  action: NodeAction
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

const ACTION_DESCRIPTIONS: Record<NodeAction, { title: string; body: string; danger: boolean }> = {
  cordon: {
    title: 'Cordon node',
    body: 'Marks the node unschedulable. Existing pods stay; no new pods land here. Reversible.',
    danger: false,
  },
  drain: {
    title: 'Drain node',
    body: 'Cordon + evict every non-DaemonSet pod. The node becomes empty. Workloads reschedule elsewhere — disruption expected.',
    danger: true,
  },
  replace: {
    title: 'Replace node',
    body: 'Drain + delete the underlying VM + provision a fresh one with the same SKU. The node id changes. Used for OS upgrades and stuck nodes.',
    danger: true,
  },
}

export function NodeActionConfirm({
  open,
  deploymentId,
  node,
  action,
  onClose,
  onSuccess,
}: NodeActionConfirmProps) {
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  const descriptor = ACTION_DESCRIPTIONS[action]

  async function handleSubmit() {
    setSubmitting(true)
    try {
      const ref = await nodeAction({ deploymentId, nodeId: node.id, action })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error(`Node ${action} failed`, err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id={`node-${action}`}
      open={open}
      title={descriptor.title}
      subtitle={`Node ${node.name} · ${node.role} · ${node.sku}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: descriptor.title,
        onClick: handleSubmit,
        loading: submitting,
        danger: descriptor.danger,
      }}
    >
      <p
        data-testid={`node-action-confirm-${action}-body`}
        style={{ margin: 0, fontSize: '0.85rem', color: 'var(--color-text)' }}
      >
        {descriptor.body}
      </p>
      {descriptor.danger && (
        <div
          style={{
            background: 'color-mix(in srgb, var(--color-danger) 10%, transparent)',
            border: '1px solid color-mix(in srgb, var(--color-danger) 35%, transparent)',
            borderRadius: 8,
            padding: 10,
            fontSize: '0.78rem',
            color: 'var(--color-danger)',
          }}
        >
          ⚠ Destructive action. A Job will be created and the operation can take several minutes.
        </div>
      )}
    </ModalShell>
  )
}
