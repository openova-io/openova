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
  vclusters: number
  tag: string
  tagColor: string
  recommended?: boolean
  diagram: React.ReactNode
  bullets: string[]
}

/* ── SVG helpers — designed for dark canvas ─────────────────────── */

// Standard physical cluster box (no vCluster layer)
const BOX = (x: number, y: number, w: number, h: number, label: string, fill: string, textFill = '#fff') => (
  <g key={`b${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={4} fill={fill} />
    <text x={x + w / 2} y={y + h / 2 + 4} textAnchor="middle" fontSize={9} fontWeight="700" fill={textFill} fontFamily="Inter,sans-serif">{label}</text>
  </g>
)

// Physical cluster box with inner vCluster boundary (dashed inner border + vC badge)
const VBOX = (x: number, y: number, w: number, h: number, label: string, fill: string, textFill = '#fff') => (
  <g key={`vb${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={4} fill={fill} />
    <rect x={x + 2.5} y={y + 2.5} width={w - 5} height={h - 5} rx={2.5} fill="none" stroke="rgba(255,255,255,0.22)" strokeWidth={0.8} strokeDasharray="2,1.5" />
    <text x={x + w / 2} y={y + h / 2 + 4} textAnchor="middle" fontSize={9} fontWeight="700" fill={textFill} fontFamily="Inter,sans-serif">{label}</text>
    <text x={x + w - 4} y={y + 8} textAnchor="end" fontSize={6} fontWeight="600" fill="rgba(255,255,255,0.45)" fontFamily="Inter,sans-serif">vC</text>
  </g>
)

const REGION = (x: number, y: number, w: number, h: number, label: string) => (
  <g key={`r${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={6} fill="none" stroke="rgba(255,255,255,0.18)" strokeWidth={1} strokeDasharray="4,3" />
    <text x={x + 7} y={y - 5} fontSize={8} fill="rgba(255,255,255,0.35)" fontFamily="Inter,sans-serif" fontWeight="500">{label}</text>
  </g>
)

const CONN = (x1: number, y1: number, x2: number, y2: number) => (
  <line key={`c${x1}${y1}`} x1={x1} y1={y1} x2={x2} y2={y2} stroke="rgba(56,189,248,0.4)" strokeWidth={1.2} strokeDasharray="3,2" />
)

/* ── CITADEL: 4 regions — 2 dedicated CP + 2 DP, 1 vCluster per physical cluster ── */
const DiagramCitadel = () => (
  <svg viewBox="0 0 280 180" width="100%">
    {REGION(5, 12, 120, 52, 'CP Region 1')}
    {VBOX(14, 24, 102, 30, 'MGMT', 'rgba(56,189,248,0.85)')}
    {REGION(155, 12, 120, 52, 'CP Region 2')}
    {VBOX(164, 24, 102, 30, 'MGMT', 'rgba(56,189,248,0.55)')}
    {REGION(5, 100, 120, 72, 'DP Region 1')}
    {VBOX(8, 116, 53, 44, 'DMZ', 'rgba(99,102,241,0.85)')}
    {VBOX(66, 116, 53, 44, 'RTZ', 'rgba(99,102,241,0.55)')}
    {REGION(155, 100, 120, 72, 'DP Region 2')}
    {VBOX(158, 116, 53, 44, 'DMZ', 'rgba(99,102,241,0.85)')}
    {VBOX(216, 116, 53, 44, 'RTZ', 'rgba(99,102,241,0.55)')}
    {CONN(125, 39, 155, 39)}
    {CONN(65, 64, 65, 100)}
    {CONN(215, 64, 215, 100)}
    {CONN(125, 138, 155, 138)}
  </svg>
)

/* ── DUAL: 2 regions — MGMT+DMZ+RTZ each, 1 vCluster per physical cluster ── */
const DiagramDual = () => (
  <svg viewBox="0 0 280 145" width="100%">
    {REGION(5, 10, 120, 126, 'Region 1 · Primary')}
    {VBOX(14, 26, 100, 28, 'MGMT', 'rgba(56,189,248,0.85)')}
    {VBOX(14, 58, 100, 28, 'DMZ',  'rgba(99,102,241,0.85)')}
    {VBOX(14, 90, 100, 28, 'RTZ',  'rgba(99,102,241,0.55)')}
    {REGION(155, 10, 120, 126, 'Region 2 · DR')}
    {VBOX(164, 26, 100, 28, 'MGMT', 'rgba(56,189,248,0.45)')}
    {VBOX(164, 58, 100, 28, 'DMZ',  'rgba(99,102,241,0.45)')}
    {VBOX(164, 90, 100, 28, 'RTZ',  'rgba(99,102,241,0.3)')}
    {CONN(125, 72, 155, 72)}
  </svg>
)

/* ── ZONED: 2 regions — DMZ isolated, MGMT·RTZ merged, 1 vCluster per physical cluster ── */
const DiagramZoned = () => (
  <svg viewBox="0 0 280 130" width="100%">
    {REGION(5, 14, 124, 100, 'Region 1 · Primary')}
    {VBOX(12, 32, 50, 65, 'DMZ',      'rgba(99,102,241,0.85)')}
    {VBOX(68, 32, 52, 65, 'MGMT·RTZ', 'rgba(56,189,248,0.75)')}
    {REGION(151, 14, 124, 100, 'Region 2 · DR')}
    {VBOX(158, 32, 50, 65, 'DMZ',      'rgba(99,102,241,0.45)')}
    {VBOX(214, 32, 52, 65, 'MGMT·RTZ', 'rgba(56,189,248,0.35)')}
    {CONN(129, 65, 151, 65)}
  </svg>
)

/* ── COMPACT: 2 regions — 3 vClusters (MGMT/DMZ/RTZ) inside each physical cluster ── */
const DiagramCompact = () => (
  <svg viewBox="0 0 280 145" width="100%">
    {REGION(5, 14, 120, 118, 'Region 1 · Primary')}
    {VBOX(12, 26, 106, 28, 'MGMT', 'rgba(56,189,248,0.85)')}
    {VBOX(12, 58, 106, 28, 'DMZ',  'rgba(99,102,241,0.85)')}
    {VBOX(12, 90, 106, 28, 'RTZ',  'rgba(99,102,241,0.55)')}
    {REGION(155, 14, 120, 118, 'Region 2 · Secondary')}
    {VBOX(162, 26, 106, 28, 'MGMT', 'rgba(56,189,248,0.45)')}
    {VBOX(162, 58, 106, 28, 'DMZ',  'rgba(99,102,241,0.45)')}
    {VBOX(162, 90, 106, 28, 'RTZ',  'rgba(99,102,241,0.3)')}
    {CONN(125, 104, 155, 104)}
  </svg>
)

/* ── SOLO: 1 region — 3 vClusters (MGMT/DMZ/RTZ) inside single physical cluster ── */
const DiagramSolo = () => (
  <svg viewBox="0 0 280 140" width="100%">
    {REGION(60, 14, 160, 114, 'Single region')}
    {VBOX(68, 26, 144, 28, 'MGMT', 'rgba(56,189,248,0.85)')}
    {VBOX(68, 58, 144, 28, 'DMZ',  'rgba(99,102,241,0.85)')}
    {VBOX(68, 90, 144, 28, 'RTZ',  'rgba(99,102,241,0.55)')}
  </svg>
)

// suppress unused warning — BOX kept for future use
void BOX

const TOPOLOGIES: TopoConfig[] = [
  {
    id: 'citadel', name: 'CITADEL',
    tagline: 'Four-region — dedicated dual-CP + dual data plane',
    clusters: 6, regions: 4, vclusters: 6,
    tag: 'Tier-1 Bank', tagColor: '#F59E0B', recommended: false,
    diagram: <DiagramCitadel />,
    bullets: [
      'Two dedicated CP regions — each hosts only the MGMT cluster, zero workload co-location',
      'MGMT is HA across geographically separate sites — no single point of control',
      'Two independent DP regions: DMZ + RTZ clusters, fully isolated from CP network',
      'One vCluster per physical cluster — uniform Catalyst/Specter interface and multi-tenancy headroom',
    ],
  },
  {
    id: 'dual', name: 'DUAL',
    tagline: 'Two-region — full cluster separation (3 per region)',
    clusters: 6, regions: 2, vclusters: 6,
    tag: 'Enterprise', tagColor: '#22C55E', recommended: true,
    diagram: <DiagramDual />,
    bullets: [
      'Each region: MGMT, DMZ, and RTZ clusters fully separated — physical blast-radius isolation',
      'MGMT active in both regions — no single point of management failure',
      'One vCluster per physical cluster — lifecycle independence and future multi-tenancy',
      'Recommended starting point for regulated banks, insurance, and fintechs',
    ],
  },
  {
    id: 'zoned', name: 'ZONED',
    tagline: 'Two-region — DMZ isolated, MGMT+RTZ merged',
    clusters: 4, regions: 2, vclusters: 4,
    tag: 'Mid-market', tagColor: '#38BDF8',
    diagram: <DiagramZoned />,
    bullets: [
      'DMZ cluster fully isolated for ingress and edge workloads in each region',
      'MGMT and RTZ co-located per region — reduces cluster overhead without losing zone separation',
      'One vCluster per physical cluster — consistent operational model across all building blocks',
      'Good fit for mid-market banks and regional lenders',
    ],
  },
  {
    id: 'compact', name: 'COMPACT',
    tagline: 'Two-region — MGMT / DMZ / RTZ as vClusters per cluster',
    clusters: 2, regions: 2, vclusters: 6,
    tag: 'Starter', tagColor: '#A78BFA',
    diagram: <DiagramCompact />,
    bullets: [
      'One physical cluster per region — geo-redundant with minimal infrastructure cost',
      'Three vClusters per cluster (MGMT / DMZ / RTZ) — building block isolation without physical separation',
      'Separate API server, etcd, and RBAC per vCluster — logical isolation at each building block',
      'Clear upgrade path: promote to ZONED or DUAL as workloads and compliance requirements grow',
    ],
  },
  {
    id: 'solo', name: 'SOLO',
    tagline: 'Single region — MGMT / DMZ / RTZ as vClusters',
    clusters: 1, regions: 1, vclusters: 3,
    tag: 'Dev / POC', tagColor: '#6B7280',
    diagram: <DiagramSolo />,
    bullets: [
      'Single physical cluster with three vClusters: MGMT, DMZ, and RTZ',
      'Separate API server, etcd, and RBAC per vCluster — logical building block separation',
      'Shared host kernel and hardware — not suitable for regulated production workloads',
      'Ideal for demos, evaluations, and development environments',
    ],
  },
]

/* ── Topology detail panel ──────────────────────────────────────── */
function TopologyDetail({ t }: { t: TopoConfig }) {
  return (
    <div style={{ borderRadius: 12, border: '1px solid rgba(56,189,248,0.15)', background: 'rgba(56,189,248,0.04)', overflow: 'hidden', display: 'flex', flexDirection: 'column', height: '100%' }}>
      <div style={{ padding: '14px 18px', borderBottom: '1px solid var(--wiz-border-sub)' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 4 }}>
          <span style={{ fontSize: 15, fontWeight: 800, color: 'var(--color-text-primary)', letterSpacing: '0.04em' }}>{t.name}</span>
          <span style={{ fontSize: 9, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase', color: t.tagColor, background: `${t.tagColor}18`, border: `1px solid ${t.tagColor}38`, borderRadius: 4, padding: '2px 7px' }}>{t.tag}</span>
          <div style={{ marginLeft: 'auto', display: 'flex', gap: 16 }}>
            {[
              { val: t.regions,   lbl: 'regions'   },
              { val: t.clusters,  lbl: 'clusters'  },
              { val: t.vclusters, lbl: 'vClusters' },
            ].map(({ val, lbl }) => (
              <div key={lbl} style={{ textAlign: 'center' }}>
                <div style={{ fontSize: 16, fontWeight: 700, color: '#38BDF8', lineHeight: 1 }}>{val}</div>
                <div style={{ fontSize: 9, color: 'var(--wiz-text-sub)', marginTop: 2 }}>{lbl}</div>
              </div>
            ))}
          </div>
        </div>
        <div style={{ fontSize: 11, color: 'var(--wiz-text-sub)', lineHeight: 1.4 }}>{t.tagline}</div>
      </div>
      {/* Dark canvas — diagrams use hardcoded colours for dark bg, pop in both light/dark modes */}
      <div style={{ padding: '18px', background: 'linear-gradient(135deg, #0a1628 0%, #0f172a 100%)' }}>
        {t.diagram}
        {/* vCluster legend */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginTop: 8 }}>
          <svg width={18} height={12}>
            <rect x={1} y={1} width={16} height={10} rx={2} fill="rgba(99,102,241,0.6)" />
            <rect x={2.5} y={2.5} width={13} height={7} rx={1.5} fill="none" stroke="rgba(255,255,255,0.3)" strokeWidth={0.8} strokeDasharray="2,1.5" />
          </svg>
          <span style={{ fontSize: 9, color: 'rgba(255,255,255,0.35)', fontFamily: 'Inter, sans-serif' }}>dashed inner border = vCluster boundary inside physical cluster</span>
        </div>
      </div>
      <div style={{ padding: '12px 18px 16px', flex: 1 }}>
        <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: 6 }}>
          {t.bullets.map(b => (
            <li key={b} style={{ display: 'flex', gap: 8, fontSize: 11, color: 'var(--wiz-text-lo)', lineHeight: 1.5 }}>
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
      description="Your topology defines regions, physical clusters, and the vCluster isolation layer inside each. Every physical cluster runs one vCluster — the operational unit Catalyst provisions and Specter monitors. DUAL is the recommended starting point for most regulated organisations."
      onNext={() => { if (store.topology) next() }}
      onBack={back}
      nextDisabled={!store.topology}
    >
      <div style={{
        display: 'flex',
        flexDirection: twoPaneLayout ? 'row' : 'column',
        gap: 16,
        alignItems: twoPaneLayout ? 'stretch' : 'flex-start',
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
                  flex: 1,
                  display: 'flex', alignItems: 'center', gap: 10,
                  padding: '10px 12px', borderRadius: 10, cursor: 'pointer',
                  border: isSelected ? '1.5px solid rgba(56,189,248,0.5)' : '1.5px solid var(--wiz-border-sub)',
                  background: isSelected ? 'rgba(56,189,248,0.07)' : 'var(--wiz-bg-xs)',
                  boxShadow: isSelected ? '0 0 0 3px rgba(56,189,248,0.07)' : 'none',
                  transition: 'all 0.15s',
                }}
              >
                <div style={{
                  width: 16, height: 16, borderRadius: '50%', flexShrink: 0,
                  border: isSelected ? 'none' : '1.5px solid var(--wiz-text-hint)',
                  background: isSelected ? '#38BDF8' : 'transparent',
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                  transition: 'all 0.15s',
                }}>
                  {isSelected && <div style={{ width: 6, height: 6, borderRadius: '50%', background: '#fff' }} />}
                </div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
                    <span style={{ fontSize: 12, fontWeight: 700, color: isSelected ? 'var(--wiz-text-hi)' : 'var(--wiz-text-md)', letterSpacing: '0.03em' }}>{t.name}</span>
                    <span style={{ fontSize: 8, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase', color: t.tagColor, background: `${t.tagColor}18`, border: `1px solid ${t.tagColor}38`, borderRadius: 4, padding: '1px 6px' }}>{t.tag}</span>
                    {t.recommended && !isSelected && <span style={{ fontSize: 9, color: 'rgba(34,197,94,0.6)', fontWeight: 500 }}>← start here</span>}
                  </div>
                  <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 2, lineHeight: 1.3 }}>{t.tagline}</div>
                </div>
                <div style={{ display: 'flex', gap: 10, flexShrink: 0 }}>
                  {[
                    { val: t.regions,   lbl: 'reg' },
                    { val: t.clusters,  lbl: 'cls' },
                    { val: t.vclusters, lbl: 'vC'  },
                  ].map(({ val, lbl }) => (
                    <div key={lbl} style={{ textAlign: 'center' }}>
                      <div style={{ fontSize: 14, fontWeight: 700, lineHeight: 1, color: isSelected ? '#38BDF8' : 'var(--wiz-text-sub)' }}>{val}</div>
                      <div style={{ fontSize: 8, color: 'var(--wiz-text-hint)', marginTop: 2 }}>{lbl}</div>
                    </div>
                  ))}
                </div>
              </div>
            )
          })}
        </div>

        {/* Detail panel */}
        <div style={{ flex: 1, minWidth: 0, width: twoPaneLayout ? undefined : '100%', display: 'flex', flexDirection: 'column' }}>
          {selected ? (
            <TopologyDetail t={selected} />
          ) : (
            <div style={{
              flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center',
              minHeight: 180,
              border: '1.5px dashed var(--wiz-border)', borderRadius: 12,
              color: 'var(--wiz-text-hint)', fontSize: 13,
            }}>
              Select a topology to see the architecture diagram
            </div>
          )}
        </div>
      </div>
    </StepShell>
  )
}
