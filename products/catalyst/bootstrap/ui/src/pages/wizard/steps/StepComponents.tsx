// StepComponents — corporate platform component grid.
//
// Visual contract: pixel-matches the SME marketplace card pattern at
// core/marketplace/src/components/AppsStep.svelte. Two tabs:
//
//   1. "Choose Your Stack" (default)  — non-mandatory components.
//      Search + category chip filter, sort-selected-first, cascade-add
//      and cascade-remove dependency logic.
//
//   2. "Always Included"               — mandatory infra. Read-only.
//      Grouped by product (PILOT, GUARDIAN, …), no search, no toggle UI.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   #2 — never compromise quality: SME marketplace is the proven shape;
//        this step copies its surface 1:1 (height, padding, hover, chips,
//        toast slot) so the SME and corporate products feel like a family.
//   #4 — never hardcode: tabs, tiers, logos, dependency edges — every
//        catalog fact comes from componentGroups.ts. Logo URLs default to
//        `/component-logos/<id>.svg` so swapping a file under public/
//        rebrands the card without touching application source.

import { useMemo, useState, useCallback } from 'react'
import { Search, Plus, Check, Lock, Info } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { path as basePath } from '@/shared/config/urls'
import { StepShell, useStepNav } from './_shared'
import {
  ALL_COMPONENTS,
  GROUPS,
  findComponent,
  resolveTransitiveDependencies,
  resolveTransitiveDependents,
  type ComponentEntry,
  type GroupDef,
} from './componentGroups'

/** Sort: selected first (cart-like UX from marketplace), then alphabetical. */
function sortComponents(
  items: readonly ComponentEntry[],
  selected: ReadonlySet<string>,
): ComponentEntry[] {
  return [...items].sort((a, b) => {
    const aSel = selected.has(a.id) ? 0 : 1
    const bSel = selected.has(b.id) ? 0 : 1
    if (aSel !== bSel) return aSel - bSel
    return a.name.localeCompare(b.name)
  })
}

/** Letter-pill icon when a component has no logo URL. Hue derived from name. */
function IconFallback({ name }: { name: string }) {
  const letter = (name[0] ?? '?').toUpperCase()
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = (hash * 31 + name.charCodeAt(i)) | 0
  const hue = Math.abs(hash) % 360
  return (
    <span
      aria-hidden
      style={{
        alignSelf: 'stretch',
        aspectRatio: '1 / 1',
        height: 'auto',
        borderRadius: 10,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        color: '#fff',
        fontSize: '1.2rem',
        fontWeight: 700,
        background: `oklch(58% 0.12 ${hue})`,
      }}
    >
      {letter}
    </span>
  )
}

/** Logo block — vendored SVG when available, letter-mark fallback otherwise. */
function ComponentLogo({ entry }: { entry: ComponentEntry }) {
  if (!entry.logoUrl) return <IconFallback name={entry.name} />
  return (
    <span
      aria-hidden
      style={{
        alignSelf: 'stretch',
        aspectRatio: '1 / 1',
        height: 'auto',
        borderRadius: 10,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        background: 'rgba(255,255,255,0.04)',
        overflow: 'hidden',
      }}
    >
      <img
        src={entry.logoUrl}
        alt=""
        loading="lazy"
        data-testid={`logo-${entry.id}`}
        style={{
          width: '100%',
          height: '100%',
          objectFit: 'contain',
          display: 'block',
        }}
      />
    </span>
  )
}

type ChipTone = 'success' | 'neutral' | 'accent' | 'warn' | 'mandatory' | 'recommended' | 'optional' | 'infra'

const CHIP_PALETTE: Record<ChipTone, { bg: string; fg: string }> = {
  success:     { bg: 'rgba(74,222,128,0.14)',  fg: '#4ADE80' },
  neutral:     { bg: 'rgba(148,163,184,0.14)', fg: 'var(--wiz-text-md)' },
  accent:      { bg: 'rgba(56,189,248,0.14)',  fg: '#38BDF8' },
  warn:        { bg: 'rgba(245,158,11,0.14)',  fg: '#F59E0B' },
  mandatory:   { bg: 'rgba(74,222,128,0.16)',  fg: '#4ADE80' },
  recommended: { bg: 'rgba(56,189,248,0.16)',  fg: '#38BDF8' },
  optional:    { bg: 'rgba(167,139,250,0.16)', fg: '#A78BFA' },
  infra:       { bg: 'rgba(148,163,184,0.16)', fg: 'var(--wiz-text-md)' },
}

