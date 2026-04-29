// StepComponents — corporate platform component grid.
//
// Visual contract: 4-line text grid spanning the FULL card body width
// (height 108px, 0.6rem padding, 0.75rem gap, 1.5px border). The card
// body has NO right-side padding reserved for affordances — every line
// uses the full available width so descriptions actually breathe.
//
// Line grid (top to bottom inside `.corp-comp-body`):
//   Line 1 — name (left, flex) + family chip + toggle button (right)
//   Line 2 — description line 1 (full width)
//   Line 3 — description line 2 (full width, clamp to 2)
//   Line 4 — tier chip + dependency chips + SELECTED pill (right)
//
// Chips ONLY appear on line 1 or line 4 — never lines 2/3. The toggle
// affordance (Plus / Check icon button) lives inline at the right end of
// line 1, sharing the chip row with the family chip — it does NOT
// reserve a vertical column to the right of the body, so the descriptions
// span the entire body width. This is the deliberate departure from the
// SME marketplace's `app-body { padding-right: 72px }` which wasted the
// right quarter for a hover-visible button.
//
// Two tabs:
//
//   1. "Choose Your Stack" (default)  — non-mandatory components.
//      Search + category chip filter, sort-selected-first, cascade-add
//      and cascade-remove dependency logic. Each card is itself an anchor
//      to the marketplace product-detail page; nested inside are:
//        • a clickable family chip (line 1, right) → family portfolio
//          page,
//        • an inline round Add/Selected icon button (line 1, far right) →
//          toggles selection.
//      Both nested affordances stopPropagation so the outer anchor
//      navigation never races the toggle.
//
//   2. "Always Included"               — mandatory infra. Read-only.
//      Grouped by product (PILOT, GUARDIAN, …), no search, no toggle UI.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   #2 — never compromise quality: descriptions read as professional
//        sentence-fragments (6-10 words each) and the card never wastes
//        valuable horizontal space.
//   #4 — never hardcode: tabs, tiers, logos, dependency edges, family
//        chip palettes — every catalog and presentation fact comes from
//        componentGroups.ts and marketplaceCopy.ts. Logo URLs default to
//        `/component-logos/<id>.svg` so swapping a file under public/
//        rebrands the card without touching application source.

import { useMemo, useState, useCallback } from 'react'
import { Search, Plus, Check, Lock, Info } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'
import {
  ALL_COMPONENTS,
  GROUPS,
  PRODUCTS,
  findComponent,
  componentsByProduct,
  resolveTransitiveDependents,
  type ComponentEntry,
  type GroupDef,
} from './componentGroups'
import { STEP_COMPONENTS_COPY } from './stepComponentsCopy'
import { getLogoToneStyle } from './logoTone'
import { familyChipPalette } from '@/pages/marketplace/marketplaceCopy'

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

/**
 * Logo tile — per-brand surface sourced from each project's canonical
 * homepage / press kit. See `logoTone.ts` for the per-id map.
 *
 * Each component renders against its own brand surface — Alloy on
 * Grafana orange, FerretDB on its navy, Temporal on its signature
 * blue, Cilium on its dark navy, etc. The marketplace bakes brand
 * surfaces into PNGs; the wizard achieves the same effect with
 * per-id metadata so vendored upstream SVGs (transparent
 * background) read crisply against the brand tile.
 *
 * The brand surface is theme-INDEPENDENT — homepage logos look the
 * same regardless of viewer theme. The card BODY surrounding the
 * tile still flips with the wizard theme (`--wiz-bg-input`); only
 * the LOGO TILE is brand-locked. Keep in sync with `.mp-related-logo`
 * / `.mp-related-icon` in `MarketplaceFamilyPage.tsx` and
 * `.mp-product-logo` / `.mp-product-icon` in `MarketplaceProductPage.tsx`
 * — the component-logo tile is a single visual contract across the
 * wizard and the marketplace surfaces.
 */
const LOGO_TILE_RADIUS = 10
const LOGO_TILE_PADDING = 6

