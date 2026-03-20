import { useState } from 'react'
import { Check } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

interface ProviderDef { id: CloudProvider; name: string; short: string; logo: React.ReactNode }

const PROVIDERS: ProviderDef[] = [
  { id:'hetzner', name:'Hetzner Cloud',         short:'Hetzner', logo:<svg viewBox="0 0 32 32" width={22} height={22} style={{flexShrink:0}}><rect width={32} height={32} rx={6} fill="#D50C2D"/><path d="M7 8h7v16H7zM18 8h7v16h-7z" fill="#fff"/></svg> },
  { id:'huawei',  name:'Huawei Cloud',           short:'Huawei',  logo:<svg viewBox="0 0 32 32" width={22} height={22} style={{flexShrink:0}}><rect width={32} height={32} rx={6} fill="#CF0A2C"/><path d="M16 8L18 13L23 13L19 16L21 21L16 18L11 21L13 16L9 13L14 13Z" fill="#fff"/></svg> },
  { id:'oci',     name:'Oracle Cloud (OCI)',     short:'OCI',     logo:<svg viewBox="0 0 32 32" width={22} height={22} style={{flexShrink:0}}><rect width={32} height={32} rx={6} fill="#F80000"/><ellipse cx={16} cy={16} rx={9} ry={6} fill="none" stroke="#fff" strokeWidth={2}/></svg> },
  { id:'aws',     name:'Amazon Web Services',    short:'AWS',     logo:<svg viewBox="0 0 32 32" width={22} height={22} style={{flexShrink:0}}><rect width={32} height={32} rx={6} fill="#232F3E"/><path d="M9 19c3.5 2.5 10.5 2.5 14 0" stroke="#FF9900" strokeWidth={2} fill="none" strokeLinecap="round"/><path d="M16 11v6" stroke="#FF9900" strokeWidth={2} strokeLinecap="round"/><path d="M13 15l3-4 3 4" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round"/></svg> },
  { id:'azure',   name:'Microsoft Azure',        short:'Azure',   logo:<svg viewBox="0 0 32 32" width={22} height={22} style={{flexShrink:0}}><rect width={32} height={32} rx={6} fill="#0078D4"/><path d="M15 10L9 22h5l3-5.5 3 5.5h5L20 10z" fill="#fff" opacity={0.9}/></svg> },
]

function RegionRow({ index, label, selectedProvider, onSelect, open, onToggle, providerCols }: {
  index: number; label: string; selectedProvider: CloudProvider | undefined
  onSelect: (p: CloudProvider) => void; open: boolean; onToggle: () => void; providerCols: string
}) {
  const def = PROVIDERS.find(p => p.id === selectedProvider)
  return (
    <div style={{
      borderRadius: 10,
      border: selectedProvider ? '1.5px solid rgba(56,189,248,0.25)' : '1.5px solid rgba(255,255,255,0.08)',
      background: selectedProvider ? 'rgba(56,189,248,0.03)' : 'rgba(255,255,255,0.02)',
      overflow: 'hidden', transition: 'all 0.15s',
    }}>
      <div onClick={onToggle} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', cursor: 'pointer' }}>
        <div style={{ width: 22, height: 22, borderRadius: '50%', flexShrink: 0, background: selectedProvider ? 'linear-gradient(135deg, #38BDF8, #818CF8)' : 'rgba(255,255,255,0.06)', border: selectedProvider ? 'none' : '1px solid rgba(255,255,255,0.12)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 10, fontWeight: 700, color: selectedProvider ? '#fff' : 'rgba(255,255,255,0.3)' }}>
          {selectedProvider ? <Check size={11} strokeWidth={2.5} /> : index + 1}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 12, fontWeight: 600, color: selectedProvider ? 'rgba(255,255,255,0.8)' : 'rgba(255,255,255,0.5)', lineHeight: 1.3 }}>{label}</div>
          <div style={{ fontSize: 11, marginTop: 2 }}>
            {selectedProvider && def
              ? <span style={{ color: 'rgba(56,189,248,0.7)' }}>{def.name} selected</span>
              : <span style={{ color: 'rgba(255,255,255,0.2)' }}>Select a cloud provider</span>}
          </div>
        </div>
        {selectedProvider && def && <div style={{ flexShrink: 0, opacity: 0.8 }}>{def.logo}</div>}
        <div style={{ color: 'rgba(255,255,255,0.25)', flexShrink: 0, fontSize: 12, transition: 'transform 0.2s', transform: open ? 'rotate(180deg)' : 'none' }}>▾</div>
      </div>

      {open && (
        <div style={{ padding: '0 14px 14px', borderTop: '1px solid rgba(255,255,255,0.06)' }}>
          <div style={{ display: 'grid', gridTemplateColumns: providerCols, gap: 8, paddingTop: 12 }}>
            {PROVIDERS.map(p => {
              const active = selectedProvider === p.id
              return (
                <div
                  key={p.id}
                  onClick={() => onSelect(p.id)}
                  style={{
                    display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6,
                    padding: '12px 10px', borderRadius: 8, cursor: 'pointer',
                    border: active ? '1.5px solid rgba(56,189,248,0.45)' : '1.5px solid rgba(255,255,255,0.07)',
                    background: active ? 'rgba(56,189,248,0.08)' : 'rgba(255,255,255,0.02)',
                    transition: 'all 0.15s', position: 'relative',
                  }}
                >
                  {active && (
                    <div style={{ position: 'absolute', top: 6, right: 6, width: 14, height: 14, borderRadius: '50%', background: '#38BDF8', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                      <Check size={8} strokeWidth={3} color="#fff" />
                    </div>
                  )}
                  {p.logo}
                  <div style={{ fontSize: 11, fontWeight: active ? 600 : 400, textAlign: 'center', color: active ? '#fff' : 'rgba(255,255,255,0.55)', lineHeight: 1.2 }}>{p.short}</div>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const bp = useBreakpoint()

  const topology = store.topology
  const regionCount  = topology ? TOPOLOGY_REGION_COUNT[topology]  : 1
  const regionLabels = topology ? TOPOLOGY_REGION_LABELS[topology] : ['Region 1']

  const [openRegion, setOpenRegion] = useState<number | null>(0)

  const allConfigured = Array.from({ length: regionCount }, (_, i) => i).every(i => store.regionProviders[i] != null)
  const firstProvider = store.regionProviders[0]
  const hasUnassigned = Array.from({ length: regionCount }, (_, i) => i).some(i => i > 0 && store.regionProviders[i] == null)

  function handleSelect(regionIndex: number, provider: CloudProvider) {
    store.setRegionProvider(regionIndex, provider)
    if (regionIndex === 0) store.setProvider(provider)
    const nextUnassigned = Array.from({ length: regionCount }, (_, i) => i).find(i => i > regionIndex && store.regionProviders[i] == null)
    setOpenRegion(nextUnassigned ?? null)
  }

  function applyToAll(provider: CloudProvider) {
    store.applyProviderToAll(provider, regionCount)
    store.setProvider(provider)
    setOpenRegion(null)
  }

  // Provider grid: 3 cols desktop, 2 cols tablet, 1 col mobile (but 5 items → 3+2 or 2+2+1)
  const providerCols = bp === 'mobile' ? 'repeat(2, 1fr)' : 'repeat(3, 1fr)'
  // Region grid: 2 cols on desktop/tablet when 3+ regions, always 1 col mobile
  const regionCols = bp === 'mobile' || regionCount < 3 ? '1fr' : '1fr 1fr'

  const uniqueProviders = [...new Set(Object.values(store.regionProviders))]

  return (
    <StepShell
      title="Cloud provider per region"
      description="Select a cloud provider for each region. You can mix providers across regions — you'll be asked for one set of credentials per provider."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      <div style={{ display: 'grid', gridTemplateColumns: regionCols, gap: 8 }}>
        {regionLabels.map((label, i) => (
          <RegionRow
            key={i} index={i} label={label}
            selectedProvider={store.regionProviders[i]}
            onSelect={(p) => handleSelect(i, p)}
            open={openRegion === i}
            onToggle={() => setOpenRegion(openRegion === i ? null : i)}
            providerCols={providerCols}
          />
        ))}
      </div>

      {firstProvider && hasUnassigned && (
        <button type="button" onClick={() => applyToAll(firstProvider)}
          style={{ width: '100%', padding: '9px 0', borderRadius: 8, cursor: 'pointer', border: '1px dashed rgba(56,189,248,0.3)', background: 'rgba(56,189,248,0.04)', color: 'rgba(56,189,248,0.7)', fontSize: 12, fontWeight: 500, fontFamily: 'Inter, sans-serif', transition: 'all 0.15s' }}>
          Apply {PROVIDERS.find(p => p.id === firstProvider)?.name ?? firstProvider} to all regions →
        </button>
      )}

      {allConfigured && uniqueProviders.length > 0 && (
        <div style={{ borderRadius: 8, background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)', padding: '10px 14px' }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'rgba(56,189,248,0.7)', margin: '0 0 6px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>Credentials required</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {uniqueProviders.map(p => {
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
