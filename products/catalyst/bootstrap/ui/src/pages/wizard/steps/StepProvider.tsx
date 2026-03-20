import { useState } from 'react'
import { ChevronDown, ChevronUp, Check } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

/* ─────────────────────────────────────────────────────────────────────────
   Provider catalogue
─────────────────────────────────────────────────────────────────────────── */
interface ProviderDef {
  id: CloudProvider
  name: string
  short: string
  available: boolean
  logo: React.ReactNode
}

const PROVIDERS: ProviderDef[] = [
  {
    id: 'hetzner', name: 'Hetzner Cloud', short: 'Hetzner', available: true,
    logo: (
      <svg viewBox="0 0 32 32" width={22} height={22} style={{ flexShrink: 0 }}>
        <rect width={32} height={32} rx={6} fill="#D50C2D" />
        <path d="M7 8h7v16H7zM18 8h7v16h-7z" fill="#fff" />
      </svg>
    ),
  },
  {
    id: 'huawei', name: 'Huawei Cloud', short: 'Huawei', available: false,
    logo: (
      <svg viewBox="0 0 32 32" width={22} height={22} style={{ flexShrink: 0 }}>
        <rect width={32} height={32} rx={6} fill="#CF0A2C" />
        <path d="M16 8L18 13L23 13L19 16L21 21L16 18L11 21L13 16L9 13L14 13Z" fill="#fff" />
      </svg>
    ),
  },
  {
    id: 'oci', name: 'Oracle Cloud', short: 'OCI', available: false,
    logo: (
      <svg viewBox="0 0 32 32" width={22} height={22} style={{ flexShrink: 0 }}>
        <rect width={32} height={32} rx={6} fill="#F80000" />
        <ellipse cx={16} cy={16} rx={9} ry={6} fill="none" stroke="#fff" strokeWidth={2} />
      </svg>
    ),
  },
  {
    id: 'aws', name: 'Amazon Web Services', short: 'AWS', available: false,
    logo: (
      <svg viewBox="0 0 32 32" width={22} height={22} style={{ flexShrink: 0 }}>
        <rect width={32} height={32} rx={6} fill="#232F3E" />
        <path d="M9 19c3.5 2.5 10.5 2.5 14 0" stroke="#FF9900" strokeWidth={2} fill="none" strokeLinecap="round" />
        <path d="M16 11v6" stroke="#FF9900" strokeWidth={2} strokeLinecap="round" />
        <path d="M13 15l3-4 3 4" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round" />
      </svg>
    ),
  },
  {
    id: 'azure', name: 'Microsoft Azure', short: 'Azure', available: false,
    logo: (
      <svg viewBox="0 0 32 32" width={22} height={22} style={{ flexShrink: 0 }}>
        <rect width={32} height={32} rx={6} fill="#0078D4" />
        <path d="M15 10L9 22h5l3-5.5 3 5.5h5L20 10z" fill="#fff" opacity={0.9} />
      </svg>
    ),
  },
]

