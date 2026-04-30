/**
 * AddClusterModal — 1-step modal that re-uses StepTopology's
 * cluster-spec form vocabulary (cluster name + version + control-plane
 * SKU) for adding a cluster to an existing region.
 *
 * Per founder spec: "Add cluster — 1-step: re-uses StepTopology
 * cluster-spec form."
 */

import { useState } from 'react'
import { ModalShell, FormRow, TextInput } from './_shared'
import { addCluster } from '@/lib/infrastructure-crud'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES, defaultNodeSizeId } from '@/shared/constants/providerSizes'

const DEFAULT_VERSION = 'v1.31.4+k3s1'

export interface AddClusterModalProps {
  open: boolean
  deploymentId: string
  regionId: string
  regionProvider: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddClusterModal({
  open,
  deploymentId,
  regionId,
  regionProvider,
  onClose,
  onSuccess,
}: AddClusterModalProps) {
  const [name, setName] = useState('')
  const [version, setVersion] = useState(DEFAULT_VERSION)
  const [cpSku, setCpSku] = useState(defaultNodeSizeId(regionProvider))
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  const skuOptions = PROVIDER_NODE_SIZES[regionProvider] ?? []

  async function handleSubmit() {
    if (!name.trim()) return
    setSubmitting(true)
    try {
      const ref = await addCluster({
        deploymentId,
        regionId,
        name: name.trim(),
        version,
        controlPlaneSku: cpSku,
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('AddCluster failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="add-cluster"
      open={open}
      title="Add cluster"
      subtitle={`Region ${regionId}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Add cluster',
        onClick: handleSubmit,
        loading: submitting,
        disabled: !name.trim(),
      }}
    >
      <FormRow label="Cluster name">
        <TextInput
          value={name}
          onChange={setName}
          placeholder="e.g. omantel-tertiary"
          testId="add-cluster-modal-name"
        />
      </FormRow>
      <FormRow label="k3s version">
        <TextInput
          value={version}
          onChange={setVersion}
          testId="add-cluster-modal-version"
        />
      </FormRow>
      <FormRow label="Control-plane SKU">
        <select
          data-testid="add-cluster-modal-cp-sku"
          value={cpSku}
          onChange={(e) => setCpSku(e.target.value)}
          style={{
            padding: '8px 10px',
            borderRadius: 6,
            border: '1px solid var(--color-border)',
            background: 'var(--color-bg)',
            color: 'var(--color-text)',
            fontSize: '0.85rem',
          }}
        >
          {skuOptions.map((s) => (
            <option key={s.id} value={s.id}>
              {s.label} · {s.vcpu} vCPU · {s.ram} GB
            </option>
          ))}
        </select>
      </FormRow>
    </ModalShell>
  )
}
