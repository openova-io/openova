import { useState, useEffect } from 'react'
import { ChevronDown, ChevronUp, Lock } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { DEFAULT_COMPONENT_GROUPS, getProfileDefaults } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'
import { COMPONENT_LOGOS } from './componentLogos'
import { GROUPS } from './componentGroups'
import type { ComponentDef, GroupDef, Tier } from './componentGroups'


/* ── Dependency map ───────────────────────────────────────────────
   When a component is turned ON, its entries here are also selected.
   Transitive: librechat→ferretdb→cnpg resolved automatically.
────────────────────────────────────────────────────────────────── */
const COMPONENT_DEPS: Record<string, string[]> = {
  // CORTEX
  kserve:     ['knative'],
  librechat:  ['ferretdb'],
  langfuse:   ['cnpg'],
  // FABRIC (intra-block)
  ferretdb:   ['cnpg'],
  debezium:   ['strimzi', 'cnpg'],
  flink:      ['strimzi', 'cnpg'],
  temporal:   ['cnpg'],
  clickhouse: ['strimzi'],
  iceberg:    ['cnpg'],
  // INSIGHTS → FABRIC
  openmeter:  ['strimzi', 'clickhouse'],
  // GUARDIAN → FABRIC
  keycloak:   ['cnpg'],
  // RELAY
  livekit:    ['stunner'],
  stalwart:   ['cnpg'],
  matrix:     ['cnpg', 'keycloak'],
}

/** Build component-id → group-id lookup */
const COMPONENT_GROUP: Record<string, string> = (() => {
  const map: Record<string, string> = {}
  for (const g of GROUPS) for (const c of g.components) map[c.id] = g.id
  return map
})()

/** Collect all transitive dependencies (BFS) */
function allDeps(id: string): string[] {
  const seen = new Set<string>()
  const queue = [id]
  while (queue.length) {
    const cur = queue.shift()!
    for (const dep of COMPONENT_DEPS[cur] ?? []) {
      if (!seen.has(dep)) { seen.add(dep); queue.push(dep) }
    }
  }
  return [...seen]
}

/* ── Color semantics ──────────────────────────────────────────────
   Green  (#4ADE80) = locked / always-on (mandatory in required block)
   Blue   (#38BDF8) = user selected (recommended / chosen)
   Indigo (#818CF8) = optional choice
   Red tag badge = block is Required (status label only, not selection)
   Purple tag badge = block is Optional
────────────────────────────────────────────────────────────────── */
const TIER_ORDER: Record<Tier, number> = { mandatory: 0, recommended: 1, optional: 2 }

const TIER_BADGE: Record<Tier, { label: string; color: string }> = {
  mandatory:   { label: 'M', color: '#4ADE80' },
  recommended: { label: 'R', color: '#38BDF8' },
  optional:    { label: 'O', color: '#A78BFA' },
}

function checkboxColor(tier: Tier, locked: boolean): string {
  if (locked)                      return '#4ADE80'   // green = always on
  if (tier === 'recommended')      return '#38BDF8'   // blue  = chosen
  return '#818CF8'                                    // indigo = optional pick
}