/* ── Per-region accordion row ─────────────────────────────────────────── */
function RegionRow({
  index,
  label,
  selectedProvider,
  onSelect,
  autoOpenFirst,
}: {
  index: number
  label: string
  selectedProvider: CloudProvider | undefined
  onSelect: (p: CloudProvider) => void
  autoOpenFirst?: boolean
}) {
  const [open, setOpen] = useState(autoOpenFirst ?? false)
  const def = PROVIDERS.find(p => p.id === selectedProvider)

  return (
    <div style={{
      borderRadius: 10,
      border: selectedProvider
        ? '1.5px solid rgba(56,189,248,0.25)'
        : '1.5px solid rgba(255,255,255,0.08)',
      background: selectedProvider ? 'rgba(56,189,248,0.03)' : 'rgba(255,255,255,0.02)',
      overflow: 'hidden',
      transition: 'all 0.15s',
    }}>
      {/* Header row */}
      <div
        onClick={() => setOpen(v => !v)}
        style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px', cursor: 'pointer' }}
      >
        {/* Region number */}
        <div style={{
          width: 22, height: 22, borderRadius: '50%', flexShrink: 0,
          background: selectedProvider ? 'linear-gradient(135deg, #38BDF8, #818CF8)' : 'rgba(255,255,255,0.06)',
          border: selectedProvider ? 'none' : '1px solid rgba(255,255,255,0.12)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 10, fontWeight: 700,
          color: selectedProvider ? '#fff' : 'rgba(255,255,255,0.3)',
        }}>
          {selectedProvider ? <Check size={11} strokeWidth={2.5} /> : index + 1}
        </div>

        {/* Label */}
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 12, fontWeight: 600, color: selectedProvider ? 'rgba(255,255,255,0.8)' : 'rgba(255,255,255,0.5)', lineHeight: 1.3 }}>
            {label}
          </div>
          {selectedProvider && def && (
            <div style={{ fontSize: 11, color: 'rgba(56,189,248,0.7)', marginTop: 2, display: 'flex', alignItems: 'center', gap: 4 }}>
              {def.short} selected
            </div>
          )}
          {!selectedProvider && (
            <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.2)', marginTop: 2 }}>Select a cloud provider</div>
          )}
        </div>

        {/* Selected provider logo */}
        {selectedProvider && def && (
          <div style={{ flexShrink: 0, opacity: 0.8 }}>{def.logo}</div>
        )}

        {/* Chevron */}
        <div style={{ color: 'rgba(255,255,255,0.25)', flexShrink: 0 }}>
          {open ? <ChevronUp size={14} /> : <ChevronDown size={14} />}
        </div>
      </div>

      {/* Expanded provider picker */}
      {open && (
        <div style={{ padding: '0 14px 12px', borderTop: '1px solid rgba(255,255,255,0.06)' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 6, paddingTop: 10 }}>
            {PROVIDERS.map(p => {
              const active = selectedProvider === p.id
              return (
                <div
                  key={p.id}
                  onClick={() => p.available && onSelect(p.id)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 10,
                    padding: '8px 10px', borderRadius: 8, cursor: p.available ? 'pointer' : 'not-allowed',
                    border: active ? '1.5px solid rgba(56,189,248,0.45)' : '1.5px solid rgba(255,255,255,0.06)',
                    background: active ? 'rgba(56,189,248,0.08)' : 'rgba(255,255,255,0.02)',
                    opacity: p.available ? 1 : 0.4,
                    transition: 'all 0.15s',
                  }}
                >
                  {p.logo}
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ fontSize: 12, fontWeight: active ? 600 : 400, color: active ? '#fff' : 'rgba(255,255,255,0.6)' }}>{p.name}</div>
                    {!p.available && (
                      <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.2)', marginTop: 1 }}>Coming soon</div>
                    )}
                  </div>
                  <div style={{
                    width: 16, height: 16, borderRadius: '50%', flexShrink: 0,
                    background: active ? '#38BDF8' : 'transparent',
                    border: active ? 'none' : '1.5px solid rgba(255,255,255,0.15)',
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                    transition: 'all 0.15s',
                  }}>
                    {active && <Check size={9} strokeWidth={3} color="#fff" />}
                  </div>
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

  const topology = store.topology
  const regionCount = topology ? TOPOLOGY_REGION_COUNT[topology] : 1
  const regionLabels = topology ? TOPOLOGY_REGION_LABELS[topology] : ['Region 1']

  const allConfigured = Array.from({ length: regionCount }, (_, i) => i)
    .every(i => store.regionProviders[i] != null)

  const firstProvider = store.regionProviders[0]
  const hasUnassigned = Array.from({ length: regionCount }, (_, i) => i)
    .some(i => i > 0 && store.regionProviders[i] == null)

  function handleSelect(regionIndex: number, provider: CloudProvider) {
    store.setRegionProvider(regionIndex, provider)
    // Compat: set single provider field to first region's choice
    if (regionIndex === 0) store.setProvider(provider)
  }

  function applyToAll(provider: CloudProvider) {
    store.applyProviderToAll(provider, regionCount)
    store.setProvider(provider)
  }

  // Unique providers summary
  const uniqueProviders = [...new Set(Object.values(store.regionProviders))]

  return (
    <StepShell
      title="Cloud provider per region"
      description="Select a cloud provider for each region. You can mix providers across regions — you'll be asked for one set of credentials per provider."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      {/* Per-region accordion */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {regionLabels.map((label, i) => (
          <RegionRow
            key={i}
            index={i}
            label={label}
            selectedProvider={store.regionProviders[i]}
            onSelect={(p) => handleSelect(i, p)}
            autoOpenFirst={i === 0}
          />
        ))}
      </div>

      {/* Apply-to-all shortcut — shown when region 0 is set and others aren't */}
      {firstProvider && hasUnassigned && (
        <button
          type="button"
          onClick={() => applyToAll(firstProvider)}
          style={{
            width: '100%', padding: '9px 0', borderRadius: 8, cursor: 'pointer',
            border: '1px dashed rgba(56,189,248,0.3)',
            background: 'rgba(56,189,248,0.04)',
            color: 'rgba(56,189,248,0.7)',
            fontSize: 12, fontWeight: 500,
            fontFamily: 'Inter, sans-serif',
            transition: 'all 0.15s',
          }}
        >
          Apply {PROVIDERS.find(p => p.id === firstProvider)?.short ?? firstProvider} to all regions →
        </button>
      )}

      {/* Summary of unique providers — shown when all configured */}
      {allConfigured && uniqueProviders.length > 0 && (
        <div style={{ borderRadius: 8, background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)', padding: '10px 14px' }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'rgba(56,189,248,0.7)', margin: '0 0 6px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>
            Credentials required
          </p>
          {uniqueProviders.map(p => {
            const def = PROVIDERS.find(d => d.id === p)
            const regions = Object.entries(store.regionProviders).filter(([, v]) => v === p).map(([k]) => Number(k))
            return (
              <div key={p} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '3px 0', fontSize: 11, color: 'rgba(255,255,255,0.5)' }}>
                {def?.logo}
                <span style={{ fontWeight: 500 }}>{def?.name}</span>
                <span style={{ color: 'rgba(255,255,255,0.25)' }}>·</span>
                <span>Region{regions.length > 1 ? 's' : ''} {regions.map(r => r + 1).join(', ')}</span>
              </div>
            )
          })}
        </div>
      )}
    </StepShell>
  )
}
