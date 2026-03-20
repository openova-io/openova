import { useEffect } from 'react'
import { Check } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS, PROVIDER_REGIONS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

interface ProviderDef { id: CloudProvider; name: string; logo: React.ReactNode }

const PROVIDERS: ProviderDef[] = [
  { id: 'hetzner', name: 'Hetzner Cloud',       logo: <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#D50C2D"/><path d="M5 6h5v12H5zM14 6h5v12h-5z" fill="#fff"/></svg> },
  { id: 'huawei',  name: 'Huawei Cloud',         logo: <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#CF0A2C"/><path d="M12 5L14 9.5L19 9.5L15 12.5L17 17L12 14L7 17L9 12.5L5 9.5L10 9.5Z" fill="#fff"/></svg> },
  { id: 'oci',     name: 'Oracle Cloud (OCI)',    logo: <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#F80000"/><ellipse cx={12} cy={12} rx={7} ry={4.5} fill="none" stroke="#fff" strokeWidth={1.5}/></svg> },
  { id: 'aws',     name: 'Amazon Web Services',   logo: <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#232F3E"/><path d="M7 15c2.5 1.8 7.5 1.8 10 0" stroke="#FF9900" strokeWidth={1.5} fill="none" strokeLinecap="round"/><path d="M12 8v5" stroke="#FF9900" strokeWidth={1.5} strokeLinecap="round"/><path d="M10 11l2-3 2 3" stroke="#FF9900" strokeWidth={1.2} fill="none" strokeLinecap="round"/></svg> },
  { id: 'azure',   name: 'Microsoft Azure',       logo: <svg viewBox="0 0 24 24" width={16} height={16} style={{flexShrink:0}}><rect width={24} height={24} rx={4} fill="#0078D4"/><path d="M11 7L7 17h4l2-4 2 4h4L15 7z" fill="#fff" opacity={0.9}/></svg> },
]

/* ── HQ-based provider + region hints ────────────────────────────── */
const HQ_HINTS: Array<{ match: RegExp; provider: CloudProvider; regions: string[] }> = [
  { match: /germany|frankfurt|berlin|munich|hamburg|cologne|düsseldorf/i, provider: 'hetzner',  regions: ['fsn1', 'nbg1', 'hel1'] },
  { match: /finland|helsinki|sweden|stockholm|norway|denmark|nordic/i,    provider: 'hetzner',  regions: ['hel1', 'fsn1', 'nbg1'] },
  { match: /netherlands|amsterdam|belgium|brussels|luxembourg/i,           provider: 'oci',      regions: ['eu-amsterdam-1', 'eu-frankfurt-1', 'ap-singapore-1'] },
  { match: /france|paris/i,                                                provider: 'huawei',   regions: ['eu-west-204', 'eu-west-101', 'ap-southeast-1'] },
  { match: /ireland|dublin|uk|london|britain/i,                            provider: 'aws',      regions: ['eu-west-1', 'eu-central-1', 'us-east-1'] },
  { match: /virginia|ashburn|washington|new york|boston|east.*us/i,        provider: 'aws',      regions: ['us-east-1', 'us-west-2', 'eu-west-1'] },
  { match: /california|oregon|seattle|phoenix|west.*us/i,                  provider: 'aws',      regions: ['us-west-2', 'us-east-1', 'ap-southeast-1'] },
  { match: /singapore|malaysia|indonesia|thailand/i,                       provider: 'aws',      regions: ['ap-southeast-1', 'us-east-1', 'eu-central-1'] },
  { match: /hong kong|china|beijing|shanghai/i,                            provider: 'huawei',   regions: ['ap-southeast-1', 'cn-north-4', 'eu-west-101'] },
  { match: /saudi|riyadh|dubai|uae|middle east|bahrain/i,                  provider: 'huawei',   regions: ['me-east-1', 'ap-southeast-1', 'eu-west-101'] },
  { match: /japan|tokyo|korea|seoul/i,                                      provider: 'aws',      regions: ['ap-southeast-1', 'us-west-2', 'eu-central-1'] },
  { match: /australia|sydney|melbourne/i,                                   provider: 'oci',      regions: ['ap-singapore-1', 'us-ashburn-1', 'eu-frankfurt-1'] },
]

function getHqHint(hq: string): { provider: CloudProvider; regions: string[] } | null {
  return HQ_HINTS.find(h => h.match.test(hq)) ?? null
}

/* ── RegionCard — always open ────────────────────────────────────── */
function RegionCard({ index, label, selectedProvider, selectedCloudRegion, onSelectProvider, onSelectCloudRegion, hintProvider }: {
  index: number
  label: string
  selectedProvider: CloudProvider | undefined
  selectedCloudRegion: string | undefined
  onSelectProvider: (p: CloudProvider) => void
  onSelectCloudRegion: (r: string) => void
  hintProvider: CloudProvider | null
}) {
  const cloudRegions = selectedProvider ? PROVIDER_REGIONS[selectedProvider] : []
  const isConfigured = selectedProvider != null && selectedCloudRegion != null && selectedCloudRegion !== ''

  return (
    <div style={{
      display: 'flex', flexDirection: 'column',
      borderRadius: 10,
      border: isConfigured ? '1.5px solid rgba(56,189,248,0.22)' : '1.5px solid rgba(255,255,255,0.08)',
      background: isConfigured ? 'rgba(56,189,248,0.02)' : 'rgba(255,255,255,0.02)',
      overflow: 'hidden', transition: 'border-color 0.15s',
    }}>
      {/* Region header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 14px', borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
        <div style={{
          width: 20, height: 20, borderRadius: '50%', flexShrink: 0,
          background: isConfigured ? 'linear-gradient(135deg, #38BDF8, #818CF8)' : 'rgba(255,255,255,0.06)',
          border: isConfigured ? 'none' : '1px solid rgba(255,255,255,0.12)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 9, fontWeight: 700, color: isConfigured ? '#fff' : 'rgba(255,255,255,0.3)',
        }}>
          {isConfigured ? <Check size={10} strokeWidth={2.5} /> : index + 1}
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: isConfigured ? 'rgba(255,255,255,0.8)' : 'rgba(255,255,255,0.45)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {label}
          </div>
          {isConfigured && selectedProvider && selectedCloudRegion && (
            <div style={{ fontSize: 10, color: 'rgba(56,189,248,0.65)', marginTop: 1, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {PROVIDERS.find(p => p.id === selectedProvider)?.name} · {PROVIDER_REGIONS[selectedProvider].find(r => r.id === selectedCloudRegion)?.location}
            </div>
          )}
        </div>
      </div>

      <div style={{ padding: '10px 14px', display: 'flex', flexDirection: 'column', gap: 12, flex: 1 }}>
        {/* Provider — radio list */}
        <div>
          <div style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'rgba(255,255,255,0.28)', marginBottom: 6 }}>Cloud Provider</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {PROVIDERS.map(p => {
              const active = selectedProvider === p.id
              const isHint = hintProvider === p.id && !selectedProvider
              return (
                <div
                  key={p.id}
                  onClick={() => onSelectProvider(p.id)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 8,
                    padding: '7px 10px', borderRadius: 7, cursor: 'pointer',
                    border: active
                      ? '1.5px solid rgba(56,189,248,0.45)'
                      : isHint
                        ? '1.5px solid rgba(56,189,248,0.18)'
                        : '1.5px solid rgba(255,255,255,0.06)',
                    background: active
                      ? 'rgba(56,189,248,0.08)'
                      : isHint
                        ? 'rgba(56,189,248,0.03)'
                        : 'transparent',
                    transition: 'all 0.12s',
                  }}
                >
                  {/* Radio dot */}
                  <div style={{
                    width: 14, height: 14, borderRadius: '50%', flexShrink: 0,
                    border: active ? '4px solid #38BDF8' : '1.5px solid rgba(255,255,255,0.2)',
                    background: active ? 'transparent' : 'transparent',
                    transition: 'all 0.12s',
                  }} />
                  {p.logo}
                  <span style={{ flex: 1, fontSize: 11, fontWeight: active ? 600 : 400, color: active ? '#fff' : 'rgba(255,255,255,0.5)' }}>
                    {p.name}
                  </span>
                  {isHint && (
                    <span style={{ fontSize: 8, fontWeight: 700, color: '#38BDF8', background: 'rgba(56,189,248,0.1)', border: '1px solid rgba(56,189,248,0.2)', borderRadius: 3, padding: '1px 5px' }}>
                      ★ nearest
                    </span>
                  )}
                </div>
              )
            })}
          </div>
        </div>

        {/* Cloud region — radio grid, visible only after provider picked */}
        {selectedProvider ? (
          <div>
            <div style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'rgba(255,255,255,0.28)', marginBottom: 6 }}>Cloud Region</div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {cloudRegions.map(r => {
                const active = selectedCloudRegion === r.id
                return (
                  <div
                    key={r.id}
                    onClick={() => onSelectCloudRegion(r.id)}
                    style={{
                      display: 'flex', alignItems: 'center', gap: 8,
                      padding: '6px 10px', borderRadius: 7, cursor: 'pointer',
                      border: active ? '1.5px solid rgba(56,189,248,0.45)' : '1.5px solid rgba(255,255,255,0.06)',
                      background: active ? 'rgba(56,189,248,0.08)' : 'transparent',
                      transition: 'all 0.12s',
                    }}
                  >
                    <div style={{
                      width: 14, height: 14, borderRadius: '50%', flexShrink: 0,
                      border: active ? '4px solid #38BDF8' : '1.5px solid rgba(255,255,255,0.2)',
                      transition: 'all 0.12s',
                    }} />
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ fontSize: 11, fontWeight: active ? 600 : 400, color: active ? '#fff' : 'rgba(255,255,255,0.55)' }}>{r.label}</div>
                      <div style={{ fontSize: 9, color: 'rgba(255,255,255,0.28)', marginTop: 1 }}>{r.location}</div>
                    </div>
                  </div>
                )
              })}
            </div>
          </div>
        ) : (
          <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.18)', fontStyle: 'italic', paddingTop: 4 }}>
            Pick a provider to see available regions
          </div>
        )}
      </div>
    </div>
  )
}

