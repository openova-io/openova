/**
 * LoadBalancerFormFields — name + listener-set CSV. Used by both Add
 * and Edit LB modals.
 */

import { FormRow, TextInput } from '../_shared'

export interface LoadBalancerFormValues {
  name: string
  /** Comma-separated port list — each entry is parsed as TCP. */
  portsCsv: string
}

export interface LoadBalancerFormFieldsProps {
  values: LoadBalancerFormValues
  onChange: (next: LoadBalancerFormValues) => void
}

export function LoadBalancerFormFields({ values, onChange }: LoadBalancerFormFieldsProps) {
  return (
    <>
      <FormRow label="Name">
        <TextInput
          value={values.name}
          onChange={(v) => onChange({ ...values, name: v })}
          placeholder="e.g. edge-https"
          testId="lb-form-name"
        />
      </FormRow>
      <FormRow label="Listener ports" hint="Comma-separated. TCP only for now.">
        <TextInput
          value={values.portsCsv}
          onChange={(v) => onChange({ ...values, portsCsv: v })}
          placeholder="80,443,6443"
          testId="lb-form-ports"
        />
      </FormRow>
    </>
  )
}

// parseLBPorts lives in ./parseLBPorts.ts so this file only exports
// React components (per react-refresh/only-export-components).
