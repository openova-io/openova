import { useWizardStore } from '@/entities/deployment/store'
import type { TopologyTemplate } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

/* ─────────────────────────────────────────────────────────────────────────
   Topology template definitions
   Each has: a mini SVG diagram, cluster count, region count, and guidance
─────────────────────────────────────────────────────────────────────────── */

interface TopoConfig {
  id: TopologyTemplate
  name: string
  tagline: string
  clusters: number
  regions: number
  tag: string
  tagColor: string
  recommended?: boolean
  diagram: React.ReactNode
  bullets: string[]
}

/* Mini diagram helpers */
const BOX = (x: number, y: number, w: number, h: number, label: string, color: string, textColor = '#fff', small = false) => (
  <g key={`${x}-${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={3} fill={color} opacity={0.9} />
    <text x={x + w / 2} y={y + h / 2 + (small ? 3.5 : 4.5)} textAnchor="middle"
      fontSize={small ? 6 : 7} fontWeight="600" fill={textColor} fontFamily="Inter,sans-serif">
      {label}
    </text>
  </g>
)

const REGION = (x: number, y: number, w: number, h: number, label: string) => (
  <g key={`r-${x}-${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={4} fill="none"
      stroke="rgba(255,255,255,0.2)" strokeWidth={0.8} strokeDasharray="3,2" />
    <text x={x + 5} y={y - 3} fontSize={6} fill="rgba(255,255,255,0.35)" fontFamily="Inter,sans-serif">{label}</text>
  </g>
)

const LINE = (x1: number, y1: number, x2: number, y2: number) => (
  <line key={`l-${x1}-${y1}`} x1={x1} y1={y1} x2={x2} y2={y2} stroke="rgba(56,189,248,0.3)" strokeWidth={0.8} />
)

const DiagramTitan = () => (
  <svg viewBox="0 0 180 80" width="100%" style={{ maxHeight: 80 }}>
    {REGION(4, 12, 46, 54, 'CP Region')}
    {BOX(12, 24, 28, 14, 'MGMT', 'rgba(56,189,248,0.7)')}
    {REGION(58, 12, 54, 54, 'DP Region 1')}
    {BOX(62, 24, 22, 12, 'DMZ', 'rgba(99,102,241,0.7)')}
    {BOX(62, 40, 22, 12, 'RTZ', 'rgba(99,102,241,0.5)')}
    {REGION(120, 12, 56, 54, 'DP Region 2')}
    {BOX(124, 24, 22, 12, 'DMZ', 'rgba(99,102,241,0.7)')}
    {BOX(124, 40, 22, 12, 'RTZ', 'rgba(99,102,241,0.5)')}
    {BOX(148, 24, 24, 12, 'DR-MGMT', 'rgba(56,189,248,0.35)', 'rgba(255,255,255,0.7)', true)}
    {LINE(40, 31, 62, 31)}
    {LINE(84, 31, 124, 31)}
    {LINE(26, 31, 148, 60)}
  </svg>
)

const DiagramTriangle = () => (
  <svg viewBox="0 0 180 80" width="100%" style={{ maxHeight: 80 }}>
    {REGION(4, 12, 46, 38, 'CP Region')}
    {BOX(12, 24, 28, 14, 'MGMT', 'rgba(56,189,248,0.7)')}
    {REGION(58, 12, 52, 54, 'DP Region 1')}
    {BOX(62, 24, 22, 12, 'DMZ', 'rgba(99,102,241,0.7)')}
    {BOX(62, 40, 22, 12, 'RTZ', 'rgba(99,102,241,0.5)')}
    {REGION(118, 12, 52, 54, 'DP Region 2')}
    {BOX(122, 24, 22, 12, 'DMZ', 'rgba(99,102,241,0.7)')}
    {BOX(122, 40, 22, 12, 'RTZ', 'rgba(99,102,241,0.5)')}
    {LINE(40, 31, 62, 31)}
    {LINE(84, 31, 122, 31)}
  </svg>
)

const DiagramDual = () => (
  <svg viewBox="0 0 180 80" width="100%" style={{ maxHeight: 80 }}>
    {REGION(10, 12, 68, 54, 'Region 1 (Primary)')}
    {BOX(18, 26, 24, 13, 'MGMT', 'rgba(56,189,248,0.7)')}
    {BOX(46, 26, 26, 13, 'Workload', 'rgba(99,102,241,0.6)')}
    {REGION(100, 12, 68, 54, 'Region 2 (DR)')}
    {BOX(108, 26, 24, 13, 'MGMT', 'rgba(56,189,248,0.4)')}
    {BOX(136, 26, 26, 13, 'Workload', 'rgba(99,102,241,0.3)')}
    {LINE(78, 33, 100, 33)}
  </svg>
)

const DiagramCompact = () => (
  <svg viewBox="0 0 180 80" width="100%" style={{ maxHeight: 80 }}>
    {REGION(40, 12, 100, 54, 'Region 1')}
    {BOX(54, 26, 28, 14, 'MGMT', 'rgba(56,189,248,0.7)')}
    {BOX(90, 26, 36, 14, 'Workload', 'rgba(99,102,241,0.6)')}
    {LINE(82, 33, 90, 33)}
  </svg>
)

const DiagramSolo = () => (
  <svg viewBox="0 0 180 80" width="100%" style={{ maxHeight: 80 }}>
    {REGION(50, 10, 80, 58, 'Single region')}
    {BOX(62, 26, 56, 26, 'All components', 'rgba(56,189,248,0.55)')}
  </svg>
)

const TOPOLOGIES: TopoConfig[] = [
  {
    id: 'titan',
    name: 'TITAN',
    tagline: 'Full enterprise — maximum resilience',
    clusters: 6,
    regions: 3,
    tag: 'Tier-1 Bank',
    tagColor: '#F59E0B',
    diagram: <DiagramTitan />,
    bullets: [
      'Dedicated CP (bunker) region — air-gapped management',
      'DR-MGMT cluster inside DP Region 2 — no 4th region needed',
      '2× data plane regions: independent DMZ + RTZ clusters',
      'Strict PCI DSS / ISO 27001 posture by default',
    ],
  },
  {
    id: 'triangle',
    name: 'TRIANGLE',
    tagline: 'Balanced — 3 regions, 5 clusters',
    clusters: 5,
    regions: 3,
    tag: 'Recommended',
    tagColor: '#22C55E',
    recommended: true,
    diagram: <DiagramTriangle />,
    bullets: [
      'Dedicated CP region (MGMT) + 2 data plane regions',
      'Each DP region: separate DMZ and RTZ clusters',
      'Strong isolation without DR overhead',
      'Ideal for regulated banks and insurance',
    ],
  },
  {
    id: 'dual',
    name: 'DUAL',
    tagline: 'Two-region active/passive',
    clusters: 4,
    regions: 2,
    tag: 'Mid-market',
    tagColor: '#38BDF8',
    diagram: <DiagramDual />,
    bullets: [
      'Primary region: MGMT + Workload clusters',
      'DR region: passive standby mirror',
      'Lower cost, simpler ops',
      'Good for fintechs and regional lenders',
    ],
  },
  {
    id: 'compact',
    name: 'COMPACT',
    tagline: 'Single-region, two-cluster',
    clusters: 2,
    regions: 1,
    tag: 'Pilot / Early-stage',
    tagColor: '#A78BFA',
    diagram: <DiagramCompact />,
    bullets: [
      'One region: dedicated MGMT + Workload cluster',
      'Clean separation without multi-region complexity',
      'Easy path to upgrade to DUAL or TRIANGLE',
      'Suitable for regulated pilots and MVPs',
    ],
  },
  {
    id: 'solo',
    name: 'SOLO',
    tagline: 'Everything on one cluster',
    clusters: 1,
    regions: 1,
    tag: 'Dev / POC',
    tagColor: '#6B7280',
    diagram: <DiagramSolo />,
    bullets: [
      'Single cluster, single region',
      'Minimal cost and complexity',
      'Not suitable for production workloads',
      'Ideal for demos, local dev, evaluations',
    ],
  },
]

export function StepTopology() {
  const store = useWizardStore()
  const { next, back } = useStepNav()

  return (
    <StepShell
      title="Choose your infrastructure topology"
      description="Your topology defines how many regions and clusters OpenOva will provision. We recommend TRIANGLE for most regulated organisations — select TITAN for Tier-1 bank requirements."
      onNext={() => { if (store.topology) next() }}
      onBack={back}
      nextDisabled={!store.topology}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {TOPOLOGIES.map(t => {
          const selected = store.topology === t.id
          return (
            <button
              key={t.id}
              type="button"
              onClick={() => store.setTopology(t.id)}
              style={{
                display: 'block', width: '100%', textAlign: 'left',
                padding: '14px 16px',
                borderRadius: 12,
                border: selected
                  ? '1.5px solid rgba(56,189,248,0.55)'
                  : '1.5px solid rgba(255,255,255,0.08)',
                background: selected
                  ? 'rgba(56,189,248,0.07)'
                  : 'rgba(255,255,255,0.03)',
                cursor: 'pointer',
                transition: 'all 0.2s',
                boxShadow: selected ? '0 0 0 3px rgba(56,189,248,0.08)' : 'none',
                fontFamily: 'Inter, sans-serif',
              }}
            >
              {/* Header row */}
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 10 }}>
                {/* Selection dot */}
                <div style={{
                  width: 16, height: 16, borderRadius: '50%', flexShrink: 0,
                  border: selected ? 'none' : '1.5px solid rgba(255,255,255,0.2)',
                  background: selected ? '#38BDF8' : 'transparent',
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                  transition: 'all 0.2s',
                }}>
                  {selected && <div style={{ width: 6, height: 6, borderRadius: '50%', background: '#fff' }} />}
                </div>

                <div style={{ flex: 1 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                    <span style={{ fontSize: 13, fontWeight: 700, color: selected ? '#fff' : 'rgba(255,255,255,0.75)', letterSpacing: '0.02em' }}>
                      {t.name}
                    </span>
                    <span style={{
                      fontSize: 9, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase',
                      color: t.tagColor, background: `${t.tagColor}18`,
                      border: `1px solid ${t.tagColor}40`, borderRadius: 4, padding: '2px 7px',
                    }}>
                      {t.tag}
                    </span>
                    {t.recommended && (
                      <span style={{ fontSize: 9, color: 'rgba(56,189,248,0.6)', fontWeight: 600 }}>← start here</span>
                    )}
                  </div>
                  <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.35)', marginTop: 2 }}>{t.tagline}</div>
                </div>

                {/* Stats */}
                <div style={{ display: 'flex', gap: 10, flexShrink: 0 }}>
                  {[
                    { label: 'regions', val: t.regions },
                    { label: 'clusters', val: t.clusters },
                  ].map(({ label, val }) => (
                    <div key={label} style={{ textAlign: 'center' }}>
                      <div style={{ fontSize: 16, fontWeight: 700, color: selected ? '#38BDF8' : 'rgba(255,255,255,0.5)', lineHeight: 1 }}>{val}</div>
                      <div style={{ fontSize: 9, color: 'rgba(255,255,255,0.25)', lineHeight: 1, marginTop: 2 }}>{label}</div>
                    </div>
                  ))}
                </div>
              </div>

              {/* Diagram */}
              <div style={{
                borderRadius: 8,
                background: 'rgba(0,0,0,0.3)',
                border: '1px solid rgba(255,255,255,0.06)',
                padding: '10px 12px 6px',
                marginBottom: 10,
              }}>
                {t.diagram}
              </div>

              {/* Bullets — only show when selected */}
              {selected && (
                <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 5 }}>
                  {t.bullets.map(b => (
                    <li key={b} style={{ display: 'flex', gap: 8, fontSize: 11, color: 'rgba(255,255,255,0.5)', alignItems: 'flex-start', lineHeight: 1.45 }}>
                      <span style={{ color: '#38BDF8', flexShrink: 0, marginTop: 1 }}>·</span>
                      {b}
                    </li>
                  ))}
                </ul>
              )}
            </button>
          )
        })}
      </div>
    </StepShell>
  )
}