export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const bp = useBreakpoint()

  const topology     = store.topology
  const regionCount  = topology ? (TOPOLOGY_REGION_COUNT[topology]  ?? 1) : 1
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? ['Region 1']) : ['Region 1']

  const hint = getHqHint(store.orgHeadquarters)

  /* Pre-select provider + staggered regions based on HQ — only on first visit */
  useEffect(() => {
    if (Object.keys(store.regionProviders).length > 0) return
    if (!hint) return
    for (let i = 0; i < regionCount; i++) {
      store.setRegionProvider(i, hint.provider)
      if (i === 0) store.setProvider(hint.provider)
      store.setRegionCloudRegion(i, hint.regions[i % hint.regions.length])
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const allConfigured = Array.from({ length: regionCount }, (_, i) => i)
    .every(i => store.regionProviders[i] != null && store.regionCloudRegions[i] != null && store.regionCloudRegions[i] !== '')

  function handleSelectProvider(regionIndex: number, provider: CloudProvider) {
    if (store.regionProviders[regionIndex] !== provider) {
      store.setRegionCloudRegion(regionIndex, '')
    }
    store.setRegionProvider(regionIndex, provider)
    if (regionIndex === 0) store.setProvider(provider)
  }

  function handleSelectCloudRegion(regionIndex: number, cloudRegion: string) {
    store.setRegionCloudRegion(regionIndex, cloudRegion)
  }

  /* Grid columns: match region count, max 3 on desktop */
  const cols = bp === 'mobile'
    ? '1fr'
    : bp === 'tablet'
      ? regionCount === 1 ? '1fr' : '1fr 1fr'
      : regionCount === 1 ? '480px' : regionCount === 2 ? '1fr 1fr' : '1fr 1fr 1fr'

  return (
    <StepShell
      title="Cloud provider per region"
      description="Select a provider and cloud region for each topology region. Providers can differ across regions."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      {hint && (
        <div style={{ borderRadius: 8, padding: '8px 12px', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.12)', fontSize: 11, color: 'rgba(56,189,248,0.65)' }}>
          ★ Pre-selected based on your HQ location: <strong style={{ color: 'rgba(56,189,248,0.85)' }}>{store.orgHeadquarters}</strong>
        </div>
      )}

      {/* All region cards side-by-side, always open */}
      <div style={{ display: 'grid', gridTemplateColumns: cols, gap: 12, alignItems: 'start' }}>
        {regionLabels.map((label, i) => (
          <RegionCard
            key={i}
            index={i}
            label={label}
            selectedProvider={store.regionProviders[i]}
            selectedCloudRegion={store.regionCloudRegions[i]}
            onSelectProvider={(p) => handleSelectProvider(i, p)}
            onSelectCloudRegion={(r) => handleSelectCloudRegion(i, r)}
            hintProvider={hint?.provider ?? null}
          />
        ))}
      </div>

      {/* Credentials summary once all configured */}
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
