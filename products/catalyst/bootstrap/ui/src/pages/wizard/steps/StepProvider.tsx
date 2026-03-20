import { useEffect } from 'react'
import { Check } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider } from '@/entities/deployment/model'
import { TOPOLOGY_REGION_COUNT, TOPOLOGY_REGION_LABELS, PROVIDER_REGIONS } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

const PROVIDERS: { id: CloudProvider; name: string }[] = [
  { id: 'hetzner', name: 'Hetzner Cloud' },
  { id: 'huawei',  name: 'Huawei Cloud' },
  { id: 'oci',     name: 'Oracle Cloud (OCI)' },
  { id: 'aws',     name: 'Amazon Web Services' },
  { id: 'azure',   name: 'Microsoft Azure' },
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

function getHqHint(hq: string) {
  return HQ_HINTS.find(h => h.match.test(hq)) ?? null
}

/* ── Shared dropdown style ───────────────────────────────────────── */
const selectStyle: React.CSSProperties = {
  width: '100%',
  padding: '8px 32px 8px 10px',
  borderRadius: 7,
  border: '1.5px solid rgba(255,255,255,0.1)',
  background: 'rgba(255,255,255,0.04)',
  color: 'rgba(255,255,255,0.75)',
  fontSize: 12,
  fontFamily: 'Inter, sans-serif',
  appearance: 'none',
  WebkitAppearance: 'none',
  cursor: 'pointer',
  outline: 'none',
}

function Dropdown({ value, onChange, children, configured }: {
  value: string
  onChange: (v: string) => void
  children: React.ReactNode
  configured?: boolean
}) {
  return (
    <div style={{ position: 'relative' }}>
      <select
        value={value}
        onChange={e => onChange(e.target.value)}
        style={{
          ...selectStyle,
          border: configured
            ? '1.5px solid rgba(56,189,248,0.4)'
            : '1.5px solid rgba(255,255,255,0.1)',
          background: configured
            ? 'rgba(56,189,248,0.06)'
            : 'rgba(255,255,255,0.04)',
        }}
      >
        {children}
      </select>
      {/* Chevron */}
      <div style={{
        position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)',
        pointerEvents: 'none', color: 'rgba(255,255,255,0.3)', fontSize: 10,
      }}>▾</div>
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

  return (
    <div style={{
      borderRadius: 10,
      border: isConfigured ? '1.5px solid rgba(56,189,248,0.22)' : '1.5px solid rgba(255,255,255,0.08)',
      background: 'rgba(255,255,255,0.02)',
      overflow: 'hidden',
    }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 14px', borderBottom: '1px solid rgba(255,255,255,0.06)' }}>
        <div style={{
          width: 20, height: 20, borderRadius: '50%', flexShrink: 0,
          background: isConfigured ? 'linear-gradient(135deg,#38BDF8,#818CF8)' : 'rgba(255,255,255,0.06)',
          border: isConfigured ? 'none' : '1px solid rgba(255,255,255,0.12)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          fontSize: 9, fontWeight: 700, color: isConfigured ? '#fff' : 'rgba(255,255,255,0.3)',
        }}>
          {isConfigured ? <Check size={10} strokeWidth={2.5}/> : index + 1}
        </div>
        <span style={{ fontSize: 11, fontWeight: 600, color: isConfigured ? 'rgba(255,255,255,0.8)' : 'rgba(255,255,255,0.4)', flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {label}
        </span>
      </div>

      {/* Dropdowns */}
      <div style={{ padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 10 }}>
        {/* Provider */}
        <div>
          <div style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'rgba(255,255,255,0.28)', marginBottom: 5 }}>Provider</div>
          <Dropdown
            value={selectedProvider ?? ''}
            onChange={v => v && onSelectProvider(v as CloudProvider)}
            configured={!!selectedProvider}
          >
            <option value="" disabled style={{ background: '#1a1a2e' }}>Select provider…</option>
            {PROVIDERS.map(p => (
              <option key={p.id} value={p.id} style={{ background: '#1a1a2e' }}>{p.name}</option>
            ))}
          </Dropdown>
        </div>

        {/* Region — only after provider picked */}
        {selectedProvider && (
          <div>
            <div style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'rgba(255,255,255,0.28)', marginBottom: 5 }}>Region</div>
            <Dropdown
              value={selectedCloudRegion ?? ''}
              onChange={v => v && onSelectCloudRegion(v)}
              configured={!!selectedCloudRegion}
            >
              <option value="" disabled style={{ background: '#1a1a2e' }}>Select region…</option>
              {PROVIDER_REGIONS[selectedProvider].map(r => (
                <option key={r.id} value={r.id} style={{ background: '#1a1a2e' }}>{r.label} — {r.location}</option>
              ))}
            </Dropdown>
          </div>
        )}
      </div>
    </div>
  )
}

export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()

  const topology     = store.topology
  const regionCount  = topology ? (TOPOLOGY_REGION_COUNT[topology]  ?? 1) : 1
  const regionLabels = topology ? (TOPOLOGY_REGION_LABELS[topology] ?? ['Region 1']) : ['Region 1']
  const hint         = getHqHint(store.orgHeadquarters)

  /* Apply HQ-based defaults on first visit */
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
    .every(i => !!store.regionProviders[i] && !!store.regionCloudRegions[i])

  function handleSelectProvider(i: number, provider: CloudProvider) {
    if (store.regionProviders[i] !== provider) store.setRegionCloudRegion(i, '')
    store.setRegionProvider(i, provider)
    if (i === 0) store.setProvider(provider)
  }

  const cols = regionCount === 1 ? '480px' : regionCount === 2 ? '1fr 1fr' : '1fr 1fr 1fr'

  return (
    <StepShell
      title="Cloud provider per region"
      description="Choose a provider and region for each topology slot. You can mix providers across regions."
      onNext={() => { if (allConfigured) next() }}
      onBack={back}
      nextDisabled={!allConfigured}
    >
      {hint && (
        <div style={{ borderRadius: 8, padding: '7px 12px', background: 'rgba(56,189,248,0.04)', border: '1px solid rgba(56,189,248,0.12)', fontSize: 11, color: 'rgba(56,189,248,0.6)' }}>
          ★ Pre-selected based on HQ: <strong style={{ color: 'rgba(56,189,248,0.8)' }}>{store.orgHeadquarters}</strong>
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

      {allConfigured && (
        <div style={{ borderRadius: 8, background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)', padding: '10px 14px' }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'rgba(56,189,248,0.7)', margin: '0 0 6px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>Credentials required next</p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {[...new Set(Object.values(store.regionProviders))].map(p => {
              const regions = Object.entries(store.regionProviders).filter(([,v]) => v === p).map(([k]) => Number(k))
              return (
                <div key={p} style={{ fontSize: 11, color: 'rgba(255,255,255,0.5)' }}>
                  <span style={{ fontWeight: 500, color: 'rgba(255,255,255,0.7)' }}>{PROVIDERS.find(d => d.id === p)?.name}</span>
                  <span style={{ color: 'rgba(255,255,255,0.25)', margin: '0 6px' }}>·</span>
                  Region{regions.length > 1 ? 's' : ''} {regions.map(r => r + 1).join(', ')}
                </div>
              )
            })}
          </div>
        </div>
      )}
    </StepShell>
  )
}
