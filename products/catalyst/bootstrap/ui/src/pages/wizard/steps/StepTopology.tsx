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

/* ── SVG primitives ─────────────────────────────────────────────── */
const PC = (x: number, y: number, w: number, h: number) => (
  <rect key={`pc${x}${y}`} x={x} y={y} width={w} height={h} rx={4}
    fill="rgba(255,255,255,0.04)" stroke="rgba(255,255,255,0.22)" strokeWidth={1} />
)
const VC = (x: number, y: number, w: number, h: number, label: string, fill: string) => (
  <g key={`vc${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={3} fill={fill} />
    <text x={x + w / 2} y={y + h / 2 + 4} textAnchor="middle" fontSize={9} fontWeight="700" fill="#fff" fontFamily="Inter,sans-serif">{label}</text>
  </g>
)
const RG = (x: number, y: number, w: number, h: number, label: string, stroke = 'rgba(255,255,255,0.18)') => (
  <g key={`rg${x}${y}`}>
    <rect x={x} y={y} width={w} height={h} rx={6} fill="none" stroke={stroke} strokeWidth={1} strokeDasharray="4,3" />
    {label && <text x={x + 7} y={y - 5} fontSize={8} fill="rgba(255,255,255,0.35)" fontFamily="Inter,sans-serif" fontWeight="500">{label}</text>}
  </g>
)
const CONN = (x1: number, y1: number, x2: number, y2: number) => (
  <line key={`cn${x1}${y1}${x2}${y2}`} x1={x1} y1={y1} x2={x2} y2={y2}
    stroke="rgba(56,189,248,0.4)" strokeWidth={1.2} strokeDasharray="3,2" />
)

/* ── Colour palette ─────────────────────────────────────────────── */
const DMZ  = 'rgba(99,102,241,0.90)'
const DMZ2 = 'rgba(99,102,241,0.50)'
const RTZ  = 'rgba(99,102,241,0.55)'
const RTZ2 = 'rgba(99,102,241,0.30)'
const MGT  = 'rgba(56,189,248,0.85)'
const MGT2 = 'rgba(56,189,248,0.45)'

/* ─────────────────────────────────────────────────────────────────
   TOPOLOGY DIAGRAMS — all use width="100%" height="100%"
   Parent container is a fixed-height div → all diagrams same canvas size
   ───────────────────────────────────────────────────────────────── */

const DiagramSolo = () => (
  <svg viewBox="0 0 280 78" width="100%" height="100%" style={{ display: 'block' }}>
    {RG(4, 14, 272, 56, 'Single region')}
    {PC(10, 22, 260, 42)}
    {VC(14,  27, 80, 30, 'DMZ',  DMZ)}
    {VC(98,  27, 80, 30, 'RTZ',  RTZ)}
    {VC(182, 27, 80, 30, 'MGMT', MGT)}
  </svg>
)

const DiagramCompact = () => (
  <svg viewBox="0 0 280 148" width="100%" height="100%" style={{ display: 'block' }}>
    {RG(4, 14, 272, 56, 'Region 1 · Primary')}
    {PC(10, 22, 260, 42)}
    {VC(14,  27, 80, 30, 'DMZ',  DMZ)}
    {VC(98,  27, 80, 30, 'RTZ',  RTZ)}
    {VC(182, 27, 80, 30, 'MGMT', MGT)}
    {CONN(140, 70, 140, 80)}
    {RG(4, 80, 272, 56, 'Region 2 · Secondary')}
    {PC(10, 88, 260, 42)}
    {VC(14,  93, 80, 30, 'DMZ',  DMZ2)}
    {VC(98,  93, 80, 30, 'RTZ',  RTZ2)}
    {VC(182, 93, 80, 30, 'MGMT', MGT2)}
  </svg>
)

const DiagramZoned = () => (
  <svg viewBox="0 0 280 112" width="100%" height="100%" style={{ display: 'block' }}>
    {RG(4, 14, 130, 90, 'Region 1 · Primary')}
    {PC(10, 24, 118, 32)}
    {VC(13, 27, 112, 26, 'DMZ', DMZ)}
    {PC(10, 62, 118, 32)}
    {VC(13, 65,  54, 26, 'RTZ',  RTZ)}
    {VC(71, 65,  54, 26, 'MGMT', MGT)}
    {RG(146, 14, 130, 90, 'Region 2 · DR')}
    {PC(152, 24, 118, 32)}
    {VC(155, 27, 112, 26, 'DMZ',  DMZ2)}
    {PC(152, 62, 118, 32)}
    {VC(155, 65,  54, 26, 'RTZ',  RTZ2)}
    {VC(213, 65,  54, 26, 'MGMT', MGT2)}
    {CONN(134, 40, 146, 40)}
  </svg>
)

const DiagramDual = () => (
  <svg viewBox="0 0 280 142" width="100%" height="100%" style={{ display: 'block' }}>
    {RG(4, 12, 120, 118, 'Region 1 · Primary')}
    {PC(10, 22, 108, 30)}
    {VC(13, 25, 102, 24, 'DMZ',  DMZ)}
    {PC(10, 57, 108, 30)}
    {VC(13, 60, 102, 24, 'RTZ',  RTZ)}
    {PC(10, 92, 108, 30)}
    {VC(13, 95, 102, 24, 'MGMT', MGT)}
    {RG(156, 12, 120, 118, 'Region 2 · DR')}
    {PC(162, 22, 108, 30)}
    {VC(165, 25, 102, 24, 'DMZ',  DMZ2)}
    {PC(162, 57, 108, 30)}
    {VC(165, 60, 102, 24, 'RTZ',  RTZ2)}
    {PC(162, 92, 108, 30)}
    {VC(165, 95, 102, 24, 'MGMT', MGT2)}
    {CONN(124, 72, 156, 72)}
  </svg>
)

const DiagramCitadel = () => (
  <svg viewBox="0 0 280 168" width="100%" height="100%" style={{ display: 'block' }}>
    {RG(4, 14, 132, 82, 'DP Region 1')}
    {PC(10, 24, 120, 30)}
    {VC(13, 27, 114, 24, 'DMZ', DMZ)}
    {PC(10, 59, 120, 30)}
    {VC(13, 62, 114, 24, 'RTZ', RTZ)}
    {RG(144, 14, 132, 82, 'DP Region 2')}
    {PC(150, 24, 120, 30)}
    {VC(153, 27, 114, 24, 'DMZ', DMZ2)}
    {PC(150, 59, 120, 30)}
    {VC(153, 62, 114, 24, 'RTZ', RTZ2)}
    {CONN(136, 74, 144, 74)}
    {CONN(70, 96, 70, 112)}
    {CONN(210, 96, 210, 112)}
    {RG(4, 112, 132, 48, 'CP · Region 1')}
    {PC(10, 120, 120, 32)}
    {VC(13, 123, 114, 26, 'MGMT', MGT)}
    {RG(144, 112, 132, 48, 'CP · Region 2')}
    {PC(150, 120, 120, 32)}
    {VC(153, 123, 114, 26, 'MGMT', MGT2)}
    {CONN(136, 136, 144, 136)}
  </svg>
)


/* ── Topology configurations ────────────────────────────────────── */
const TOPOLOGIES: TopoConfig[] = [
  {
    id: 'citadel', name: 'CITADEL',
    tagline: 'Four-region — dedicated dual-CP (MGMT) + dual data plane',
    clusters: 6, regions: 4, vclusters: 6,
    tag: 'Tier-1 Bank', tagColor: '#F59E0B',
    diagram: <DiagramCitadel />,
    bullets: [
      'DP regions (top): DMZ and RTZ clusters fully isolated from control plane',
      'CP regions (bottom): two dedicated MGMT-only regions — zero workload co-location',
      'MGMT is HA across geographically separate sites — no single point of control',
      'One vCluster per physical cluster — uniform interface for Catalyst and Specter',
    ],
  },
  {
    id: 'dual', name: 'DUAL',
    tagline: 'Two-region — DMZ · RTZ · MGMT per region (3 clusters each)',
    clusters: 6, regions: 2, vclusters: 6,
    tag: 'Enterprise', tagColor: '#22C55E', recommended: true,
    diagram: <DiagramDual />,
    bullets: [
      'Each region: DMZ (top), RTZ (mid), MGMT (bottom) — fully separate physical clusters',
      'MGMT active in both regions — no single point of management failure',
      'One vCluster per physical cluster — lifecycle independence and multi-tenancy headroom',
      'Recommended starting point for regulated banks, insurance, and fintechs',
    ],
  },
  {
    id: 'zoned', name: 'ZONED',
    tagline: 'Two-region — DMZ cluster (top) + RTZ·MGMT cluster (bottom, 2 vClusters)',
    clusters: 4, regions: 2, vclusters: 6,
    tag: 'Mid-market', tagColor: '#38BDF8',
    diagram: <DiagramZoned />,
    bullets: [
      'DMZ cluster on top — isolated ingress and edge workloads per region',
      'RTZ and MGMT as separate vClusters inside the bottom cluster — logical isolation with lower cost',
      'MGMT present in both regions — eliminates single-site management risk',
      'Good fit for mid-market banks and regional lenders',
    ],
  },
  {
    id: 'compact', name: 'COMPACT',
    tagline: 'Two-region — DMZ · RTZ · MGMT as vClusters inside each cluster',
    clusters: 2, regions: 2, vclusters: 6,
    tag: 'Starter', tagColor: '#A78BFA',
    diagram: <DiagramCompact />,
    bullets: [
      'One physical cluster per region — geo-redundant with minimal infrastructure cost',
      'Three vClusters per cluster: DMZ (left), RTZ (mid), MGMT (right)',
      'Separate API server, etcd, and RBAC per vCluster — logical isolation at each layer',
      'Clear upgrade path: promote to ZONED or DUAL as compliance requirements grow',
    ],
  },
  {
    id: 'solo', name: 'SOLO',
    tagline: 'Single region — DMZ · RTZ · MGMT as vClusters',
    clusters: 1, regions: 1, vclusters: 3,
    tag: 'Dev / POC', tagColor: '#6B7280',
    diagram: <DiagramSolo />,
    bullets: [
      'Three vClusters inside one physical cluster: DMZ (left), RTZ (mid), MGMT (right)',
      'Separate API server, etcd, and RBAC per vCluster — logical building block separation',
      'Shared host kernel — not suitable for regulated production workloads',
      'Ideal for demos, evaluations, and development environments',
    ],
  },
]

/* ── Topology detail panel ──────────────────────────────────────── */
function TopologyDetail({ t }: { t: TopoConfig }) {
  return (
    <div style={{ borderRadius: 12, border: '1px solid rgba(56,189,248,0.15)', background: 'rgba(56,189,248,0.04)', overflow: 'hidden', display: 'flex', flexDirection: 'column' }}>
      {/* Header — fixed */}
      <div style={{ padding: '14px 18px', borderBottom: '1px solid var(--wiz-border-sub)', flexShrink: 0 }}>
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

      {/* Canvas — fixed minHeight ensures diagram is always substantial */}
      <div style={{ display: 'flex', flexDirection: 'column', background: 'linear-gradient(135deg, #0a1628 0%, #0f172a 100%)', padding: '14px 18px 10px' }}>
        {/* SVG area — fixed height, all diagrams same canvas size */}
        <div style={{ height: 200, overflow: 'hidden' }}>
          {t.diagram}
        </div>
        {/* Legend */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 10, flexShrink: 0 }}>
          {[
            { color: 'rgba(99,102,241,0.85)', label: 'DMZ' },
            { color: 'rgba(99,102,241,0.55)', label: 'RTZ' },
            { color: 'rgba(56,189,248,0.85)', label: 'MGMT' },
          ].map(({ color, label }) => (
            <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
              <div style={{ width: 10, height: 10, borderRadius: 2, background: color, flexShrink: 0 }} />
              <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.45)', fontFamily: 'Inter, sans-serif' }}>{label}</span>
            </div>
          ))}
          <span style={{ fontSize: 10, color: 'rgba(255,255,255,0.3)', marginLeft: 4 }}>· outer box = physical cluster · inner box = vCluster</span>
        </div>
      </div>

      {/* Bullets — fixed at bottom */}
      <div style={{ padding: '12px 18px 16px', flexShrink: 0 }}>
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

/* ── AIR-GAP add-on card — toggle only, no expansion ───────────── */
function AirgapAddon() {
  const store = useWizardStore()
  const enabled = store.airgap

  return (
    <div style={{ marginTop: 16 }}>
      <div style={{ fontSize: 10, fontWeight: 700, letterSpacing: '0.1em', textTransform: 'uppercase', color: 'var(--wiz-text-hint)', marginBottom: 8 }}>
        Add-on · optional
      </div>
      <div
        onClick={() => store.setAirgap(!enabled)}
        style={{
          borderRadius: 12, cursor: 'pointer',
          border: enabled ? '1.5px solid rgba(245,158,11,0.5)' : '1.5px solid var(--wiz-border-sub)',
          background: enabled ? 'rgba(245,158,11,0.05)' : 'var(--wiz-bg-xs)',
          boxShadow: enabled ? '0 0 0 3px rgba(245,158,11,0.07)' : 'none',
          transition: 'all 0.15s',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px' }}>
          <div style={{
            width: 32, height: 18, borderRadius: 9, flexShrink: 0,
            background: enabled ? 'rgba(245,158,11,0.85)' : 'var(--wiz-border)',
            position: 'relative', transition: 'background 0.2s',
          }}>
            <div style={{
              position: 'absolute', top: 2, left: enabled ? 16 : 2,
              width: 14, height: 14, borderRadius: 7, background: '#fff',
              transition: 'left 0.2s', boxShadow: '0 1px 3px rgba(0,0,0,0.3)',
            }} />
          </div>
          <div style={{ flex: 1 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span style={{ fontSize: 12, fontWeight: 700, color: enabled ? '#F59E0B' : 'var(--wiz-text-md)', letterSpacing: '0.03em' }}>AIR-GAP</span>
              <span style={{ fontSize: 8, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase', color: '#F59E0B', background: 'rgba(245,158,11,0.12)', border: '1px solid rgba(245,158,11,0.25)', borderRadius: 4, padding: '1px 6px' }}>Ransomware Recovery</span>
            </div>
            <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 2 }}>+1 isolated region · +1 cluster · pull-only replication · Specter forensic mode</div>
          </div>
          <div style={{ display: 'flex', gap: 10, flexShrink: 0 }}>
            {[{ val: '+1', lbl: 'reg' }, { val: '+1', lbl: 'cls' }, { val: '+1', lbl: 'vC' }].map(({ val, lbl }) => (
              <div key={lbl} style={{ textAlign: 'center' }}>
                <div style={{ fontSize: 13, fontWeight: 700, lineHeight: 1, color: enabled ? '#F59E0B' : 'var(--wiz-text-sub)' }}>{val}</div>
                <div style={{ fontSize: 8, color: 'var(--wiz-text-hint)', marginTop: 2 }}>{lbl}</div>
              </div>
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

/* ── StepTopology ───────────────────────────────────────────────── */
/**
 * StepTopology mode.
 *   • 'wizard'        — canonical wizard step (default)
 *   • 'add-cluster'   — embedded in InfrastructurePage's
 *                       AddClusterModal (cluster-spec form re-use).
 */
export type StepTopologyMode = 'wizard' | 'add-cluster'

export interface StepTopologyProps {
  mode?: StepTopologyMode
}

export function StepTopology({ mode = 'wizard' }: StepTopologyProps = {}) {
  const store = useWizardStore()
  const { next, back } = useStepNav()
  const bp = useBreakpoint()
  const isAddCluster = mode === 'add-cluster'
  // Reserved — `isAddCluster` will gate the topology-pattern picker
  // once cluster-only-add lands here (the AddClusterModal currently
  // owns its own form). Issue #228 reserves the prop for forward
  // compat.
  void isAddCluster

  const selected = TOPOLOGIES.find(t => t.id === store.topology) ?? null
  const twoPaneLayout = bp === 'desktop'

  // Validation: a topology must be selected. SKU + worker-count validation
  // happens in StepProvider, where each region has access to its own
  // provider's SKU vocabulary (cx32 ≠ Standard_D4s_v5 ≠ m6i.xlarge — picking
  // sizing here would force a one-vendor catalog, which is the wrong shape).
  const canProceed = !!store.topology

  return (
    <StepShell
      title="Choose your infrastructure topology"
      description="Your topology defines regions, physical clusters, and the vCluster isolation layer inside each. Outer box = physical cluster. Inner coloured box = vCluster. Network order top→bottom: DMZ · RTZ · MGMT. AIR-GAP is an optional add-on to any topology."
      onNext={() => { if (canProceed) next() }}
      onBack={back}
      nextDisabled={!canProceed}
    >
      <div style={{
        display: 'flex',
        flexDirection: twoPaneLayout ? 'row' : 'column',
        gap: 16,
        alignItems: 'flex-start',
      }}>
        {/* Option list + AIR-GAP toggle */}
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
                  /* Fixed height — all 5 options are identical height */
                  height: 62,
                  boxSizing: 'border-box',
                  border: isSelected ? '1.5px solid rgba(56,189,248,0.5)' : '1.5px solid var(--wiz-border-sub)',
                  background: isSelected ? 'rgba(56,189,248,0.07)' : 'var(--wiz-bg-xs)',
                  boxShadow: isSelected ? '0 0 0 3px rgba(56,189,248,0.07)' : 'none',
                  transition: 'border-color 0.15s, background 0.15s, box-shadow 0.15s',
                  overflow: 'hidden',
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
                <div style={{ flex: 1, minWidth: 0, overflow: 'hidden' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ fontSize: 12, fontWeight: 700, color: isSelected ? 'var(--wiz-text-hi)' : 'var(--wiz-text-md)', letterSpacing: '0.03em', flexShrink: 0 }}>{t.name}</span>
                    <span style={{ fontSize: 8, fontWeight: 700, letterSpacing: '0.07em', textTransform: 'uppercase', color: t.tagColor, background: `${t.tagColor}18`, border: `1px solid ${t.tagColor}38`, borderRadius: 4, padding: '1px 6px', flexShrink: 0 }}>{t.tag}</span>
                    {t.recommended && !isSelected && <span style={{ fontSize: 9, color: 'rgba(34,197,94,0.6)', fontWeight: 500, flexShrink: 0 }}>← start here</span>}
                  </div>
                  {/* Single-line tagline — truncated if too long */}
                  <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 3, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{t.tagline}</div>
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
          <AirgapAddon />
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

      {/* Sizing intentionally lives in StepProvider, NOT here. SKU
          vocabulary is per-provider — Hetzner cx32 means nothing on Azure
          — so each region's control-plane + worker SKU pickers are
          rendered next to that region's provider chooser, where the
          catalog is unambiguous. */}
    </StepShell>
  )
}