function CardChip({
  label,
  tone = 'neutral',
  title,
  testId,
}: {
  label: React.ReactNode
  tone?: ChipTone
  title?: string
  testId?: string
}) {
  const palette = CHIP_PALETTE[tone]
  return (
    <span
      title={title}
      data-testid={testId}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 4,
        padding: '0.1rem 0.45rem',
        borderRadius: 999,
        fontSize: '0.65rem',
        fontWeight: 600,
        lineHeight: 1.4,
        whiteSpace: 'nowrap',
        background: palette.bg,
        color: palette.fg,
      }}
    >
      {label}
    </span>
  )
}

/* ── Card ─────────────────────────────────────────────────────────── */

interface ComponentCardProps {
  entry: ComponentEntry
  selected: boolean
  onToggle?: () => void
  /** When true, renders the read-only Tab 2 layout (no toggle, INFRA pill). */
  readOnly?: boolean
}

function ComponentCard({ entry, selected, onToggle, readOnly = false }: ComponentCardProps) {
  const isMandatoryCard = entry.tier === 'mandatory'
  const includesNote = (entry.dependencies ?? []).length > 0
    ? `Includes: ${(entry.dependencies ?? []).map(d => findComponent(d)?.name ?? d).join(', ')}`
    : null

  const cardClass = [
    'corp-comp-card',
    selected ? 'in-cart' : '',
    readOnly ? 'read-only' : '',
    isMandatoryCard ? 'mandatory' : '',
  ].filter(Boolean).join(' ')

  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={selected}
      data-testid={`component-card-${entry.id}`}
      data-selected={selected ? 'true' : 'false'}
      data-tier={entry.tier}
      className={cardClass}
      // Disable click handling for read-only cards entirely so
      // keyboard activation can't bypass the no-toggle contract.
      disabled={readOnly && !onToggle}
    >
      <ComponentLogo entry={entry} />

      <div className="corp-comp-body">
        <div className="corp-comp-top">
          <span className="corp-comp-name">{entry.name}</span>
          <span className="corp-comp-cat">{entry.groupName}</span>
        </div>
        <p className="corp-comp-desc">{entry.desc}</p>
        <div className="corp-comp-chips">
          {/* Tier badge — primary trait. In the read-only Tab 2 the
              MANDATORY pill is replaced by an INFRASTRUCTURE pill so users
              read it as platform infra rather than a wizard-controlled
              option. */}
          {readOnly ? (
            <CardChip
              label="INFRASTRUCTURE"
              tone="infra"
              testId={`tier-${entry.id}`}
            />
          ) : entry.tier === 'mandatory' ? (
            <CardChip
              label={
                <>
                  <Lock size={9} strokeWidth={3} aria-hidden /> MANDATORY
                </>
              }
              tone="mandatory"
              testId={`tier-${entry.id}`}
            />
          ) : entry.tier === 'recommended' ? (
            <CardChip label="RECOMMENDED" tone="recommended" testId={`tier-${entry.id}`} />
          ) : (
            <CardChip label="OPTIONAL" tone="optional" testId={`tier-${entry.id}`} />
          )}
          {includesNote && (
            <CardChip
              label={`+ ${(entry.dependencies ?? []).length} dep${
                (entry.dependencies ?? []).length > 1 ? 's' : ''
              }`}
              tone="accent"
              title={includesNote}
              testId={`deps-${entry.id}`}
            />
          )}
        </div>
        {includesNote && (
          <p
            className="corp-comp-includes"
            data-testid={`includes-${entry.id}`}
          >
            {includesNote}
          </p>
        )}
      </div>

      {/* Selected status corner — bottom-right, mirrors SME .status-corner.
          Suppressed in read-only mode (Tab 2); selection isn't a concept
          there. */}
      {selected && !readOnly && (
        <div
          className="corp-comp-status"
          data-testid={`selected-${entry.id}`}
        >
          <span className="corp-comp-status-pill">
            <span className="corp-comp-status-dot" /> SELECTED
          </span>
        </div>
      )}

      {/* Add/remove circle button (top-right). Hidden in read-only mode.
          Default-hidden then revealed on hover, matching SME's
          .app-add-btn opacity-0 → 1 transition. */}
      {!readOnly && (
        <span aria-hidden className="corp-comp-add-btn" data-testid={`toggle-${entry.id}`}>
          {selected ? <Check size={16} strokeWidth={3} /> : <Plus size={16} strokeWidth={2.5} />}
        </span>
      )}
    </button>
  )
}

