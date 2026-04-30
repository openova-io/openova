/**
 * AddPeeringModal — form for adding a VPC peering between two networks
 * in the topology tree.
 */

import { useState } from 'react'
import { ModalShell, FormRow, TextInput } from './_shared'
import { addPeering } from '@/lib/infrastructure-crud'
import type { NetworkSpec } from '@/lib/infrastructure.types'

export interface AddPeeringModalProps {
  open: boolean
  deploymentId: string
  /** Available networks for picking the from/to side. */
  networks: NetworkSpec[]
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddPeeringModal({
  open,
  deploymentId,
  networks,
  onClose,
  onSuccess,
}: AddPeeringModalProps) {
  const [fromVpcId, setFromVpcId] = useState(networks[0]?.id ?? '')
  const [toVpcId, setToVpcId] = useState(networks[1]?.id ?? networks[0]?.id ?? '')
  const [subnets, setSubnets] = useState('')
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  async function handleSubmit() {
    if (!fromVpcId || !toVpcId || fromVpcId === toVpcId) return
    setSubmitting(true)
    try {
      const ref = await addPeering({
        deploymentId,
        fromVpcId,
        toVpcId,
        subnets: subnets.trim() || '0.0.0.0/0',
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('AddPeering failed', err)
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
      id="add-peering"
      open={open}
      title="Add VPC peering"
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Add peering',
        onClick: handleSubmit,
        loading: submitting,
        disabled: !fromVpcId || !toVpcId || fromVpcId === toVpcId,
      }}
    >
      <FormRow label="From VPC">
        <select
          data-testid="add-peering-modal-from"
          value={fromVpcId}
          onChange={(e) => setFromVpcId(e.target.value)}
          style={selectStyle}
        >
          {networks.map((n) => (
            <option key={n.id} value={n.id}>
              {n.id} ({n.cidr})
            </option>
          ))}
        </select>
      </FormRow>
      <FormRow label="To VPC">
        <select
          data-testid="add-peering-modal-to"
          value={toVpcId}
          onChange={(e) => setToVpcId(e.target.value)}
          style={selectStyle}
        >
          {networks.map((n) => (
            <option key={n.id} value={n.id}>
              {n.id} ({n.cidr})
            </option>
          ))}
        </select>
      </FormRow>
      <FormRow label="Subnets" hint="Comma-separated CIDR list, or blank for full peering.">
        <TextInput
          value={subnets}
          onChange={setSubnets}
          placeholder="10.0.0.0/16,10.1.0.0/16"
          testId="add-peering-modal-subnets"
        />
      </FormRow>
    </ModalShell>
  )
}
