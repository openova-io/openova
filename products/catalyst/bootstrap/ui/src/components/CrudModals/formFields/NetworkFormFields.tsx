/**
 * NetworkFormFields — name + CIDR. Add modal exposes both; Edit modal
 * exposes only name (CIDR is immutable post-create on every supported
 * cloud provider).
 */

import { FormRow, TextInput } from '../_shared'

export interface NetworkFormValues {
  name: string
  cidr: string
}

export interface NetworkFormFieldsProps {
  values: NetworkFormValues
  onChange: (next: NetworkFormValues) => void
  /** When true (Edit), the CIDR field is read-only. */
  cidrReadOnly?: boolean
}

export function NetworkFormFields({
  values,
  onChange,
  cidrReadOnly = false,
}: NetworkFormFieldsProps) {
  return (
    <>
      <FormRow label="Network name">
        <TextInput
          value={values.name}
          onChange={(v) => onChange({ ...values, name: v })}
          placeholder="e.g. eu-central-vpc"
          testId="network-form-name"
        />
      </FormRow>
      <FormRow label="CIDR" hint={cidrReadOnly ? 'CIDR is immutable post-create.' : 'IPv4 block — e.g. 10.0.0.0/16.'}>
        {cidrReadOnly ? (
          <input
            type="text"
            value={values.cidr}
            readOnly
            data-testid="network-form-cidr-readonly"
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
            value={values.cidr}
            onChange={(v) => onChange({ ...values, cidr: v })}
            placeholder="10.0.0.0/16"
            testId="network-form-cidr"
          />
        )}
      </FormRow>
    </>
  )
}