/* ── Toast ─────────────────────────────────────────────────────────── */

interface Toast {
  id: number
  kind: 'added' | 'removed' | 'mandatory'
  primary: string
  extra?: string
}

function ToastStack({ toasts }: { toasts: Toast[] }) {
  return (
    <div
      data-testid="toast-stack"
      style={{
        position: 'fixed',
        top: '4.5rem',
        right: '1.25rem',
        zIndex: 200,
        display: 'flex',
        flexDirection: 'column',
        gap: '0.4rem',
        pointerEvents: 'none',
      }}
    >
      {toasts.map((t) => (
        <div
          key={t.id}
          data-testid={`toast-${t.kind}`}
          style={{
            background: 'var(--wiz-bg-sub)',
            border:
              '1px solid ' +
              (t.kind === 'added'
                ? '#4ADE80'
                : t.kind === 'mandatory'
                  ? '#F59E0B'
                  : 'var(--wiz-border-sub)'),
            borderRadius: 8,
            padding: '0.5rem 0.85rem',
            fontSize: '0.82rem',
            color: 'var(--wiz-text-hi)',
            boxShadow: '0 4px 16px rgba(0,0,0,0.2)',
            maxWidth: 360,
            display: 'flex',
            flexDirection: 'column',
            gap: 2,
            animation: 'corp-toast-in 0.25s ease-out',
          }}
        >
          <span style={{ fontWeight: 600 }}>{t.primary}</span>
          {t.extra && (
            <span style={{ color: 'var(--wiz-text-sub)', fontSize: '0.72rem' }}>{t.extra}</span>
          )}
        </div>
      ))}
    </div>
  )
}

/* ── Confirm dialog (cascading remove) ─────────────────────────────── */

interface ConfirmCascade {
  componentId: string
  componentName: string
  dependents: ComponentEntry[]
}

function ConfirmCascadeDialog({
  cascade,
  onConfirm,
  onCancel,
}: {
  cascade: ConfirmCascade
  onConfirm: () => void
  onCancel: () => void
}) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      data-testid="cascade-dialog"
      style={{
        position: 'fixed',
        inset: 0,
        background: 'rgba(2,6,23,0.65)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        zIndex: 300,
        padding: '1rem',
      }}
      onClick={onCancel}
    >
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          background: 'var(--wiz-bg-sub)',
          border: '1px solid var(--wiz-border-sub)',
          borderRadius: 12,
          padding: '1.25rem 1.4rem',
          maxWidth: 480,
          width: '100%',
          boxShadow: '0 12px 40px rgba(0,0,0,0.4)',
          display: 'flex',
          flexDirection: 'column',
          gap: '0.85rem',
        }}
      >
        <h3
          style={{
            margin: 0,
            fontSize: '1.05rem',
            color: 'var(--wiz-text-hi)',
            fontWeight: 600,
          }}
        >
          Remove {cascade.componentName}?
        </h3>
        <p
          style={{
            margin: 0,
            color: 'var(--wiz-text-md)',
            fontSize: '0.85rem',
            lineHeight: 1.5,
          }}
        >
          {cascade.componentName} is used by{' '}
          <strong>{cascade.dependents.length}</strong> other component
          {cascade.dependents.length > 1 ? 's' : ''}. Removing it will also
          remove:
        </p>
        <ul
          data-testid="cascade-dependents"
          style={{
            margin: 0,
            padding: '0 0 0 1.25rem',
            color: 'var(--wiz-text-md)',
            fontSize: '0.82rem',
            lineHeight: 1.6,
          }}
        >
          {cascade.dependents.map((d) => (
            <li key={d.id}>
              <strong style={{ color: 'var(--wiz-text-hi)' }}>{d.name}</strong>{' '}
              <span style={{ color: 'var(--wiz-text-sub)' }}>— {d.desc}</span>
            </li>
          ))}
        </ul>
        <div
          style={{
            display: 'flex',
            gap: '0.5rem',
            justifyContent: 'flex-end',
            marginTop: '0.25rem',
          }}
        >
          <button
            type="button"
            onClick={onCancel}
            data-testid="cascade-cancel"
            style={{
              padding: '0.55rem 1.1rem',
              borderRadius: 8,
              border: '1px solid var(--wiz-border-sub)',
              background: 'transparent',
              color: 'var(--wiz-text-md)',
              cursor: 'pointer',
              font: 'inherit',
              fontSize: '0.85rem',
            }}
          >
            Keep
          </button>
          <button
            type="button"
            onClick={onConfirm}
            data-testid="cascade-confirm"
            style={{
              padding: '0.55rem 1.1rem',
              borderRadius: 8,
              border: '1px solid #F87171',
              background: '#F87171',
              color: '#fff',
              cursor: 'pointer',
              font: 'inherit',
              fontSize: '0.85rem',
              fontWeight: 600,
            }}
          >
            Remove all
          </button>
        </div>
      </div>
    </div>
  )
}

