/**
 * EditFirewallRulesModal — RuleSet form for managing firewall rules.
 *
 * MVP scope: append a single rule (UI for full rule-set edit lands
 * with the backend follow-up).
 */

import { useState } from 'react'
import { ModalShell, FormRow, TextInput } from './_shared'
import { addFirewallRule } from '@/lib/infrastructure-crud'
import type { FirewallSpec } from '@/lib/infrastructure.types'

export interface EditFirewallRulesModalProps {
  open: boolean
  deploymentId: string
  firewall: FirewallSpec
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function EditFirewallRulesModal({
  open,
  deploymentId,
  firewall,
  onClose,
  onSuccess,
}: EditFirewallRulesModalProps) {
  const [protocol, setProtocol] = useState('tcp')
  const [port, setPort] = useState('443')
  const [source, setSource] = useState('0.0.0.0/0')
  const [action, setAction] = useState<'allow' | 'deny'>('allow')
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  async function handleSubmit() {
    setSubmitting(true)
    try {
      const ref = await addFirewallRule({
        deploymentId,
        firewallId: firewall.id,
        rule: { protocol, port, source, action },
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('AddFirewallRule failed', err)
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
      id="edit-firewall-rules"
      open={open}
      title="Add firewall rule"
      subtitle={`Firewall ${firewall.name}`}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{ label: 'Add rule', onClick: handleSubmit, loading: submitting }}
    >
      <div
        data-testid="edit-firewall-modal-existing"
        style={{
          background: 'var(--color-bg)',
          border: '1px solid var(--color-border)',
          borderRadius: 8,
          padding: 10,
          fontSize: '0.78rem',
        }}
      >
        <div style={{ fontWeight: 600, marginBottom: 6 }}>Existing rules ({firewall.rules.length})</div>
        {firewall.rules.length === 0 ? (
          <div style={{ color: 'var(--color-text-dim)' }}>No rules — first rule will be appended.</div>
        ) : (
          firewall.rules.map((r) => (
            <div key={r.id} style={{ color: 'var(--color-text-dim)', fontFamily: 'monospace', fontSize: '0.72rem' }}>
              {r.action} {r.protocol}/{r.port} from {r.source}
            </div>
          ))
        )}
      </div>

      <FormRow label="Protocol">
        <select data-testid="edit-firewall-modal-protocol" value={protocol} onChange={(e) => setProtocol(e.target.value)} style={selectStyle}>
          <option value="tcp">tcp</option>
          <option value="udp">udp</option>
          <option value="icmp">icmp</option>
        </select>
      </FormRow>
      <FormRow label="Port(s)">
        <TextInput value={port} onChange={setPort} placeholder="443 or 8000-9000" testId="edit-firewall-modal-port" />
      </FormRow>
      <FormRow label="Source CIDR">
        <TextInput value={source} onChange={setSource} placeholder="0.0.0.0/0" testId="edit-firewall-modal-source" />
      </FormRow>
      <FormRow label="Action">
        <select
          data-testid="edit-firewall-modal-action"
          value={action}
          onChange={(e) => setAction(e.target.value as 'allow' | 'deny')}
          style={selectStyle}
        >
          <option value="allow">allow</option>
          <option value="deny">deny</option>
        </select>
      </FormRow>
    </ModalShell>
  )
}
