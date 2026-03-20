import { useState } from 'react'
import { Check } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS, PROVIDER_REGIONS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

interface ProviderDef { id: CloudProvider; name: string; logo: React.ReactNode }

const PROVIDERS: ProviderDef[] = [
  { id: 'hetzner', name: 'Hetzner Cloud',        logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#D50C2D"/><path d="M5 6h5v12H5zM14 6h5v12h-5z" fill="#fff"/></svg> },
  { id: 'huawei',  name: 'Huawei Cloud',          logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#CF0A2C"/><path d="M12 5L14 9.5L19 9.5L15 12.5L17 17L12 14L7 17L9 12.5L5 9.5L10 9.5Z" fill="#fff"/></svg> },
  { id: 'oci',     name: 'Oracle Cloud (OCI)',     logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#F80000"/><ellipse cx={12} cy={12} rx={7} ry={4.5} fill="none" stroke="#fff" strokeWidth={1.5}/></svg> },
  { id: 'aws',     name: 'Amazon Web Services',    logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#232F3E"/><path d="M7 15c2.5 1.8 7.5 1.8 10 0" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round"/><path d="M12 8v5" stroke="#FF9900" strokeWidth={1.5} strokeLinecap="round"/><path d="M10 11l2-3 2 3" stroke="#FF9900" strokeWidth={1.2} fill="none" strokeLinecap="round"/></svg> },
  { id: 'azure',   name: 'Microsoft Azure',        logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#0078D4"/><path d="M11 7L7 17h4l2-4 2 4h4L15 7z" fill="#fff" opacity={0.9}/></svg> },
]

function RegionRow({ index, label, selectedProvider, selectedCloudRegion, onSelectProvider, onSelectCloudRegion, open, onToggle }: {
  index: number
  label: string
  selectedProvider: CloudProvider | undefined
  selectedCloudRegion: string | undefined
  onSelectProvider: (p: CloudProvider) => void
  onSelectCloudRegion: (r: string) => void
  open: boolean
  onToggle: () => void
}) {
  const bp = useBreakpoint()
  const providerDef = PROVIDERS.find(p => p.id === selectedProvider)
  const cloudRegions = selectedProvider ? PROVIDER_REGIONS[selectedProvider] : []
  const selectedRegionDef = cloudRegions.find(r => r.id === selectedCloudRegion)
  const isFullyConfigured = selectedProvider != null && selectedCloudRegion != null && selectedCloudRegion !== ''

  return (
    <div style={{
      borderRadius: 10,
      border: isFullyConfigured ? '1.5px solid rgba(56,189,248,0.25)' : '1.5px solid rgba(255,255,255,0.08)',
      background: isFullyConfigured ? 'rgba(56,189,248,0.03)' : 'rgba(255,255,255,0.02)',
      overflow: 'hidden', transition: 'all 0.15s',
    }}>
      {/* Header */}
      <div onClick={onToggle} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', cursor: 'pointer' }}>
        <div style={{
          width: 22, height: 22, borderRadius: '50%', flexShrink: 0,
          background: isFullyConfigured ? 'linear-gradient(135deg, #38BDF8, #818CF8)' : 'rgba(255,255,255,0.06)',
          border: isFullyConfigured ? 'none' : '1px solid rgba(255,255,255,0.12)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 10, fontWeight: 700, color: isFullyConfigured ? '#fff' : 'rgba(255,255,255,0.3)',
        }}>
          {isFullyConfigured ? <Check size={11} strokeWidth={2.5} /> : index + 1}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 12, fontWeight: 600, color: isFullyConfigured ? 'rgba(255,255,255,0.8)' : 'rgba(255,255,255,0.5)', lineHeight: 1.3 }}>{label}</div>
          <div style={{ fontSize: 11, marginTop: 2 }}>
            {isFullyConfigured && selectedRegionDef
              ? <span style={{ color: 'rgba(56,189,248,0.7)' }}>{providerDef?.name} · {selectedRegionDef.label} — {selectedRegionDef.location}</span>
              : selectedProvider
              ? <span style={{ color: 'rgba(255,255,255,0.35)' }}>{providerDef?.name} · select a cloud region →</span>
              : <span style={{ color: 'rgba(255,255,255,0.2)' }}>Select a cloud provider and region</span>}
          </div>
        </div>
        {providerDef && <div style={{ flexShrink: 0, opacity: 0.8 }}>{providerDef.logo}</div>}
        <div style={{ color: 'rgba(255,255,255,0.25)', flexShrink: 0, fontSize: 12, transition: 'transform 0.2s', transform: open ? 'rotate(180deg)' : 'none' }}>▾</div>
      </div>

      {open && (
        <div style={{ borderTop: '1px solid rgba(255,255,255,0.06)', padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 14 }}>
          {/* Provider list — 1 per line */}
          <div>
            <div style={{ fontSize: 10, fontWeight: 600, color: 'rgba(255,255,255,0.3)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 8 }}>Cloud Provider</div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {PROVIDERS.map(p => {
                const active = selectedProvider === p.id
                return (
                  <div
                    key={p.id}
                    onClick={() => onSelectProvider(p.id)}
                    style={{
                      display: 'flex', alignItems: 'center', gap: 10,
                      padding: '9px 12px', borderRadius: 8, cursor: 'pointer',
                      border: active ? '1.5px solid rgba(56,189,248,0.45)' : '1.5px solid rgba(255,255,255,0.07)',
                      background: active ? 'rgba(56,189,248,0.08)' : 'rgba(255,255,255,0.02)',
                      transition: 'all 0.15s',
                    }}
                  >
                    {p.logo}
                    <span style={{ flex: 1, fontSize: 12, fontWeight: active ? 600 : 400, color: active ? '#fff' : 'rgba(255,255,255,0.55)' }}>{p.name}</span>
                    {active && <Check size={13} strokeWidth={2.5} style={{ color: '#38BDF8', flexShrink: 0 }} />}
                  </div>
                )
              })}
            </div>
          </div>

          {/* Cloud region grid — shown after provider selected */}
          {selectedProvider && (
            <div>
              <div style={{ fontSize: 10, fontWeight: 600, color: 'rgba(255,255,255,0.3)', textTransform: 'uppercase', letterSpacing: '0.08em', marginBottom: 8 }}>Cloud Region</div>
              <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? '1fr' : '1fr 1fr', gap: 6 }}>
                {PROVIDER_REGIONS[selectedProvider].map(r => {
                  const active = selectedCloudRegion === r.id
                  return (
                    <div
                      key={r.id}
                      onClick={() => onSelectCloudRegion(r.id)}
                      style={{
                        padding: '8px 12px', borderRadius: 8, cursor: 'pointer',
                        border: active ? '1.5px solid rgba(56,189,248,0.45)' : '1.5px solid rgba(255,255,255,0.07)',
                        background: active ? 'rgba(56,189,248,0.08)' : 'rgba(255,255,255,0.02)',
                        transition: 'all 0.15s',
                        display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8,
                      }}
                    >
                      <div>
                        <div style={{ fontSize: 12, fontWeight: active ? 600 : 500, color: active ? '#fff' : 'rgba(255,255,255,0.65)' }}>{r.label}</div>
                        <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.3)', marginTop: 2 }}>{r.location}</div>
                      </div>
                      {active && <Check size={12} strokeWidth={2.5} style={{ color: '#38BDF8', flexShrink: 0 }} />}
                    </div>
                  )
                })}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()

  const topology = store.topology
  const regionCount  = topology ? (TOPOLOGY_REGION_COUNT[topology]  ?? 1) : 1
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? ['Region 1']) : ['Region 1']

  const [openRegion, setOpenRegion] = useState<number | null>(0)

  const allConfigured = Array.from({ length: regionCount }, (_, i) => i)
    .every(i => store.regionProviders[i] != null && store.regionCloudRegions[i] != null && store.regionCloudRegions[i] !== '')

  const firstProvider = store.regionProviders[0]
  const hasUnassigned = Array.from({ length: regionCount }, (_, i) => i)
    .some(i => store.regionProviders[i] == null)

  function handleSelectProvider(regionIndex: number, provider: CloudProvider) {
    store.setRegionProvider(regionIndex, provider)
    if (regionIndex === 0) store.setProvider(provider)
    // Clear cloud region if provider changed
    if (store.regionProviders[regionIndex] !== provider) {
      store.setRegionCloudRegion(regionIndex, '')
    }
    // Don't auto-advance — user still needs to pick a cloud region
  }

  function handleSelectCloudRegion(regionIndex: number, cloudRegion: string) {
    store.setRegionCloudRegion(regionIndex, cloudRegion)
    // Auto-advance to next unassigned region
    const nextUnassigned = Array.from({ length: regionCount }, (_, i) => i)
      .find(i => i > regionIndex && (store.regionProviders[i] == null || store.regionCloudRegions[i] == null || store.regionCloudRegions[i] === ''))
    setOpenRegion(nextUnassigned ?? null)
  }

  function applyToAll(provider: CloudProvider) {
    store.applyProviderToAll(provider, regionCount)
    store.setProvider(provider)
    // Don't apply cloud regions — each region may need a different one
  }

  return (
    <StepShell
      title="Cloud provider per region"
      description="Select a cloud provider and specific region for each topology region. You can mix providers across regions — credentials are collected per provider."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {regionLabels.map((label, i) => (
          <RegionRow
            key={i}
            index={i}
            label={label}
            selectedProvider={store.regionProviders[i]}
            selectedCloudRegion={store.regionCloudRegions[i]}
            onSelectProvider={(p) => handleSelectProvider(i, p)}
            onSelectCloudRegion={(r) => handleSelectCloudRegion(i, r)}
            open={openRegion === i}
            onToggle={() => setOpenRegion(openRegion === i ? null : i)}
          />
        ))}
      </div>

      {firstProvider && hasUnassigned && (
        <button
          type="button"
          onClick={() => applyToAll(firstProvider)}
          style={{ width: '100%', padding: '9px 0', borderRadius: 8, cursor: 'pointer', border: '1px dashed rgba(56,189,248,0.3)', background: 'rgba(56,189,248,0.04)', color: 'rgba(56,189,248,0.7)', fontSize: 12, fontWeight: 500, fontFamily: 'Inter, sans-serif', transition: 'all 0.15s' }}
        >
          Apply {PROVIDERS.find(p => p.id === firstProvider)?.name ?? firstProvider} to all regions →
        </button>
      )}

      {allConfigured && (
        <div style={{ borderRadius: 8, background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)', padding: '10px 14px' }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'rgba(56,189,248,0.7)', margin: '0 0 6px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>Credentials required</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {[...new Set(Object.values(store.regionProviders))].map(p => {
              const def = PROVIDERS.find(d => d.id === p)
              const regions = Object.entries(store.regionProviders).filter(([, v]) => v === p).map(([k]) => Number(k))
              return (
                <div key={p} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 11, color: 'rgba(255,255,255,0.5)' }}>
                  {def?.logo}
                  <span style={{ fontWeight: 500 }}>{def?.name}</span>
                  <span style={{ color: 'rgba(255,255,255,0.25)' }}>·</span>
                  <span>Region{regions.length > 1 ? 's' : ''} {regions.map(r => r + 1).join(', ')}</span>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </StepShell>
  )
}
