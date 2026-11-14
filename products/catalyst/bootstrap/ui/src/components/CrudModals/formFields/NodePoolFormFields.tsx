/**
 * NodePoolFormFields — SKU picker + replicas slider, shared by
 * AddNodePool and EditNodePool modals.
 */

import { FormRow, NumberSlider, SelectInput } from '../_shared'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES } from '@/shared/constants/providerSizes'

export interface NodePoolFormValues {
  sku: string
  replicas: number
}

export interface NodePoolFormFieldsProps {
  values: NodePoolFormValues
  onChange: (next: NodePoolFormValues) => void
  provider: CloudProvider
}

export function NodePoolFormFields({ values, onChange, provider }: NodePoolFormFieldsProps) {
  const skus = PROVIDER_NODE_SIZES[provider] ?? []
  const skuOptions = skus.map((s) => ({
    value: s.id,
    label: `${s.label} · ${s.vcpu} vCPU · ${s.ram} GB`,
  }))

  return (
    <>
      <FormRow label="SKU" hint="Changing the SKU triggers a rolling replace of every node in the pool.">
        <SelectInput
          value={values.sku}
          onChange={(v) => onChange({ ...values, sku: v })}
          options={skuOptions}
          testId="nodepool-form-sku"
        />
      </FormRow>
      <FormRow label="Replicas" hint="0 = pause pool, 1-50 = active.">
        <NumberSlider
          value={values.replicas}
          onChange={(n) => onChange({ ...values, replicas: n })}
          min={0}
          max={50}
          testId="nodepool-form-replicas"
        />
      </FormRow>
    </>
  )
}
