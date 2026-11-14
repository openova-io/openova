/**
 * BucketFormFields — name + capacity + retention. Add exposes all
 * three; Edit exposes capacity + retention only (rename is not
 * supported on most object stores).
 */

import { FormRow, TextInput } from '../_shared'

export interface BucketFormValues {
  name: string
  capacity: string
  retentionDays: string
}

export interface BucketFormFieldsProps {
  values: BucketFormValues
  onChange: (next: BucketFormValues) => void
  /** When true (Edit) the name field is read-only. */
  nameReadOnly?: boolean
}

export function BucketFormFields({ values, onChange, nameReadOnly = false }: BucketFormFieldsProps) {
  return (
    <>
      <FormRow label="Bucket name">
        {nameReadOnly ? (
          <input
            type="text"
            value={values.name}
            readOnly
            data-testid="bucket-form-name-readonly"
            style={readOnlyInputStyle}
          />
        ) : (
          <TextInput
            value={values.name}
            onChange={(v) => onChange({ ...values, name: v })}
            placeholder="e.g. backups-prod"
            testId="bucket-form-name"
          />
        )}
      </FormRow>
      <FormRow label="Capacity quota" hint="Allocated quota — e.g. 100Gi, 1Ti.">
        <TextInput
          value={values.capacity}
          onChange={(v) => onChange({ ...values, capacity: v })}
          placeholder="100Gi"
          testId="bucket-form-capacity"
        />
      </FormRow>
      <FormRow label="Retention (days)" hint="0 or empty = indefinite.">
        <TextInput
          value={values.retentionDays}
          onChange={(v) => onChange({ ...values, retentionDays: v })}
          placeholder="30"
          testId="bucket-form-retention"
        />
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
