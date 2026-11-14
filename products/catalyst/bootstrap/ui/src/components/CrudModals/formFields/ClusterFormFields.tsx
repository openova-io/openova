/**
 * ClusterFormFields — typed form fields shared by the Cluster Add and
 * Edit modals. Add: name + version + CP SKU. Edit: same set, but
 * `name` is the rename surface and CP SKU resize triggers a CP
 * replace.
 */

import { FormRow, SelectInput, TextInput } from '../_shared'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES } from '@/shared/constants/providerSizes'

export interface ClusterFormValues {
  name: string
  version: string
  controlPlaneSku: string
}

export interface ClusterFormFieldsProps {
  values: ClusterFormValues
  onChange: (next: ClusterFormValues) => void
  provider: CloudProvider
}

export function ClusterFormFields({
  values,
  onChange,
  provider,
}: ClusterFormFieldsProps) {
  const skus = PROVIDER_NODE_SIZES[provider] ?? []
  const skuOptions = skus.map((s) => ({
    value: s.id,
    label: `${s.label} · ${s.vcpu} vCPU · ${s.ram} GB`,
  }))

  return (
    <>
      <FormRow label="Cluster name">
        <TextInput
          value={values.name}
          onChange={(v) => onChange({ ...values, name: v })}
          placeholder="e.g. omantel-tertiary"
          testId="cluster-form-name"
        />
      </FormRow>
      <FormRow label="k3s version" hint="Editing triggers a rolling control-plane upgrade.">
        <TextInput
          value={values.version}
          onChange={(v) => onChange({ ...values, version: v })}
          testId="cluster-form-version"
        />
      </FormRow>
      <FormRow label="Control-plane SKU" hint="Resizing triggers a control-plane replace.">
        <SelectInput
          value={values.controlPlaneSku}
          onChange={(v) => onChange({ ...values, controlPlaneSku: v })}
          options={skuOptions}
          testId="cluster-form-cp-sku"
        />
      </FormRow>
    </>
  )
}