function GroupCard({ group, open, onToggle }: { group: GroupDef; open: boolean; onToggle: () => void }) {
  const store = useWizardStore()
  const bp = useBreakpoint()

  const storedIds    = store.componentGroups[group.id] ?? []
  const mandatoryIds = group.components.filter(c => c.tier === 'mandatory').map(c => c.id)
  const selectedIds  = group.required
    ? [...new Set([...mandatoryIds, ...storedIds])]
    : storedIds

  const sortedComponents = [...group.components].sort(
    (a, b) => TIER_ORDER[a.tier] - TIER_ORDER[b.tier]
  )

  /* Per-tier counts for header chips */
  const mItems = group.components.filter(c => c.tier === 'mandatory')
  const rItems = group.components.filter(c => c.tier === 'recommended')
  const oItems = group.components.filter(c => c.tier === 'optional')
  const mSel   = mItems.filter(c => selectedIds.includes(c.id)).length
  const rSel   = rItems.filter(c => selectedIds.includes(c.id)).length
  const oSel   = oItems.filter(c => selectedIds.includes(c.id)).length

  function toggleAll() {
    if (group.required) return
    if (selectedIds.length === 0) {
      const defaults = DEFAULT_COMPONENT_GROUPS[group.id]?.length
        ? DEFAULT_COMPONENT_GROUPS[group.id]
        : group.components.filter(c => c.tier !== 'optional').map(c => c.id)
      store.setGroupComponents(group.id, defaults)
    } else {
      store.setGroupComponents(group.id, [])
    }
  }

  function toggleOne(c: ComponentDef) {
    if (group.required && c.tier === 'mandatory') return
    const allIds = group.components.map(x => x.id)
    const isOn = selectedIds.includes(c.id)
    store.toggleGroupComponent(group.id, c.id, allIds)

    if (!isOn) {
      const deps = allDeps(c.id)
      const byGroup: Record<string, string[]> = {}
      for (const depId of deps) {
        const gid = COMPONENT_GROUP[depId]
        if (gid) (byGroup[gid] ??= []).push(depId)
      }
      for (const [gid, depIds] of Object.entries(byGroup)) {
        const targetGroup = GROUPS.find(g => g.id === gid)
        if (!targetGroup) continue
        const validIds = targetGroup.components.map(x => x.id)
        // Read live store state (post-toggle) — not the stale React render snapshot
        const current = useWizardStore.getState().componentGroups[gid] ?? []
        const merged = [...new Set([...current, ...depIds])].filter(id => validIds.includes(id))
        store.setGroupComponents(gid, merged)
      }
    }
  }

  const colsInner = bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : '1fr 1fr 1fr'
  const active = selectedIds.length > 0

  return (
    <div style={{
      height: '100%', display: 'flex', flexDirection: 'column',
      borderRadius: 10,
      border: active ? '1.5px solid var(--wiz-border)' : '1.5px solid var(--wiz-border-sub)',
      background: active ? 'var(--wiz-bg-sub)' : 'rgba(0,0,0,0.1)',
      overflow: 'hidden', transition: 'all 0.15s',
    }}>
      {/* Header — fixed 3-line height so all cards align */}
      <div
        style={{ display: 'flex', alignItems: 'flex-start', gap: 10, padding: '11px 14px', cursor: group.required ? 'default' : 'pointer' }}
        onClick={toggleAll}
      >
        <div style={{ flex: 1, minWidth: 0 }}>
          {/* Line 1 — PRODUCT NAME — Conceptual Subtitle */}
          <div style={{
            fontSize: 12, fontWeight: 700, lineHeight: 1.3,
            color: active ? 'var(--wiz-text-hi)' : 'var(--wiz-text-sub)',
            whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
          }}>
            {group.productName}
            <span style={{ fontWeight: 400, color: 'var(--wiz-text-sub)', marginLeft: 5 }}>— {group.subtitle}</span>
          </div>
          {/* Line 2 — 1-line description */}
          <div style={{
            fontSize: 10, color: 'var(--wiz-text-sub)', marginTop: 3, lineHeight: 1.3,
            whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
          }}>
            {group.description}
          </div>
          {/* Line 3 — tier chips */}
          <div style={{ display: 'flex', gap: 4, marginTop: 6, flexWrap: 'nowrap' }}>
            {mItems.length > 0 && (
              <span style={{
                fontSize: 9, fontWeight: 700, padding: '2px 6px', borderRadius: 4, lineHeight: 1.4,
                background: 'var(--wiz-border-sub)', border: '1px solid var(--wiz-border)',
                color: 'var(--wiz-text-sub)',
              }}>
                {mSel}/{mItems.length}M
              </span>
            )}
            {rItems.length > 0 && (
              <span style={{
                fontSize: 9, fontWeight: 700, padding: '2px 6px', borderRadius: 4, lineHeight: 1.4,
                background: rSel > 0 ? 'rgba(56,189,248,0.12)' : 'var(--wiz-bg-card)',
                border: rSel > 0 ? '1px solid rgba(56,189,248,0.25)' : '1px solid var(--wiz-border-sub)',
                color: rSel > 0 ? '#38BDF8' : 'var(--wiz-text-hint)',
              }}>
                {rSel}/{rItems.length}R
              </span>
            )}
            {oItems.length > 0 && (
              <span style={{
                fontSize: 9, fontWeight: 700, padding: '2px 6px', borderRadius: 4, lineHeight: 1.4,
                background: oSel > 0 ? 'rgba(167,139,250,0.12)' : 'var(--wiz-bg-card)',
                border: oSel > 0 ? '1px solid rgba(167,139,250,0.25)' : '1px solid var(--wiz-border-sub)',
                color: oSel > 0 ? '#A78BFA' : 'var(--wiz-text-hint)',
              }}>
                {oSel}/{oItems.length}O
              </span>
            )}
          </div>
        </div>

        <button
          type="button"
          onClick={e => { e.stopPropagation(); onToggle() }}
          style={{ width: 26, height: 26, borderRadius: 6, border: '1px solid var(--wiz-border-sub)', background: 'var(--wiz-bg-card)', color: 'var(--wiz-text-sub)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer', flexShrink: 0, marginTop: 1 }}
        >
          {open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
        </button>
      </div>

      {/* Expanded list */}
      {open && (
        <div style={{ borderTop: '1px solid var(--wiz-border-sub)', padding: '8px 14px 12px' }}>
          <div style={{ display: 'grid', gridTemplateColumns: colsInner, gap: 4 }}>
            {sortedComponents.map(c => {
              const on     = selectedIds.includes(c.id)
              const locked = group.required && c.tier === 'mandatory'
              const badge  = TIER_BADGE[c.tier]
              return (
                <div
                  key={c.id}
                  onClick={() => toggleOne(c)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 8,
                    padding: '6px 10px', borderRadius: 7,
                    background: on ? 'var(--wiz-bg-card)' : 'transparent',
                    cursor: locked ? 'default' : 'pointer',
                    opacity: locked ? 0.6 : 1,
                    transition: 'background 0.12s',
                  }}
                >
                  {/* Checkbox — locked = grey checked, user-selected = colored */}
                  <div style={{
                    width: 16, height: 16, borderRadius: 4, flexShrink: 0,
                    border: on ? 'none' : '1.5px solid var(--wiz-border)',
                    background: locked
                      ? 'var(--wiz-text-hint)'
                      : on
                        ? checkboxColor(c.tier, false)
                        : 'transparent',
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                    transition: 'all 0.15s',
                  }}>
                    {on && (
                      <svg width={9} height={9} viewBox="0 0 12 12" fill="none">
                        <path d="M2 6l3 3 5-5" stroke={locked ? 'var(--wiz-text-md)' : '#fff'} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round"/>
                      </svg>
                    )}
                  </div>

                  {/* Component logo */}
                  <div style={{ flexShrink: 0, opacity: locked ? 0.5 : on ? 1 : 0.45, transition: 'opacity 0.15s' }}>
                    {COMPONENT_LOGOS[c.id]}
                  </div>

                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                      <span style={{ fontSize: 12, fontWeight: on ? 600 : 400, color: locked ? 'var(--wiz-text-lo)' : on ? 'var(--wiz-text-hi)' : 'var(--wiz-text-sub)' }}>
                        {c.name}
                      </span>
                      <span style={{ fontSize: 8, fontWeight: 700, padding: '1px 4px', borderRadius: 3, background: `${badge.color}15`, border: `1px solid ${badge.color}30`, color: badge.color }}>
                        {badge.label}
                      </span>
                    </div>
                    <div style={{ fontSize: 10, color: 'var(--wiz-text-hint)', lineHeight: 1.3 }}>{c.desc}</div>
                  </div>

                  {locked && <Lock size={10} style={{ color: 'var(--wiz-text-sub)', flexShrink: 0 }} />}
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

/* ── CardGrid at module level — stable type, never remounted ─────── */
function CardGrid({ groups, cols, onOpen }: { groups: GroupDef[]; cols: string; onOpen: (id: string) => void }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: cols, gap: 8 }}>
      {groups.map(g => (
        <GroupCard key={g.id} group={g} open={false} onToggle={() => onOpen(g.id)} />
      ))}
    </div>
  )
}

export function StepComponents() {
  const { next, back } = useStepNav()
  const store = useWizardStore()
  const bp = useBreakpoint()

  const totalSelected = GROUPS.reduce((sum, g) => {
    const stored = store.componentGroups[g.id] ?? []
    const mandatory = g.required ? g.components.filter(c => c.tier === 'mandatory').map(c => c.id) : []
    return sum + new Set([...mandatory, ...stored]).size
  }, 0)
  const totalAll = GROUPS.reduce((sum, g) => sum + g.components.length, 0)

  const colCount = bp === 'mobile' ? 1 : bp === 'tablet' ? 2 : 3
  const cols     = bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : '1fr 1fr 1fr'

  const [openGroupId, setOpenGroupId] = useState<string | null>(null)

  /* Apply profile-based defaults on first visit, or when org profile changes */
  useEffect(() => {
    const profileHash = [store.orgIndustry, store.orgSize, ...store.orgCompliance.slice().sort()].join('|')
    if (store.componentsAppliedForProfile === profileHash) return
    const defaults = getProfileDefaults(store.orgIndustry, store.orgCompliance, store.orgSize)
    for (const [gid, ids] of Object.entries(defaults)) {
      store.setGroupComponents(gid, ids)
    }
    store.setComponentsAppliedForProfile(profileHash)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [store.orgIndustry, store.orgSize, store.orgCompliance])

  /* Row-boundary split — all row-mates (left + right) shift below expanded card */
  const openIdx      = openGroupId ? GROUPS.findIndex(g => g.id === openGroupId) : -1
  const rowStart     = openIdx > -1 ? Math.floor(openIdx / colCount) * colCount : -1
  const beforeGroups = openIdx > -1 ? GROUPS.slice(0, rowStart) : GROUPS
  const openGroup    = openIdx > -1 ? GROUPS[openIdx] : null
  const afterGroups  = openIdx > -1
    ? [...GROUPS.slice(rowStart, openIdx), ...GROUPS.slice(openIdx + 1)]
    : []

  return (
    <StepShell
      title="Platform components"
      description="Required blocks are pre-selected and locked. Toggle recommended/optional components. Click an optional block to enable it."
      onNext={next}
      onBack={back}
    >
      {/* Summary bar */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 12px', borderRadius: 8, background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)' }}>
        <span style={{ fontSize: 12, color: 'var(--wiz-accent)', fontWeight: 600 }}>
          {totalSelected} of {totalAll} components selected
        </span>
        <div style={{ flex: 1, height: 3, borderRadius: 2, background: 'var(--wiz-border-sub)', overflow: 'hidden' }}>
          <div style={{ height: '100%', width: `${(totalSelected / totalAll) * 100}%`, background: 'linear-gradient(90deg, #38BDF8, #818CF8)', transition: 'width 0.3s' }} />
        </div>
      </div>

      {beforeGroups.length > 0 && <CardGrid groups={beforeGroups} cols={cols} onOpen={setOpenGroupId} />}
      {openGroup && <GroupCard group={openGroup} open={true} onToggle={() => setOpenGroupId(null)} />}
      {afterGroups.length > 0 && <CardGrid groups={afterGroups} cols={cols} onOpen={setOpenGroupId} />}
    </StepShell>
  )
}
