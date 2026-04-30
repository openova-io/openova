/**
 * AddVClusterModal — 1-step picker + isolation-mode selector for adding
 * a vCluster (DMZ / RTZ / MGMT) to an existing physical cluster.
 */

import { useState } from 'react'
import { ModalShell, FormRow, TextInput } from './_shared'
import { addVCluster } from '@/lib/infrastructure-crud'
import type { IsolationMode } from '@/lib/infrastructure.types'

export interface AddVClusterModalProps {
  open: boolean
  deploymentId: string
  clusterId: string
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

const ISOLATION_MODES: { value: IsolationMode; label: string; sub: string }[] = [
  { value: 'dmz', label: 'DMZ', sub: 'Public-facing workloads' },
  { value: 'rtz', label: 'RTZ', sub: 'Restricted trust zone' },
  { value: 'mgmt', label: 'MGMT', sub: 'Operator / control-plane' },
]

export function AddVClusterModal({
  open,
  deploymentId,
  clusterId,
  onClose,
  onSuccess,
}: AddVClusterModalProps) {
  const [name, setName] = useState('')
  const [mode, setMode] = useState<IsolationMode>('rtz')
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  async function handleSubmit() {
    if (!name.trim()) return
    setSubmitting(true)
    try {
      const ref = await addVCluster({
        deploymentId,
        clusterId,
        name: name.trim(),
        isolationMode: mode,
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('AddVCluster failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="add-vcluster"
      open={open}
      title="Add vCluster"
      subtitle={`Physical cluster ${clusterId}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Add vCluster',
        onClick: handleSubmit,
        loading: submitting,
        disabled: !name.trim(),
      }}
    >
      <FormRow label="vCluster name">
        <TextInput
          value={name}
          onChange={setName}
          placeholder="e.g. tenant-a-rtz"
          testId="add-vcluster-modal-name"
        />
      </FormRow>
      <FormRow label="Isolation mode">
        <div
          data-testid="add-vcluster-modal-isolation"
          style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 8 }}
        >
          {ISOLATION_MODES.map((m) => (
            <button
              key={m.value}
              type="button"
              data-testid={`add-vcluster-modal-isolation-${m.value}`}
              onClick={() => setMode(m.value)}
              style={{
                padding: '10px',
                borderRadius: 8,
                border:
                  mode === m.value
                    ? '1.5px solid var(--color-accent)'
                    : '1px solid var(--color-border)',
                background:
                  mode === m.value
                    ? 'color-mix(in srgb, var(--color-accent) 8%, transparent)'
                    : 'var(--color-bg)',
                color: 'var(--color-text)',
                cursor: 'pointer',
                textAlign: 'left',
              }}
            >
              <div style={{ fontWeight: 700, fontSize: '0.85rem' }}>{m.label}</div>
              <div style={{ fontSize: '0.7rem', color: 'var(--color-text-dim)', marginTop: 2 }}>
                {m.sub}
              </div>
            </button>
          ))}
        </div>
      </FormRow>
    </ModalShell>
  )
}