/* ── Tab 2: "Always Included" — read-only mandatory grid ──────────── */

function AlwaysIncludedTab({ groups, cols }: { groups: readonly GroupDef[]; cols: string }) {
  // Group by product, but only the mandatory components per group, hiding
  // any group that has zero mandatories.
  const productSections = useMemo(() => {
    return groups
      .map((g) => ({
        group: g,
        mandatories: g.components.filter((c) => c.tier === 'mandatory'),
      }))
      .filter((s) => s.mandatories.length > 0)
  }, [groups])

  return (
    <div data-testid="always-included-tab">
      <p
        data-testid="always-included-blurb"
        style={{
          margin: '0 0 1rem',
          padding: '0.75rem 1rem',
          background: 'var(--wiz-bg-xs)',
          border: '1px solid var(--wiz-border-sub)',
          borderRadius: 8,
          color: 'var(--wiz-text-md)',
          fontSize: '0.85rem',
          lineHeight: 1.55,
        }}
      >
        These platform components run on every Sovereign. They’re foundational
        — you don’t pay extra for them.
      </p>

      <div
        data-testid="always-included-grid"
        style={{ display: 'flex', flexDirection: 'column', gap: '1.25rem' }}
      >
        {productSections.map(({ group, mandatories }) => (
          <section
            key={group.id}
            data-testid={`always-included-section-${group.id}`}
          >
            <h4
              style={{
                margin: '0 0 0.5rem',
                color: 'var(--wiz-text-md)',
                fontSize: '0.78rem',
                fontWeight: 600,
                letterSpacing: '0.08em',
                textTransform: 'uppercase',
              }}
            >
              {group.productName}{' '}
              <span
                style={{
                  color: 'var(--wiz-text-sub)',
                  fontWeight: 500,
                  letterSpacing: 0,
                  textTransform: 'none',
                  marginLeft: 6,
                }}
              >
                {group.subtitle}
              </span>
            </h4>
            <div style={{ display: 'grid', gridTemplateColumns: cols, gap: '0.65rem' }}>
              {mandatories.map((c) => {
                const entry: ComponentEntry = {
                  ...c,
                  dependencies: c.dependencies ?? [],
                  logoUrl: c.logoUrl === undefined ? basePath(`component-logos/${c.id}.svg`) : c.logoUrl,
                  groupId: group.id,
                  groupName: group.productName,
                  groupSubtitle: group.subtitle,
                }
                return (
                  <ComponentCard
                    key={entry.id}
                    entry={entry}
                    selected={false}
                    readOnly
                  />
                )
              })}
            </div>
          </section>
        ))}
      </div>
    </div>
  )
}

/* ── Step ─────────────────────────────────────────────────────────── */

type TabKey = 'choose' | 'always'

