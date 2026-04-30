/**
 * EditDNSRecordsModal — RecordSet form for managing DNS zone records.
 *
 * MVP scope: append a single record (full record-set diff edit lands
 * with the backend follow-up).
 */

import { useState } from 'react'
import { ModalShell, FormRow, TextInput } from './_shared'
import { editDNSRecords, type DNSRecordPayload } from '@/lib/infrastructure-crud'

export interface EditDNSRecordsModalProps {
  open: boolean
  deploymentId: string
  zoneId: string
  existingRecords?: DNSRecordPayload[]
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditDNSRecordsModal({
  open,
  deploymentId,
  zoneId,
  existingRecords = [],
  onClose,
  onSuccess,
}: EditDNSRecordsModalProps) {
  const [name, setName] = useState('')
  const [type, setType] = useState('A')
  const [value, setValue] = useState('')
  const [ttl, setTtl] = useState('300')
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  async function handleSubmit() {
    if (!name.trim() || !value.trim()) return
    const newRecord: DNSRecordPayload = {
      name: name.trim(),
      type,
      value: value.trim(),
      ttl: parseInt(ttl, 10) || 300,
    }
    setSubmitting(true)
    try {
      const ref = await editDNSRecords({
        deploymentId,
        zoneId,
        records: [...existingRecords, newRecord],
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('EditDNSRecords failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  const selectStyle: React.CSSProperties = {
    padding: '8px 10px',
    borderRadius: 6,
    border: '1px solid var(--color-border)',
    background: 'var(--color-bg)',
    color: 'var(--color-text)',
    fontSize: '0.85rem',
  }

  return (
    <ModalShell
      id="edit-dns-records"
      open={open}
      title="Edit DNS zone records"
      subtitle={`Zone ${zoneId}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Save records',
        onClick: handleSubmit,
        loading: submitting,
        disabled: !name.trim() || !value.trim(),
      }}
    >
      <FormRow label="Record name">
        <TextInput value={name} onChange={setName} placeholder="api" testId="edit-dns-modal-name" />
      </FormRow>
      <FormRow label="Type">
        <select data-testid="edit-dns-modal-type" value={type} onChange={(e) => setType(e.target.value)} style={selectStyle}>
          {['A', 'AAAA', 'CNAME', 'TXT', 'MX'].map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
      </FormRow>
      <FormRow label="Value">
        <TextInput value={value} onChange={setValue} placeholder="116.203.42.1" testId="edit-dns-modal-value" />
      </FormRow>
      <FormRow label="TTL (seconds)">
        <TextInput value={ttl} onChange={setTtl} testId="edit-dns-modal-ttl" />
      </FormRow>
    </ModalShell>
  )
}
