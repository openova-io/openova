/**
 * RegionFormFields — typed form fields used by both AddRegionModal and
 * EditRegionModal. The Add modal is a 3-step wizard (kept in
 * AddRegionModal for legacy reasons) so this file only feeds the Edit
 * surface — but it stays here so future consolidation lives in one
 * place.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 — every option flows from
 * shared/constants, no inlined SKU strings.
 */

import { FormRow, NumberSlider, SelectInput } from '../_shared'
import type { CloudProvider } from '@/entities/deployment/model'
import {
  PROVIDER_NODE_SIZES,
} from '@/shared/constants/providerSizes'

export interface RegionFormValues {
  skuCp: string
  skuWorker: string
  workerCount: number
}

export interface RegionFormFieldsProps {
  values: RegionFormValues
  onChange: (next: RegionFormValues) => void
  provider: CloudProvider
  /** When true, the Worker count slider stays enabled but the
   *  control-plane-SKU select is read-only (existing region's CP is
   *  immutable in-place). */
  readOnlyCpSku?: boolean
}

export function RegionFormFields({
  values,
  onChange,
  provider,
  readOnlyCpSku = false,
}: RegionFormFieldsProps) {
  const skus = PROVIDER_NODE_SIZES[provider] ?? []
  const skuOptions = skus.map((s) => ({
    value: s.id,
    label: `${s.label} · ${s.vcpu} vCPU · ${s.ram} GB`,
  }))

  return (
    <>
      <FormRow
        label="Control-plane SKU"
        hint={readOnlyCpSku ? 'Existing CP SKU is immutable — resize via NodePool replace.' : undefined}
      >
        {readOnlyCpSku ? (
          <input
            type="text"
            value={values.skuCp}
            readOnly
            data-testid="edit-region-modal-cp-sku-readonly"
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
            value={values.skuCp}
            onChange={(v) => onChange({ ...values, skuCp: v })}
            options={skuOptions}
            testId="region-form-cp-sku"
          />
        )}
      </FormRow>
      <FormRow label="Worker SKU" hint="Used for new node pools spawned in this region.">
        <SelectInput
          value={values.skuWorker}
          onChange={(v) => onChange({ ...values, skuWorker: v })}
          options={skuOptions}
          testId="region-form-worker-sku"
        />
      </FormRow>
      <FormRow label="Worker count" hint="Resizes the default node pool. Set 0 for solo control-plane mode.">
        <NumberSlider
          value={values.workerCount}
          onChange={(n) => onChange({ ...values, workerCount: n })}
          min={0}
          max={20}
          testId="region-form-worker-count"
        />
      </FormRow>
    </>
  )
}
