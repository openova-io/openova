/**
 * AddLBModal — StepDomain-style form for adding a load balancer to a
 * region.
 */

import { useState } from 'react'
import { ModalShell, FormRow, SelectInput, TextInput } from './_shared'
import { addLB } from '@/lib/infrastructure-crud'

export interface AddLBModalProps {
  open: boolean
  deploymentId: string
  /** Pre-selected region id when triggered from a region's context
   *  menu. When empty, the modal renders a region selector. */
  regionId: string
  /** When provided (list-page entry point), the modal renders a region
   *  selector pre-filled with `regionId`. Empty array = single-region
   *  context-menu invocation, no selector shown. */
  regionIdChoices?: readonly string[]
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddLBModal({
  open,
  deploymentId,
  regionId,
  regionIdChoices,
  onClose,
  onSuccess,
}: AddLBModalProps) {
  const [name, setName] = useState('')
  const [portsCsv, setPortsCsv] = useState('80,443')
  const [pickedRegion, setPickedRegion] = useState<string>(regionId)
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  function parsePorts(): { port: number; protocol: string }[] {
    return portsCsv
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean)
      .map((p) => ({ port: parseInt(p, 10), protocol: 'tcp' }))
      .filter((p) => Number.isFinite(p.port) && p.port > 0)
  }

  const showRegionSelector = (regionIdChoices?.length ?? 0) > 0
  const effectiveRegion = showRegionSelector ? pickedRegion : regionId

  async function handleSubmit() {
    const listeners = parsePorts()
    if (!name.trim() || listeners.length === 0 || !effectiveRegion) return
    setSubmitting(true)
    try {
      const ref = await addLB({
        deploymentId,
        regionId: effectiveRegion,
        name: name.trim(),
        listeners,
      })
      onSuccess?.(ref.jobId)
      onClose()
    } catch (err) {
      console.error('AddLB failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="add-lb"
      open={open}
      title="Add load balancer"
      subtitle={effectiveRegion ? `Region ${effectiveRegion}` : 'Pick a region'}
      onClose={onClose}
      secondary={{ label: 'Cancel', onClick: onClose }}
      primary={{
        label: 'Add load balancer',
        onClick: handleSubmit,
        loading: submitting,
        disabled: !name.trim() || parsePorts().length === 0 || !effectiveRegion,
      }}
    >
      {showRegionSelector && (
        <FormRow label="Region">
          <SelectInput
            value={pickedRegion}
            onChange={setPickedRegion}
            options={(regionIdChoices ?? []).map((r) => ({ value: r, label: r }))}
            testId="add-lb-modal-region"
          />
        </FormRow>
      )}
      <FormRow label="Name">
        <TextInput
          value={name}
          onChange={setName}
          placeholder="e.g. edge-https"
          testId="add-lb-modal-name"
        />
      </FormRow>
      <FormRow label="Listener ports" hint="Comma-separated. TCP only for now.">
        <TextInput
          value={portsCsv}
          onChange={setPortsCsv}
          placeholder="80,443,6443"
          testId="add-lb-modal-ports"
        />
      </FormRow>
    </ModalShell>
  )
}