/** Letter-pill icon when a component has no logo URL. */
function IconFallback({ entry }: { entry: ComponentEntry }) {
  const letter = (entry.name[0] ?? '?').toUpperCase()
  const tone = getLogoToneStyle(entry.id)
  return (
    <span
      aria-hidden
      style={{
        alignSelf: 'stretch',
        aspectRatio: '1 / 1',
        height: 'auto',
        borderRadius: LOGO_TILE_RADIUS,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        color: tone.text,
        fontSize: '1.2rem',
        fontWeight: 700,
        background: tone.background,
        border: `1px solid ${tone.border}`,
      }}
    >
      {letter}
    </span>
  )
}

/** Logo block — vendored SVG when available, letter-mark fallback otherwise. */
function ComponentLogo({ entry }: { entry: ComponentEntry }) {
  if (!entry.logoUrl) return <IconFallback entry={entry} />
  const tone = getLogoToneStyle(entry.id)
  return (
    <span
      aria-hidden
      style={{
        alignSelf: 'stretch',
        aspectRatio: '1 / 1',
        height: 'auto',
        borderRadius: LOGO_TILE_RADIUS,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        background: tone.background,
        border: `1px solid ${tone.border}`,
        overflow: 'hidden',
        padding: LOGO_TILE_PADDING,
        boxSizing: 'border-box',
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
    ? `${STEP_COMPONENTS_COPY.includesPrefix} ${(entry.dependencies ?? []).map(d => findComponent(d)?.name ?? d).join(', ')}`
    : null

  const palette = familyChipPalette(entry.product)

  const cardClass = [
    'corp-comp-card',
    selected ? 'in-cart' : '',
    readOnly ? 'read-only' : '',
    isMandatoryCard ? 'mandatory' : '',
  ].filter(Boolean).join(' ')

  // Card body and chips — shared between the interactive (Tab 1) and
  // read-only (Tab 2) layouts. Lifted into a constant so the outer
  // wrapper can be either an anchor (Tab 1, navigates to product detail)
  // or a plain div (Tab 2, inert) without duplicating the body markup.
  const inner = (
    <>
      <ComponentLogo entry={entry} />

      <div className="corp-comp-body">
        {/* Line 1 — name (left, flex) + family chip + inline toggle
            button (right). Chips and the toggle share this row so no
            vertical column on the right is reserved; lines 2-3 (desc)
            consume the FULL body width. The toggle button is suppressed
            in read-only mode and replaced by the static category pill. */}
        <div className="corp-comp-top">
          <span className="corp-comp-name">{entry.name}</span>
          {readOnly ? (
            <span className="corp-comp-cat">{entry.groupName}</span>
          ) : (
            <>
              <Link
                to="/marketplace/family/$familyId"
                params={{ familyId: entry.product }}
                data-testid={`family-chip-${entry.id}`}
                onClick={(e) => e.stopPropagation()}
                className="corp-comp-family-chip"
                style={{
                  background: palette.bg,
                  color: palette.fg,
                  border: `1px solid ${palette.border}`,
                }}
                aria-label={`Open ${entry.groupName} family portfolio`}
                title={`Open ${entry.groupName} family portfolio`}
              >
                {entry.groupName}
              </Link>
              {/* Inline Add / Remove circle button — sits at the end of
                  line 1 next to the family chip. Opacity 0 by default,
                  fades to opacity 1 on card hover; always visible (and
                  tinted green) when the card is in-cart so removal is one
                  click without hover-fishing. Stops propagation so the
                  outer anchor's product-detail navigation never fires
                  when the operator intends to toggle selection. */}
              <button
                type="button"
                aria-pressed={selected}
                aria-label={selected ? `Remove ${entry.name}` : `Add ${entry.name}`}
                onClick={(e) => {
                  e.preventDefault()
                  e.stopPropagation()
                  onToggle?.()
                }}
                data-testid={`toggle-${entry.id}`}
                className={`corp-comp-add-btn ${selected ? 'added' : ''}`}
                title={selected ? `Remove ${entry.name} from stack` : `Add ${entry.name} to stack`}
              >
                {selected ? (
                  <Check size={12} strokeWidth={3} aria-hidden />
                ) : (
                  <Plus size={12} strokeWidth={2.5} aria-hidden />
                )}
              </button>
            </>
          )}
        </div>
        {/* Lines 2-3 — description, two-line clamp, full body width. */}
        <p className="corp-comp-desc">{entry.desc}</p>
        {/* Line 4 — tier chip + dependency chips + SELECTED pill. Chips
            ONLY appear on line 1 or line 4; lines 2-3 are description
            only. The `.corp-comp-chips` mask gradient on the right edge
            fades any overflow gracefully when many deps are present. */}
        <div className="corp-comp-chips">
          {readOnly ? (
            <CardChip
              label={STEP_COMPONENTS_COPY.pillInfra}
              tone="infra"
              testId={`tier-${entry.id}`}
            />
          ) : entry.tier === 'mandatory' ? (
            <CardChip
              label={
                <>
                  <Lock size={9} strokeWidth={3} aria-hidden /> {STEP_COMPONENTS_COPY.pillMandatory}
                </>
              }
              tone="mandatory"
              testId={`tier-${entry.id}`}
            />
          ) : entry.tier === 'recommended' ? (
            <CardChip label={STEP_COMPONENTS_COPY.pillRecommended} tone="recommended" testId={`tier-${entry.id}`} />
          ) : (
            <CardChip label={STEP_COMPONENTS_COPY.pillOptional} tone="optional" testId={`tier-${entry.id}`} />
          )}
          {(entry.dependencies ?? []).map((depId) => {
            const depEntry = findComponent(depId)
            const label = depEntry?.name ?? depId
            return (
              <CardChip
                key={depId}
                label={`+ ${label}`}
                tone="accent"
                title={`Bundled dependency: ${label}`}
                testId={`deps-${entry.id}-${depId}`}
              />
            )
          })}
          {/* SELECTED indicator — compact green dot pinned to the right
              end of line 4. The card already conveys selection through
              its green border, green-tinted background, and the green ✓
              toggle button on line 1, so a verbose SELECTED text pill on
              line 4 would be redundant and would crowd the dependency
              chips. The dot keeps the test-surface (`selected-<id>`) in
              the DOM without competing with chips for horizontal space. */}
          {selected && !readOnly && (
            <span
              className="corp-comp-status"
              data-testid={`selected-${entry.id}`}
              aria-label={STEP_COMPONENTS_COPY.selectedPill}
              title={STEP_COMPONENTS_COPY.selectedPill}
            >
              <span className="corp-comp-status-dot" />
            </span>
          )}
        </div>
        {/* Off-screen accessibility / test hint — surfaces the full
            dependency list as a single sentence for screen readers and
            the includes-<id> assertion in StepComponents.test.tsx. Does
            not occupy card layout space (the .corp-comp-includes class
            is absolute-positioned off-screen). */}
        {includesNote && (
          <p
            className="corp-comp-includes"
            data-testid={`includes-${entry.id}`}
          >
            {includesNote}
          </p>
        )}
      </div>
    </>
  )

  // Read-only Tab 2 — plain div, no navigation, no toggle.
  if (readOnly) {
    return (
      <div
        data-testid={`component-card-${entry.id}`}
        data-selected="false"
        data-tier={entry.tier}
        className={cardClass}
        aria-label={`${entry.name} component card`}
      >
        {inner}
      </div>
    )
  }

  // Tab 1 — the whole card is an anchor to the product detail page,
  // exactly mirroring SME's `<a href="/app?slug=X" class="app-card">`
  // wrapper. The toggle button and family chip live inside as nested
  // interactive elements that stopPropagation on click.
  return (
    <Link
      to="/marketplace/product/$componentId"
      params={{ componentId: entry.id }}
      data-testid={`component-card-${entry.id}`}
      data-selected={selected ? 'true' : 'false'}
      data-tier={entry.tier}
      className={cardClass}
      aria-label={`${entry.name} component card`}
    >
      {inner}
    </Link>
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
  entry: ComponentEntry
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
          {STEP_COMPONENTS_COPY.confirmTitle(cascade.entry)}
        </h3>
        <p
          style={{
            margin: 0,
            color: 'var(--wiz-text-md)',
            fontSize: '0.85rem',
            lineHeight: 1.5,
          }}
        >
          {STEP_COMPONENTS_COPY.confirmIntro(cascade.entry, cascade.dependents.length)}
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
            {STEP_COMPONENTS_COPY.confirmKeep}
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
            {STEP_COMPONENTS_COPY.confirmRemove}
          </button>
        </div>
      </div>
    </div>
  )
}

/* ── Tab 2: "Always Included" — read-only mandatory grid ──────────── */

function AlwaysIncludedTab({ groups: _groups, cols }: { groups: readonly GroupDef[]; cols: string }) {
  // Group by product using the post-promotion catalog (`ALL_COMPONENTS`)
  // so transitive-mandatory promotions (cnpg, valkey) surface in their
  // owning product section rather than vanishing into Tab 1's pool. Hide
  // any product that has zero mandatories.
  void _groups // keep prop for callsite back-compat (#175 cleanup deferred)
  const productSections = useMemo(() => {
    return PRODUCTS
      .map((product) => ({
        product,
        mandatories: componentsByProduct(product.id).filter(c => c.tier === 'mandatory'),
      }))
      .filter((s) => s.mandatories.length > 0)
  }, [])

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
        {STEP_COMPONENTS_COPY.alwaysIncludedBlurb}
      </p>

      <div
        data-testid="always-included-grid"
        style={{ display: 'flex', flexDirection: 'column', gap: '1.25rem' }}
      >
        {productSections.map(({ product, mandatories }) => (
          <section
            key={product.id}
            data-testid={`always-included-section-${product.id}`}
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
              {product.name}{' '}
              <span
                style={{
                  color: 'var(--wiz-text-sub)',
                  fontWeight: 500,
                  letterSpacing: 0,
                  textTransform: 'none',
                  marginLeft: 6,
                }}
              >
                {product.subtitle}
              </span>
            </h4>
            <div style={{ display: 'grid', gridTemplateColumns: cols, gap: '0.65rem' }}>
              {mandatories.map((entry) => (
                <ComponentCard
                  key={entry.id}
                  entry={entry}
                  selected={false}
                  readOnly
                />
              ))}
            </div>
          </section>
        ))}
      </div>
    </div>
  )
}

/* ── Empty state card (no matches) ────────────────────────────────── */

function EmptyStateCard() {
  return (
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
        {STEP_COMPONENTS_COPY.emptyState}
      </p>
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
        const t = STEP_COMPONENTS_COPY.toastMandatory(entry)
        pushToast('mandatory', t.primary, t.extra)
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
            entry,
            dependents,
          })
          return
        }
        store.removeComponent(entry.id)
        const t = STEP_COMPONENTS_COPY.toastRemoved(entry, [])
        pushToast('removed', t.primary, t.extra)
        return
      }

      // Snapshot the selection BEFORE dispatching addComponent so we can
      // diff the post-cascade state and surface every newly-added id
      // (component deps + product family cascade) in a single toast.
      const beforeSel = new Set(selectedSet)

      store.addComponent(entry.id)

      const afterSel = useWizardStore.getState().selectedComponents
      const newlyAdded = afterSel
        .filter((id) => id !== entry.id && !beforeSel.has(id))
        .map((id) => findComponent(id))
        .filter((c): c is ComponentEntry => !!c)

      // Identify the *cascading* product family (if any) — pick the
      // product whose entire member set was just added (i.e. the product
      // whose cascadeOnMemberSelection flag fired through the store's
      // walk). This is robust whether the seed was a CORTEX member (BGE)
      // or an INSIGHTS member (Specter) whose deps reach into CORTEX.
      const afterSet = new Set(afterSel)
      const triggeredFamily = PRODUCTS.find((p) =>
        p.cascadeOnMemberSelection &&
        p.tier !== 'mandatory' &&
        componentsByProduct(p.id).every((c) => afterSet.has(c.id)) &&
        // ... and at least one of its members was newly added (not just
        // already-selected before the click).
        componentsByProduct(p.id).some((c) => !beforeSel.has(c.id)),
      )

      if (triggeredFamily) {
        const familyMembers = newlyAdded.filter((c) => c.product === triggeredFamily.id)
        const t = STEP_COMPONENTS_COPY.toastAddedWithFamily(entry, triggeredFamily, familyMembers)
        pushToast('added', t.primary, t.extra)
      } else if (newlyAdded.length > 0) {
        const t = STEP_COMPONENTS_COPY.toastAddedWithDeps(entry, newlyAdded)
        pushToast('added', t.primary, t.extra)
      } else {
        const t = STEP_COMPONENTS_COPY.toastAddedSimple(entry)
        pushToast('added', t.primary)
      }
    },
    [selectedSet, pushToast, store],
  )

  const confirmCascadeRemove = useCallback(() => {
    if (!pendingRemoval) return
    const target = pendingRemoval
    store.removeComponent(target.componentId)
    const t = STEP_COMPONENTS_COPY.toastRemoved(target.entry, target.dependents)
    pushToast('removed', t.primary, t.extra)
    setPendingRemoval(null)
  }, [pendingRemoval, store, pushToast])

  return (
    <StepShell
      title={STEP_COMPONENTS_COPY.stepTitle}
      description={STEP_COMPONENTS_COPY.stepDescription}
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
          {STEP_COMPONENTS_COPY.tabChooseLabel(chooseSelectedCount)}
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'always'}
          data-testid="tab-always"
          onClick={() => setTab('always')}
          className={`corp-tab ${tab === 'always' ? 'active' : ''}`}
        >
          {STEP_COMPONENTS_COPY.tabAlwaysLabel(mandatoryTotal)}
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
              {STEP_COMPONENTS_COPY.selectedCounter(store.selectedComponents.length, ALL_COMPONENTS.length)}
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
              title={STEP_COMPONENTS_COPY.resetTooltip}
            >
              {STEP_COMPONENTS_COPY.resetButton}
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
                placeholder={STEP_COMPONENTS_COPY.searchPlaceholder(choosePool.length)}
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
              {STEP_COMPONENTS_COPY.sectionTitle}
            </h3>
            <span style={{ color: 'var(--wiz-text-sub)', fontSize: '0.78rem' }}>
              {STEP_COMPONENTS_COPY.sectionSummary(visible.length, chooseSelectedCount)}
            </span>
          </div>

          {/* Single flat marketplace card grid — no family-group section
              headers. Family relationship is conveyed through the
              clickable family chip on each card (see ComponentCard). */}
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
            {visible.length === 0 && <EmptyStateCard />}
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

        /* ── Card (mirrors AppsStep .app-card 1:1) ───────────────── */
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
          text-decoration: none;
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

        /* Body column — NO right-side padding. The toggle button and
           SELECTED pill are inline in the chip rows (line 1 / line 4),
           NOT absolute overlays — so descriptions on lines 2-3 use the
           FULL body width. (Departure from SME's .app-body which
           reserves padding-right: 72px for an absolute overlay button.) */
        .corp-comp-body {
          flex: 1;
          min-width: 0;
          display: flex;
          flex-direction: column;
          gap: 0.2rem;
          overflow: hidden;
        }
        /* Line 1 — name + family chip + toggle. align-items: center so the
           inline toggle button and family chip sit on the same baseline
           with the name. min-height 22px keeps the row tall enough for
           the 22×22 toggle without baseline drift. */
        .corp-comp-top {
          display: flex;
          align-items: center;
          gap: 0.4rem;
          min-height: 22px;
        }
        .corp-comp-name {
          color: var(--wiz-text-hi);
          font-size: 0.9rem;
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

        /* Family chip — clickable, top-right of card body. Rendered at
           the same physical size as .corp-comp-cat so the card geometry
           is identical between read-only and interactive layouts. */
        .corp-comp-family-chip {
          display: inline-flex;
          align-items: center;
          padding: 0.1rem 0.45rem;
          border-radius: 999px;
          font-size: 0.62rem;
          font-weight: 700;
          letter-spacing: 0.05em;
          text-transform: uppercase;
          text-decoration: none;
          flex-shrink: 0;
          line-height: 1.4;
          transition: filter 0.15s, transform 0.1s;
        }
        .corp-comp-family-chip:hover {
          filter: brightness(1.15);
          transform: translateY(-1px);
        }

        /* Lines 2-3 — description, two-line clamp. Spans the FULL body
           width (no padding-right cap) so descriptions actually breathe.
           font-size + line-height tuned so two filled lines plus line 1
           and line 4 fit cleanly inside the 108px card height. */
        .corp-comp-desc {
          margin: 0;
          color: var(--wiz-text-md);
          font-size: 0.76rem;
          line-height: 1.4;
          display: -webkit-box;
          -webkit-line-clamp: 2;
          -webkit-box-orient: vertical;
          overflow: hidden;
        }
        /* Line 4 — chip row. align-items: center keeps the SELECTED
           pill (margin-left: auto) and the tier / dep chips on the same
           visual baseline. The mask gradient gracefully fades any chip
           overflow on the right when many deps are present, but the
           SELECTED pill is rendered AFTER the gradient terminus so it
           remains fully visible. */
        .corp-comp-chips {
          margin-top: 0.1rem;
          display: flex;
          flex-wrap: nowrap;
          gap: 0.25rem;
          overflow: hidden;
          min-height: 1.3rem;
          align-items: center;
        }

        /* Visually-hidden dependency hint — surfaces the full
           "Includes: A, B, C" sentence for screen readers and the
           includes-<id> assertion in StepComponents.test.tsx. Removed
           from layout so the card stays at the canonical 108px even when
           a component has many dependencies (the visual chips take care
           of conveying the dep list to sighted users). */
        .corp-comp-includes {
          position: absolute;
          width: 1px;
          height: 1px;
          padding: 0;
          margin: -1px;
          overflow: hidden;
          clip: rect(0, 0, 0, 0);
          white-space: nowrap;
          border: 0;
        }

        /* SELECTED indicator — compact green dot at the right end of
           line 4. margin-left: auto pushes it to the trailing edge so
           it always anchors to the right regardless of how many
           dependency chips precede it. The dot is small enough not to
           compete with chips for horizontal space; the card's border,
           background, and toggle-button colour carry the loud-selection
           signal. */
        .corp-comp-status {
          margin-left: auto;
          flex-shrink: 0;
          display: inline-flex;
          align-items: center;
          padding: 0 0.15rem;
          color: #4ADE80;
          pointer-events: none;
        }
        .corp-comp-status-dot {
          width: 7px;
          height: 7px;
          border-radius: 50%;
          background: currentColor;
          display: inline-block;
          box-shadow: 0 0 0 2px rgba(74,222,128,0.18);
        }

        /* Inline Add / Remove button — sits at the end of line 1 next
           to the family chip, sharing horizontal space with chips. 22×22
           round (smaller than SME's 32×32 to fit cleanly inline without
           displacing the line-1 chips). Opacity 0 by default, fades to
           opacity 1 on card hover; always visible (and tinted green)
           when in-cart so removal is one click without hover-fishing.
           margin-left: auto pins it to the trailing edge of line 1
           regardless of family-chip width. */
        .corp-comp-add-btn {
          margin-left: 0;
          width: 22px;
          height: 22px;
          flex-shrink: 0;
          border-radius: 50%;
          border: none;
          cursor: pointer;
          display: flex;
          align-items: center;
          justify-content: center;
          opacity: 0;
          transform: scale(0.85);
          transition: opacity 0.15s, transform 0.15s, background 0.15s, filter 0.15s;
          background: rgba(var(--wiz-accent-ch), 1);
          color: #fff;
          padding: 0;
          box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
        }
        .corp-comp-card:hover .corp-comp-add-btn {
          opacity: 1;
          transform: scale(1);
        }
        .corp-comp-add-btn.added {
          background: #4ADE80;
          opacity: 1;
          transform: scale(1);
        }
        .corp-comp-add-btn:hover {
          filter: brightness(0.85);
        }
        .corp-comp-add-btn:focus-visible {
          opacity: 1;
          transform: scale(1);
          outline: 2px solid rgba(var(--wiz-accent-ch), 1);
          outline-offset: 2px;
        }

        /* Tablet/mobile — keep the canonical 108px floor; let the card
           grow only when the description / chips would otherwise clip,
           matching the SME marketplace responsive behaviour. */
        @media (max-width: 768px) {
          .corp-comp-card { height: auto; min-height: 108px; }
        }
      `}</style>
    </StepShell>
  )
}
