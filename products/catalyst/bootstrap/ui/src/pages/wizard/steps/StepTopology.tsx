import { useWizardStore } from '@/entities/deployment/store'
import { TOPOLOGY_REGION_COUNT, type TopologyTemplate } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

/*
 * The region-count step of the wizard (ADR-0019).
 *
 * A Sovereign region is one cluster. The edge, the platform and the
 * Organizations run inside it and are separated by network policy, so the
 * only thing this step decides is how many regions the provisioner builds.
 * The choice is stored as a TopologyTemplate id. Its one effect is the
 * number of regions: TOPOLOGY_REGION_COUNT and TOPOLOGY_REGION_LABELS in
 * model.ts, read by StepProvider and StepReview.
 */
const REGION_CHOICE_TEXT =
  'One cluster per region. The edge, the platform and Organizations are separated by network policy.'

interface RegionChoice {
  id: TopologyTemplate
  name: string
}

const REGION_CHOICES: RegionChoice[] = [
  { id: 'solo', name: 'One region' },
  { id: 'dual', name: 'Two or more regions' },
]

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
            <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 2 }}>+1 isolated region · +1 cluster · pull-only replication</div>
          </div>
          <div style={{ display: 'flex', gap: 10, flexShrink: 0 }}>
            {[{ val: '+1', lbl: 'reg' }, { val: '+1', lbl: 'cls' }].map(({ val, lbl }) => (
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
  const isAddCluster = mode === 'add-cluster'
  // Reserved — `isAddCluster` will gate the region-count picker once
  // cluster-only-add lands here (the AddClusterModal currently owns its
  // own form). The prop is kept for that.
  void isAddCluster

  // Validation: a choice must be made. SKU + worker-count validation
  // happens in StepProvider, where each region has access to its own
  // provider's SKU vocabulary.
  const canProceed = !!store.topology

  return (
    <StepShell
      title="Choose the number of regions"
      description="A region is one cluster. AIR-GAP is an optional add-on to either choice."
      onNext={() => { if (canProceed) next() }}
      onBack={back}
      nextDisabled={!canProceed}
    >
      <div role="radiogroup" aria-label="Number of regions" style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {REGION_CHOICES.map(c => {
          const isSelected = store.topology === c.id
          const regions = TOPOLOGY_REGION_COUNT[c.id]
          return (
            <button
              type="button"
              role="radio"
              aria-checked={isSelected}
              key={c.id}
              data-testid={`topology-choice-${c.id}`}
              onClick={() => store.setTopology(c.id)}
              style={{
                display: 'flex', alignItems: 'center', gap: 10, width: '100%', textAlign: 'left',
                padding: '12px 14px', borderRadius: 10, cursor: 'pointer',
                font: 'inherit', color: 'inherit',
                border: isSelected ? '1.5px solid rgba(56,189,248,0.5)' : '1.5px solid var(--wiz-border-sub)',
                background: isSelected ? 'rgba(56,189,248,0.07)' : 'var(--wiz-bg-xs)',
                boxShadow: isSelected ? '0 0 0 3px rgba(56,189,248,0.07)' : 'none',
                transition: 'border-color 0.15s, background 0.15s, box-shadow 0.15s',
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
                <div style={{ fontSize: 13, fontWeight: 700, color: isSelected ? 'var(--wiz-text-hi)' : 'var(--wiz-text-md)' }}>{c.name}</div>
                <div style={{ fontSize: 11, color: 'var(--wiz-text-sub)', marginTop: 3, lineHeight: 1.5 }}>{REGION_CHOICE_TEXT}</div>
              </div>
              <div style={{ textAlign: 'center', flexShrink: 0 }}>
                <div style={{ fontSize: 14, fontWeight: 700, lineHeight: 1, color: isSelected ? '#38BDF8' : 'var(--wiz-text-sub)' }}>{regions}</div>
                <div style={{ fontSize: 8, color: 'var(--wiz-text-hint)', marginTop: 2 }}>{regions === 1 ? 'region' : 'regions'}</div>
              </div>
            </button>
          )
        })}
      </div>
      <AirgapAddon />

      {/* Sizing intentionally lives in StepProvider, NOT here. SKU
          vocabulary is per-provider — Hetzner cx32 means nothing on Azure
          — so each region's control-plane + worker SKU pickers are
          rendered next to that region's provider chooser, where the
          catalog is unambiguous. */}
    </StepShell>
  )
}
