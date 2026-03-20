import { useWizardStore } from '@/entities/deployment/store'
import type { CloudProvider, TopologyTemplate } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

const TOPOLOGY_REGION_LABELS: Record<TopologyTemplate, string[]> = {
  titan:    ['CP Region (MGMT / Bunker)', 'DP Region 1 (DMZ + RTZ)', 'DP Region 2 (DMZ + RTZ + DR-MGMT)'],
  triangle: ['CP Region (MGMT)', 'DP Region 1 (DMZ + RTZ)', 'DP Region 2 (DMZ + RTZ)'],
  dual:     ['Region 1 (Primary — MGMT + Workload)', 'Region 2 (DR — MGMT + Workload)'],
  compact:  ['Region 1 (MGMT + Workload)'],
  solo:     ['Region 1 (Single cluster)'],
}

interface ProviderDef {
  id: CloudProvider
  name: string
  description: string
  regions: Record<string, string>  // code → display label
  available: boolean
}

const PROVIDERS: ProviderDef[] = [
  {
    id: 'hetzner',
    name: 'Hetzner Cloud',
    description: 'Cost-effective European cloud with excellent price-performance.',
    available: true,
    regions: {
      'nbg1': 'Nuremberg, Germany',
      'fsn1': 'Falkenstein, Germany',
      'hel1': 'Helsinki, Finland',
      'ash':  'Ashburn, VA (US East)',
      'hil':  'Hillsboro, OR (US West)',
      'sin':  'Singapore',
    },
  },
  {
    id: 'huawei',
    name: 'Huawei Cloud',
    description: 'Enterprise cloud with strong presence in Asia and EMEA.',
    available: false,
    regions: {},
  },
  {
    id: 'oci',
    name: 'Oracle Cloud (OCI)',
    description: 'High-performance cloud optimised for enterprise workloads.',
    available: false,
    regions: {},
  },
  {
    id: 'aws',
    name: 'Amazon Web Services',
    description: 'Global hyperscaler with the broadest service portfolio.',
    available: false,
    regions: {},
  },
  {
    id: 'azure',
    name: 'Microsoft Azure',
    description: 'Enterprise-grade cloud with deep Microsoft ecosystem integration.',
    available: false,
    regions: {},
  },
]

const PROVIDER_LOGOS: Record<CloudProvider, React.ReactNode> = {
  hetzner: (
    <svg viewBox="0 0 40 40" width={28} height={28}>
      <rect width={40} height={40} rx={8} fill="#D50C2D" />
      <path d="M10 10h8v20h-8zM22 10h8v20h-8z" fill="#fff" />
    </svg>
  ),
  huawei: (
    <svg viewBox="0 0 40 40" width={28} height={28}>
      <rect width={40} height={40} rx={8} fill="#CF0A2C" />
      <circle cx={20} cy={20} r={10} fill="none" stroke="#fff" strokeWidth={2} />
      <path d="M20 10 L22 16 L28 16 L23 20 L25 26 L20 22 L15 26 L17 20 L12 16 L18 16Z" fill="#fff" />
    </svg>
  ),
  oci: (
    <svg viewBox="0 0 40 40" width={28} height={28}>
      <rect width={40} height={40} rx={8} fill="#F80000" />
      <ellipse cx={20} cy={20} rx={12} ry={8} fill="none" stroke="#fff" strokeWidth={2.5} />
    </svg>
  ),
  aws: (
    <svg viewBox="0 0 40 40" width={28} height={28}>
      <rect width={40} height={40} rx={8} fill="#232F3E" />
      <path d="M12 22c4 3 12 3 16 0" stroke="#FF9900" strokeWidth={2.5} strokeLinecap="round" fill="none" />
      <path d="M20 14v8" stroke="#FF9900" strokeWidth={2.5} strokeLinecap="round" />
      <path d="M16 18l4-4 4 4" stroke="#FF9900" strokeWidth={2} fill="none" strokeLinecap="round" />
    </svg>
  ),
  azure: (
    <svg viewBox="0 0 40 40" width={28} height={28}>
      <rect width={40} height={40} rx={8} fill="#0078D4" />
      <path d="M18 12l-8 16h6l4-7 4 7h6z" fill="#fff" opacity={0.9} />
    </svg>
  ),
}

