import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { CheckCircle2, Server, Globe, Package, Cpu, Shield } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { PLATFORM_COMPONENTS } from '@/shared/constants/components'
import { HETZNER_NODE_SIZES } from '@/shared/constants/hetzner'
import { Badge } from '@/shared/ui/badge'
import { Button } from '@/shared/ui/button'

function ReviewRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4 py-2.5">
      <span className="text-xs text-[oklch(45%_0.01_250)] shrink-0 w-36">{label}</span>
      <span className="text-xs text-[oklch(80%_0.01_250)] text-right flex-1">{value}</span>
    </div>
  )
}

function Section({ icon: Icon, title, children }: { icon: React.ElementType; title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-1] overflow-hidden">
      <div className="flex items-center gap-2.5 border-b border-[--color-surface-border] px-4 py-3">
        <Icon className="h-4 w-4 text-[oklch(45%_0.01_250)]" />
        <span className="text-sm font-semibold text-[oklch(80%_0.01_250)]">{title}</span>
      </div>
      <div className="px-4 divide-y divide-[--color-surface-border]">
        {children}
      </div>
    </div>
  )
}

export function StepReview() {
  const store = useWizardStore()
  const navigate = useNavigate()
  const [provisioning, setProvisioning] = useState(false)
  const { back } = { back: () => store.setStep(store.currentStep - 1) }

  const cpSize = HETZNER_NODE_SIZES.find((s) => s.id === store.controlPlaneSize)
  const wkSize = HETZNER_NODE_SIZES.find((s) => s.id === store.workerSize)
  const mandatoryCount = PLATFORM_COMPONENTS.filter((c) => c.required).length

  const regionCount = store.regions.length || 1
  const cpCost = (cpSize?.priceHour ?? 0) * regionCount
  const wkCost = (wkSize?.priceHour ?? 0) * store.workerCount * regionCount
  const totalHour = cpCost + wkCost

  // Derived cluster context names per naming convention
  const clusterContexts = store.regions.map((r) => {
    const env = 'prod'
    const bb = 'rtz'
    return `hz-${r.id}-${bb}-${env}`
  })

  async function provision() {
    setProvisioning(true)
    // TODO: POST /api/v1/deployments
    await new Promise((r) => setTimeout(r, 800))
    await navigate({ to: '/provision' })
  }

  return (
    <div className="flex flex-col gap-6">
      <div>
        <h2 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Review and provision</h2>
        <p className="mt-1.5 text-sm text-[oklch(50%_0.01_250)]">
          Verify your configuration. Provisioning will begin immediately after confirmation.
        </p>
      </div>

      {/* Organisation */}
      <Section icon={Globe} title="Organisation">
        <ReviewRow label="Name" value={store.orgName || '—'} />
        <ReviewRow label="Domain" value={store.orgDomain || '—'} />
        <ReviewRow label="Contact" value={store.orgEmail || '—'} />
      </Section>

      {/* Infrastructure */}
      <Section icon={Server} title="Infrastructure">
        <ReviewRow
          label="Provider"
          value={<Badge variant="brand">Hetzner Cloud</Badge>}
        />
        <ReviewRow
          label="Clusters"
          value={
            <div className="flex flex-col items-end gap-1">
              {clusterContexts.length > 0
                ? clusterContexts.map((ctx) => (
                    <code key={ctx} className="text-[10px] font-mono text-[--color-brand-300] bg-[--color-brand-500]/10 px-1.5 py-0.5 rounded">
                      {ctx}
                    </code>
                  ))
                : '—'}
            </div>
          }
        />
        <ReviewRow
          label="Control plane"
          value={`${cpSize?.label ?? '—'} · ${cpSize?.vcpu} vCPU · ${cpSize?.ram} GB RAM`}
        />
        <ReviewRow
          label="Workers"
          value={store.workerCount > 0 ? `${store.workerCount}× ${wkSize?.label}` : 'None (control-plane only)'}
        />
        <ReviewRow
          label="HA mode"
          value={store.haEnabled ? '3-node etcd per region' : 'Single control-plane'}
        />
      </Section>

      {/* Components */}
      <Section icon={Package} title="Components">
        <ReviewRow label="Core (required)" value={`${mandatoryCount} components`} />
        <ReviewRow label="Optional selected" value={`${store.selectedComponents.length} components`} />
        <ReviewRow
          label="Selected"
          value={
            store.selectedComponents.length > 0 ? (
              <div className="flex flex-wrap gap-1 justify-end">
                {store.selectedComponents.map((c) => (
                  <Badge key={c.id} variant="default" className="text-[10px]">{c.name}</Badge>
                ))}
              </div>
            ) : 'Core only'
          }
        />
      </Section>

      {/* Cost */}
      <div className="rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-2] p-4 flex items-start gap-3">
        <Cpu className="h-4 w-4 text-[oklch(45%_0.01_250)] mt-0.5 shrink-0" />
        <div className="flex-1">
          <p className="text-xs font-semibold text-[oklch(70%_0.01_250)]">Estimated running cost</p>
          <p className="mt-1 text-lg font-bold font-mono text-[oklch(92%_0.01_250)]">
            €{totalHour.toFixed(3)}<span className="text-sm font-normal text-[oklch(50%_0.01_250)]">/hr</span>
          </p>
          <p className="mt-1 text-xs text-[oklch(45%_0.01_250)]">
            Hetzner bills hourly. Delete the cluster and billing stops at that hour.
          </p>
        </div>
      </div>

      {/* Security notice */}
      <div className="flex items-start gap-2.5 text-xs text-[oklch(45%_0.01_250)]">
        <Shield className="h-4 w-4 shrink-0 mt-0.5" />
        <p>
          Your API token is used only during provisioning. It is passed directly to the OpenTofu module running in your browser session and is not stored on any server.
        </p>
      </div>

      {/* Actions */}
      <div className="flex items-center justify-between pt-2">
        <Button variant="ghost" size="md" onClick={back}>
          Back
        </Button>
        <div className="flex items-center gap-3">
          <Button variant="secondary" size="md" onClick={back}>
            Edit
          </Button>
          <Button
            size="md"
            loading={provisioning}
            onClick={provision}
            className="min-w-36"
          >
            {!provisioning && <CheckCircle2 className="h-4 w-4" />}
            {provisioning ? 'Starting…' : 'Provision cluster'}
          </Button>
        </div>
      </div>
    </div>
  )
}
