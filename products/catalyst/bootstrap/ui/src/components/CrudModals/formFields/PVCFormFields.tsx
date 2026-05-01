/**
 * PVCFormFields — name + namespace + capacity + storage class. Add
 * modal exposes all four; Edit modal exposes only `capacity` because
 * Kubernetes PVCs only support expansion (and cannot rename).
 */

import { FormRow, SelectInput, TextInput } from '../_shared'

export interface PVCFormValues {
  name: string
  namespace: string
  capacity: string
  storageClass: string
}

export interface PVCFormFieldsProps {
  values: PVCFormValues
  onChange: (next: PVCFormValues) => void
  /** Storage classes available on the Sovereign — pulled from the
   *  cloud topology tree. Empty falls back to a single "default" pick. */
  storageClasses: readonly string[]
  /** When true (Edit), name + namespace + storage class are read-only. */
  expandOnly?: boolean
}

export function PVCFormFields({
  values,
  onChange,
  storageClasses,
  expandOnly = false,
}: PVCFormFieldsProps) {
  const classes = storageClasses.length > 0 ? storageClasses : ['default']
  const classOptions = classes.map((c) => ({ value: c, label: c }))

  return (
    <>
      <FormRow label="Name">
        {expandOnly ? (
          <input
            type="text"
            value={values.name}
            readOnly
            data-testid="pvc-form-name-readonly"
            style={readOnlyInputStyle}
          />
        ) : (
          <TextInput
            value={values.name}
            onChange={(v) => onChange({ ...values, name: v })}
            placeholder="e.g. postgres-data"
            testId="pvc-form-name"
          />
        )}
      </FormRow>
      <FormRow label="Namespace">
        {expandOnly ? (
          <input
            type="text"
            value={values.namespace}
            readOnly
            data-testid="pvc-form-namespace-readonly"
            style={readOnlyInputStyle}
          />
        ) : (
          <TextInput
            value={values.namespace}
            onChange={(v) => onChange({ ...values, namespace: v })}
            placeholder="default"
            testId="pvc-form-namespace"
          />
        )}
      </FormRow>
      <FormRow
        label="Capacity"
        hint={
          expandOnly
            ? 'Kubernetes PVCs only support expand — set a value larger than the current.'
            : 'Kubernetes capacity string, e.g. 10Gi, 500Mi.'
        }
      >
        <TextInput
          value={values.capacity}
          onChange={(v) => onChange({ ...values, capacity: v })}
          placeholder="10Gi"
          testId="pvc-form-capacity"
        />
      </FormRow>
      <FormRow label="Storage class">
        {expandOnly ? (
          <input
            type="text"
            value={values.storageClass}
            readOnly
            data-testid="pvc-form-storage-class-readonly"
            style={readOnlyInputStyle}
          />
        ) : (
          <SelectInput
            value={values.storageClass}
            onChange={(v) => onChange({ ...values, storageClass: v })}
            options={classOptions}
            testId="pvc-form-storage-class"
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