export function StepComponents() {
  const { next, back } = useStepNav()
  const store = useWizardStore()
  const bp = useBreakpoint()

  const [tab, setTab] = useState<TabKey>('choose')
  const [query, setQuery] = useState('')
  const [activeCategory, setActiveCategory] = useState<string | null>(null)
  const [toasts, setToasts] = useState<Toast[]>([])
  const [pendingRemoval, setPendingRemoval] = useState<ConfirmCascade | null>(null)

  const selectedSet = useMemo(
    () => new Set(store.selectedComponents),
    [store.selectedComponents],
  )

  // Tab 1 source pool — non-mandatory only.
  const choosePool = useMemo(
    () => ALL_COMPONENTS.filter((c) => c.tier !== 'mandatory'),
    [],
  )

  // Mandatory total — drives Tab 2 counter (computed from data, not hardcoded).
  const mandatoryTotal = useMemo(
    () => ALL_COMPONENTS.filter((c) => c.tier === 'mandatory').length,
    [],
  )

  // Selected count for Tab 1's badge — mandatory ids are always selected
  // and not user-controllable, so the counter shows the user-meaningful
  // subset (recommended + optional currently chosen).
  const chooseSelectedCount = useMemo(
    () => store.selectedComponents.filter((id) => {
      const c = findComponent(id)
      return c && c.tier !== 'mandatory'
    }).length,
    [store.selectedComponents],
  )

  // Categories — only product groups that have at least one non-mandatory
  // component (so Tab 1's chips don't present empty groups).
  const categories = useMemo(() => {
    const ids = new Set<string>()
    for (const c of choosePool) ids.add(c.groupId)
    return [...ids].sort()
  }, [choosePool])

  const visible = useMemo(() => {
    let result = choosePool.slice()
    if (activeCategory) {
      result = result.filter((c) => c.groupId === activeCategory)
    }
    if (query) {
      const q = query.toLowerCase()
      result = result.filter(
        (c) =>
          c.name.toLowerCase().includes(q) ||
          c.id.toLowerCase().includes(q) ||
          c.desc.toLowerCase().includes(q) ||
          c.groupName.toLowerCase().includes(q) ||
          c.groupSubtitle.toLowerCase().includes(q),
      )
    }
    return sortComponents(result, selectedSet)
  }, [query, activeCategory, selectedSet, choosePool])

  const cols = bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : 'repeat(3, minmax(0, 1fr))'

  /** Ephemeral toast — auto-dismisses after 2.5s. */
  const pushToast = useCallback((kind: Toast['kind'], primary: string, extra?: string) => {
    setToasts((prev) => {
      const id = (prev[prev.length - 1]?.id ?? 0) + 1
      const next = [...prev, { id, kind, primary, extra }]
      setTimeout(() => {
        setToasts((cur) => cur.filter((t) => t.id !== id))
      }, 2500)
      return next
    })
  }, [])

  const handleToggle = useCallback(
    (entry: ComponentEntry) => {
      if (entry.tier === 'mandatory') {
        pushToast(
          'mandatory',
          `${entry.name} is mandatory`,
          'Core platform components are always installed.',
        )
        return
      }

      if (selectedSet.has(entry.id)) {
        const dependentIds = resolveTransitiveDependents(entry.id).filter((id) =>
          selectedSet.has(id),
        )
        const dependents = dependentIds
          .map((id) => findComponent(id))
          .filter((c): c is ComponentEntry => !!c)
        if (dependents.length > 0) {
          setPendingRemoval({
            componentId: entry.id,
            componentName: entry.name,
            dependents,
          })
          return
        }
        store.removeComponent(entry.id)
        pushToast('removed', `${entry.name} removed`)
        return
      }

      const newDeps = resolveTransitiveDependencies(entry.id).filter((id) => !selectedSet.has(id))
      store.addComponent(entry.id)
      if (newDeps.length > 0) {
        const depNames = newDeps
          .map((id) => findComponent(id)?.name ?? id)
          .join(', ')
        pushToast(
          'added',
          `${entry.name} added`,
          `Also added: ${depNames}`,
        )
      } else {
        pushToast('added', `${entry.name} added`)
      }
    },
    [selectedSet, pushToast, store],
  )

  const confirmCascadeRemove = useCallback(() => {
    if (!pendingRemoval) return
    const target = pendingRemoval
    store.removeComponent(target.componentId)
    pushToast(
      'removed',
      `${target.componentName} removed`,
      `Also removed: ${target.dependents.map((d) => d.name).join(', ')}`,
    )
    setPendingRemoval(null)
  }, [pendingRemoval, store, pushToast])

  return (
    <StepShell
      title="Platform Components"
      description="Choose Your Stack lists the components you can opt into for this Sovereign — search, filter by product, and click to add or remove. Cascading dependencies are handled automatically (Harbor pulls in cnpg, seaweedfs, valkey). Always Included shows the foundational platform components every Sovereign runs."
      onNext={next}
      onBack={back}
    >
      {/* ── Tabs ──────────────────────────────────────────────── */}
      <div role="tablist" data-testid="component-tabs" className="corp-tabs">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'choose'}
          data-testid="tab-choose"
          onClick={() => setTab('choose')}
          className={`corp-tab ${tab === 'choose' ? 'active' : ''}`}
        >
          Choose Your Stack ({chooseSelectedCount})
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'always'}
          data-testid="tab-always"
          onClick={() => setTab('always')}
          className={`corp-tab ${tab === 'always' ? 'active' : ''}`}
        >
          Always Included ({mandatoryTotal})
        </button>
      </div>

      {tab === 'choose' && (
        <>
          {/* Selected counter top bar */}
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 10,
              padding: '8px 12px',
              borderRadius: 8,
              background: 'rgba(56,189,248,0.05)',
              border: '1px solid rgba(56,189,248,0.12)',
            }}
          >
            <span
              data-testid="selected-counter"
              style={{ fontSize: 12, color: 'var(--wiz-accent)', fontWeight: 600 }}
            >
              Selected ({store.selectedComponents.length}) of {ALL_COMPONENTS.length}
            </span>
            <div
              style={{
                flex: 1,
                height: 3,
                borderRadius: 2,
                background: 'var(--wiz-border-sub)',
                overflow: 'hidden',
              }}
            >
              <div
                style={{
                  height: '100%',
                  width: `${(store.selectedComponents.length / Math.max(1, ALL_COMPONENTS.length)) * 100}%`,
                  background: 'linear-gradient(90deg, #38BDF8, #818CF8)',
                  transition: 'width 0.3s',
                }}
              />
            </div>
            <button
              type="button"
              onClick={() => store.resetSelectedComponentsToDefault()}
              data-testid="reset-defaults"
              style={{
                padding: '0.35rem 0.7rem',
                borderRadius: 6,
                border: '1px solid var(--wiz-border-sub)',
                background: 'transparent',
                color: 'var(--wiz-text-sub)',
                cursor: 'pointer',
                font: 'inherit',
                fontSize: '0.75rem',
              }}
              title="Restore the default selection (all mandatory + recommended + their dependencies)"
            >
              Reset to defaults
            </button>
          </div>

          {/* Toolbar */}
          <div
            style={{
              background: 'var(--wiz-bg-xs)',
              border: '1px solid var(--wiz-border-sub)',
              borderRadius: 12,
              padding: '0.75rem 1rem',
              display: 'flex',
              flexDirection: 'column',
              gap: '0.65rem',
            }}
          >
            <div style={{ position: 'relative' }}>
              <Search
                size={14}
                aria-hidden
                style={{
                  position: 'absolute',
                  left: 12,
                  top: '50%',
                  transform: 'translateY(-50%)',
                  color: 'var(--wiz-text-sub)',
                  opacity: 0.5,
                }}
              />
              <input
                type="search"
                data-testid="component-search"
                placeholder={`Search ${choosePool.length} components…`}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                style={{
                  width: '100%',
                  padding: '0.6rem 0.85rem 0.6rem 2.2rem',
                  background: 'var(--wiz-bg-sub)',
                  border: '1px solid var(--wiz-border-sub)',
                  borderRadius: 8,
                  color: 'var(--wiz-text-hi)',
                  font: 'inherit',
                  fontSize: '0.88rem',
                }}
              />
            </div>
            <div
              data-testid="category-chips"
              style={{ display: 'flex', flexWrap: 'wrap', gap: '0.35rem' }}
            >
              <button
                type="button"
                data-testid="category-chip-all"
                onClick={() => setActiveCategory(null)}
                style={{
                  padding: '0.4rem 0.7rem',
                  borderRadius: 999,
                  background: activeCategory === null ? 'var(--wiz-accent)' : 'var(--wiz-bg-sub)',
                  border:
                    '1px solid ' +
                    (activeCategory === null ? 'var(--wiz-accent)' : 'var(--wiz-border-sub)'),
                  color: activeCategory === null ? '#fff' : 'var(--wiz-text-sub)',
                  font: 'inherit',
                  fontSize: '0.78rem',
                  cursor: 'pointer',
                  transition: 'all 0.15s',
                }}
              >
                All
              </button>
              {categories.map((cat) => (
                <button
                  key={cat}
                  type="button"
                  data-testid={`category-chip-${cat}`}
                  onClick={() => setActiveCategory(activeCategory === cat ? null : cat)}
                  style={{
                    padding: '0.4rem 0.7rem',
                    borderRadius: 999,
                    background: activeCategory === cat ? 'var(--wiz-accent)' : 'var(--wiz-bg-sub)',
                    border:
                      '1px solid ' +
                      (activeCategory === cat ? 'var(--wiz-accent)' : 'var(--wiz-border-sub)'),
                    color: activeCategory === cat ? '#fff' : 'var(--wiz-text-sub)',
                    font: 'inherit',
                    fontSize: '0.78rem',
                    cursor: 'pointer',
                    transition: 'all 0.15s',
                    textTransform: 'uppercase',
                    letterSpacing: '0.04em',
                  }}
                >
                  {cat}
                </button>
              ))}
            </div>
          </div>

          {/* Section head */}
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'baseline',
              paddingBottom: '0.4rem',
              borderBottom: '1px solid var(--wiz-border-sub)',
            }}
          >
            <h3
              style={{
                color: 'var(--wiz-text-hi)',
                fontSize: '0.95rem',
                margin: 0,
                fontWeight: 600,
              }}
            >
              All components
            </h3>
            <span style={{ color: 'var(--wiz-text-sub)', fontSize: '0.78rem' }}>
              {visible.length} shown · {chooseSelectedCount} selected
            </span>
          </div>

          {/* Card grid */}
          <div
            data-testid="component-grid"
            style={{ display: 'grid', gridTemplateColumns: cols, gap: '0.65rem' }}
          >
            {visible.map((entry) => (
              <ComponentCard
                key={entry.id}
                entry={entry}
                selected={selectedSet.has(entry.id)}
                onToggle={() => handleToggle(entry)}
              />
            ))}
            {visible.length === 0 && (
              <div
                data-testid="empty-state"
                style={{
                  gridColumn: '1 / -1',
                  padding: '2rem',
                  borderRadius: 12,
                  border: '1.5px dashed var(--wiz-border-sub)',
                  display: 'flex',
                  flexDirection: 'column',
                  alignItems: 'center',
                  gap: '0.5rem',
                }}
              >
                <Info size={20} style={{ color: 'var(--wiz-text-sub)' }} aria-hidden />
                <p
                  style={{
                    margin: 0,
                    color: 'var(--wiz-text-sub)',
                    fontSize: '0.85rem',
                    textAlign: 'center',
                  }}
                >
                  No components match your filters.
                </p>
              </div>
            )}
          </div>
        </>
      )}

      {tab === 'always' && (
        <AlwaysIncludedTab groups={GROUPS} cols={cols} />
      )}

      <ToastStack toasts={toasts} />
      {pendingRemoval && (
        <ConfirmCascadeDialog
          cascade={pendingRemoval}
          onConfirm={confirmCascadeRemove}
          onCancel={() => setPendingRemoval(null)}
        />
      )}

      {/* SME-marketplace pixel-perfect card surface — single source of
          card visual rules. Matches AppsStep.svelte's .app-card 1:1. */}
      <style>{`
        @keyframes corp-toast-in {
          from { transform: translateY(-16px); opacity: 0; }
          to   { transform: translateY(0);     opacity: 1; }
        }

        /* ── Tabs (top of step) ───────────────────────────────────── */
        .corp-tabs {
          display: flex;
          gap: 0;
          border-bottom: 1px solid var(--wiz-border-sub);
          margin-bottom: 0.25rem;
        }
        .corp-tab {
          background: transparent;
          border: none;
          border-bottom: 2px solid transparent;
          color: var(--wiz-text-sub);
          padding: 0.65rem 1.1rem;
          font: inherit;
          font-size: 0.88rem;
          font-weight: 600;
          cursor: pointer;
          transition: color 0.15s, border-color 0.15s;
          margin-bottom: -1px;
        }
        .corp-tab:hover { color: var(--wiz-text-md); }
        .corp-tab.active {
          color: var(--wiz-text-hi);
          border-bottom-color: rgba(var(--wiz-accent-ch), 1);
        }

        /* ── Card (mirrors AppsStep .app-card) ─────────────────────── */
        .corp-comp-card {
          position: relative;
          background: var(--wiz-bg-sub);
          border: 1.5px solid var(--wiz-border-sub);
          border-radius: 12px;
          padding: 0.6rem;
          display: flex;
          align-items: stretch;
          gap: 0.75rem;
          cursor: pointer;
          transition: transform 0.15s, border-color 0.15s, background 0.15s;
          color: inherit;
          text-align: left;
          font: inherit;
          height: 108px;
          overflow: hidden;
        }
        .corp-comp-card:hover {
          transform: translateY(-2px);
          border-color: rgba(var(--wiz-accent-ch), 0.7);
          box-shadow: 0 4px 16px rgba(0, 0, 0, 0.12);
        }
        .corp-comp-card.in-cart {
          border-color: #4ADE80;
          background: color-mix(in srgb, #4ADE80 6%, var(--wiz-bg-sub));
        }
        .corp-comp-card.read-only {
          cursor: default;
          opacity: 0.92;
        }
        .corp-comp-card.read-only:hover {
          transform: none;
          border-color: var(--wiz-border-sub);
          box-shadow: none;
          opacity: 1;
        }
        .corp-comp-card.read-only .corp-comp-name,
        .corp-comp-card.read-only .corp-comp-desc {
          color: var(--wiz-text-md);
        }

        /* Body column */
        .corp-comp-body {
          flex: 1;
          min-width: 0;
          display: flex;
          flex-direction: column;
          gap: 0.25rem;
          padding-right: 4.5rem;
          overflow: hidden;
        }
        .corp-comp-top {
          display: flex;
          align-items: baseline;
          gap: 0.5rem;
        }
        .corp-comp-name {
          color: var(--wiz-text-hi);
          font-size: 0.92rem;
          font-weight: 600;
          line-height: 1.2;
          overflow: hidden;
          text-overflow: ellipsis;
          white-space: nowrap;
          flex: 1 1 auto;
          min-width: 0;
        }
        .corp-comp-cat {
          color: var(--wiz-text-sub);
          font-size: 0.68rem;
          text-transform: capitalize;
          background: var(--wiz-border-sub);
          padding: 0.1rem 0.4rem;
          border-radius: 3px;
          flex-shrink: 0;
        }
        .corp-comp-desc {
          margin: 0;
          color: var(--wiz-text-md);
          font-size: 0.78rem;
          line-height: 1.45;
          display: -webkit-box;
          -webkit-line-clamp: 2;
          -webkit-box-orient: vertical;
          overflow: hidden;
        }
        .corp-comp-chips {
          margin-top: 0.25rem;
          display: flex;
          flex-wrap: nowrap;
          gap: 0.25rem;
          overflow: hidden;
          mask-image: linear-gradient(to right, #000 85%, transparent);
          -webkit-mask-image: linear-gradient(to right, #000 85%, transparent);
          min-height: 1.4rem;
          align-items: center;
        }
        .corp-comp-includes {
          margin: 0;
          color: var(--wiz-text-hint);
          font-size: 0.7rem;
          line-height: 1.4;
          overflow: hidden;
          text-overflow: ellipsis;
          white-space: nowrap;
        }

        /* Selected status pill — bottom-right */
        .corp-comp-status {
          position: absolute;
          bottom: 0.5rem;
          right: 0.55rem;
          pointer-events: none;
        }
        .corp-comp-status-pill {
          display: inline-flex;
          align-items: center;
          gap: 0.3rem;
          padding: 0.15rem 0.55rem;
          border-radius: 999px;
          font-size: 0.65rem;
          font-weight: 600;
          line-height: 1.4;
          letter-spacing: 0.03em;
          background: rgba(74,222,128,0.16);
          color: #4ADE80;
        }
        .corp-comp-status-dot {
          width: 6px;
          height: 6px;
          border-radius: 50%;
          background: currentColor;
          display: inline-block;
        }

        /* Add/remove button — top-right, hidden until hover (SME pattern) */
        .corp-comp-add-btn {
          position: absolute;
          top: 0.6rem;
          right: 0.6rem;
          width: 32px;
          height: 32px;
          border-radius: 50%;
          display: flex;
          align-items: center;
          justify-content: center;
          opacity: 0;
          transform: scale(0.8);
          transition: opacity 0.15s, transform 0.15s, background 0.15s;
          background: rgba(var(--wiz-accent-ch), 1);
          color: #fff;
          z-index: 2;
          pointer-events: none;
          box-shadow: 0 1px 4px rgba(0, 0, 0, 0.2);
        }
        .corp-comp-card:hover .corp-comp-add-btn {
          opacity: 1;
          transform: scale(1);
        }
        .corp-comp-card.in-cart .corp-comp-add-btn {
          background: #4ADE80;
          opacity: 1;
          transform: scale(1);
        }
        .corp-comp-card.mandatory .corp-comp-add-btn {
          /* Mandatory cards never appear in Tab 1 grid (the choose pool
             excludes mandatory) so this rule only applies to direct
             rendering by AlwaysIncludedTab — there the add button is
             suppressed entirely via readOnly. Defensive fallback: tint
             the icon green if it ever leaks through. */
          background: rgba(74,222,128,0.85);
        }

        /* Tablet/mobile */
        @media (max-width: 1080px) {
          .corp-comp-body { padding-right: 4rem; }
        }
        @media (max-width: 768px) {
          .corp-comp-card { height: auto; min-height: 108px; }
        }
      `}</style>
    </StepShell>
  )
}
