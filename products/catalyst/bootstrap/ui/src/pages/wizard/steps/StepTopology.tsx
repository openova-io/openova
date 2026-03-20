import { useWizardStore } from '@/entities/deployment/store'
import type { TopologyTemplate } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

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

const BOX = (x: number, y: number, w: number, h: number, label: string, fill: string, textFill = '#fff') => (
  <g key={`b${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={4} fill={fill} />
    <text x={x + w / 2} y={y + h / 2 + 5} textAnchor="middle" fontSize={9} fontWeight="700" fill={textFill} fontFamily="Inter,sans-serif">{label}</text>
  </g>
)
const REGION = (x: number, y: number, w: number, h: number, label: string) => (
  <g key={`r${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={6} fill="none" stroke="rgba(255,255,255,0.18)" strokeWidth={1} strokeDasharray="4,3" />
    <text x={x + 7} y={y - 5} fontSize={8} fill="rgba(255,255,255,0.4)" fontFamily="Inter,sans-serif" fontWeight="500">{label}</text>
  </g>
)
const CONN = (x1: number, y1: number, x2: number, y2: number) => (
  <line key={`c${x1}${y1}`} x1={x1} y1={y1} x2={x2} y2={y2} stroke="rgba(56,189,248,0.35)" strokeWidth={1.2} strokeDasharray="3,2" />
)

const DiagramTriangle = () => (
  <svg viewBox="0 0 280 170" width="100%">
    {REGION(90, 14, 100, 56, 'CP Region')}
    {BOX(100, 26, 80, 32, 'MGMT', 'rgba(56,189,248,0.75)')}
    {REGION(5, 92, 120, 72, 'DP Region 1')}
    {BOX(10, 108, 50, 48, 'DMZ', 'rgba(99,102,241,0.75)')}
    {BOX(65, 108, 50, 48, 'RTZ', 'rgba(99,102,241,0.5)')}
    {REGION(155, 92, 120, 72, 'DP Region 2')}
    {BOX(160, 108, 50, 48, 'DMZ', 'rgba(99,102,241,0.75)')}
    {BOX(215, 108, 50, 48, 'RTZ', 'rgba(99,102,241,0.5)')}
    {CONN(140, 70, 65, 92)}
    {CONN(140, 70, 215, 92)}
    {CONN(125, 130, 155, 130)}
  </svg>
)

const DiagramDual = () => (
  <svg viewBox="0 0 280 145" width="100%">
    {REGION(5, 10, 120, 126, 'Region 1 · Primary')}
    {BOX(14, 26, 100, 28, 'MGMT', 'rgba(56,189,248,0.75)')}
    {BOX(14, 58, 100, 28, 'DMZ', 'rgba(99,102,241,0.75)')}
    {BOX(14, 90, 100, 28, 'RTZ', 'rgba(99,102,241,0.5)')}
    {REGION(155, 10, 120, 126, 'Region 2 · DR')}
    {BOX(164, 26, 100, 28, 'MGMT', 'rgba(56,189,248,0.3)', 'rgba(255,255,255,0.6)')}
    {BOX(164, 58, 100, 28, 'DMZ', 'rgba(99,102,241,0.4)', 'rgba(255,255,255,0.6)')}
    {BOX(164, 90, 100, 28, 'RTZ', 'rgba(99,102,241,0.3)', 'rgba(255,255,255,0.6)')}
    {CONN(125, 72, 155, 72)}
  </svg>
)

const DiagramZoned = () => (
  <svg viewBox="0 0 280 130" width="100%">
    {REGION(5, 14, 124, 100, 'Region 1 · Primary')}
    {BOX(12, 32, 50, 65, 'DMZ', 'rgba(99,102,241,0.75)')}
    {BOX(68, 32, 52, 65, 'MGMT·RTZ', 'rgba(56,189,248,0.65)')}
    {REGION(151, 14, 124, 100, 'Region 2 · DR')}
    {BOX(158, 32, 50, 65, 'DMZ', 'rgba(99,102,241,0.45)', 'rgba(255,255,255,0.6)')}
    {BOX(214, 32, 52, 65, 'MGMT·RTZ', 'rgba(56,189,248,0.3)', 'rgba(255,255,255,0.6)')}
    {CONN(129, 65, 151, 65)}
  </svg>
)

const DiagramCompact = () => (
  <svg viewBox="0 0 280 120" width="100%">
    {REGION(10, 14, 114, 90, 'Region 1 · Primary')}
    {BOX(22, 36, 90, 44, 'All components', 'rgba(56,189,248,0.55)')}
    {REGION(156, 14, 114, 90, 'Region 2 · Secondary')}
    {BOX(168, 36, 90, 44, 'All components', 'rgba(56,189,248,0.3)', 'rgba(255,255,255,0.6)')}
    {CONN(124, 58, 156, 58)}
  </svg>
)

const DiagramSolo = () => (
  <svg viewBox="0 0 280 130" width="100%">
    {REGION(70, 18, 140, 88, 'Single region')}
    {BOX(90, 38, 100, 44, 'All components', 'rgba(56,189,248,0.55)')}
  </svg>
)

const TOPOLOGIES: TopoConfig[] = [
  {
    id: 'triangle', name: 'TRIANGLE', tagline: 'Three-region — dedicated CP + dual data plane',
    clusters: 5, regions: 3, tag: 'Tier-1 Bank', tagColor: '#F59E0B',
    diagram: <DiagramTriangle />,
    bullets: [
      'Dedicated CP region — MGMT cluster only, isolated from data plane',
      'Two data plane regions: independent DMZ + RTZ clusters each',
      'Triangle connectivity — CP↔DP1, CP↔DP2, DP1↔DP2',
      'Designed for PCI DSS / DORA / ISO 27001 from day one',
    ],
  },
  {
    id: 'dual', name: 'DUAL', tagline: 'Two-region — full cluster separation (3 per region)',
    clusters: 6, regions: 2, tag: 'Enterprise', tagColor: '#22C55E', recommended: true,
    diagram: <DiagramDual />,
    bullets: [
      'Each region: MGMT, DMZ, and RTZ clusters fully separated',
      'Primary + DR region with identical cluster topology',
      'Strong workload isolation with a proven upgrade path to TRIANGLE',
      'Recommended for regulated banks, insurance, and fintechs',
    ],
  },
  {
    id: 'zoned', name: 'ZONED', tagline: 'Two-region — DMZ isolated, MGMT+RTZ merged',
    clusters: 4, regions: 2, tag: 'Mid-market', tagColor: '#38BDF8',
    diagram: <DiagramZoned />,
    bullets: [
      'DMZ cluster fully isolated for ingress and edge workloads',
      'MGMT and RTZ co-located — reduces cluster overhead',
      'Lower cost than DUAL, higher isolation than COMPACT',
      'Good fit for mid-market banks and regional lenders',
    ],
  },
  {
    id: 'compact', name: 'COMPACT', tagline: 'Two-region — single all-in-one cluster each',
    clusters: 2, regions: 2, tag: 'Starter', tagColor: '#A78BFA',
    diagram: <DiagramCompact />,
    bullets: [
      'Two regions, one cluster per region — geo-redundant SOLO',
      'All platform components share a single cluster per site',
      'Lowest cost multi-region option — clear path to ZONED or DUAL',
      'Ideal for regulated pilots and geo-HA evaluations',
    ],
  },
  {
    id: 'solo', name: 'SOLO', tagline: 'Single region, single cluster',
    clusters: 1, regions: 1, tag: 'Dev / POC', tagColor: '#6B7280',
    diagram: <DiagramSolo />,
    bullets: [
      'Single cluster, single region — lowest cost and simplest',
      'No isolation between management and workloads',
      'Not suitable for production or regulated workloads',
      'Ideal for demos, evaluations, and training environments',
    ],
  },
]

/* ── Topology detail panel (shared between desktop/mobile) ─────────── */
function TopologyDetail({ t }: { t: TopoConfig }) {
  return (
    <div style={{ borderRadius: 12, border: '1px solid rgba(56,189,248,0.15)', background: 'rgba(56,189,248,0.04)', overflow: 'hidden' }}>
      <div style={{ padding: '14px 18px', borderBottom: '1px solid rgba(255,255,255,0.07)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
          <span style={{ fontSize: 15, fontWeight: 800, color: '#fff', letterSpacing: '0.04em' }}>{t.name}</span>
          <span style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase', color: t.tagColor, background: `${t.tagColor}18`, border: `1px solid ${t.tagColor}38`, borderRadius: 4, padding: '2px 7px' }}>{t.tag}</span>
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 16 }}>
            {[{ val: t.regions, lbl: 'regions' }, { val: t.clusters, lbl: 'clusters' }].map(({ val, lbl }) => (
              <div key={lbl} style={{ textAlign: 'center' }}>
                <div style={{ fontSize: 16, fontWeight: 700, color: '#38BDF8', lineHeight: 1 }}>{val}</div>
                <div style={{ fontSize: 9, color: 'rgba(255,255,255,0.3)', marginTop: 2 }}>{lbl}</div>
              </div>
            ))}
          </div>
        </div>
        <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.4)', lineHeight: 1.4 }}>{t.tagline}</div>
      </div>
      <div style={{ padding: '16px 18px 10px', background: 'rgba(0,0,0,0.25)' }}>{t.diagram}</div>
      <div style={{ padding: '12px 18px 16px' }}>
        <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }}>
          {t.bullets.map(b => (
            <li key={b} style={{ display: 'flex', gap: 8, fontSize: 11, color: 'rgba(255,255,255,0.5)', lineHeight: 1.5 }}>
              <span style={{ color: '#38BDF8', flexShrink: 0, marginTop: 1 }}>·</span>{b}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}

export function StepTopology() {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const bp = useBreakpoint()

  const selected = TOPOLOGIES.find(t => t.id === store.topology) ?? null
  const twoPaneLayout = bp === 'desktop'

  return (
    <StepShell
      title="Choose your infrastructure topology"
      description="Your topology defines how many regions and clusters OpenOva provisions. DUAL is the recommended starting point for most regulated organisations."
      onNext={() => { if (store.topology) next() }}
      onBack={back}
      nextDisabled={!store.topology}
    >
      <div style={{
        display: 'flex',
        flexDirection: twoPaneLayout ? 'row' : 'column',
        gap: 16,
        alignItems: 'flex-start',
      }}>
        {/* Option list */}
        <div style={{
          width: twoPaneLayout ? '40%' : '100%',
          flexShrink: 0,
          display: 'flex', flexDirection: 'column', gap: 6,
        }}>
          {TOPOLOGIES.map(t => {
            const isSelected = store.topology === t.id
            return (
              <div
                key={t.id}
                onClick={() => store.setTopology(t.id)}
                style={{
                  display: 'flex', alignItems: 'center', gap: 10,
                  padding: '10px 12px', borderRadius: 10, cursor: 'pointer',
                  border: isSelected ? '1.5px solid rgba(56,189,248,0.5)' : '1.5px solid rgba(255,255,255,0.07)',
                  background: isSelected ? 'rgba(56,189,248,0.07)' : 'rgba(255,255,255,0.02)',
                  boxShadow: isSelected ? '0 0 0 3px rgba(56,189,248,0.07)' : 'none',
                  transition: 'all 0.15s',
                }}
              >
                <div style={{
                  width: 16, height: 16, borderRadius: '50%', flexShrink: 0,
                  border: isSelected ? 'none' : '1.5px solid rgba(255,255,255,0.2)',
                  background: isSelected ? '#38BDF8' : 'transparent',
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                  transition: 'all 0.15s',
                }}>
                  {isSelected && <div style={{ width: 6, height: 6, borderRadius: '50%', background: '#fff' }} />}
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                    <span style={{ fontSize: 12, fontWeight: 700, color: isSelected ? '#fff' : 'rgba(255,255,255,0.7)', letterSpacing: '0.03em' }}>{t.name}</span>
                    <span style={{ fontSize: 8, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase', color: t.tagColor, background: `${t.tagColor}18`, border: `1px solid ${t.tagColor}38`, borderRadius: 4, padding: '1px 6px' }}>{t.tag}</span>
                    {t.recommended && !isSelected && <span style={{ fontSize: 9, color: 'rgba(34,197,94,0.6)', fontWeight: 500 }}>← start here</span>}
                  </div>
                  <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.3)', marginTop: 2, lineHeight: 1.3 }}>{t.tagline}</div>
                </div>
                <div style={{ display: 'flex', gap: 10, flexShrink: 0 }}>
                  {[{ val: t.regions, lbl: 'reg' }, { val: t.clusters, lbl: 'cls' }].map(({ val, lbl }) => (
                    <div key={lbl} style={{ textAlign: 'center' }}>
                      <div style={{ fontSize: 14, fontWeight: 700, lineHeight: 1, color: isSelected ? '#38BDF8' : 'rgba(255,255,255,0.4)' }}>{val}</div>
                      <div style={{ fontSize: 8, color: 'rgba(255,255,255,0.2)', marginTop: 2 }}>{lbl}</div>
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </div>

        {/* Detail panel — right on desktop, below on mobile/tablet */}
        <div style={{ flex: 1, minWidth: 0, width: twoPaneLayout ? undefined : '100%' }}>
          {selected ? (
            <TopologyDetail t={selected} />
          ) : (
            <div style={{
              minHeight: 180, display: 'flex', alignItems: 'center', justifyContent: 'center',
              border: '1.5px dashed rgba(255,255,255,0.1)', borderRadius: 12,
              color: 'rgba(255,255,255,0.2)', fontSize: 13,
            }}>
              Select a topology to see the architecture diagram
            </div>
          )}
        </div>
      </div>
    </StepShell>
  )
}
