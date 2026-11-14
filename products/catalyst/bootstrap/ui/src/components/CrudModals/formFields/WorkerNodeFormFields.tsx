/**
 * WorkerNodeFormFields — name + machine-type picker + role + taints.
 * Used by both Add and Edit Worker Node modals.
 */

import { FormRow, SelectInput, TextInput } from '../_shared'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES } from '@/shared/constants/providerSizes'

export interface WorkerNodeFormValues {
  name: string
  sku: string
  role: 'worker' | 'control-plane'
  taints: string
  labels: string
}

export interface WorkerNodeFormFieldsProps {
  values: WorkerNodeFormValues
  onChange: (next: WorkerNodeFormValues) => void
  provider: CloudProvider
  /** When true the name field is read-only (Edit on existing node). */
  nameReadOnly?: boolean
  /** When true the role select is read-only (existing node's role
   *  cannot be flipped in-place). */
  roleReadOnly?: boolean
}

export function WorkerNodeFormFields({
  values,
  onChange,
  provider,
  nameReadOnly = false,
  roleReadOnly = false,
}: WorkerNodeFormFieldsProps) {
  const skus = PROVIDER_NODE_SIZES[provider] ?? []
  const skuOptions = skus.map((s) => ({
    value: s.id,
    label: `${s.label} · ${s.vcpu} vCPU · ${s.ram} GB`,
  }))

  return (
    <>
      <FormRow label="Node name">
        {nameReadOnly ? (
          <input
            type="text"
            value={values.name}
            readOnly
            data-testid="worker-node-form-name-readonly"
            style={{
              padding: '8px 10px',
              borderRadius: 6,
              border: '1px solid var(--color-border)',
              background: 'var(--color-bg-2)',
              color: 'var(--color-text-dim)',
              fontSize: '0.85rem',
              fontFamily: 'monospace',
            }}
          />
        ) : (
          <TextInput
            value={values.name}
            onChange={(v) => onChange({ ...values, name: v })}
            placeholder="e.g. worker-eu-3"
            testId="worker-node-form-name"
          />
        )}
      </FormRow>
      <FormRow label="Machine type" hint="Resizing triggers a rolling replace of the node.">
        <SelectInput
          value={values.sku}
          onChange={(v) => onChange({ ...values, sku: v })}
          options={skuOptions}
          testId="worker-node-form-sku"
        />
      </FormRow>
      <FormRow label="Role">
        {roleReadOnly ? (
          <input
            type="text"
            value={values.role}
            readOnly
            data-testid="worker-node-form-role-readonly"
            style={{
              padding: '8px 10px',
              borderRadius: 6,
              border: '1px solid var(--color-border)',
              background: 'var(--color-bg-2)',
              color: 'var(--color-text-dim)',
              fontSize: '0.85rem',
              fontFamily: 'monospace',
            }}
          />
        ) : (
          <SelectInput
            value={values.role}
            onChange={(v) =>
              onChange({ ...values, role: v as 'worker' | 'control-plane' })
            }
            options={[
              { value: 'worker', label: 'Worker' },
              { value: 'control-plane', label: 'Control plane' },
            ]}
            testId="worker-node-form-role"
          />
        )}
      </FormRow>
      <FormRow label="Taints" hint="Comma-separated key=value:effect — e.g. org=dmz:NoSchedule.">
        <TextInput
          value={values.taints}
          onChange={(v) => onChange({ ...values, taints: v })}
          placeholder="org=dmz:NoSchedule"
          testId="worker-node-form-taints"
        />
      </FormRow>
      <FormRow label="Labels" hint="Comma-separated key=value pairs for selector targeting.">
        <TextInput
          value={values.labels}
          onChange={(v) => onChange({ ...values, labels: v })}
          placeholder="role=worker,tier=hot"
          testId="worker-node-form-labels"
        />
      </FormRow>
    </>
  )
}