export function StepProvider() {
  const store = useWizardStore()
  const { next, back } = useStepNav()

  const regionLabels = store.topology
    ? TOPOLOGY_REGION_LABELS[store.topology]
    : ['Region 1']

  const selectedDef = PROVIDERS.find(p => p.id === store.provider)

  return (
    <StepShell
      title="Select your cloud provider"
      description="Choose where OpenOva will provision your infrastructure. You'll need API credentials in the next step."
      onNext={() => { if (store.provider) next() }}
      onBack={back}
      nextDisabled={!store.provider}
    >
      {/* Topology context — remind the user what regions need to be filled */}
      {store.topology && (
        <div style={{
          borderRadius: 10, background: 'rgba(56,189,248,0.05)',
          border: '1px solid rgba(56,189,248,0.12)', padding: '10px 14px',
        }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'rgba(56,189,248,0.7)', margin: '0 0 6px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>
            Regions to provision
          </p>
          {regionLabels.map((r, i) => (
            <div key={i} style={{ display: 'flex', gap: 8, fontSize: 11, color: 'rgba(255,255,255,0.45)', padding: '3px 0', alignItems: 'flex-start' }}>
              <span style={{ color: '#38BDF8', flexShrink: 0, fontWeight: 700 }}>{i + 1}</span>
              {r}
            </div>
          ))}
        </div>
      )}

      {/* Provider cards */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {PROVIDERS.map(p => {
          const selected = store.provider === p.id
          return (
            <button
              key={p.id}
              type="button"
              onClick={() => p.available && store.setProvider(p.id)}
              disabled={!p.available}
              style={{
                display: 'flex', alignItems: 'center', gap: 14, width: '100%',
                padding: '12px 14px', borderRadius: 10, textAlign: 'left',
                border: selected
                  ? '1.5px solid rgba(56,189,248,0.5)'
                  : '1.5px solid rgba(255,255,255,0.08)',
                background: selected ? 'rgba(56,189,248,0.06)' : 'rgba(255,255,255,0.02)',
                cursor: p.available ? 'pointer' : 'not-allowed',
                opacity: p.available ? 1 : 0.45,
                transition: 'all 0.18s',
                boxShadow: selected ? '0 0 0 3px rgba(56,189,248,0.08)' : 'none',
                fontFamily: 'Inter, sans-serif',
              }}
            >
              {/* Logo */}
              <div style={{ flexShrink: 0 }}>{PROVIDER_LOGOS[p.id]}</div>

              {/* Text */}
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ fontSize: 13, fontWeight: 600, color: selected ? '#fff' : 'rgba(255,255,255,0.7)' }}>
                    {p.name}
                  </span>
                  {!p.available && (
                    <span style={{ fontSize: 9, fontWeight: 700, color: 'rgba(255,255,255,0.25)', background: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.1)', borderRadius: 4, padding: '2px 6px', letterSpacing: '0.06em', textTransform: 'uppercase' }}>
                      coming soon
                    </span>
                  )}
                </div>
                <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.3)', marginTop: 2, lineHeight: 1.4 }}>
                  {p.description}
                </div>
              </div>

              {/* Selected check */}
              <div style={{
                width: 18, height: 18, borderRadius: '50%', flexShrink: 0,
                border: selected ? 'none' : '1.5px solid rgba(255,255,255,0.15)',
                background: selected ? '#38BDF8' : 'transparent',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                transition: 'all 0.2s',
              }}>
                {selected && (
                  <svg width={10} height={10} viewBox="0 0 12 12" fill="none">
                    <path d="M2 6l3 3 5-5" stroke="#fff" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round"/>
                  </svg>
                )}
              </div>
            </button>
          )
        })}
      </div>

      {/* Region mapping for selected provider */}
      {selectedDef && selectedDef.available && Object.keys(selectedDef.regions).length > 0 && (
        <div style={{
          borderRadius: 10,
          border: '1px solid rgba(255,255,255,0.08)',
          background: 'rgba(255,255,255,0.02)',
          padding: '12px 14px',
        }}>
          <p style={{ fontSize: 11, fontWeight: 600, color: 'rgba(255,255,255,0.35)', margin: '0 0 8px', textTransform: 'uppercase', letterSpacing: '0.08em' }}>
            Available regions
          </p>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {Object.entries(selectedDef.regions).map(([code, label]) => (
              <div key={code} style={{
                fontSize: 11, padding: '3px 10px', borderRadius: 6,
                border: '1px solid rgba(255,255,255,0.1)',
                background: 'rgba(255,255,255,0.04)',
                color: 'rgba(255,255,255,0.5)',
              }}>
                <span style={{ fontWeight: 600, color: 'rgba(56,189,248,0.7)' }}>{code}</span>
                <span style={{ margin: '0 4px', color: 'rgba(255,255,255,0.2)' }}>·</span>
                {label}
              </div>
            ))}
          </div>
        </div>
      )}
    </StepShell>
  )
}
