// StepComponents — corporate platform component grid.
//
// Mirrors core/marketplace/src/components/AppsStep.svelte's pattern (search
// + category chips + flat sort-selected-first card grid) with the corporate
// platform catalog from componentGroups.ts as the data source. Replaces the
// previous bp-<x> Blueprint card grid (visibility:listed) which is currently
// always empty — wizard users land on this step expecting to pick from the
// 60+ platform components, not a marketing-blueprint card list.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   #2 — never compromise from quality: SME marketplace UX pattern is the
//        proven shape, copy it exactly here so users moving between corporate
//        provisioning and SME marketplace recognise the surface.
//   #4 — never hardcode: every label, dependency edge, tier comes from
//        componentGroups.ts. No app-side knowledge of which component
//        depends on which.
//
// Dependency-aware selection:
//   - Adding Harbor cascades → cnpg + seaweedfs + valkey are added too,
//     and a single toast announces "Harbor added (incl. cnpg, seaweedfs,
//     valkey)" so the user understands what just happened.
//   - Removing cnpg cascades the OTHER way: cnpg's transitive dependents
//     (Harbor, Keycloak, Gitea, …) would all be removed too, so the wizard
//     prompts the user with a confirm before letting the cascade run.
//   - Mandatory components carry a locked toggle — clicking them is a no-op
//     and a one-shot toast "Component X is mandatory" explains why.

