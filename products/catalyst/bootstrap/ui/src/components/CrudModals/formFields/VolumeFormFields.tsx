/**
 * VolumeFormFields — name + capacity + attach-target. Add exposes all
 * three; Edit exposes capacity + attach-target only (cloud volume
 * names are immutable on most providers).
 */

import { FormRow, SelectInput, TextInput } from '../_shared'

export interface VolumeFormValues {
  name: string
  capacity: string
  attachedTo: string
}

export interface VolumeFormFieldsProps {
  values: VolumeFormValues
  onChange: (next: VolumeFormValues) => void
  /** Available worker node ids — when present the attach-target uses
   *  a select; otherwise falls back to a text input. */
  nodeIds?: readonly string[]
  /** When true (Edit) the name field is read-only. */
  nameReadOnly?: boolean
}

export function VolumeFormFields({
  values,
  onChange,
  nodeIds = [],
  nameReadOnly = false,
}: VolumeFormFieldsProps) {
  const attachOptions = [
    { value: '', label: '— Detached —' },
    ...nodeIds.map((n) => ({ value: n, label: n })),
  ]

  return (
    <>
      <FormRow label="Volume name">
        {nameReadOnly ? (
          <input
            type="text"
            value={values.name}
            readOnly
            data-testid="volume-form-name-readonly"
            style={readOnlyInputStyle}
          />
        ) : (
          <TextInput
            value={values.name}
            onChange={(v) => onChange({ ...values, name: v })}
            placeholder="e.g. postgres-data-eu"
            testId="volume-form-name"
          />
        )}
      </FormRow>
      <FormRow label="Capacity" hint="Provider sizes vary — typical block 10Gi to 10Ti.">
        <TextInput
          value={values.capacity}
          onChange={(v) => onChange({ ...values, capacity: v })}
          placeholder="50Gi"
          testId="volume-form-capacity"
        />
      </FormRow>
      <FormRow label="Attached to" hint="Pick a worker node id or leave detached.">
        {nodeIds.length > 0 ? (
          <SelectInput
            value={values.attachedTo}
            onChange={(v) => onChange({ ...values, attachedTo: v })}
            options={attachOptions}
            testId="volume-form-attached-to"
          />
        ) : (
          <TextInput
            value={values.attachedTo}
            onChange={(v) => onChange({ ...values, attachedTo: v })}
            placeholder="(leave empty for detached)"
            testId="volume-form-attached-to"
          />
        )}
      </FormRow>
    </>
  )
}

const readOnlyInputStyle: React.CSSProperties = {
  padding: '8px 10px',
  borderRadius: 6,
  border: '1px solid var(--color-border)',
  background: 'var(--color-bg-2)',
  color: 'var(--color-text-dim)',
  fontSize: '0.85rem',
  fontFamily: 'monospace',
}
