import { useState } from 'react'
import { Plus, X, Cpu, MemoryStick, HardDrive, Info } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { HETZNER_REGIONS, HETZNER_NODE_SIZES } from '@/shared/constants/hetzner'
import type { NodeSize, Region } from '@/entities/deployment/model'
import { Badge } from '@/shared/ui/badge'
import { Switch } from '@/shared/ui/switch'
import { Separator } from '@/shared/ui/separator'
import { cn } from '@/shared/lib/utils'
import { StepShell, useStepNav } from './_shared'

function RegionPill({ region, role, onRemove }: { region: Region; role: 'A' | 'B'; onRemove: () => void }) {
  return (
    <div className="flex items-center gap-2 rounded-[--radius-md] border border-[--color-brand-500]/30 bg-[--color-brand-500]/8 px-3 py-2">
      <span className="text-xs font-semibold text-[--color-brand-300] w-5">{region.flag}</span>
      <div className="flex-1">
        <p className="text-xs font-semibold text-[oklch(85%_0.01_250)]">{region.name}</p>
        <p className="text-xs text-[oklch(45%_0.01_250)]">{region.location}</p>
      </div>
      <Badge variant="brand">{role === 'A' ? 'rtz' : 'rtz'}</Badge>
      <button onClick={onRemove} className="text-[oklch(40%_0.01_250)] hover:text-[oklch(70%_0.01_250)] transition-colors ml-1">
        <X className="h-3.5 w-3.5" />
      </button>
    </div>
  )
}

function NodeSizeCard({ size, selected, onSelect }: {
  size: typeof HETZNER_NODE_SIZES[number]
  selected: boolean
  onSelect: () => void
}) {
  return (
    <button
      onClick={onSelect}
      className={cn(
        'flex flex-col gap-2 rounded-[--radius-lg] border p-4 text-left transition-all duration-150',
        selected
          ? 'border-[--color-brand-500]/60 bg-[--color-brand-500]/8'
          : 'border-[--color-surface-border] bg-[--color-surface-1] hover:border-[oklch(28%_0.02_250)] hover:bg-[--color-surface-2]'
      )}
    >
      <div className="flex items-center justify-between">
        <span className="text-sm font-semibold text-[oklch(92%_0.01_250)]">{size.label}</span>
        {size.recommended && <Badge variant="success">Recommended</Badge>}
      </div>
      <div className="flex gap-3 text-xs text-[oklch(50%_0.01_250)]">
        <span className="flex items-center gap-1"><Cpu className="h-3 w-3" />{size.vcpu} vCPU</span>
        <span className="flex items-center gap-1"><MemoryStick className="h-3 w-3" />{size.ram} GB</span>
        <span className="flex items-center gap-1"><HardDrive className="h-3 w-3" />{size.disk} GB</span>
      </div>
      <p className="text-xs text-[oklch(40%_0.01_250)]">{size.description}</p>
      <p className="text-xs font-mono text-[oklch(55%_0.01_250)]">
        €{size.priceHour.toFixed(3)}/hr · €{size.priceMonth.toFixed(2)}/mo cap
      </p>
    </button>
  )
}

