/**
 * VClusterFormFields — name + isolation-mode picker, used by both Add
 * and Edit vCluster modals.
 */

import { FormRow, TextInput } from '../_shared'
import type { IsolationMode } from '@/lib/infrastructure.types'

export interface VClusterFormValues {
  name: string
  isolationMode: IsolationMode
}

export interface VClusterFormFieldsProps {
  values: VClusterFormValues
  onChange: (next: VClusterFormValues) => void
}

const ISOLATION_MODES: { value: IsolationMode; label: string; sub: string }[] = [
  { value: 'dmz', label: 'DMZ', sub: 'Public-facing workloads' },
  { value: 'rtz', label: 'RTZ', sub: 'Restricted trust zone' },
  { value: 'mgmt', label: 'MGMT', sub: 'Operator / control-plane' },
]

export function VClusterFormFields({ values, onChange }: VClusterFormFieldsProps) {
  return (
    <>
      <FormRow label="vCluster name">
        <TextInput
          value={values.name}
          onChange={(v) => onChange({ ...values, name: v })}
          placeholder="e.g. acme-rtz"
          testId="vcluster-form-name"
        />
      </FormRow>
      <FormRow label="Isolation mode">
        <div
          data-testid="vcluster-form-isolation"
          style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 8 }}
        >
          {ISOLATION_MODES.map((m) => (
            <button
              key={m.value}
              type="button"
              data-testid={`vcluster-form-isolation-${m.value}`}
              onClick={() => onChange({ ...values, isolationMode: m.value })}
              style={{
                padding: '10px',
                borderRadius: 8,
                border:
                  values.isolationMode === m.value
                    ? '1.5px solid var(--color-accent)'
                    : '1px solid var(--color-border)',
                background:
                  values.isolationMode === m.value
                    ? 'color-mix(in srgb, var(--color-accent) 8%, transparent)'
                    : 'var(--color-bg)',
                color: 'var(--color-text)',
                cursor: 'pointer',
                textAlign: 'left',
              }}
            >
              <div style={{ fontWeight: 700, fontSize: '0.85rem' }}>{m.label}</div>
              <div style={{ fontSize: '0.7rem', color: 'var(--color-text-dim)', marginTop: 2 }}>
                {m.sub}
              </div>
            </button>
          ))}
        </div>
      </FormRow>
    </>
  )
}
