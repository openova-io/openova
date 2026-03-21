import { useEffect, useRef, useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS, PROVIDER_REGIONS } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

/* ── Provider definitions with logos ─────────────────────────────── */
interface ProviderDef { id: CloudProvider; name: string; logo: React.ReactNode }
const PROVIDERS: ProviderDef[] = [
  { id: 'hetzner', name: 'Hetzner Cloud',      logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#D50C2D"/><path d="M5 6h5v12H5zM14 6h5v12h-5z" fill="#fff"/></svg> },
  { id: 'huawei',  name: 'Huawei Cloud',        logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#CF0A2C"/><path d="M12 5L14 9.5L19 9.5L15 12.5L17 17L12 14L7 17L9 12.5L5 9.5L10 9.5Z" fill="#fff"/></svg> },
  { id: 'oci',     name: 'Oracle Cloud (OCI)',   logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#F80000"/><ellipse cx={12} cy={12} rx={7} ry={4.5} fill="none" stroke="#fff" strokeWidth={1.5}/></svg> },
  { id: 'aws',     name: 'Amazon Web Services',  logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#232F3E"/><path d="M7 15c2.5 1.8 7.5 1.8 10 0" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round"/><path d="M12 8v5" stroke="#FF9900" strokeWidth={1.5} strokeLinecap="round"/><path d="M10 11l2-3 2 3" stroke="#FF9900" strokeWidth={1.2} fill="none" strokeLinecap="round"/></svg> },
  { id: 'azure',   name: 'Microsoft Azure',      logo: <svg viewBox="0 0 24 24" width={18} height={18} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#0078D4"/><path d="M11 7L7 17h4l2-4 2 4h4L15 7z" fill="#fff" opacity={0.9}/></svg> },
]

/* ── HQ → nearest provider + staggered regions ───────────────────── */
const HQ_HINTS: Array<{ match: RegExp; provider: CloudProvider; regions: string[] }> = [
  { match: /germany|frankfurt|berlin|munich|hamburg|cologne/i, provider: 'hetzner', regions: ['fsn1',           'nbg1',           'hel1'] },
  { match: /finland|helsinki|sweden|norway|denmark|nordic/i,   provider: 'hetzner', regions: ['hel1',           'fsn1',           'nbg1'] },
  { match: /netherlands|amsterdam|belgium/i,                    provider: 'oci',     regions: ['eu-amsterdam-1', 'eu-frankfurt-1',  'ap-singapore-1'] },
  { match: /france|paris/i,                                     provider: 'huawei',  regions: ['eu-west-204',    'eu-west-101',     'ap-southeast-1'] },
  { match: /ireland|dublin|uk|london|britain/i,                 provider: 'aws',     regions: ['eu-west-1',      'eu-central-1',    'us-east-1'] },
  { match: /virginia|ashburn|washington|new york|east.*us/i,    provider: 'aws',     regions: ['us-east-1',      'us-west-2',       'eu-west-1'] },
  { match: /california|oregon|seattle|phoenix|west.*us/i,       provider: 'aws',     regions: ['us-west-2',      'us-east-1',       'ap-southeast-1'] },
  { match: /singapore|malaysia|indonesia/i,                     provider: 'aws',     regions: ['ap-southeast-1', 'us-east-1',       'eu-central-1'] },
  { match: /hong kong|china|beijing/i,                          provider: 'huawei',  regions: ['ap-southeast-1', 'cn-north-4',      'eu-west-101'] },
  { match: /saudi|riyadh|dubai|uae|middle east/i,               provider: 'huawei',  regions: ['me-east-1',      'ap-southeast-1',  'eu-west-101'] },
  { match: /japan|tokyo|korea|seoul/i,                          provider: 'aws',     regions: ['ap-southeast-1', 'us-west-2',       'eu-central-1'] },
  { match: /australia|sydney|melbourne/i,                       provider: 'oci',     regions: ['ap-singapore-1', 'us-ashburn-1',    'eu-frankfurt-1'] },
]
function getHqHint(hq: string) { return HQ_HINTS.find(h => h.match.test(hq)) ?? null }

/* ── Custom dropdown ─────────────────────────────────────────────── */
interface SelectOption { value: string; label: string; sublabel?: string; logo?: React.ReactNode }

function CustomSelect({ value, onChange, options, placeholder = 'Select…' }: {
  value: string
  onChange: (v: string) => void
  options: SelectOption[]
  placeholder?: string
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    function onDown(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [])

  const selected = options.find(o => o.value === value)

  return (
    <div ref={ref} style={{ position: 'relative' }}>
      {/* Trigger */}
      <div
        onClick={() => setOpen(v => !v)}
        style={{
          display: 'flex', alignItems: 'center', gap: 8,
          padding: '8px 10px', borderRadius: 8, cursor: 'pointer',
          border: selected ? '1.5px solid rgba(56,189,248,0.35)' : '1.5px solid var(--wiz-border)',
          background: selected ? 'rgba(56,189,248,0.06)' : 'var(--wiz-bg-sub)',
          transition: 'all 0.12s',
        }}
      >
        {selected?.logo}
        <span style={{ flex: 1, fontSize: 12, fontWeight: selected ? 500 : 400, color: selected ? 'var(--wiz-text-hi)' : 'var(--wiz-text-sub)' }}>
          {selected ? selected.label : placeholder}
        </span>
        {selected?.sublabel && (
          <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)' }}>{selected.sublabel}</span>
        )}
        <ChevronDown size={13} style={{ color: 'var(--wiz-text-sub)', flexShrink: 0, transform: open ? 'rotate(180deg)' : 'none', transition: 'transform 0.15s' }} />
      </div>

      {/* Panel */}
      {open && (
        <div style={{
          position: 'absolute', top: 'calc(100% + 4px)', left: 0, right: 0, zIndex: 200,
          borderRadius: 9, border: '1px solid var(--wiz-border)',
          background: 'var(--wiz-panel-bg)', boxShadow: '0 4px 20px rgba(0,0,0,0.15)',
          overflow: 'hidden',
        }}>
          {options.map(o => {
            const active = o.value === value
            return (
              <div
                key={o.value}
                onClick={() => { onChange(o.value); setOpen(false) }}
                style={{
                  display: 'flex', alignItems: 'center', gap: 8,
                  padding: '9px 12px', cursor: 'pointer',
                  background: active ? 'rgba(56,189,248,0.08)' : 'transparent',
                  borderBottom: '1px solid var(--wiz-bg-card)',
                  transition: 'background 0.1s',
                }}
              >
                {o.logo}
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontSize: 12, fontWeight: active ? 600 : 400, color: active ? 'var(--wiz-text-hi)' : 'var(--wiz-text-md)' }}>{o.label}</div>
                  {o.sublabel && <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 1 }}>{o.sublabel}</div>}
                </div>
                {active && <Check size={13} strokeWidth={2.5} style={{ color: '#38BDF8', flexShrink: 0 }} />}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

/* ── RegionCard ──────────────────────────────────────────────────── */
function RegionCard({ index, label, selectedProvider, selectedCloudRegion, onSelectProvider, onSelectCloudRegion }: {
  index: number
  label: string
  selectedProvider: CloudProvider | undefined
  selectedCloudRegion: string | undefined
  onSelectProvider: (p: CloudProvider) => void
  onSelectCloudRegion: (r: string) => void
}) {
  const isConfigured = !!selectedProvider && !!selectedCloudRegion
  const providerDef = PROVIDERS.find(p => p.id === selectedProvider)

  const providerOptions: SelectOption[] = PROVIDERS.map(p => ({
    value: p.id, label: p.name, logo: p.logo,
  }))

  const regionOptions: SelectOption[] = selectedProvider
    ? PROVIDER_REGIONS[selectedProvider].map(r => ({
        value: r.id, label: r.label, sublabel: r.location,
      }))
    : []

  return (
    <div style={{
      borderRadius: 10,
      border: isConfigured ? '1.5px solid rgba(56,189,248,0.22)' : '1.5px solid var(--wiz-border-sub)',
      background: 'var(--wiz-bg-xs)',
    }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 14px', borderBottom: '1px solid var(--wiz-border-sub)' }}>
        <div style={{
          width: 20, height: 20, borderRadius: '50%', flexShrink: 0,
          background: isConfigured ? 'linear-gradient(135deg,#38BDF8,#818CF8)' : 'var(--wiz-border-sub)',
          border: isConfigured ? 'none' : '1px solid var(--wiz-border)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 9, fontWeight: 700, color: isConfigured ? '#fff' : 'var(--wiz-text-sub)',
        }}>
          {isConfigured ? <Check size={10} strokeWidth={2.5}/> : index + 1}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: 'var(--wiz-text-md)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {label}
          </div>
          {isConfigured && providerDef && selectedCloudRegion && (
            <div style={{ fontSize: 10, color: 'var(--wiz-accent)', marginTop: 1 }}>
              {providerDef.name} · {PROVIDER_REGIONS[selectedProvider!].find(r => r.id === selectedCloudRegion)?.location}
            </div>
          )}
        </div>
      </div>

      {/* Dropdowns */}
      <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        <div>
          <div style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--wiz-text-sub)', marginBottom: 5 }}>Provider</div>
          <CustomSelect
            value={selectedProvider ?? ''}
            onChange={v => onSelectProvider(v as CloudProvider)}
            options={providerOptions}
            placeholder="Select provider…"
          />
        </div>

        {selectedProvider && (
          <div>
            <div style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--wiz-text-sub)', marginBottom: 5 }}>Region</div>
            <CustomSelect
              value={selectedCloudRegion ?? ''}
              onChange={v => onSelectCloudRegion(v)}
              options={regionOptions}
              placeholder="Select region…"
            />
          </div>
        )}
      </div>
    </div>
  )
}

/* ── StepProvider ────────────────────────────────────────────────── */
export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()

  const topology     = store.topology
  const regionCount  = topology ? (TOPOLOGY_REGION_COUNT[topology]  ?? 1) : 1
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? ['Region 1']) : ['Region 1']
  const hint         = getHqHint(store.orgHeadquarters)

  /* On first visit: apply HQ hint, or fall back to first provider + first region */
  useEffect(() => {
    if (Object.keys(store.regionProviders).length > 0) return
    const provider = hint?.provider ?? PROVIDERS[0].id
    for (let i = 0; i < regionCount; i++) {
      store.setRegionProvider(i, provider)
      if (i === 0) store.setProvider(provider)
      const regions = PROVIDER_REGIONS[provider]
      const region  = hint?.regions[i % hint.regions.length] ?? regions[i % regions.length].id
      store.setRegionCloudRegion(i, region)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const allConfigured = Array.from({ length: regionCount }, (_, i) => i)
    .every(i => !!store.regionProviders[i] && !!store.regionCloudRegions[i])

  function handleSelectProvider(i: number, provider: CloudProvider) {
    store.setRegionProvider(i, provider)
    if (i === 0) store.setProvider(provider)
    // Auto-select first region of the new provider
    const regions = PROVIDER_REGIONS[provider]
    const hintRegion = hint?.provider === provider ? hint.regions[i % hint.regions.length] : null
    const autoRegion = hintRegion ?? regions[0].id
    store.setRegionCloudRegion(i, autoRegion)
  }

  const cols = Array(regionCount).fill('1fr').join(' ')

  return (
    <StepShell
      title="Cloud provider per region"
      description="Choose a provider and region for each topology slot. You can mix providers across regions."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      {hint && (
        <div style={{ borderRadius: 8, padding: '7px 12px', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.12)', fontSize: 11, color: 'var(--wiz-accent)' }}>
          ★ Pre-selected based on HQ: <strong style={{ color: 'var(--wiz-accent)' }}>{store.orgHeadquarters}</strong>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: cols, gap: 12, alignItems: 'start' }}>
        {regionLabels.map((label, i) => (
          <RegionCard
            key={i}
            index={i}
            label={label}
            selectedProvider={store.regionProviders[i]}
            selectedCloudRegion={store.regionCloudRegions[i]}
            onSelectProvider={p => handleSelectProvider(i, p)}
            onSelectCloudRegion={r => store.setRegionCloudRegion(i, r)}
          />
        ))}
      </div>

    </StepShell>
  )
}
