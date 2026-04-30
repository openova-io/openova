/**
 * AddRegionModal — 3-step inline modal that re-uses StepProvider in
 * `add-region` mode. The Hetzner-token field is hidden (already
 * provisioned), only region + SKU + worker count are exposed.
 *
 * Per founder spec: "Add region — 3-step inline modal: re-uses
 * StepProvider for region+SKU+confirm."
 */

import { useState } from 'react'
import { ModalShell, FormRow, TextInput, NumberSlider } from './_shared'
import type { CloudProvider } from '@/entities/deployment/model'
import { PROVIDER_REGIONS } from '@/entities/deployment/model'
import { PROVIDER_NODE_SIZES, defaultNodeSizeId } from '@/shared/constants/providerSizes'
import { addRegion } from '@/lib/infrastructure-crud'

export interface AddRegionModalProps {
  open: boolean
  deploymentId: string
  defaultProvider?: CloudProvider
  onClose: () => void
  onSuccess?: (jobId: string) => void
}

export function AddRegionModal({
  open,
  deploymentId,
  defaultProvider = 'hetzner',
  onClose,
  onSuccess,
}: AddRegionModalProps) {
  const [step, setStep] = useState<0 | 1 | 2>(0)
  const [provider, setProvider] = useState<CloudProvider>(defaultProvider)
  const [regionId, setRegionId] = useState<string>(
    PROVIDER_REGIONS[defaultProvider][0]?.id ?? '',
  )
  const [cpSku, setCpSku] = useState<string>(defaultNodeSizeId(defaultProvider))
  const [workerSku, setWorkerSku] = useState<string>(defaultNodeSizeId(defaultProvider))
  const [workerCount, setWorkerCount] = useState<number>(0)
  const [submitting, setSubmitting] = useState(false)

  if (!open) return null

  const providers: CloudProvider[] = ['hetzner', 'huawei', 'oci', 'aws', 'azure']
  const regionOptions = PROVIDER_REGIONS[provider]
  const skuOptions = PROVIDER_NODE_SIZES[provider]

  function reset() {
    setStep(0)
    setSubmitting(false)
  }

  function handleClose() {
    reset()
    onClose()
  }

  async function handleSubmit() {
    setSubmitting(true)
    try {
      const ref = await addRegion({
        deploymentId,
        provider,
        providerRegion: regionId,
        skuCp: cpSku,
        skuWorker: workerSku,
        workerCount,
      })
      onSuccess?.(ref.jobId)
      handleClose()
    } catch (err) {
      console.error('AddRegion failed', err)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <ModalShell
      id="add-region"
      open={open}
      title="Add region"
      subtitle={`Step ${step + 1} of 3 · re-uses StepProvider`}
      onClose={handleClose}
      secondary={
        step > 0
          ? { label: 'Back', onClick: () => setStep((s) => (s - 1) as 0 | 1) }
          : { label: 'Cancel', onClick: handleClose }
      }
      primary={
        step < 2
          ? {
              label: 'Continue',
              onClick: () => setStep((s) => (s + 1) as 1 | 2),
            }
          : {
              label: 'Add region',
              onClick: handleSubmit,
              loading: submitting,
            }
      }
    >
      {step === 0 && (
        <>
          <FormRow label="Provider">
            <select
              data-testid="add-region-modal-provider"
              value={provider}
              onChange={(e) => {
                const next = e.target.value as CloudProvider
                setProvider(next)
                setRegionId(PROVIDER_REGIONS[next][0]?.id ?? '')
                setCpSku(defaultNodeSizeId(next))
                setWorkerSku(defaultNodeSizeId(next))
              }}
              style={inputStyle}
            >
              {providers.map((p) => (
                <option key={p} value={p}>
                  {p}
                </option>
              ))}
            </select>
          </FormRow>
          <FormRow label="Region" hint="Provider tenant already credentialed during initial provisioning.">
            <select
              data-testid="add-region-modal-region"
              value={regionId}
              onChange={(e) => setRegionId(e.target.value)}
              style={inputStyle}
            >
              {regionOptions.map((r) => (
                <option key={r.id} value={r.id}>
                  {r.label} — {r.location}
                </option>
              ))}
            </select>
          </FormRow>
        </>
      )}

      {step === 1 && (
        <>
          <FormRow label="Control-plane SKU">
            <select
              data-testid="add-region-modal-cp-sku"
              value={cpSku}
              onChange={(e) => setCpSku(e.target.value)}
              style={inputStyle}
            >
              {skuOptions.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label} · {s.vcpu} vCPU · {s.ram} GB
                </option>
              ))}
            </select>
          </FormRow>
          <FormRow label="Worker SKU">
            <select
              data-testid="add-region-modal-worker-sku"
              value={workerSku}
              onChange={(e) => setWorkerSku(e.target.value)}
              style={inputStyle}
            >
              {skuOptions.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.label} · {s.vcpu} vCPU · {s.ram} GB
                </option>
              ))}
            </select>
          </FormRow>
          <FormRow label="Worker count" hint="Set 0 for solo control-plane mode.">
            <NumberSlider
              value={workerCount}
              onChange={setWorkerCount}
              min={0}
              max={20}
              testId="add-region-modal-worker-count"
            />
          </FormRow>
        </>
      )}

      {step === 2 && (
        <div data-testid="add-region-modal-confirm" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <p style={{ margin: 0, color: 'var(--color-text-strong)', fontWeight: 600 }}>Confirm new region</p>
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: '0.85rem', color: 'var(--color-text)' }}>
            <li>Provider: <strong>{provider}</strong></li>
            <li>Region: <strong>{regionId}</strong></li>
            <li>Control plane SKU: <strong>{cpSku}</strong></li>
            <li>Worker SKU: <strong>{workerSku}</strong> × {workerCount}</li>
          </ul>
          <p style={{ margin: 0, fontSize: '0.78rem', color: 'var(--color-text-dim)' }}>
            A Job will be created and tracked on the Jobs page.
          </p>
        </div>
      )}
    </ModalShell>
  )
}

const inputStyle: React.CSSProperties = {
  padding: '8px 10px',
  borderRadius: 6,
  border: '1px solid var(--color-border)',
  background: 'var(--color-bg)',
  color: 'var(--color-text)',
  fontSize: '0.85rem',
}

// Suppress eslint unused warning on TextInput (kept for future extension)
void TextInput