import { useMemo, useState, useCallback } from 'react'
import { Search, Plus, Check, Lock, Info } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'
import {
  ALL_COMPONENTS,
  findComponent,
  resolveTransitiveDependencies,
  resolveTransitiveDependents,
  type ComponentEntry,
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

/** Letter-pill icon when a component has no logo. Hue derived from name. */
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

type ChipTone = 'success' | 'neutral' | 'accent' | 'warn' | 'mandatory' | 'recommended' | 'optional'

const CHIP_PALETTE: Record<ChipTone, { bg: string; fg: string }> = {
  success:     { bg: 'rgba(74,222,128,0.14)',  fg: '#4ADE80' },
  neutral:     { bg: 'rgba(148,163,184,0.14)', fg: 'var(--wiz-text-md)' },
  accent:      { bg: 'rgba(56,189,248,0.14)',  fg: '#38BDF8' },
  warn:        { bg: 'rgba(245,158,11,0.14)',  fg: '#F59E0B' },
  mandatory:   { bg: 'rgba(74,222,128,0.16)',  fg: '#4ADE80' },
  recommended: { bg: 'rgba(56,189,248,0.16)',  fg: '#38BDF8' },
  optional:    { bg: 'rgba(167,139,250,0.16)', fg: '#A78BFA' },
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
  onToggle: () => void
}

function ComponentCard({ entry, selected, onToggle }: ComponentCardProps) {
  const isMandatoryCard = entry.tier === 'mandatory'
  const includesNote = (entry.dependencies ?? []).length > 0
    ? `Includes: ${(entry.dependencies ?? []).map(d => findComponent(d)?.name ?? d).join(', ')}`
    : null

  return (
    <button
      type="button"
      onClick={onToggle}
      aria-pressed={selected}
      data-testid={`component-card-${entry.id}`}
      data-selected={selected ? 'true' : 'false'}
      data-tier={entry.tier}
      style={{
        position: 'relative',
        background: selected
          ? 'color-mix(in srgb, #4ADE80 6%, var(--wiz-bg-sub))'
          : 'var(--wiz-bg-sub)',
        border: selected ? '1.5px solid #4ADE80' : '1.5px solid var(--wiz-border-sub)',
        borderRadius: 12,
        padding: '0.6rem',
        display: 'flex',
        alignItems: 'stretch',
        gap: '0.75rem',
        cursor: isMandatoryCard ? 'not-allowed' : 'pointer',
        transition: 'transform 0.15s, border-color 0.15s, background 0.15s',
        color: 'inherit',
        textAlign: 'left',
        font: 'inherit',
        minHeight: 108,
        overflow: 'hidden',
        opacity: isMandatoryCard ? 0.96 : 1,
      }}
    >
      <IconFallback name={entry.name} />

      <div
        style={{
          flex: 1,
          minWidth: 0,
          display: 'flex',
          flexDirection: 'column',
          gap: '0.25rem',
          paddingRight: '4.5rem',
          overflow: 'hidden',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem' }}>
          <span
            style={{
              color: 'var(--wiz-text-hi)',
              fontSize: '0.92rem',
              fontWeight: 600,
              lineHeight: 1.2,
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
              flex: '1 1 auto',
              minWidth: 0,
            }}
          >
            {entry.name}
          </span>
          <span
            style={{
              color: 'var(--wiz-text-sub)',
              fontSize: '0.68rem',
              textTransform: 'capitalize',
              background: 'var(--wiz-border-sub)',
              padding: '0.1rem 0.4rem',
              borderRadius: 3,
              flexShrink: 0,
            }}
          >
            {entry.groupName}
          </span>
        </div>
        <p
          style={{
            margin: 0,
            color: 'var(--wiz-text-md)',
            fontSize: '0.78rem',
            lineHeight: 1.45,
            display: '-webkit-box',
            WebkitLineClamp: 2,
            WebkitBoxOrient: 'vertical',
            overflow: 'hidden',
          }}
        >
          {entry.desc}
        </p>
        <div
          style={{
            marginTop: '0.25rem',
            display: 'flex',
            flexWrap: 'wrap',
            gap: '0.25rem',
            overflow: 'hidden',
            minHeight: '1.4rem',
            alignItems: 'center',
          }}
        >
          {/* Tier badge — primary trait */}
          {entry.tier === 'mandatory' && (
            <CardChip
              label={
                <>
                  <Lock size={9} strokeWidth={3} aria-hidden /> MANDATORY
                </>
              }
              tone="mandatory"
              testId={`tier-${entry.id}`}
            />
          )}
          {entry.tier === 'recommended' && (
            <CardChip label="RECOMMENDED" tone="recommended" testId={`tier-${entry.id}`} />
          )}
          {entry.tier === 'optional' && (
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
            style={{
              margin: 0,
              color: 'var(--wiz-text-hint)',
              fontSize: '0.7rem',
              lineHeight: 1.4,
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
            data-testid={`includes-${entry.id}`}
          >
            {includesNote}
          </p>
        )}
      </div>

      {/* Selected status corner */}
      {selected && (
        <div
          style={{ position: 'absolute', bottom: '0.5rem', right: '0.55rem', pointerEvents: 'none' }}
          data-testid={`selected-${entry.id}`}
        >
          <span
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: '0.3rem',
              padding: '0.15rem 0.55rem',
              borderRadius: 999,
              fontSize: '0.65rem',
              fontWeight: 600,
              lineHeight: 1.4,
              letterSpacing: '0.03em',
              background: 'rgba(74,222,128,0.16)',
              color: '#4ADE80',
            }}
          >
            <span
              style={{
                width: 6,
                height: 6,
                borderRadius: '50%',
                background: 'currentColor',
                display: 'inline-block',
              }}
            />
            SELECTED
          </span>
        </div>
      )}

      {/* Add/remove circle button (top-right) */}
      <span
        aria-hidden
        style={{
          position: 'absolute',
          top: '0.6rem',
          right: '0.6rem',
          width: 32,
          height: 32,
          borderRadius: '50%',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: isMandatoryCard
            ? 'rgba(74,222,128,0.85)'
            : selected
              ? '#4ADE80'
              : 'rgba(56,189,248,0.85)',
          color: '#fff',
          boxShadow: '0 1px 4px rgba(0,0,0,0.2)',
        }}
      >
        {isMandatoryCard ? (
          <Lock size={14} strokeWidth={3} />
        ) : selected ? (
          <Check size={16} strokeWidth={3} />
        ) : (
          <Plus size={16} strokeWidth={2.5} />
        )}
      </span>
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

/* ── Step ─────────────────────────────────────────────────────────── */

export function StepComponents() {
  const { next, back } = useStepNav()
  const store = useWizardStore()
  const bp = useBreakpoint()

  const [query, setQuery] = useState('')
  const [activeCategory, setActiveCategory] = useState<string | null>(null)
  const [toasts, setToasts] = useState<Toast[]>([])
  const [pendingRemoval, setPendingRemoval] = useState<ConfirmCascade | null>(null)

  const selectedSet = useMemo(
    () => new Set(store.selectedComponents),
    [store.selectedComponents],
  )

  const categories = useMemo(() => {
    return [...new Set(ALL_COMPONENTS.map((c) => c.groupId))].sort()
  }, [])

  const visible = useMemo(() => {
    let result = ALL_COMPONENTS.slice()
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
  }, [query, activeCategory, selectedSet])

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
        // Cascade-aware remove
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

      // Add — cascade-add deps
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
      description="Select the platform-engineering components for the new Sovereign. Mandatory tiles are locked on. Recommended tiles are pre-selected based on industry best practice — uncheck any you don't need. Optional tiles are off by default. Selecting a component automatically adds the data services it requires (e.g. Harbor → cnpg, seaweedfs, valkey)."
      onNext={next}
      onBack={back}
    >
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
            placeholder={`Search ${ALL_COMPONENTS.length} components…`}
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
          {visible.length} shown · {store.selectedComponents.length} selected
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

      <ToastStack toasts={toasts} />
      {pendingRemoval && (
        <ConfirmCascadeDialog
          cascade={pendingRemoval}
          onConfirm={confirmCascadeRemove}
          onCancel={() => setPendingRemoval(null)}
        />
      )}
    </StepShell>
  )
}