export function StepInfrastructure() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const [showRegionPicker, setShowRegionPicker] = useState(false)

  const canAddRegion = store.regions.length < 2
  const isValid = store.regions.length >= 1

  function addRegion(r: typeof HETZNER_REGIONS[number]) {
    if (store.regions.find((x) => x.id === r.id) || store.regions.length >= 2) return
    store.addRegion({ id: r.id, code: r.code, name: r.name, location: r.location, countryCode: r.countryCode, flag: r.flag })
    setShowRegionPicker(false)
  }

  const selectedRegionIds = new Set(store.regions.map((r) => r.id))

  // Cost estimate
  const cpCost = HETZNER_NODE_SIZES.find((s) => s.id === store.controlPlaneSize)?.priceHour ?? 0
  const wkCost = HETZNER_NODE_SIZES.find((s) => s.id === store.workerSize)?.priceHour ?? 0
  const regionCount = store.regions.length || 1
  const totalHour = (cpCost + wkCost * store.workerCount) * regionCount

  return (
    <StepShell
      title="Configure your infrastructure"
      description="Select regions and node sizes. Both regions run symmetric trust zone clusters — PowerDNS lua-records handle health-checked traffic distribution automatically."
      onNext={next}
      onBack={back}
      nextDisabled={!isValid}
    >
      {/* Regions */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-semibold text-[oklch(85%_0.01_250)]">Regions</p>
            <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">
              Select 1 region (single-site) or 2 regions (geographic redundancy via PowerDNS lua-records).
            </p>
          </div>
          {canAddRegion && (
            <button
              onClick={() => setShowRegionPicker((v) => !v)}
              className="flex items-center gap-1 text-xs text-[--color-brand-400] hover:text-[--color-brand-300] transition-colors"
            >
              <Plus className="h-3.5 w-3.5" />
              Add region
            </button>
          )}
        </div>

        {store.regions.map((r, i) => (
          <RegionPill key={r.id} region={r} role={i === 0 ? 'A' : 'B'} onRemove={() => store.removeRegion(r.id)} />
        ))}

        {store.regions.length === 0 && (
          <div className="rounded-[--radius-lg] border border-dashed border-[--color-surface-border] p-6 text-center">
            <p className="text-sm text-[oklch(40%_0.01_250)]">No region selected. Add at least one region to continue.</p>
          </div>
        )}

        {showRegionPicker && (
          <div className="rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-2] p-3 flex flex-col gap-1">
            {HETZNER_REGIONS.map((r) => {
              const taken = selectedRegionIds.has(r.id)
              return (
                <button
                  key={r.id}
                  disabled={taken}
                  onClick={() => addRegion(r)}
                  className={cn(
                    'flex items-center gap-3 rounded-[--radius-md] px-3 py-2 text-left transition-colors',
                    taken ? 'opacity-40 cursor-not-allowed' : 'hover:bg-[--color-surface-3] cursor-pointer'
                  )}
                >
                  <span>{r.flag}</span>
                  <div>
                    <p className="text-sm text-[oklch(85%_0.01_250)]">{r.name}</p>
                    <p className="text-xs text-[oklch(45%_0.01_250)]">{r.location}</p>
                  </div>
                  {taken && <Badge variant="default" className="ml-auto text-xs">Added</Badge>}
                </button>
              )
            })}
          </div>
        )}
      </div>

      <Separator />

      {/* Control plane sizing */}
      <div className="flex flex-col gap-3">
        <div>
          <p className="text-sm font-semibold text-[oklch(85%_0.01_250)]">Control plane node size</p>
          <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">One control-plane node per region.</p>
        </div>
        <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
          {HETZNER_NODE_SIZES.map((s) => (
            <NodeSizeCard
              key={s.id}
              size={s}
              selected={store.controlPlaneSize === s.id}
              onSelect={() => store.setControlPlaneSize(s.id as NodeSize)}
            />
          ))}
        </div>
      </div>

      {/* Workers */}
      <div className="flex flex-col gap-3">
        <div className="flex items-center justify-between">
          <div>
            <p className="text-sm font-semibold text-[oklch(85%_0.01_250)]">Worker nodes</p>
            <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">Optional — control-plane can run workloads in experiments.</p>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-xs text-[oklch(50%_0.01_250)]">{store.workerCount} worker{store.workerCount !== 1 ? 's' : ''}</span>
            <div className="flex items-center gap-1">
              <button
                onClick={() => store.setWorkerCount(Math.max(0, store.workerCount - 1))}
                className="flex h-6 w-6 items-center justify-center rounded border border-[--color-surface-border] text-[oklch(60%_0.01_250)] hover:bg-[--color-surface-2] transition-colors text-sm"
              >−</button>
              <button
                onClick={() => store.setWorkerCount(Math.min(6, store.workerCount + 1))}
                className="flex h-6 w-6 items-center justify-center rounded border border-[--color-surface-border] text-[oklch(60%_0.01_250)] hover:bg-[--color-surface-2] transition-colors text-sm"
              >+</button>
            </div>
          </div>
        </div>

        {store.workerCount > 0 && (
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
            {HETZNER_NODE_SIZES.map((s) => (
              <NodeSizeCard
                key={s.id}
                size={s}
                selected={store.workerSize === s.id}
                onSelect={() => store.setWorkerSize(s.id as NodeSize)}
              />
            ))}
          </div>
        )}
      </div>

      {/* HA toggle */}
      <div className="flex items-center justify-between rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-1] px-4 py-3">
        <div>
          <p className="text-sm font-semibold text-[oklch(85%_0.01_250)]">High availability control plane</p>
          <p className="text-xs text-[oklch(45%_0.01_250)] mt-0.5">3-node etcd cluster per region. Required for production.</p>
        </div>
        <Switch checked={store.haEnabled} onCheckedChange={store.setHaEnabled} />
      </div>

      {/* Cost estimate */}
      <div className="flex items-center gap-2 rounded-[--radius-lg] border border-[--color-surface-border] bg-[--color-surface-2] px-4 py-3">
        <Info className="h-4 w-4 text-[oklch(45%_0.01_250)] shrink-0" />
        <p className="text-xs text-[oklch(50%_0.01_250)]">
          Estimated cost: <span className="font-mono font-semibold text-[oklch(70%_0.01_250)]">€{totalHour.toFixed(3)}/hr</span>
          {' '}across {regionCount} region{regionCount > 1 ? 's' : ''}.
          Hetzner bills hourly — delete the cluster and billing stops immediately.
        </p>
      </div>
    </StepShell>
  )
}
